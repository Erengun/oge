package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

const (
	refreshSub  = cutAdd + "\nfunc Sub(a, b int) int { return a - b }\n"
	refreshTest = "package fx\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n\tif Sub(5, 3) != 2 {\n\t\tt.Fatal(\"Sub\")\n\t}\n}\n"
	refreshMul  = "package fx\n\nfunc Mul(a, b int) int { return a * b }\n"
)

// openRefresh opens a Chat whose turns add Sub with a test, then Mul.
func openRefresh(t *testing.T) *Chat {
	t.Helper()
	return openCut(t, "",
		multiturn.Turn{Reply: "sub", Writes: []multiturn.File{{Path: "add.go", Content: refreshSub}, {Path: "sub_test.go", Content: refreshTest}}},
		multiturn.Turn{Reply: "mul", Writes: []multiturn.File{{Path: "mul.go", Content: refreshMul}}})
}

// applyTo stands in for /apply: it writes the files into the user's tree
// and marks the cut delivered, as chatApply does.
func applyTo(t *testing.T, c *Chat, res *Result, files map[string]string) {
	t.Helper()
	for p, s := range files {
		if err := os.WriteFile(filepath.Join(c.source, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Delivered(res.ID, "apply", res.Candidate); err != nil {
		t.Fatal(err)
	}
}

func refreshed(t *testing.T, c *Chat) []map[string]any {
	t.Helper()
	return runRecords(t, c.Dir, RecSnapshotRefreshed)
}

// After an apply that left the user's tree as delivered, the Snapshot is
// retaken: Oracle v0 and the protected set are the new base's, scope and
// diffs are relative to it, and the next apply still merges on the last
// applied commit.
func TestRefreshAfterApplyRetakesTheBase(t *testing.T) {
	c := openRefresh(t)
	old, oldV0 := c.Snapshot, c.v0
	drain(t, send(t, c, "add Sub"), nil)
	first := cut(t, c)
	if first.Outcome != Accepted {
		t.Fatalf("outcome %s %v", first.Outcome, first.Why)
	}
	if got, err := c.RefreshSnapshot(); err != nil || got != RefreshNone {
		t.Fatalf("before any apply: %v %v, want nothing to do", got, err)
	}
	applyTo(t, c, first, map[string]string{"add.go": refreshSub, "sub_test.go": refreshTest})
	got, err := c.RefreshSnapshot()
	if err != nil || got != RefreshDone {
		t.Fatalf("refresh: %v %v", got, err)
	}
	if c.Snapshot == old || c.v0 == oldV0 {
		t.Error("the Snapshot and Oracle v0 are still the old base's")
	}
	recs := refreshed(t, c)
	if len(recs) != 1 || recs[0]["commit"] != c.Snapshot || recs[0]["prev"] != old || recs[0]["manifest"] != c.v0 || recs[0]["tests"] != float64(2) {
		t.Errorf("SnapshotRefreshed %v", recs)
	}
	if got, _ := c.RefreshSnapshot(); got != RefreshNone || len(refreshed(t, c)) != 1 {
		t.Errorf("a second refresh on the same tree: %v, want nothing to do and no new record", got)
	}
	// The delivered test is part of the base now: protected, and the
	// Implementer's tampering with it is caught.
	if c.protected("sub_test.go") == "" {
		t.Error("a delivered test isn't protected on the new base")
	}
	drain(t, send(t, c, "now Mul"), nil)
	changed, err := c.repo.ChangedFiles(c.Snapshot, turnRef(t, c, 2))
	if err != nil || !reflect.DeepEqual(changed, []string{"mul.go"}) {
		t.Errorf("changed against the new Snapshot %v %v, want mul.go only", changed, err)
	}
	second := cut(t, c)
	if second.Outcome != Accepted {
		t.Fatalf("second cut %s %v", second.Outcome, second.Why)
	}
	// Delivery base is the last applied Candidate, as before the retake.
	if rev, dir := c.Applied(); rev != first.Candidate || dir != first.Dir {
		t.Errorf("Applied %q in %q after a retake, want the first cut's Candidate", rev, dir)
	}
	applyTo(t, c, second, map[string]string{"mul.go": refreshMul})
	if got, err := c.RefreshSnapshot(); err != nil || got != RefreshDone {
		t.Fatalf("second refresh: %v %v", got, err)
	}
	if rev, _ := c.Applied(); rev != second.Candidate || len(refreshed(t, c)) != 2 {
		t.Errorf("Applied %q, records %d", rev, len(refreshed(t, c)))
	}
}

// A repo edited outside Öge, committed or not, is never merged into the
// base: its files don't become the Implementer's, and nothing moves.
func TestRefreshOutsideChangeIsNeverMerged(t *testing.T) {
	for _, commit := range []bool{false, true} {
		c := openRefresh(t)
		old, oldV0 := c.Snapshot, c.v0
		drain(t, send(t, c, "add Sub"), nil)
		first := cut(t, c)
		mine := "package fx\n\nimport \"testing\"\n\nfunc TestMine(t *testing.T) {}\n"
		applyTo(t, c, first, map[string]string{"add.go": refreshSub, "sub_test.go": refreshTest, "mine_test.go": mine})
		if commit {
			f := &followFixture{user: c.source}
			f.git(t, "add", "-A")
			f.git(t, "commit", "-q", "-m", "mine")
		}
		got, err := c.RefreshSnapshot()
		if err != nil || got != RefreshOutside {
			t.Fatalf("commit=%v: %v %v, want RefreshOutside", commit, got, err)
		}
		if c.Snapshot != old || c.v0 != oldV0 || len(refreshed(t, c)) != 0 {
			t.Errorf("commit=%v: the base moved on an outside change", commit)
		}
		drain(t, send(t, c, "now Mul"), nil)
		changed, _ := c.repo.ChangedFiles(c.Snapshot, turnRef(t, c, 2))
		for _, p := range changed {
			if p == "mine_test.go" {
				t.Errorf("commit=%v: the user's file is in the Implementer's changes", commit)
			}
		}
		if b, _ := os.ReadFile(filepath.Join(c.source, "mine_test.go")); string(b) != mine {
			t.Errorf("commit=%v: the user's file was touched", commit)
		}
	}
}

// A cut in flight keeps its old base: the retake waits until the cuts
// settle, then runs.
func TestRefreshWaitsForACutInFlight(t *testing.T) {
	c := openRefresh(t)
	old := c.Snapshot
	drain(t, send(t, c, "add Sub"), nil)
	first := cut(t, c)
	applyTo(t, c, first, map[string]string{"add.go": refreshSub, "sub_test.go": refreshTest})
	c.cutMu.Lock() // a cut is in flight
	got, err := c.RefreshSnapshot()
	if err != nil || got != RefreshDeferred {
		t.Fatalf("refresh with a cut in flight: %v %v, want deferred", got, err)
	}
	if c.snapshot() != old || len(refreshed(t, c)) != 0 {
		t.Error("the base moved under a cut in flight")
	}
	c.cutMu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 30e9)
	defer cancel()
	if _, err := c.Cut(ctx, CutParams{}); !errors.Is(err, ErrNothingToCut) {
		t.Fatalf("cut %v", err)
	}
	if c.snapshot() == old || len(refreshed(t, c)) != 1 {
		t.Error("the deferred retake didn't run when the cut settled")
	}
}

// A retake changes the base, not the chain: the feedback budget, the
// chain and the automatic Consultation stay as the user's turn left them.
func TestRefreshLeavesTheChainAlone(t *testing.T) {
	c := openRefresh(t)
	drain(t, send(t, c, "add Sub"), nil)
	first := cut(t, c)
	applyTo(t, c, first, map[string]string{"add.go": refreshSub, "sub_test.go": refreshTest})
	c.mu.Lock()
	c.feedbacks, c.autos = 2, 1
	chain := c.chain
	c.mu.Unlock()
	if got, err := c.RefreshSnapshot(); err != nil || got != RefreshDone {
		t.Fatalf("refresh: %v %v", got, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.feedbacks != 2 || c.autos != 1 || c.chain != chain {
		t.Errorf("feedbacks %d autos %d chain %d, want 2 1 %d", c.feedbacks, c.autos, c.chain, chain)
	}
}

// An undelivered turn after the applied cut leaves the Workspace unlike
// the delivered tree: no retake, or its edit to a file the new base
// protects would read as Tamper on the next turn.
func TestRefreshSkipsWithUndeliveredTurns(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		c := openCut(t, "",
			multiturn.Turn{Reply: "sub", Writes: []multiturn.File{{Path: "add.go", Content: refreshSub}, {Path: "sub_test.go", Content: refreshTest}}},
			multiturn.Turn{Reply: "rewrote", Writes: []multiturn.File{{Path: "sub_test.go", Content: refreshTest + "\n// rewritten\n"}}},
			multiturn.Turn{Reply: "mul", Writes: []multiturn.File{{Path: "mul.go", Content: refreshMul}}})
		old := c.Snapshot
		drain(t, send(t, c, "add Sub"), nil)
		first := cut(t, c)
		drain(t, send(t, c, "touch the test"), nil)
		applyTo(t, c, first, map[string]string{"add.go": refreshSub, "sub_test.go": refreshTest})
		if deferred {
			c.cutMu.Lock() // a cut is in flight at /apply
			if got, _ := c.RefreshSnapshot(); got != RefreshDeferred {
				t.Fatalf("got %v, want deferred", got)
			}
			c.cutMu.Unlock()
			cut(t, c) // settles; the retry must not retake either
		} else if got, err := c.RefreshSnapshot(); err != nil || got != RefreshNone {
			t.Fatalf("refresh with an undelivered turn: %v %v, want none", got, err)
		}
		if c.snapshot() != old || len(refreshed(t, c)) != 0 {
			t.Errorf("deferred=%v: the base moved with an undelivered turn", deferred)
		}
		drain(t, send(t, c, "now Mul"), nil)
		if tam := runRecords(t, c.Dir, RecTamperEvent); len(tam) != 0 {
			t.Errorf("deferred=%v: TamperEvents %v after a skipped retake", deferred, tam)
		}
	}
}
