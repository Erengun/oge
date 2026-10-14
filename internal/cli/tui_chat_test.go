package cli

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// A turn's cut runs in the Pipeline the turn was sent in, so a Shift+Tab
// to Standard after the Chat opened in Fast gets QA (B3).
func TestChatCutUsesTheTurnsPipeline(t *testing.T) {
	standard := &pipeline.Frozen{Mode: pipeline.Standard}
	var got *pipeline.Frozen
	h := &home{chat: &homeChat{cut: func(_ context.Context, _ *run.Chat, f *pipeline.Frozen, _ gate.Port) (*run.Result, error) {
		got = f
		return nil, run.ErrNothingToCut
	}}, turn: &chatTurn{frozen: standard, changed: 1}}
	cmd := h.startCut()
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

// A cut's end moves h.last only with a Verdict, decided from its Run,
// not from Ctrl+C: one that reached its Verdict first shows it. Either
// way the action row closes, so [a] never takes a cut not yet seen.
func TestCutEndedDecidesFromTheRun(t *testing.T) {
	older, newer := &lastRun{id: "older"}, &lastRun{id: "newer"}
	for _, tc := range []struct {
		name    string
		res     *run.Result
		want    *lastRun
		printed string
	}{
		{"verdict after ctrl+c", &run.Result{Outcome: run.Accepted, Check: &oracle.Result{Pass: true}}, newer, "✓ shown"},
		{"cancelled", &run.Result{Outcome: run.InfrastructureStop}, nil, "verification cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &home{st: newStyles(false), comp: newComposer(), last: older, actions: true,
				chat:   &homeChat{verdict: func(*run.Result, int) (string, *lastRun) { return "✓ shown", newer }},
				verify: &chatVerify{seq: 1, cancel: func() {}, stopped: true}}
			h.cutEnded(chatCutMsg{seq: 1, res: tc.res})
			if h.last != tc.want || h.actions || h.verify != nil {
				t.Errorf("last %v, actions %v, verify %v", h.last, h.actions, h.verify)
			}
			if got := strings.Join(h.scroll, "\n"); !strings.Contains(got, tc.printed) {
				t.Errorf("printed %q, want %q", got, tc.printed)
			}
		})
	}
	// A superseded cut, or one /new let go, ends unshown.
	h := &home{st: newStyles(false), comp: newComposer(), last: older, verify: &chatVerify{seq: 2, cancel: func() {}}}
	h.cutEnded(chatCutMsg{seq: 1, res: &run.Result{Outcome: run.Accepted, Check: &oracle.Result{Pass: true}}})
	h.cutEnded(chatCutMsg{seq: 2, err: run.ErrSuperseded})
	if h.last != older || len(h.scroll) != 0 {
		t.Errorf("a superseded cut showed: last %v, %q", h.last, h.scroll)
	}
}

// A command in the composer when a question takes it is never queued for
// the agent: it is back in the composer when the turn ends.
func TestShowAskKeepsASlashCommandFromTheAgent(t *testing.T) {
	a := agent.NewAsk("AskUserQuestion", "", "", func(agent.Answer) error { return nil })
	a.Questions = []agent.AskQuestion{{Text: "Which?"}}
	h := &home{st: newStyles(false), turn: &chatTurn{asks: []*agent.Ask{a}},
		resolve: func(*setupState) (*resolved, []string) { return nil, nil }}
	h.comp = h.composer()
	h.comp.set("/apply")
	h.showAsk()
	if len(h.turn.queued) != 0 || h.comp.text() != "" {
		t.Fatalf("queued %q, composer %q", h.turn.queued, h.comp.text())
	}
	if queued := h.endTurn(); len(queued) != 0 || h.comp.text() != "/apply" {
		t.Errorf("after the turn: queued %q, composer %q", queued, h.comp.text())
	}
}
