package run

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ClassOracleTestAddition is the class of an additive edit to an Oracle
// test file that the scope check kept: an Implementer-authored test
// addition, never a Tamper event (#117, ADR-0007).
const ClassOracleTestAddition = "oracle_test_addition"

// KeptTest is one Oracle test file the implementer only added to, kept in
// the Candidate unreverted. ScopeObserved's "kept" list holds one per such
// file (always present, empty when there are none); an Attempt's last
// ScopeObserved record (its "late" one, when a kept file was written after
// the comparison and so reverted) is authoritative. The Receipt counts
// Added (#119). The Check still runs the Oracle's version of the file.
type KeptTest struct {
	Path   string   `json:"path"`
	Class  string   `json:"class"` // always ClassOracleTestAddition
	Tamper bool     `json:"tamper"`
	OID    string   `json:"oid"`   // git object id of the content classified
	Added  []string `json:"added"` // the Test, Benchmark, Fuzz and Example funcs new in the file
}

// additiveTestEdit reports whether after only adds to the Oracle test file
// before, and if so the test funcs it adds (#117). Both must be Go test
// files that parse, with the same package clause and file-level build
// constraints; after's imports must include before's; every top-level
// declaration of before other than its imports must appear in after (a
// multiset, in any order); and after declares no new TestMain or init.
// Anything else, a parse error included, is not additive: the edit is
// reverted and is a Tamper event.
//
// Declarations are compared as gofmt prints them, so a gofmt-only change
// is additive. A declaration's text excludes its doc comment, so editing
// a doc comment or a comment between declarations is additive; a comment
// inside a declaration is part of it, and changing one is not. Directives
// are never comments here: a //go: line in a doc comment is part of its
// declaration (//go:embed changes a var), and every //go: line before the
// package clause (//go:build, //go:debug) must be unchanged.
func additiveTestEdit(path string, before, after []byte) ([]string, bool) {
	if !strings.HasSuffix(path, "_test.go") {
		return nil, false
	}
	b, ok := parseTest(before)
	if !ok {
		return nil, false
	}
	a, ok := parseTest(after)
	if !ok || a.pkg != b.pkg || a.header != b.header {
		return nil, false
	}
	for imp := range b.imports {
		if !a.imports[imp] {
			return nil, false
		}
	}
	for text, n := range b.decls {
		if a.decls[text] < n {
			return nil, false
		}
	}
	if a.special > b.special {
		return nil, false // a new TestMain or init
	}
	var added []string
	for _, name := range a.tests {
		if !b.hasTest[name] {
			added = append(added, name)
		}
	}
	return added, true
}

// testFile is what additiveTestEdit compares of one version of a file.
type testFile struct {
	pkg, header string             // header: the file-level //go: and // +build lines
	imports     map[[2]string]bool // name (or ""), quoted path
	decls       map[string]int     // non-import top-level declarations, as gofmt prints them
	special     int                // top-level TestMain and init funcs
	tests       []string
	hasTest     map[string]bool
}

func parseTest(src []byte) (*testFile, bool) {
	src, err := format.Source(src)
	if err != nil {
		return nil, false
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x_test.go", src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, false
	}
	t := &testFile{pkg: f.Name.Name, header: fileDirectives(src), imports: map[[2]string]bool{},
		decls: map[string]int{}, hasTest: map[string]bool{}}
	for _, imp := range f.Imports {
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		}
		t.imports[[2]string{name, imp.Path.Value}] = true
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			c := *d
			c.Doc = nil
			text, ok := declText(fset, f, &c, d.Doc)
			if !ok {
				return nil, false
			}
			t.decls[text]++
		case *ast.FuncDecl:
			if d.Recv == nil && (d.Name.Name == "TestMain" || d.Name.Name == "init") {
				t.special++
			}
			if d.Recv == nil && isTestFunc(d.Name.Name) {
				t.tests = append(t.tests, d.Name.Name)
				t.hasTest[d.Name.Name] = true
			}
			c := *d
			c.Doc = nil
			text, ok := declText(fset, f, &c, d.Doc)
			if !ok {
				return nil, false
			}
			t.decls[text]++
		default:
			return nil, false
		}
	}
	return t, true
}

// declText prints d with the comments inside it, after the directives of
// its doc comment doc (which d no longer holds).
func declText(fset *token.FileSet, f *ast.File, d ast.Decl, doc *ast.CommentGroup) (string, bool) {
	var buf bytes.Buffer
	if doc != nil {
		for _, c := range doc.List {
			if strings.HasPrefix(c.Text, "//go:") {
				buf.WriteString(c.Text + "\n")
			}
		}
	}
	if err := printer.Fprint(&buf, fset, &printer.CommentedNode{Node: d, Comments: f.Comments}); err != nil {
		return "", false
	}
	return buf.String(), true
}

// fileDirectives is src's file-level directive lines, in order: every
// //go: and // +build line before the package clause, the build
// constraints oracle's constraint reads among them.
func fileDirectives(src []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(src))
	var lines []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "//go:") || strings.HasPrefix(line, "// +build") {
			lines = append(lines, line)
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// isTestFunc follows go test's naming rule for Test, Benchmark, Fuzz and
// Example funcs: the prefix, then nothing or a non-lowercase rune.
func isTestFunc(name string) bool {
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		if rest == "" {
			return name != "TestMain"
		}
		r, _ := utf8.DecodeRuneInString(rest)
		return !unicode.IsLower(r) && name != "TestMain"
	}
	return false
}
