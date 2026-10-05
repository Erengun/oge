package oracle

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
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

// expectedTests reads every top-level func TestXxx(t *testing.T) in the
// Oracle's Go test files. files is the Snapshot's file list; show reads a
// Snapshot file. A test file that doesn't parse adds nothing: it fails the
// Check on its own.
func expectedTests(tests []string, files []string, show func(string) ([]byte, error)) []TestID {
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
	for _, f := range tests {
		if !strings.HasSuffix(f, "_test.go") || !inDotDotDot(path.Dir(f), mods) {
			continue
		}
		src, err := show(f)
		if err != nil {
			continue
		}
		pkg := importPath(path.Dir(f), mods)
		for _, name := range testNames(src) {
			id := TestID{Package: pkg, Name: name}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// inDotDotDot reports whether go test ./... from the root would build
// dir: not under testdata, vendor or a _ or . directory, and, when the
// root has a go.mod, not inside a nested module.
func inDotDotDot(dir string, mods map[string]string) bool {
	if dir == "." {
		return true
	}
	for _, part := range strings.Split(dir, "/") {
		if part == "testdata" || part == "vendor" || strings.HasPrefix(part, "_") || strings.HasPrefix(part, ".") {
			return false
		}
	}
	if _, ok := mods["."]; ok {
		for d := dir; d != "."; d = path.Dir(d) {
			if _, nested := mods[d]; nested {
				return false
			}
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

// testNames lists src's top-level func TestXxx(t *testing.T) declarations.
func testNames(src []byte) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var names []string
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
			names = append(names, fn.Name.Name)
		}
	}
	return names
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

// missingTests lists the expected tests no report shows passing, leaving
// out those a report shows failing: their command already failed.
func missingTests(expected []TestID, reports []*Report) []string {
	var missing []string
	for _, id := range expected {
		passed, failed := false, false
		for _, rep := range reports {
			switch rep.outcome(id) {
			case "pass":
				passed = true
			case "fail":
				failed = true
			}
		}
		if !passed && !failed {
			missing = append(missing, id.String())
		}
	}
	return missing
}
