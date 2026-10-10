package run

import (
	"reflect"
	"strings"
	"testing"
)

const additiveBase = `//go:build linux

package fx

import "testing"

// TestAdd checks Add.
func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add(2, 3) != 5")
	}
}

var cases = []int{1, 2}

func TestSub(t *testing.T) {
	if Sub(3, 2) != 1 {
		t.Fatal("Sub")
	}
}
`

const testZero = `
func TestZero(t *testing.T) {
	if Add(0, 0) != 0 {
		t.Fatal("zero")
	}
}
`

func TestAdditiveTestEdit(t *testing.T) {
	addFunc := strings.Index(additiveBase, "var cases")
	sub := strings.Index(additiveBase, "func TestSub")
	for _, c := range []struct {
		name  string
		path  string
		after string
		added []string // nil: not additive
	}{
		{"append a test", "x_test.go", additiveBase + testZero, []string{"TestZero"}},
		{"append a benchmark, a fuzz target and a helper", "x_test.go", additiveBase +
			"\nfunc BenchmarkAdd(b *testing.B) {}\n\nfunc FuzzAdd(f *testing.F) {}\n\nfunc helper() {}\n", []string{"BenchmarkAdd", "FuzzAdd"}},
		{"insert between declarations", "x_test.go", additiveBase[:addFunc] + strings.TrimPrefix(testZero, "\n") + "\n" + additiveBase[addFunc:], []string{"TestZero"}},
		{"add an import", "x_test.go", strings.Replace(additiveBase, `import "testing"`, "import (\n\t\"fmt\"\n\t\"testing\"\n)", 1) +
			"\nfunc ExampleAdd() {\n\tfmt.Println(Add(1, 1))\n\t// Output: 2\n}\n", []string{"ExampleAdd"}},
		{"reorder declarations", "x_test.go", additiveBase[:strings.Index(additiveBase, "// TestAdd")] + additiveBase[sub:] + "\n" +
			additiveBase[strings.Index(additiveBase, "// TestAdd"):sub], []string{}},
		{"comment-only change", "x_test.go", strings.Replace(additiveBase, "// TestAdd checks Add.", "// TestAdd checks Add, which sums.\n// More words.", 1) +
			"\n// A trailing note.\n", []string{}},
		{"gofmt-only change", "x_test.go", strings.NewReplacer("if Add(2, 3) != 5 {", "if Add(2,3)!=5 {", "\t\tt.Fatal(\"Sub\")", "  t.Fatal(\"Sub\")").Replace(additiveBase), []string{}},

		{"t.Skip inserted in an existing test", "x_test.go", strings.Replace(additiveBase, "func TestSub(t *testing.T) {\n", "func TestSub(t *testing.T) {\n\tt.Skip()\n", 1), nil},
		{"an assertion deleted", "x_test.go", strings.Replace(additiveBase, "\tif Sub(3, 2) != 1 {\n\t\tt.Fatal(\"Sub\")\n\t}\n", "", 1), nil},
		{"a comment inside a test changed", "x_test.go", strings.Replace(additiveBase, "\tif Sub(3, 2) != 1 {", "\t// hmm\n\tif Sub(3, 2) != 1 {", 1), nil},
		{"a build tag added", "x_test.go", strings.Replace(additiveBase, "//go:build linux", "//go:build linux && slow", 1), nil},
		{"a build tag removed", "x_test.go", strings.Replace(additiveBase, "//go:build linux\n\n", "", 1), nil},
		{"a //go:debug line added", "x_test.go", strings.Replace(additiveBase, "//go:build linux\n", "//go:build linux\n//go:debug panicnil=1\n", 1), nil},
		{"//go:embed added to an existing var", "x_test.go", strings.Replace(additiveBase, "var cases", "//go:embed cases.txt\nvar cases", 1), nil},
		{"TestMain added", "x_test.go", additiveBase + "\nfunc TestMain(m *testing.M) {}\n", nil},
		{"init added", "x_test.go", additiveBase + "\nfunc init() {}\n", nil},
		{"package clause changed", "x_test.go", strings.Replace(additiveBase, "package fx", "package fx_test", 1), nil},
		{"an import renamed", "x_test.go", strings.Replace(additiveBase, `import "testing"`, `import testing "testing"`, 1), nil},
		{"parse failure", "x_test.go", additiveBase + "\nfunc TestBroken(t *testing.T) {\n", nil},
		{"a non-Go protected file", "testdata/golden.txt", additiveBase + testZero, nil},
		{"a Go file that is not a test", "x.go", additiveBase + testZero, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			added, ok := additiveTestEdit(c.path, []byte(additiveBase), []byte(c.after))
			if ok != (c.added != nil) {
				t.Fatalf("additive = %v, want %v", ok, c.added != nil)
			}
			if ok && len(added)+len(c.added) > 0 && !reflect.DeepEqual(added, c.added) {
				t.Errorf("added = %v, want %v", added, c.added)
			}
		})
	}
}

func TestAdditiveTestEditKeepsDuplicateDeclarations(t *testing.T) {
	before := "package fx\n\nfunc init() {}\n\nfunc init() {}\n"
	if _, ok := additiveTestEdit("x_test.go", []byte(before), []byte("package fx\n\nfunc init() {}\n")); ok {
		t.Error("dropping one of two identical declarations was additive")
	}
	if _, ok := additiveTestEdit("x_test.go", []byte(before), []byte(before+"\nfunc TestX(t *testing.T) {}\n")); !ok {
		t.Error("an addition beside existing init funcs was not additive")
	}
}
