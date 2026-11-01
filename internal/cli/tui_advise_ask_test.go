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
	h := &home{width: 80, height: 24, st: newStyles(false), chatView: &chatView{comp: newComposer(), turn: &chatTurn{}}}
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

// A settled turn closes its asks, and a consult for one goes with it.
func TestAskAdviceDroppedWhenTheTurnSettles(t *testing.T) {
	var got []agent.Answer
	h, a := askHome(&got)
	cancelled := false
	h.turn.askAdv = &askAdvice{ask: a, cancel: func() { cancelled = true }, running: true}
	h.chatEvent(agent.Event{Kind: agent.TurnSettled})
	if !cancelled || h.turn.askAdv != nil {
		t.Errorf("the consult outlived its turn")
	}
}

// /advise is refused while the ask's consult runs: one Consultation at a time.
func TestAdviseRefusedDuringAskConsult(t *testing.T) {
	var got []agent.Answer
	h, a := askHome(&got)
	h.chat = &homeChat{}
	h.turn.askAdv = &askAdvice{ask: a, cancel: func() {}, running: true}
	if _, msg := h.adviseCmd([]string{"how?"}); !strings.Contains(msg, "already advising") && !strings.Contains(msg, "no Chat") {
		t.Errorf("msg %q", msg)
	}
}

// "always" is text at a question; only "a" and enter is the advisor key.
func TestAskAdviceKeyIsAEnter(t *testing.T) {
	var got []agent.Answer
	h, _ := askHome(&got)
	h.chatKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if h.comp.text() != "a" || h.turn.askAdv != nil {
		t.Errorf("typing a: box %q askAdv %v", h.comp.text(), h.turn.askAdv)
	}
	h.comp.set("always")
	h.chatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(got) != 1 || got[0].Answers[0] != "always" || h.turn.askAdv != nil {
		t.Errorf("answer %+v", got)
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

// A tick of an earlier consult (Esc, then the key again on the same
// question) is dropped: only the current consult's tick re-arms.
func TestAskAdviceStaleTickDoesNotRearm(t *testing.T) {
	var got []agent.Answer
	h, a := askHome(&got)
	h.turn.askAdv = &askAdvice{ask: a, q: 0, seq: 2, cancel: func() {}, running: true}
	if _, cmd := h.Update(askAdviceTickMsg{ask: a, q: 0, seq: 1}); cmd != nil {
		t.Error("a stale tick re-armed")
	}
	if _, cmd := h.Update(askAdviceTickMsg{ask: a, q: 0, seq: 2}); cmd == nil {
		t.Error("the current tick did not re-arm")
	}
}

// A cancelled consult 1 landing after consult 2 began (Esc, then the key
// again) leaves consult 2's state alone.
func TestAskAdviceStaleLandingKeepsNewerConsult(t *testing.T) {
	var got []agent.Answer
	h, a := askHome(&got)
	cancelled := false
	h.turn.askAdv = &askAdvice{ask: a, seq: 2, cancel: func() { cancelled = true }, running: true}
	h.askAdviceLanded(askAdviceMsg{ask: a, seq: 1, err: context.Canceled})
	if h.turn.askAdv == nil || !h.turn.askAdv.running || h.turn.askAdv.seq != 2 || cancelled {
		t.Errorf("consult 2 lost: %+v", h.turn.askAdv)
	}
}

// AdviceShown is logged only when the advice is displayed: not for a
// failed, empty, cancelled or stale (discarded) landing.
func TestAskAdviceLoggedShownOnlyWhenDisplayed(t *testing.T) {
	var logged []string
	setup := func() (*home, *agent.Ask) {
		var got []agent.Answer
		h, a := askHome(&got)
		h.chat = &homeChat{c: &run.Chat{}, shown: func(_ *run.Chat, id string) { logged = append(logged, id) }}
		h.turn.askAdv = &askAdvice{ask: a, cancel: func() {}, running: true}
		return h, a
	}
	for name, m := range map[string]askAdviceMsg{
		"failed":    {err: errors.New("boom")},
		"empty":     {advice: &run.Advice{ID: "e", Text: " "}},
		"cancelled": {err: context.Canceled},
	} {
		h, a := setup()
		m.ask = a
		h.askAdviceLanded(m)
		if len(logged) != 0 {
			t.Fatalf("%s: logged %v", name, logged)
		}
	}
	h, a := setup()
	h.turn.askAdv.seq = 1
	h.askAdviceLanded(askAdviceMsg{ask: a, seq: 2, advice: &run.Advice{ID: "stale", Text: "1"}})
	if len(logged) != 0 {
		t.Fatalf("stale: logged %v", logged)
	}
	h.askAdviceLanded(askAdviceMsg{ask: a, seq: 1, advice: &run.Advice{ID: "k-1", Text: "2. blue"}})
	if len(logged) != 1 || logged[0] != "k-1" {
		t.Fatalf("displayed: logged %v", logged)
	}
}
