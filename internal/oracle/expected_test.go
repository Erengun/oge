package oracle

import (
	"fmt"
	"reflect"
	"testing"
)

func TestExpectedTestsComeFromTheOracle(t *testing.T) {
	snap := map[string]string{
		"go.mod":              "module example.com/fx // the root\n\ngo 1.22\n",
		"add_test.go":         "package fx\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {}\nfunc Test(t *testing.T) {}\nfunc Testable(t *testing.T) {}\nfunc TestMain(m *testing.M) {}\nfunc BenchmarkX(b *testing.B) {}\nfunc helper(t *testing.T) {}\ntype s struct{}\nfunc (s) TestM(t *testing.T) {}\n",
		"sub/x_test.go":       "package sub_test\n\nimport tt \"testing\"\n\nfunc TestX(t *tt.T) {}\n",
		"mod2/go.mod":         "module \"example.com/two\"\n",
		"mod2/deep/y_test.go": "package deep\n\nimport \"testing\"\n\nfunc TestY(t *testing.T) {}\n",
		"broken_test.go":      "package fx\n\nfunc TestBroken(",
		"testdata/t_test.go":  "package t\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) {}\n",
		"_old/o_test.go":      "package o\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) {}\n",
	}
	var files, tests []string
	for p := range snap {
		files = append(files, p)
		if MatchAny([]string{"**/*_test.go"}, p) {
			tests = append(tests, p)
		}
	}
	got, byFile := expectedTests(tests, files, func(p string) ([]byte, error) {
		s, ok := snap[p]
		if !ok {
			return nil, fmt.Errorf("no %s", p)
		}
		return []byte(s), nil
	})
	// testdata, _old and the nested module mod2 are outside ./...
	want := []TestID{{"example.com/fx", "Test"}, {"example.com/fx", "TestAdd"}, {"example.com/fx/sub", "TestX"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if want := []TestID{{"example.com/fx/sub", "TestX"}}; !reflect.DeepEqual(byFile["sub/x_test.go"], want) {
		t.Errorf("sub/x_test.go declares %v, want %v", byFile["sub/x_test.go"], want)
	}
}
