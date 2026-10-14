package run

import (
	"errors"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// A delivered cut moves the Task: the next cut's turns are only those
// after it (D7), and the Chat log records the delivery once.
func TestDeliveredMovesTheTask(t *testing.T) {
	sub := cutAdd + "\nfunc Sub(a, b int) int { return a - b }\n"
	c := openCut(t, "",
		multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: sub}}},
		multiturn.Turn{Reply: "and mul", Writes: []multiturn.File{{Path: "mul.go", Content: "package fx\n\nfunc Mul(a, b int) int { return a * b }\n"}}})
	drain(t, send(t, c, "add Sub"), nil)
	first := cut(t, c)
	if first.Outcome != Accepted {
		t.Fatalf("outcome %s %v", first.Outcome, first.Why)
	}
	if err := c.Delivered("no-such-run", "apply", ""); err == nil {
		t.Error("delivering a Run that is no cut of the Chat was taken")
	}
	if err := c.Delivered(first.ID, "apply", first.Candidate); err != nil {
		t.Fatal(err)
	}
	if err := c.Delivered(first.ID, "branch", first.Candidate); err != nil {
		t.Fatal(err) // delivered again: nothing moves
	}
	drain(t, send(t, c, "now Mul"), nil)
	second := cut(t, c)
	started := runRecords(t, second.Dir, RecRunStarted)[0]
	hashes, _ := started["task_turns"].([]any)
	if len(hashes) != 1 || hashes[0].(map[string]any)["sha256"] != sha("now Mul") {
		t.Errorf("task_turns %v, want the turn after the delivery only", started["task_turns"])
	}
	recs := runRecords(t, c.Dir, RecDelivered)
	if len(recs) != 1 || recs[0]["run"] != first.ID || recs[0]["turn"] != float64(1) || recs[0]["how"] != "apply" {
		t.Errorf("Delivered records %v", recs)
	}
	if c.Applied() != first.Candidate {
		t.Errorf("Applied %q, want the first cut's Candidate", c.Applied())
	}
}

// A late Tamper event on a cut's turn, recorded after the cut was
// verified, refuses delivering it, and the Chat cuts its turns again: the
// next cut, with no new turn, carries the event to its Gate.
func TestDeliverableRefusesALateTamper(t *testing.T) {
	sub := cutAdd + "\nfunc Sub(a, b int) int { return a - b }\n"
	c := openCut(t, "", multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: sub}}})
	drain(t, send(t, c, "add Sub"), nil)
	first := cut(t, c)
	if first.Outcome != Accepted {
		t.Fatalf("first cut %s %v", first.Outcome, first.Why)
	}
	if err := c.Deliverable(first.ID, first.Dir); err != nil {
		t.Fatalf("deliverable before the late event: %v", err)
	}
	writeIn(t, c.Workspace, "add_test.go", "package fx\n")
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	_ = sess.Close()
	select {
	case <-c.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the Session never ended")
	}
	if err := c.Deliverable(first.ID, first.Dir); !errors.Is(err, ErrTamperAfterCut) {
		t.Fatalf("deliverable after a late Tamper event: %v", err)
	}
	again := cut(t, c)
	if tamper := runRecords(t, again.Dir, RecTamperEvent); again.Outcome != Parked || len(tamper) != 1 {
		t.Errorf("the cut again %s %v, TamperEvent %v", again.Outcome, again.Why, tamper)
	}
}
