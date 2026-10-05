package run

import (
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
)

// A send-back names visible failures in full, held-out ones only as a
// count plus criterion ids (ADR-0009).
func TestSendBackKeepsHeldOutTestsOut(t *testing.T) {
	blobs, err := ledger.OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := `{"Action":"run","Package":"fx","Test":"TestNegate"}
{"Action":"output","Package":"fx","Test":"TestNegate","Output":"    add_test.go:9: Negate(1) = 1, want -1\n"}
{"Action":"fail","Package":"fx","Test":"TestNegate"}
{"Action":"run","Package":"fx","Test":"TestNeg"}
{"Action":"output","Package":"fx","Test":"TestNeg","Output":"    secret_test.go:7: Add(-2, 1) = 0, want -1\n"}
{"Action":"fail","Package":"fx","Test":"TestNeg"}
{"Action":"output","Package":"fx","Output":"panic in fx.TestNeg at secret_test.go:7\n"}
{"Action":"output","Package":"fx","Output":"fx.checkSecret(0x1)\n"}
{"Action":"output","Package":"fx","Output":"\t/tmp/x/add_v2_test.go:3 +0x1\n"}
{"Action":"output","Package":"fx","Output":"FAIL\tfx\t0.1s\n"}
{"Action":"fail","Package":"fx"}
`
	id, _ := blobs.Put([]byte(out))
	rep := oracle.ParseGoTestJSON([]byte(out))
	cr := &oracle.Result{Why: "Oracle tests that never passed (2): fx.TestGone, fx.TestHidden", Missing: []string{"fx.TestGone", "fx.TestHidden"},
		Commands: []oracle.Execution{{Run: "go test -json ./...", Why: "2 failed", Report: &rep, Stdout: oracle.Output{Blob: id}}}}
	helper, _ := blobs.Put([]byte("package fx\n\nimport \"testing\"\n\nfunc checkSecret(t *testing.T) {}\n\nfunc TestHidden(t *testing.T) { checkSecret(t) }\n"))
	m := &oracle.Manifest{HeldOut: []oracle.HeldOut{
		{Test: oracle.TestID{Package: "fx", Name: "TestNeg"}, File: "secret_test.go", Criteria: []string{"AC-1"}},
		{Test: oracle.TestID{Package: "fx", Name: "TestHidden"}, File: "add_v2_test.go"},
	}, Tests: []oracle.File{{Path: "add_v2_test.go", Blob: helper, HeldOut: true}}}
	got := sendBackTurn(cr, blobs, nil, m)
	for _, want := range []string{"TestNegate", "Negate(1) = 1, want -1", "FAIL\tfx", "fx.TestGone",
		"2 held-out tests failed: AC-1 ×1, unmapped ×1.", "Öge left it out"} {
		if !strings.Contains(got, want) {
			t.Errorf("the send-back lacks %q:\n%s", want, got)
		}
	}
	for _, never := range []string{"TestNeg ", "TestNeg\n", "TestHidden", "secret_test.go", "Add(-2, 1)", "checkSecret", "add_v2_test.go"} {
		if strings.Contains(got, never) {
			t.Errorf("the send-back reveals %q:\n%s", never, got)
		}
	}
	// With no held-out tests, it is exactly the Fast-mode send-back.
	if a, b := sendBackTurn(cr, blobs, nil, &oracle.Manifest{}), sendBackTurn(cr, blobs, nil, nil); a != b || !strings.Contains(a, "TestHidden") {
		t.Errorf("Fast-mode send-backs differ:\n%s\n---\n%s", a, b)
	}
}

// When a package holding held-out tests no longer builds, the
// implementer is told of a conflict, never of a held-out failure.
func TestSendBackNamesAHeldOutBuildConflictNeutrally(t *testing.T) {
	blobs, err := ledger.OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := `{"Action":"output","Package":"fx","Output":"# fx [fx.test]\n"}
{"Action":"output","Package":"fx","Output":"./neg_test.go:9:2: undefined: Sub\n"}
{"Action":"fail","Package":"fx","FailedBuild":"fx.test"}
`
	id, _ := blobs.Put([]byte(out))
	rep := oracle.ParseGoTestJSON([]byte(out))
	cr := &oracle.Result{Commands: []oracle.Execution{{Run: "go test -json ./...", Why: "exit 1", Report: &rep, Stdout: oracle.Output{Blob: id}}}}
	m := &oracle.Manifest{HeldOut: []oracle.HeldOut{{Test: oracle.TestID{Package: "fx", Name: "TestNeg"}, File: "neg_test.go", Criteria: []string{"AC-1"}}}}
	got := sendBackTurn(cr, blobs, nil, m)
	if !strings.Contains(got, "Your change conflicts with a held-out test in package fx (names withheld).") {
		t.Errorf("no conflict line:\n%s", got)
	}
	for _, never := range []string{"held-out test failed", "neg_test.go", "Sub"} {
		if strings.Contains(got, never) {
			t.Errorf("the send-back says %q:\n%s", never, got)
		}
	}
}

// A held-out helper's name is filtered only when it is held-out-only: a
// common name the visible Oracle or the Candidate also uses never hides a
// visible failure (#46 re-review).
func TestSendBackKeepsVisibleLinesWithCommonHelperNames(t *testing.T) {
	blobs, err := ledger.OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := `{"Action":"output","Package":"fx","Test":"TestAdd","Output":"    add_test.go:9: check failed: Add(2, 3) = 0\n"}
{"Action":"output","Package":"fx","Test":"TestAdd","Output":"    add_test.go:12: helperY says no\n"}
{"Action":"fail","Package":"fx","Test":"TestAdd"}
{"Action":"output","Package":"fx","Output":"fx.secretOnly(0x1)\n"}
`
	id, _ := blobs.Put([]byte(out))
	rep := oracle.ParseGoTestJSON([]byte(out))
	cr := &oracle.Result{Commands: []oracle.Execution{{Run: "go test -json ./...", Why: "1 failed", Report: &rep, Stdout: oracle.Output{Blob: id}}}}
	held, _ := blobs.Put([]byte("package fx\n\nimport \"testing\"\n\nfunc check() {}\nfunc helperY() {}\nfunc secretOnly() {}\n\nfunc TestNeg(t *testing.T) {}\n"))
	visible, _ := blobs.Put([]byte("package fx\n\nimport \"testing\"\n\nfunc check() {}\n\nfunc TestAdd(t *testing.T) {}\n"))
	m := &oracle.Manifest{HeldOut: []oracle.HeldOut{{Test: oracle.TestID{Package: "fx", Name: "TestNeg"}, File: "neg_test.go"}},
		Tests: []oracle.File{{Path: "add_test.go", Blob: visible}, {Path: "neg_test.go", Blob: held, HeldOut: true}}}
	candidate := func() []string { return []string{"package fx\n\nfunc helperY() {}\n"} }
	got := sendBackTurnFrom(cr, blobs, nil, m, candidate)
	for _, want := range []string{"check failed", "helperY says no"} {
		if !strings.Contains(got, want) {
			t.Errorf("a visible line was hidden: %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "secretOnly") {
		t.Errorf("a held-out-only helper leaked:\n%s", got)
	}
}
