package oracle

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
)

// fakeSource is a commit's files.
type fakeSource map[string]string

func (s fakeSource) Files(string) ([]string, error) {
	var out []string
	for p := range s {
		out = append(out, p)
	}
	return out, nil
}

func (s fakeSource) Show(_, p string) ([]byte, bool, error) {
	b, ok := s[p]
	return []byte(b), ok, nil
}

func TestNewVersionAddsHeldOutTests(t *testing.T) {
	blobs, err := ledger.OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := fakeSource{"go.mod": "module fx\n", "add.go": "package fx\n", "add_test.go": "package fx\n"}
	v0 := &Manifest{Format: ManifestFormat, TestGlobs: []string{"**/*_test.go"},
		Tests: []File{{Path: "add_test.go", Blob: "b0"}}, Expected: []TestID{{"fx", "TestAdd"}}}
	adds := []Addition{
		{Path: "neg_test.go", Data: []byte("package fx\n\nimport \"testing\"\n\n// AC-1: negatives.\nfunc TestNeg(t *testing.T) {}\n\n// Covers AC-2 and AC-9.\nfunc TestBig(t *testing.T) {}\n\nfunc TestPlain(t *testing.T) {}\n")},
		// Same path as an Oracle test: kept under another name.
		{Path: "add_test.go", Data: []byte("package fx\n\nimport \"testing\"\n\nfunc TestZero(t *testing.T) {}\n")},
		// Redeclares an Oracle test: it could never compile, so it's left out.
		{Path: "dup_test.go", Data: []byte("package fx\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {}\n")},
		// Outside go test ./...: no Check command would run it.
		{Path: "testdata/x_test.go", Data: []byte("package x\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")},
	}
	m, id, dropped, err := NewVersion(v0, "parent", "verify#1", adds, src, "c1", []string{"AC-1", "AC-2"}, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || m.Parent != "parent" || id == "" {
		t.Fatalf("version %d parent %q id %q", m.Version, m.Parent, id)
	}
	var paths []string
	for _, f := range m.Tests {
		paths = append(paths, fmt.Sprintf("%s:%v", f.Path, f.HeldOut))
	}
	if want := []string{"add_test.go:false", "add_v1_test.go:true", "neg_test.go:true"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("tests %v, want %v", paths, want)
	}
	if want := []string{"add_v1_test.go", "neg_test.go"}; !reflect.DeepEqual(m.Added, want) {
		t.Errorf("added %v, want %v", m.Added, want)
	}
	var held []string
	for _, h := range m.HeldOut {
		held = append(held, fmt.Sprintf("%s %s %v", h.Test, h.File, h.Criteria))
	}
	want := []string{"fx.TestBig neg_test.go [AC-2]", "fx.TestNeg neg_test.go [AC-1]", "fx.TestPlain neg_test.go []", "fx.TestZero add_v1_test.go []"}
	if !reflect.DeepEqual(held, want) {
		t.Errorf("held-out %q\nwant %q", held, want)
	}
	if len(m.Expected) != 5 {
		t.Errorf("expected %v", m.Expected)
	}
	var why []string
	for _, d := range dropped {
		why = append(why, d.Path+": "+d.Why)
	}
	if len(why) != 2 || !strings.Contains(why[0], "dup_test.go: redeclares fx.TestAdd") || !strings.Contains(why[1], "testdata/x_test.go") {
		t.Errorf("dropped %q", why)
	}
	if got := m.Unmapped(); got != 2 {
		t.Errorf("unmapped %d, want 2", got)
	}
	// The source is only in blobs, never in the manifest.
	raw, _ := blobs.Get(id)
	if strings.Contains(string(raw), "func Test") {
		t.Error("the manifest holds test source")
	}
}

func TestHeldOutFailures(t *testing.T) {
	m := &Manifest{HeldOut: []HeldOut{
		{Test: TestID{"fx", "TestNeg"}, Criteria: []string{"AC-1"}},
		{Test: TestID{"fx", "TestBig"}, Criteria: []string{"AC-2"}},
		{Test: TestID{"fx", "TestGone"}},
	}}
	out := `{"Action":"run","Package":"fx","Test":"TestNeg"}
{"Action":"output","Package":"fx","Test":"TestNeg","Output":"    neg_test.go:7: Add(-2, 1) = 0, want -1\n"}
{"Action":"fail","Package":"fx","Test":"TestNeg"}
{"Action":"pass","Package":"fx","Test":"TestBig"}
{"Action":"fail","Package":"fx"}
`
	rep := ParseGoTestJSON([]byte(out))
	r := &Result{Commands: []Execution{{Report: &rep}}}
	var got []string
	for _, h := range m.HeldOutFailures(r) {
		got = append(got, h.Test.String())
	}
	if want := []string{"fx.TestNeg", "fx.TestGone"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if (&Manifest{}).HeldOutFailures(r) != nil {
		t.Error("no held-out tests, no failures")
	}
}

func TestSplitGoTestOutput(t *testing.T) {
	out := `{"Action":"output","Package":"fx","Test":"TestNeg","Output":"=== RUN   TestNeg\n"}
{"Action":"output","Package":"fx","Test":"TestNeg/sub","Output":"    neg_test.go:7: boom\n"}
{"Action":"output","Package":"fx","Output":"FAIL\tfx\t0.1s\n"}
not json
`
	var got []string
	for _, l := range SplitGoTestOutput([]byte(out)) {
		got = append(got, l.Test.String()+"|"+l.Text)
	}
	want := []string{"fx.TestNeg|=== RUN   TestNeg\n", "fx.TestNeg|    neg_test.go:7: boom\n", "fx.|FAIL\tfx\t0.1s\n"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// A verifier file that would change how every test runs, or adds no test,
// is QA's own defect: it never joins the Oracle (#46 review H1, M1).
func TestNewVersionScreensQAsDefects(t *testing.T) {
	blobs, err := ledger.OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v0 := &Manifest{Format: ManifestFormat, TestGlobs: []string{"**/*_test.go"}}
	hdr := "package fx\n\nimport \"testing\"\n\n"
	adds := []Addition{
		{Path: "main_test.go", Data: []byte(hdr + "func TestMain(m *testing.M) {}\nfunc TestA(t *testing.T) {}\n")},
		{Path: "init_test.go", Data: []byte(hdr + "func init() {}\nfunc TestB(t *testing.T) {}\n")},
		{Path: "helper_test.go", Data: []byte(hdr + "func check(t *testing.T) {}\n")},
		{Path: "broken_test.go", Data: []byte(hdr + "func TestC(")},
		{Path: "ok_test.go", Data: []byte(hdr + "func TestD(t *testing.T) {}\n")},
	}
	m, _, dropped, err := NewVersion(v0, "p", "verify#1", adds, fakeSource{"go.mod": "module fx\n"}, "c", nil, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Added, []string{"ok_test.go"}) {
		t.Errorf("added %v", m.Added)
	}
	why := map[string]string{}
	for _, d := range dropped {
		why[d.Path] = d.Why
	}
	for p, want := range map[string]string{"main_test.go": "declares TestMain", "init_test.go": "declares init",
		"helper_test.go": "declares no TestXxx", "broken_test.go": "doesn't parse"} {
		if !strings.Contains(why[p], want) {
			t.Errorf("%s: %q, want %q", p, why[p], want)
		}
	}
	if f := m.Tests[0]; len(f.Expected) != 1 || f.Expected[0].Name != "TestD" {
		t.Errorf("the held-out file attests nothing: %+v", f)
	}
	// Without takes a file back out, with its tests.
	w, _, err := m.Without([]string{"ok_test.go"}, blobs)
	if err != nil || len(w.Tests)+len(w.Added)+len(w.HeldOut)+len(w.Expected) != 0 {
		t.Errorf("Without left %+v (%v)", w, err)
	}
}

// Attestation, not the report, decides whether a held-out test passed.
func TestHeldOutFailuresTrustAttestation(t *testing.T) {
	m := &Manifest{HeldOut: []HeldOut{{Test: TestID{"fx", "TestNeg"}}}}
	out := `{"Action":"pass","Package":"fx","Test":"TestNeg"}` + "\n"
	rep := ParseGoTestJSON([]byte(out))
	r := &Result{Commands: []Execution{{Report: &rep}}, Tests: []TestResult{{TestID: TestID{"fx", "TestNeg"}, Attested: AttestExited}}}
	if got := m.HeldOutFailures(r); len(got) != 1 {
		t.Errorf("a forged PASS frame counted: %v", got)
	}
}

// A package whose build fails while it holds held-out tests is a
// conflict, not a held-out failure: nobody can tell whose fault it is
// (#46 re-review R1).
func TestHeldOutBuildConflicts(t *testing.T) {
	m := &Manifest{HeldOut: []HeldOut{
		{Test: TestID{"fx", "TestNeg"}, Criteria: []string{"AC-1"}},
		{Test: TestID{"fx/sub", "TestSub"}},
	}}
	out := `{"Action":"fail","Package":"fx","FailedBuild":"fx.test"}
{"Action":"fail","Package":"fx/sub","Test":"TestSub"}
`
	rep := ParseGoTestJSON([]byte(out))
	r := &Result{Commands: []Execution{{Report: &rep}}}
	if got := m.HeldOutBuildConflicts(r); !reflect.DeepEqual(got, []string{"fx"}) {
		t.Errorf("conflicts %v", got)
	}
	var failing []string
	for _, h := range m.HeldOutFailures(r) {
		failing = append(failing, h.Test.String())
	}
	if !reflect.DeepEqual(failing, []string{"fx/sub.TestSub"}) {
		t.Errorf("failures %v: a test in a package that didn't build isn't QA's finding", failing)
	}
}

// A top-level var whose initializer calls a function runs before every
// test in its package, as init does (#46 re-review R2).
func TestScreenRejectsInitializingVars(t *testing.T) {
	hdr := "package fx\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n"
	for src, bad := range map[string]bool{
		hdr + "var x = setup()\n":                     true,
		hdr + "var y = func() int { return 1 }()\n":   true,
		hdr + "var a, b = 1, f(2)\n":                  true,
		hdr + "var z = 3\n":                           false,
		hdr + "var w = []int{1, 2}\n":                 false,
		hdr + "var f = func() int { return 1 }\n":     false, // not called
		hdr + "func g() { var v = setup(); _ = v }\n": false, // not top level
	} {
		if got := screen([]byte(src)) != ""; got != bad {
			t.Errorf("screen(%q) rejects = %v, want %v", src[len(hdr):], got, bad)
		}
	}
}
