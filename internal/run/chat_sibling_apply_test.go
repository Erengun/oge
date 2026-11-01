package run_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/run"
)

// A cut made on a Snapshot retaken on a sibling Chat's apply merges on
// that Snapshot: the sibling's file is never this Chat's write, so the
// user's delete or edit of it stays, and the apply writes the cut's own
// change only.
func TestSiblingRetakeThenApply(t *testing.T) {
	for name, user := range map[string]func(t *testing.T, x string){
		"delete": func(t *testing.T, x string) {
			if err := os.Remove(x); err != nil {
				t.Fatal(err)
			}
		},
		"edit": func(t *testing.T, x string) {
			if err := os.WriteFile(x, []byte(run.SiblingX+"// mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, res, source := run.SiblingRetakeCut(t)
			x := filepath.Join(source, "x.go")
			user(t, x)
			before, _ := os.ReadFile(x)
			r, err := delivery.Load(res.Dir)
			if err != nil {
				t.Fatal(err)
			}
			var base delivery.Base
			base.Rev, base.From = c.ApplyBase(r.Snapshot)
			a, err := delivery.ApplyOnto(r, source, delivery.Choice{}, base)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := a.Plan.Paths(); !reflect.DeepEqual(got, []string{"mul.go"}) {
				t.Errorf("written %v, want mul.go only", got)
			}
			if after, _ := os.ReadFile(x); string(after) != string(before) {
				t.Errorf("x.go is %q after the apply, want the user's %q", after, before)
			}
			if err := c.Delivered(res.ID, "apply", a.Held.Rev); err != nil {
				t.Fatal(err)
			}
			if got, err := c.RefreshSnapshot(); err != nil || got != run.RefreshOutside {
				t.Errorf("refresh %v %v, want RefreshOutside: the user's change to x.go is outside Öge", got, err)
			}
		})
	}
}

// A sibling's later apply deleting what the retake took, while this
// Chat's cut on it is pending: the apply keeps the delete, and the
// refresh after it takes the delete as Öge's.
func TestSiblingDeleteThenApply(t *testing.T) {
	c, res, source := run.SiblingRetakeCut(t)
	if err := os.Remove(filepath.Join(source, "x.go")); err != nil {
		t.Fatal(err)
	}
	if got, err := c.NoteDelivered([]run.Wrote{{Path: "x.go"}}); err != nil || got != run.RefreshNone {
		t.Fatalf("sibling delete: %v %v, want the base kept for the pending cut", got, err)
	}
	r, err := delivery.Load(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var base delivery.Base
	base.Rev, base.From = c.ApplyBase(r.Snapshot)
	a, err := delivery.ApplyOnto(r, source, delivery.Choice{}, base)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := a.Plan.Paths(); !reflect.DeepEqual(got, []string{"mul.go"}) {
		t.Errorf("written %v, want mul.go only", got)
	}
	if _, err := os.Lstat(filepath.Join(source, "x.go")); err == nil {
		t.Error("the apply brought x.go back")
	}
	if err := c.Delivered(res.ID, "apply", a.Held.Rev); err != nil {
		t.Fatal(err)
	}
	if got, err := c.RefreshSnapshot(); err != nil || got != run.RefreshDone {
		t.Errorf("refresh %v %v, want RefreshDone: the delete is the sibling's", got, err)
	}
	if _, err := os.Lstat(filepath.Join(c.Workspace, "x.go")); err == nil {
		t.Error("x.go is still in the Workspace")
	}
}
