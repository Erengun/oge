package run

import (
	"testing"

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
	if err := c.Delivered("no-such-run", "apply"); err == nil {
		t.Error("delivering a Run that is no cut of the Chat was taken")
	}
	if err := c.Delivered(first.ID, "apply"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delivered(first.ID, "branch"); err != nil {
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
}
