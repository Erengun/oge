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
