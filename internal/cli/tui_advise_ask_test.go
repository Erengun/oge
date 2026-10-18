package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
)

func TestSuggestedOption(t *testing.T) {
	opts := []string{"Redis", "in-memory"}
	for _, tc := range []struct {
		text string
		want int
	}{
		{"2. keep the map; it is simpler", 2},
		{"Option 1 because", 1},
		{"**1** Redis", 1},
		{"in-memory\nit is simpler", 2},
		{"3. out of range", 0},
		{"It depends on scale.", 0},
		{"", 0},
	} {
		if got := suggestedOption(tc.text, opts); got != tc.want {
			t.Errorf("%q: %d, want %d", tc.text, got, tc.want)
		}
	}
}

// askHome is a home with one question ask shown, and the answers it got.
func askHome(got *[]agent.Answer) (*home, *agent.Ask) {
	a := agent.NewAsk("AskUserQuestion", "q", "Which?", func(ans agent.Answer) error {
		*got = append(*got, ans)
		return nil
	})
	a.Questions = []agent.AskQuestion{{Text: "Which?", Options: []string{"red", "blue"}}}
	h := &home{width: 80, height: 24, st: newStyles(false), comp: newComposer(), turn: &chatTurn{}}
	h.turn.asks = []*agent.Ask{a}
	h.showAsk()
	return h, a
}

// Advice only highlights: the answer is the human's, and it records the
// Consultation next to who answered.
func TestAskAdviceHighlightsAndRecordsAdvisedBy(t *testing.T) {
	var got []agent.Answer
	h, a := askHome(&got)
	h.turn.askAdv = &askAdvice{ask: a, cancel: func() {}, running: true}
	h.askAdviceLanded(askAdviceMsg{ask: a, advice: &run.Advice{ID: "c-1", Text: "2. blue is safer"}})
	if got := h.turn.askAdv.suggest; got != 2 {
		t.Fatalf("suggest %d", got)
	}
	if len(got) != 0 {
		t.Fatalf("the advice answered: %+v", got)
	}
	v := h.turnView()
	if !strings.Contains(v, "2. blue   ← advisor suggests") || !strings.Contains(v, "blue is safer") {
		t.Errorf("view:\n%s", v)
	}
	h.comp.set("1")
	h.chatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(got) != 1 || !got[0].Allow || got[0].Answers[0] != "red" || got[0].AdvisedBy != "c-1" {
		t.Errorf("answer %+v", got)
	}
}

// A failed, empty or cancelled consult leaves the ask as it was, and
// advice that lands after its ask went is discarded.
func TestAskAdviceFailuresAndLateAdvice(t *testing.T) {
	for name, m := range map[string]askAdviceMsg{
		"failed":    {err: errors.New("boom")},
		"empty":     {advice: &run.Advice{ID: "c", Text: "  "}},
		"cancelled": {err: context.Canceled},
	} {
		var got []agent.Answer
		h, a := askHome(&got)
		h.turn.askAdv = &askAdvice{ask: a, cancel: func() {}, running: true}
		m.ask = a
		h.askAdviceLanded(m)
		if h.turn.askAdv != nil || h.turn.advisedBy != "" || strings.Contains(h.turnView(), "suggests") {
			t.Errorf("%s: left %+v", name, h.turn.askAdv)
		}
		h.comp.set("blue")
		h.chatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		if len(got) != 1 || got[0].AdvisedBy != "" {
			t.Errorf("%s: answer %+v", name, got)
		}
	}

	var got []agent.Answer
	h, a := askHome(&got)
	cancelled := false
	h.turn.askAdv = &askAdvice{ask: a, cancel: func() { cancelled = true }, running: true}
	h.turn.asks = nil // withdrawn, or answered elsewhere
	h.showAsk()
	if !cancelled || h.turn.askAdv != nil {
		t.Errorf("the consult outlived its ask")
	}
	h.askAdviceLanded(askAdviceMsg{ask: a, advice: &run.Advice{ID: "late", Text: "1"}})
	if h.turn.askAdv != nil || h.turn.advisedBy != "" {
		t.Errorf("late advice kept: %+v", h.turn)
	}
}

// Esc cancels a running consult and not the ask; the next Esc is the ask's.
func TestAskAdviceEscCancelsTheConsult(t *testing.T) {
	var got []agent.Answer
	h, a := askHome(&got)
	cancelled := false
	h.turn.askAdv = &askAdvice{ask: a, cancel: func() { cancelled = true }, running: true}
	h.chatKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !cancelled || h.turn.askAdv != nil || h.turn.stopped || h.ask() != a {
		t.Errorf("cancelled %v, askAdv %v, stopped %v", cancelled, h.turn.askAdv, h.turn.stopped)
	}
}
