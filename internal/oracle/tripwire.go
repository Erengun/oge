package oracle

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
)

// Tripwires flags Candidate changes to obvious test-process control
// points (ADR-0020): a TestMain added or changed, a process exit called
// from init, and any mention of the attestation descriptor. They are
// signals recorded as Observations, never proof: the scan is a simple
// look at each changed file, it claims neither completeness nor intent,
// and it never changes a Verdict.
//
// changed are the Candidate's changed paths; before and after read a
// path from the Snapshot and the Candidate (ok false when absent).
func Tripwires(changed []string, before, after func(string) ([]byte, bool)) []string {
	var out []string
	for _, p := range changed {
		src, ok := after(p)
		if !ok {
			continue
		}
		if bytes.Contains(src, []byte(AttestEnv)) {
			out = append(out, p+": mentions "+AttestEnv)
		}
		if path.Ext(p) != ".go" {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if tm := testMain(fset, f, src); tm != "" {
			old := ""
			if b, ok := before(p); ok {
				ofset := token.NewFileSet()
				if of, err := parser.ParseFile(ofset, p, b, parser.SkipObjectResolution); err == nil {
					old = testMain(ofset, of, b)
				}
			}
			if tm != old {
				out = append(out, p+": TestMain added or changed")
			}
		}
		for _, call := range exitsFromInit(f) {
			out = append(out, p+": "+call+" called from init")
		}
	}
	return out
}

// testMain is the source of f's func TestMain, or "".
func testMain(fset *token.FileSet, f *ast.File, src []byte) string {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "TestMain" {
			return string(src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset])
		}
	}
	return ""
}

// exitsFromInit lists the process exits (os.Exit, syscall.Exit,
// runtime.Goexit) reachable at package initialisation within f: from its
// init functions and package-level variable initialisers, through calls
// to f's own top-level functions.
func exitsFromInit(f *ast.File) []string {
	pkgs := map[string]string{} // local name → import path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pkgs[name] = p
	}
	funcs := map[string]*ast.FuncDecl{}
	var roots []ast.Node
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil || d.Body == nil {
				continue
			}
			if d.Name.Name == "init" {
				roots = append(roots, d.Body)
			} else {
				funcs[d.Name.Name] = d
			}
		case *ast.GenDecl:
			if d.Tok == token.VAR {
				roots = append(roots, d)
			}
		}
	}
	exits := map[string]bool{"os.Exit": true, "syscall.Exit": true, "runtime.Goexit": true}
	seen, found := map[string]bool{}, map[string]bool{}
	var list []string
	for len(roots) > 0 {
		n := roots[0]
		roots = roots[1:]
		ast.Inspect(n, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fn := funcs[fun.Name]; fn != nil && !seen[fun.Name] {
					seen[fun.Name] = true
					roots = append(roots, fn.Body)
				}
			case *ast.SelectorExpr:
				if x, ok := fun.X.(*ast.Ident); ok {
					if p, ok := pkgs[x.Name]; ok {
						name := p + "." + fun.Sel.Name
						if exits[name] && !found[name] {
							found[name] = true
							list = append(list, name)
						}
					}
				}
			}
			return true
		})
	}
	return list
}
