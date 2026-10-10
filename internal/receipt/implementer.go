package receipt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/erengun/oge/internal/oracle"
)

// newTests counts the implementer-authored tests in the new files the
// Candidate adds that match the test globs (#119): the Test, Benchmark,
// Fuzz and Example funcs of a Go file, and at least one for each matching
// new file (a file with no test func, that isn't Go source, doesn't parse
// or can't be read counts 1: the count is a lower bound). Files
// that existed in the Snapshot are not new: their additions come from the
// Ledger's kept list.
func newTests(src Source, snapshot, cand string, changed, testGlobs []string) int {
	if len(testGlobs) == 0 {
		return 0
	}
	n := 0
	for _, f := range changed {
		if !oracle.MatchAny(testGlobs, f) {
			continue
		}
		if _, existed, err := src.Show(snapshot, f); err == nil && existed {
			continue
		}
		data, ok, err := src.Show(cand, f)
		switch {
		case err == nil && !ok:
			continue // deleted
		case err != nil:
			n++ // unreadable: still delivered
		default:
			c, _ := goTestFuncs(f, data)
			n += max(c, 1)
		}
	}
	return n
}

// goTestFuncs counts the top-level Test, Benchmark, Fuzz and Example
// funcs in a Go source file, or ok=false when it isn't one that parses.
func goTestFuncs(path string, data []byte) (int, bool) {
	if !strings.HasSuffix(path, ".go") {
		return 0, false
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && isTestName(fn.Name.Name) {
			n++
		}
	}
	return n, true
}

// isTestName is the go tool's rule: a prefix, then nothing or a character
// that isn't a lower-case letter.
func isTestName(name string) bool {
	for _, p := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if rest, ok := strings.CutPrefix(name, p); ok {
			r, _ := utf8.DecodeRuneInString(rest)
			return rest == "" || !unicode.IsLower(r)
		}
	}
	return false
}

func cleanAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, clean(s))
	}
	return out
}
