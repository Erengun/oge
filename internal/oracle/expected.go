package oracle

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TestID names one top-level Go test the Oracle expects to pass: its
// package's import path ("" when the Snapshot has no go.mod above it, so
// any package matches) and its name.
type TestID struct {
	Package string `json:"package,omitempty"`
	Name    string `json:"name"`
}

func (id TestID) String() string {
	if id.Package == "" {
		return id.Name
	}
	return id.Package + "." + id.Name
}

// expectedTests reads every top-level func TestXxx(t *testing.T), and
// every ExampleXxx with an output comment, in the Oracle's Go test files,
// overall and by file. Nested modules count: a Check command run inside
// one runs their tests, and one that never does leaves them unattested. files is the Snapshot's
// file list; show reads a Snapshot file. A test file that doesn't parse
// adds nothing: it fails the Check on its own.
func expectedTests(tests []string, files []string, show func(string) ([]byte, error)) ([]TestID, map[string][]TestID) {
	mods := map[string]string{} // directory → module path, from the Snapshot's go.mod files
	for _, f := range files {
		if path.Base(f) != "go.mod" {
			continue
		}
		if b, err := show(f); err == nil {
			if m := modulePath(b); m != "" {
				mods[path.Dir(f)] = m
			}
		}
	}
	seen := map[TestID]bool{}
	var ids []TestID
	byFile := map[string][]TestID{}
	for _, f := range tests {
		if !strings.HasSuffix(f, "_test.go") || !goPackageDir(path.Dir(f)) {
			continue
		}
		src, err := show(f)
		if err != nil {
			continue
		}
		pkg := importPath(path.Dir(f), mods)
		for _, name := range testNames(src) {
			id := TestID{Package: pkg, Name: name}
			byFile[f] = append(byFile[f], id)
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids, byFile
}

// goPackageDir reports whether the go command ever builds dir as a
// package: not under testdata, vendor or a _ or . directory.
func goPackageDir(dir string) bool {
	if dir == "." {
		return true
	}
	for _, part := range strings.Split(dir, "/") {
		if part == "testdata" || part == "vendor" || strings.HasPrefix(part, "_") || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// importPath is dir's import path under the nearest go.mod above it.
func importPath(dir string, mods map[string]string) string {
	for d := dir; ; d = path.Dir(d) {
		if m, ok := mods[d]; ok {
			switch {
			case d == dir:
				return m
			case d == ".":
				return m + "/" + dir
			default:
				return m + "/" + strings.TrimPrefix(dir, d+"/")
			}
		}
		if d == "." || d == "/" {
			return ""
		}
	}
}

// modulePath reads the module directive of a go.mod.
func modulePath(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if rest, ok := strings.CutPrefix(line, "module"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			return strings.Trim(strings.TrimSpace(rest), "\"`")
		}
	}
	return ""
}

// testNames lists src's tests and run examples.
func testNames(src []byte) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil
	}
	var names []string
	for _, fn := range testFuncs(f) {
		names = append(names, fn.Name.Name)
	}
	for _, fn := range exampleFuncs(f) {
		names = append(names, fn.Name.Name)
	}
	return names
}

// exampleFuncs are f's examples that go test runs: those with an output
// comment (f must be parsed with comments).
func exampleFuncs(f *ast.File) []*ast.FuncDecl {
	run := map[string]bool{}
	for _, ex := range doc.Examples(f) {
		if ex.Output != "" || ex.EmptyOutput {
			run["Example"+ex.Name] = true
		}
	}
	var fns []*ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil && run[fn.Name.Name] {
			fns = append(fns, fn)
		}
	}
	return fns
}

// testFuncs are f's top-level func TestXxx(t *testing.T) declarations:
// the tests go test runs.
func testFuncs(f *ast.File) []*ast.FuncDecl {
	var fns []*ast.FuncDecl
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !isTestName(fn.Name.Name) || fn.Type.TypeParams != nil {
			continue
		}
		params := fn.Type.Params.List
		if len(params) != 1 || len(params[0].Names) > 1 || fn.Type.Results != nil {
			continue
		}
		star, ok := params[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if sel, ok := star.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "T" {
			fns = append(fns, fn)
		}
	}
	return fns
}

// isTestName follows go test: Test, then nothing or a non-lowercase rune.
func isTestName(name string) bool {
	rest, ok := strings.CutPrefix(name, "Test")
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(r)
}

var (
	knownOS   = strings.Fields("aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos")
	knownArch = strings.Fields("386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm")
)

// PlainlyExpected reports whether the _test.go file at path, holding src,
// surely expects a test to run wherever the go command builds its package:
// it is in a package dir, has no GOOS/GOARCH filename suffix or build
// constraint, and declares at least one test or example.
func PlainlyExpected(p string, src []byte) bool {
	if !goPackageDir(path.Dir(p)) || constraint(src) != "" || len(testNames(src)) == 0 {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(path.Base(p), "_test.go"), "_")
	for i := len(parts) - 1; i >= 1 && i >= len(parts)-2; i-- {
		if slices.Contains(knownOS, parts[i]) || slices.Contains(knownArch, parts[i]) {
			return false
		}
	}
	return true
}

// GoPackageDir reports whether the go command ever builds dir as a package.
func GoPackageDir(dir string) bool { return goPackageDir(dir) }
