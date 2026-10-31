package run

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
)

func rec(typ string, d map[string]any) ledger.Record {
	b, _ := json.Marshal(d)
	return ledger.Record{Type: typ, Data: b}
}

func ended(turn int, tokens any) ledger.Record {
	d := map[string]any{"chat": "c", "turn": turn}
	if tokens != "none" {
		d["usage"] = map[string]any{"cost_usd": nil, "tokens": tokens}
	}
	return rec(RecAttemptEnded, d)
}

// usageIn counts turns by the Chat log's records and sums tokens only
// from the turns that reported them.
func TestUsageInClassifiesFromRecords(t *testing.T) {
	recs := []ledger.Record{
		rec(RecTurnStarted, map[string]any{"chat": "c", "turn": 1}), ended(1, 100),
		rec(RecTurnStarted, map[string]any{"chat": "c", "turn": 2, "by": "oge"}), ended(2, 50),
		rec(RecTurnStarted, map[string]any{"chat": "c", "turn": 3, "by": "oge", "consultation": "k"}),
		rec(RecAdviceSent, map[string]any{"chat": "c", "turn": 3, "consultation": "k", "how": "verbatim", "by": "oge"}), ended(3, 25),
		rec(RecTurnStarted, map[string]any{"chat": "c", "turn": 4, "consultation": "k2"}), // edited advice: the user's
		rec(RecAdviceSent, map[string]any{"chat": "c", "turn": 4, "consultation": "k2", "how": "edited", "by": "user"}), ended(4, "none"),
		rec(RecAttemptEnded, map[string]any{"chat": "c", "consultation": "k", "usage": map[string]any{"tokens": 9999}}), // a Consultation, not a turn
		rec(RecTurnStarted, map[string]any{"chat": "c", "turn": 5}), ended(5, 7),                                        // outside the range
	}
	recs = append(recs,
		rec(RecTurnStarted, map[string]any{"chat": "c", "turn": 6, "by": "oge"}), // Send failed: never reached the agent
		rec(RecAttemptEnded, map[string]any{"chat": "c", "turn": 6, "failure": "send_failed: broken pipe"}))
	got := usageIn(recs, "c", 1, 4)
	want := CutUsage{You: 2, Feedback: 1, Advice: 1, Reported: 3, Tokens: 175}
	if got != want {
		t.Errorf("usageIn = %+v, want %+v", got, want)
	}
	if u := usageIn(recs, "c", 6, 6); u != (CutUsage{}) {
		t.Errorf("an unsent turn: %+v", u)
	}
	if u := usageIn(recs, "c", 2, 2); u != (CutUsage{Feedback: 1, Reported: 1, Tokens: 50}) {
		t.Errorf("one turn: %+v", u)
	}
}

// A Chat on the multi-turn fake, through the cut: the Task's turns since
// the last delivery are counted (both cuts' turns), and the tokens are
// summed only when every turn reported.
func TestCutRecordsChainUsageFromTheFake(t *testing.T) {
	sub := cutAdd + "\nfunc Sub(a, b int) int { return a - b }\n"
	mul := multiturn.File{Path: "mul.go", Content: "package fx\n\nfunc Mul(a, b int) int { return a * b }\n"}
	c := openCut(t, "",
		multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: sub}}, Usage: &multiturn.Usage{Tokens: 100}},
		multiturn.Turn{Reply: "and mul", Writes: []multiturn.File{mul}, Usage: &multiturn.Usage{Tokens: 40}})
	drain(t, send(t, c, "add Sub"), nil)
	cut(t, c)
	drain(t, send(t, c, "now Mul"), nil)
	second := cut(t, c)
	u, _ := runRecords(t, second.Dir, RecRunStarted)[0]["usage"].(map[string]any)
	want := map[string]any{"you": float64(2), "feedback": float64(0), "advice": float64(0), "reported": float64(2), "tokens": float64(140)}
	if !reflect.DeepEqual(u, want) {
		t.Errorf("usage %v, want %v", u, want)
	}
}

// One turn that reported nothing: the count of reports says so, no sum.
func TestCutUsagePartialWhenATurnReportedNothing(t *testing.T) {
	sub := cutAdd + "\nfunc Sub(a, b int) int { return a - b }\n"
	c := openCut(t, "",
		multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: sub}}, Usage: &multiturn.Usage{Tokens: 100}},
		multiturn.Turn{Reply: "more"})
	drain(t, send(t, c, "add Sub"), nil)
	drain(t, send(t, c, "and more"), nil)
	u, _ := runRecords(t, cut(t, c).Dir, RecRunStarted)[0]["usage"].(map[string]any)
	if u["you"] != float64(2) || u["reported"] != float64(1) {
		t.Errorf("usage %v", u)
	}
}
