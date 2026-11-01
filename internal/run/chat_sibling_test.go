package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/workspace"
)

const siblingX = "package fx\n\nfunc X() int { return 1 }\n"

// sibling stands in for another Chat's apply on c's repository: it
// writes files there and tells c what it wrote.
func sibling(t *testing.T, c *Chat, files map[string]string) (Refresh, error) {
	t.Helper()
	var wrote []Wrote
	for p, s := range files {
		if err := os.WriteFile(filepath.Join(c.source, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		wrote = append(wrote, Wrote{Path: p, Mode: "100644", OID: workspace.BlobOID([]byte(s))})
	}
	return c.NoteDelivered(wrote)
}

// appliedSub opens a Chat whose first turn, adding Sub, is cut, applied
// and retaken as the base.
func appliedSub(t *testing.T, more ...multiturn.Turn) *Chat {
	t.Helper()
	turns := append([]multiturn.Turn{{Reply: "sub", Writes: []multiturn.File{{Path: "add.go", Content: refreshSub}, {Path: "sub_test.go", Content: refreshTest}}}}, more...)
	c := openCut(t, "", turns...)
	drain(t, send(t, c, "add Sub"), nil)
	first := cut(t, c)
	applyTo(t, c, first, map[string]string{"add.go": refreshSub, "sub_test.go": refreshTest})
	if got, err := c.RefreshSnapshot(); err != nil || got != RefreshDone {
		t.Fatalf("own refresh: %v %v", got, err)
	}
	return c
}

func wsHas(t *testing.T, c *Chat, rel, want string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(c.Workspace, rel)); err != nil || string(b) != want {
		t.Errorf("Workspace %s: %q %v, want %q", rel, b, err, want)
	}
}

// Another Chat's apply is Öge's own work: the Snapshot is retaken on it,
// the Workspace holds it, and the next turn's changes are its own only.
func TestSiblingApplyIsRetakenOn(t *testing.T) {
	c := appliedSub(t, multiturn.Turn{Reply: "mul", Writes: []multiturn.File{{Path: "mul.go", Content: refreshMul}}})
	old := c.snapshot()
	if got, err := sibling(t, c, map[string]string{"x.go": siblingX}); err != nil || got != RefreshDone {
		t.Fatalf("sibling apply: %v %v, want a retake", got, err)
	}
	if c.snapshot() == old {
		t.Fatal("the base didn't move")
	}
	wsHas(t, c, "x.go", siblingX)
	recs := refreshed(t, c)
	if len(recs) != 2 || !reflect.DeepEqual(recs[1]["sibling"], []any{"x.go"}) {
		t.Errorf("SnapshotRefreshed %v", recs)
	}
	drain(t, send(t, c, "now Mul"), nil)
	changed, err := c.repo.ChangedFiles(c.snapshot(), turnRef(t, c, 2))
	if err != nil || !reflect.DeepEqual(changed, []string{"mul.go"}) {
		t.Errorf("changed against the new Snapshot %v %v, want mul.go only", changed, err)
	}
	if tam := runRecords(t, c.Dir, RecTamperEvent); len(tam) != 0 {
		t.Errorf("TamperEvents %v", tam)
	}
}

// Chat A applies x.go before Chat B applies its own: B's refresh after
// its apply retakes cleanly, with no outside note.
func TestSiblingThenOwnApplyRetakesCleanly(t *testing.T) {
	c := openRefresh(t)
	old := c.snapshot()
	drain(t, send(t, c, "add Sub"), nil)
	first := cut(t, c)
	if got, err := sibling(t, c, map[string]string{"x.go": siblingX}); err != nil || got != RefreshNone {
		t.Fatalf("before its own apply: %v %v, want nothing to do", got, err)
	}
	if c.snapshot() != old {
		t.Fatal("the base moved before the Chat's own apply")
	}
	applyTo(t, c, first, map[string]string{"add.go": refreshSub, "sub_test.go": refreshTest})
	if got, err := c.RefreshSnapshot(); err != nil || got != RefreshDone {
		t.Fatalf("own refresh: %v %v, want a retake", got, err)
	}
	wsHas(t, c, "x.go", siblingX)
	if tree := func(rev string) string { s, _ := c.repo.TreeOf(rev); return s }; tree(c.last) != tree(c.snapshot()) {
		t.Error("the Workspace's commit isn't the new base")
	}
}

// A user's edit beside a sibling's apply, or over it, is still outside
// Öge: never merged, and nothing moves or is written.
func TestSiblingApplyWithAnOutsideEdit(t *testing.T) {
	for name, outside := range map[string]map[string]string{
		"another file": {"mine.go": "package fx\n"},
		"same file":    {"x.go": siblingX + "// mine\n"},
	} {
		t.Run(name, func(t *testing.T) {
			c := appliedSub(t)
			old := c.snapshot()
			if _, err := sibling(t, c, map[string]string{"x.go": siblingX}); err != nil {
				t.Fatal(err)
			}
			old = c.snapshot()
			for p, s := range outside {
				if err := os.WriteFile(filepath.Join(c.source, p), []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := c.RefreshSnapshot(); err != nil || got != RefreshOutside {
				t.Fatalf("%v %v, want RefreshOutside", got, err)
			}
			if c.snapshot() != old || len(refreshed(t, c)) != 2 {
				t.Error("the base moved on an outside change")
			}
			for p := range outside {
				if p == "x.go" {
					wsHas(t, c, p, siblingX)
				} else if _, err := os.Stat(filepath.Join(c.Workspace, p)); err == nil {
					t.Errorf("the user's %s is in the Workspace", p)
				}
			}
		})
	}
}

// Before any retake, the user's edit and a sibling's apply together read
// as outside too.
func TestSiblingApplyAndUserEditTogether(t *testing.T) {
	c := appliedSub(t)
	old := c.snapshot()
	if err := os.WriteFile(filepath.Join(c.source, "mine.go"), []byte("package fx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := sibling(t, c, map[string]string{"x.go": siblingX}); err != nil || got != RefreshOutside {
		t.Fatalf("%v %v, want RefreshOutside", got, err)
	}
	if c.snapshot() != old {
		t.Error("the base moved")
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "x.go")); err == nil {
		t.Error("the sibling's file was written into the Workspace")
	}
}

// A sibling's apply to a file this Chat has edited since its own apply is
// a conflict: nothing is merged, written or moved. One to another file
// carries the Chat's edit onto the new base.
func TestSiblingApplyOverPendingEdits(t *testing.T) {
	mine := "package fx\n\nfunc Mul(a, b int) int { return b * a }\n"
	for _, overlap := range []bool{true, false} {
		c := appliedSub(t, multiturn.Turn{Reply: "mul", Writes: []multiturn.File{{Path: "mul.go", Content: mine}}})
		drain(t, send(t, c, "add Mul"), nil) // pending: not cut or applied
		old, last := c.snapshot(), c.last
		files := map[string]string{"x.go": siblingX}
		if overlap {
			files = map[string]string{"mul.go": refreshMul}
		}
		got, err := sibling(t, c, files)
		if overlap {
			if err != nil || got != RefreshConflict {
				t.Fatalf("overlap: %v %v, want RefreshConflict", got, err)
			}
			if c.snapshot() != old || c.last != last || len(refreshed(t, c)) != 1 {
				t.Error("overlap: the base or the Workspace's commit moved")
			}
			wsHas(t, c, "mul.go", mine)
			continue
		}
		if err != nil || got != RefreshDone {
			t.Fatalf("disjoint: %v %v, want a retake", got, err)
		}
		wsHas(t, c, "x.go", siblingX)
		wsHas(t, c, "mul.go", mine)
		changed, err := c.repo.ChangedFiles(c.snapshot(), c.last)
		if err != nil || !reflect.DeepEqual(changed, []string{"mul.go"}) {
			t.Errorf("pending on the new base %v %v, want mul.go only", changed, err)
		}
	}
}

// A cut in flight, or a turn, defers the retake until it settles.
func TestSiblingApplyWaitsForACutOrTurn(t *testing.T) {
	t.Run("cut", func(t *testing.T) {
		c := appliedSub(t)
		old := c.snapshot()
		c.cutMu.Lock()
		got, err := sibling(t, c, map[string]string{"x.go": siblingX})
		c.cutMu.Unlock()
		if err != nil || got != RefreshDeferred || c.snapshot() != old {
			t.Fatalf("%v %v, want deferred with the base kept", got, err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30e9)
		defer cancel()
		if _, err := c.Cut(ctx, CutParams{}); !errors.Is(err, ErrNothingToCut) {
			t.Fatalf("cut %v", err)
		}
		if c.snapshot() == old {
			t.Error("the deferred retake didn't run when the cut settled")
		}
		wsHas(t, c, "x.go", siblingX)
	})
	t.Run("turn", func(t *testing.T) {
		c := appliedSub(t)
		old := c.snapshot()
		out := newTurnOut()
		c.mu.Lock()
		c.cur = out // a turn in flight
		c.mu.Unlock()
		got, err := sibling(t, c, map[string]string{"x.go": siblingX})
		if err != nil || got != RefreshDeferred || c.snapshot() != old || len(refreshed(t, c)) != 1 {
			t.Fatalf("%v %v, want deferred with the base kept", got, err)
		}
		if _, err := os.Stat(filepath.Join(c.Workspace, "x.go")); err == nil {
			t.Error("written into the Workspace under a turn in flight")
		}
		c.settled(out)
		if c.snapshot() == old {
			t.Error("the deferred retake didn't run when the turn settled")
		}
		wsHas(t, c, "x.go", siblingX)
	})
}
