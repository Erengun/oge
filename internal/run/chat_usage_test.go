package run

import (
	"encoding/json"
	"testing"

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
		rec(RecTurnStarted, map[string]any{"chat": "c", "turn": 5}), ended(5, 7), // outside the range
	}
	got := usageIn(recs, "c", 1, 4)
	want := CutUsage{You: 2, Feedback: 1, Advice: 1, Reported: 3, Tokens: 175}
	if got != want {
		t.Errorf("usageIn = %+v, want %+v", got, want)
	}
	if u := usageIn(recs, "c", 2, 2); u != (CutUsage{Feedback: 1, Reported: 1, Tokens: 50}) {
		t.Errorf("one turn: %+v", u)
	}
}
