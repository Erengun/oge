package cli

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// A turn's cut runs in the Pipeline the turn was sent in, so a Shift+Tab
// to Standard after the Chat opened in Fast gets QA (B3).
func TestChatCutUsesTheTurnsPipeline(t *testing.T) {
	standard := &pipeline.Frozen{Mode: pipeline.Standard}
	var got *pipeline.Frozen
	h := &home{chat: &homeChat{cut: func(_ context.Context, _ *run.Chat, f *pipeline.Frozen) (*run.Result, error) {
		got = f
		return nil, run.ErrNothingToCut
	}}, turn: &chatTurn{frozen: standard, changed: 1}}
	_, cmd := h.turnEnded()
	if cmd == nil {
		t.Fatal("a turn with changes wasn't cut")
	}
	if _, ok := cmd().(chatCutMsg); !ok || got != standard {
		t.Errorf("the cut ran in %v, not the turn's Pipeline", got)
	}
}

// A cut refused for setup drift says so, and how to go on.
func TestVerdictLineSetupDrift(t *testing.T) {
	res := &run.Result{Outcome: run.Refused, Why: []string{run.WhySetupChanged}}
	if got, want := verdictLine(res, 1), "✗ Refused: setup changed since this Chat opened; /new to start a new Chat"; got != want {
		t.Errorf("verdictLine = %q, want %q", got, want)
	}
}

// An ask's rows are wrapped by display width: no row is wider than the
// frame, with wide characters too. When a resize leaves the prompt too
// tall to show, y does nothing and the prompt says why.
func TestAskRowsFitTheFrame(t *testing.T) {
	var got []agent.Answer
	a := agent.NewAsk("Bash", "echo", "echo "+strings.Repeat("漢字😀", 12), func(ans agent.Answer) error {
		got = append(got, ans)
		return nil
	})
	h := &home{width: 30, height: 24, st: newStyles(false), turn: &chatTurn{asks: []*agent.Ask{a}}}
	for _, r := range h.askRows(a) {
		if w := ansi.StringWidth("    " + r); w > h.width {
			t.Errorf("row %q is %d wide in a %d-wide frame", r, w, h.width)
		}
	}
	if !h.askFits(a) {
		t.Fatal("the ask doesn't fit at 30 columns")
	}
	h.width = 12 // more than 8 rows now
	if h.askFits(a) {
		t.Fatal("the ask fits at 12 columns")
	}
	if v := h.turnView(); !strings.Contains(v, "too long to show at this width") {
		t.Errorf("the prompt doesn't say why y is off:\n%s", v)
	}
	h.chatKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(got) != 0 || h.ask() != a {
		t.Errorf("y approved what wasn't shown: %+v", got)
	}
}

// AskUserQuestion's questions are asked one at a time, each with its own
// numbers, and answered together; what was typed before is queued.
func TestQuestionsOneAtATime(t *testing.T) {
	var got []agent.Answer
	a := agent.NewAsk("AskUserQuestion", "a question", "Which? Name?", func(ans agent.Answer) error {
		got = append(got, ans)
		return nil
	})
	a.Questions = []agent.AskQuestion{{Text: "Which?", Options: []string{"red", "blue"}}, {Text: "Name?", Options: []string{"Ada"}}}
	h := &home{width: 80, height: 24, st: newStyles(false), comp: newComposer(), turn: &chatTurn{}}
	h.comp.set("a draft")
	h.turn.asks = []*agent.Ask{a}
	h.showAsk()
	if h.comp.text() != "" || len(h.turn.queued) != 1 || h.turn.queued[0] != "a draft" {
		t.Fatalf("the draft wasn't queued: %q, %q", h.comp.text(), h.turn.queued)
	}
	if v := h.turnView(); !strings.Contains(v, "  Which?") || strings.Contains(v, "Name?") || strings.Contains(v, "oge › Which?") {
		t.Errorf("first question view:\n%s", v)
	}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	h.comp.set("2")
	h.chatKey(enter)
	if v := h.turnView(); !strings.Contains(v, "Name?") {
		t.Errorf("second question not shown:\n%s", v)
	}
	h.comp.set("Grace")
	h.chatKey(enter)
	if len(got) != 1 || !got[0].Allow || strings.Join(got[0].Answers, ",") != "blue,Grace" {
		t.Errorf("answers %+v", got)
	}
}
