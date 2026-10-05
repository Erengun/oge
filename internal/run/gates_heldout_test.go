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
{"Action":"output","Package":"fx","Output":"FAIL\tfx\t0.1s\n"}
{"Action":"fail","Package":"fx"}
`
	id, _ := blobs.Put([]byte(out))
	rep := oracle.ParseGoTestJSON([]byte(out))
	cr := &oracle.Result{Why: "Oracle tests that never passed (2): fx.TestGone, fx.TestHidden", Missing: []string{"fx.TestGone", "fx.TestHidden"},
		Commands: []oracle.Execution{{Run: "go test -json ./...", Why: "2 failed", Report: &rep, Stdout: oracle.Output{Blob: id}}}}
	m := &oracle.Manifest{HeldOut: []oracle.HeldOut{
		{Test: oracle.TestID{Package: "fx", Name: "TestNeg"}, File: "secret_test.go", Criteria: []string{"AC-1"}},
		{Test: oracle.TestID{Package: "fx", Name: "TestHidden"}, File: "secret_test.go"},
	}}
	got := sendBackTurn(cr, blobs, nil, m)
	for _, want := range []string{"TestNegate", "Negate(1) = 1, want -1", "FAIL\tfx", "fx.TestGone",
		"2 held-out tests failed: AC-1 ×1, unmapped ×1.", "Öge left it out"} {
		if !strings.Contains(got, want) {
			t.Errorf("the send-back lacks %q:\n%s", want, got)
		}
	}
	for _, never := range []string{"TestNeg ", "TestNeg\n", "TestHidden", "secret_test.go", "Add(-2, 1)"} {
		if strings.Contains(got, never) {
			t.Errorf("the send-back reveals %q:\n%s", never, got)
		}
	}
	// With no held-out tests, it is exactly the Fast-mode send-back.
	if a, b := sendBackTurn(cr, blobs, nil, &oracle.Manifest{}), sendBackTurn(cr, blobs, nil, nil); a != b || !strings.Contains(a, "TestHidden") {
		t.Errorf("Fast-mode send-backs differ:\n%s\n---\n%s", a, b)
	}
}
