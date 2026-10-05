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
	}
	var files, tests []string
	for p := range snap {
		files = append(files, p)
		if MatchAny([]string{"**/*_test.go"}, p) {
			tests = append(tests, p)
		}
	}
	got := expectedTests(tests, files, func(p string) ([]byte, error) {
		s, ok := snap[p]
		if !ok {
			return nil, fmt.Errorf("no %s", p)
		}
		return []byte(s), nil
	})
	want := []TestID{
		{"example.com/fx", "Test"}, {"example.com/fx", "TestAdd"},
		{"example.com/fx/sub", "TestX"}, {"example.com/two/deep", "TestY"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestMissingTests(t *testing.T) {
	rep := ParseGoTestJSON([]byte(passing))
	for _, c := range []struct {
		id      TestID
		missing bool
	}{
		{TestID{"fx", "TestAdd"}, false},
		{TestID{"", "TestAdd"}, false},
		{TestID{"other", "TestAdd"}, true},
		{TestID{"fx", "TestSub"}, true}, // skipped
		{TestID{"fx", "TestGone"}, true},
	} {
		if got := len(missingTests([]TestID{c.id}, []*Report{&rep})) == 1; got != c.missing {
			t.Errorf("%v: missing = %v, want %v", c.id, got, c.missing)
		}
	}
}
