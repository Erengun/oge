package receipt_test

import (
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/receipt"
	"github.com/erengun/oge/internal/receipt/receipttest"
	"github.com/erengun/oge/internal/run"
)

const ms = time.Millisecond

func usageReceipt(u any) *receipt.Receipt {
	f := map[string]any{"chat": "c-1", "turns": map[string]int{"from": 1, "to": 4}}
	if u != nil {
		f["usage"] = u
	}
	b := receipttest.NewCut(pipeline.Fast, f)
	b.Attempt(receipttest.Attempt{ID: "implement#1", From: 500 * ms, To: 6100 * ms, Candidate: receipttest.C1, Changed: []string{"add.go"}, Exit: "done"})
	b.Check(1, receipttest.C1, 0, 6200*ms, 7200*ms, []receipttest.Test{{Name: "TestAdd", Attested: "pass"}}, nil)
	b.End(7300*ms, run.Accepted, receipttest.C1)
	return b.Receipt()
}

// Turns by kind always show for a Chat cut; tokens only when every turn
// reported them, and a partial report is said, never summed.
func TestReceiptUsage(t *testing.T) {
	for _, c := range []struct {
		name  string
		usage any
		want  []string
		not   []string
	}{
		{"all reported", run.CutUsage{You: 2, Feedback: 1, Advice: 1, Reported: 4, Tokens: 12345},
			[]string{"4 turns (you 2, feedback 1, advice 1) · 12345 tokens"}, []string{"partial"}},
		{"partial", run.CutUsage{You: 2, Feedback: 1, Advice: 1, Reported: 3, Tokens: 999},
			[]string{"4 turns (you 2, feedback 1, advice 1) · tokens: partial (3 of 4 turns reported)"}, []string{"999"}},
		{"none reported", run.CutUsage{You: 1}, []string{"1 turn (you 1, feedback 0, advice 0)"}, []string{"tokens"}},
		{"an old Ledger", nil, nil, []string{"feedback 0", "tokens"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := usageReceipt(c.usage)
			js, _ := r.JSON()
			for name, got := range map[string]string{"text": r.Text(receipt.Paint{}), "md": r.Markdown(), "json": string(js)} {
				for _, w := range c.want {
					if name != "json" && !strings.Contains(got, w) {
						t.Errorf("%s lacks %q:\n%s", name, w, got)
					}
				}
				for _, n := range c.not {
					if name == "json" {
						continue
					}
					if strings.Contains(got, n) {
						t.Errorf("%s has %q:\n%s", name, n, got)
					}
				}
			}
			if c.usage == nil && strings.Contains(string(js), `"usage"`) {
				t.Errorf("JSON has usage with none recorded: %s", js)
			}
			if c.name != "all reported" && strings.Contains(string(js), `"tokens"`) {
				t.Errorf("JSON has tokens without a full report: %s", js)
			}
			if c.name == "all reported" && !strings.Contains(string(js), `"tokens": 12345`) {
				t.Errorf("JSON lacks the tokens: %s", js)
			}
		})
	}
}

// A one-shot Run has no Chat, so no usage line.
func TestReceiptUsageOneShotUnchanged(t *testing.T) {
	r := scenario(t, "accepted-delivered").Receipt()
	if r.Chat != nil || strings.Contains(r.Text(receipt.Paint{}), "turns (") {
		t.Errorf("a one-shot Run shows Chat usage:\n%s", r.Text(receipt.Paint{}))
	}
}
