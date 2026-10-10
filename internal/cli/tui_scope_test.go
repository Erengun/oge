package cli

import (
	"strings"
	"testing"

	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// A reverted Oracle test shows as one line under the implementer, and the
// Run parks instead of being Accepted.
func TestTUIParkedOnATamperEvent(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.att.Reverted = []workspace.Revert{{Path: "add_test.go", Class: run.ClassOracleTest, Tamper: true, Change: "modified"}}
	h.implemented("")
	h.checked(true)
	h.res.Gate = "gate.tamper"
	got := h.end(run.Parked, "Öge's Check passed, but an Attempt wrote to a protected file. Öge reverted it and recorded a Tamper event, which must be acknowledged before the Run can be Accepted.")
	golden(t, "parked", got)
	if !strings.Contains(got, "scope 1 protected test change reverted: add_test.go") || strings.Contains(got, "✓ Accepted") {
		t.Errorf("frame:\n%s", got)
	}
}

// Kept additions to an Oracle test file show as one line under the
// implementer, and the Receipt says what was kept and that it wasn't run
// (#119).
func TestTUIAcceptedWithKeptTestAdditions(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.att.Kept = []run.KeptTest{{Path: "add_test.go", Class: run.ClassOracleTestAddition, Added: []string{"TestAddZero", "TestAddNeg"}}}
	h.implemented("")
	h.checked(true)
	got := h.end(run.Accepted)
	golden(t, "accepted-kept-additions", got)
	for _, want := range []string{"scope kept 2 test additions in add_test.go", "2 test additions kept in add_test.go", "2 implementer-authored tests delivered, not run by Öge"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "no test changes kept") {
		t.Errorf("stale Protected line:\n%s", got)
	}
}

func TestScopeTextKeptAdditions(t *testing.T) {
	a := &run.Attempt{Kept: []run.KeptTest{{Path: "a_test.go", Added: []string{"TestA"}}}}
	if got, want := scopeText(a), "kept 1 test addition in a_test.go"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	a.Reverted = []workspace.Revert{{Path: "b_test.go", Class: run.ClassOracleTest, Tamper: true}}
	if got := scopeText(a); !strings.Contains(got, "reverted: b_test.go · kept 1 test addition in a_test.go") {
		t.Errorf("got %q", got)
	}
}
