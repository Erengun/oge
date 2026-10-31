package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/run"
)

// twoChats is a home with two Chats, a focused, each with a turn and a cut
// verifying; cancelled counts each cut's cancels.
func twoChats() (h *home, a, b *chatView, cancelled map[*chatView]int) {
	cancelled = map[*chatView]int{}
	view := func() *chatView {
		v := &chatView{turn: &chatTurn{}, chat: &homeChat{}}
		v.verify = &chatVerify{seq: 1, changed: 2, cancel: func() { cancelled[v]++ }}
		return v
	}
	a, b = view(), view()
	h = &home{width: 80, height: 24, st: newStyles(false), comp: newComposer(), chatView: a, views: []*chatView{a, b}}
	return h, a, b, cancelled
}

func printed(v *chatView) string { return strings.Join(v.scroll, "\n") }

// Each Chat's messages change that Chat only, whichever one has the
// focus, and the focus stays where it was.
func TestChatMessagesGoToTheirChat(t *testing.T) {
	h, a, b, _ := twoChats()
	claim := func(text string) agent.Event { return agent.Event{Kind: agent.Claim, Text: text} }
	steps := []struct {
		msg   tea.Msg
		check func() bool
	}{
		{chatEvMsg{view: b, ev: claim("from b"), ok: true}, func() bool {
			return strings.Contains(printed(b), "from b") && printed(a) == ""
		}},
		{chatStepMsg{view: a, seq: 1, ev: run.Event{Kind: run.EvStage, Step: "checks"}}, func() bool {
			return a.verify.step == "running the Checks" && b.verify.step == ""
		}},
		{chatEvMsg{view: a, ev: claim("from a"), ok: true}, func() bool {
			return strings.Contains(printed(a), "from a") && !strings.Contains(printed(b), "from a")
		}},
		{chatCutMsg{view: b, seq: 1, err: run.ErrSuperseded}, func() bool {
			return b.verify == nil && a.verify != nil
		}},
		{chatStepMsg{view: b, seq: 1, ev: run.Event{Kind: run.EvStage, Step: "verify"}}, func() bool {
			return a.verify.step == "running the Checks" // b's cut is gone: its step lands nowhere
		}},
		{chatCutMsg{view: a, seq: 1, err: run.ErrNothingToCut}, func() bool {
			return a.verify == nil && a.unchecked && !b.unchecked
		}},
	}
	for i, s := range steps {
		h.Update(s.msg)
		if !s.check() {
			t.Errorf("step %d (%T): a %+v\nb %+v", i, s.msg, a, b)
		}
		if h.chatView != a {
			t.Fatalf("step %d (%T) moved the focus", i, s.msg)
		}
	}
}

// A message for a Chat the home no longer has is dropped; one that names
// no Chat is the focused one's.
func TestChatMessageForAGoneChatIsDropped(t *testing.T) {
	h, a, b, _ := twoChats()
	gone := &chatView{turn: &chatTurn{}, chat: &homeChat{}}
	h.Update(chatEvMsg{view: gone, ev: agent.Event{Kind: agent.Claim, Text: "lost"}, ok: true})
	if h.dropped != 1 || printed(a)+printed(b)+printed(gone) != "" || h.chatView != a {
		t.Errorf("dropped %d, a %q, b %q, gone %q", h.dropped, printed(a), printed(b), printed(gone))
	}
	h.Update(chatEvMsg{ev: agent.Event{Kind: agent.Claim, Text: "focused"}, ok: true})
	if !strings.Contains(printed(a), "focused") || printed(b) != "" {
		t.Errorf("a %q, b %q", printed(a), printed(b))
	}
}

// Ctrl+C cancels the focused Chat's cut, then interrupts its turn; the
// Chat in the background goes on.
func TestCtrlCStopsTheFocusedChatOnly(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	h, a, b, cancelled := twoChats()
	a.turn, b.turn = nil, nil
	h.Update(ctrlC)
	if !a.verify.stopped || cancelled[a] != 1 || b.verify.stopped || cancelled[b] != 0 || h.quit {
		t.Errorf("verify: a stopped %v (%d), b stopped %v (%d), quit %v",
			a.verify.stopped, cancelled[a], b.verify.stopped, cancelled[b], h.quit)
	}

	h, a, b, cancelled = twoChats()
	h.Update(ctrlC)
	if !a.turn.stopped || b.turn.stopped || cancelled[b] != 0 || h.quit || h.chatView != a {
		t.Errorf("turn: a stopped %v, b stopped %v, b cancelled %d, quit %v", a.turn.stopped, b.turn.stopped, cancelled[b], h.quit)
	}
}

// deliver runs cmd off the update loop and hands the home each Chat's
// message it ends in, as the program would.
func deliver(h *home, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch m := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range m {
			deliver(h, c)
		}
	case chatDiffMsg, diffEntryMsg, sideChangesMsg:
		_, next := h.Update(m)
		deliver(h, next)
	default:
		if chatViewOf(m) != nil {
			_, next := h.Update(m)
			deliver(h, next)
			return
		}
		for _, c := range seqCmds(m) {
			deliver(h, c)
		}
	}
}

// A cut ending in the background prints its Verdict and its diff in its
// own Chat, and fills that Chat's sidebar: never the focused one's.
func TestBackgroundVerdictDiffGoesToItsChat(t *testing.T) {
	for _, full := range []bool{false, true} {
		h, a, b, _ := twoChats()
		h.full = full
		a.tr, b.tr = &transcript{}, &transcript{}
		b.chat.verdict = func(*run.Result, int) (string, *lastRun) {
			return "✓ accepted", &lastRun{id: "b-run", patch: func() ([]byte, error) {
				return []byte("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"), nil
			}}
		}
		_, cmd := h.Update(chatCutMsg{view: b, seq: 1, res: &run.Result{Outcome: run.Accepted, Check: &oracle.Result{Pass: true}}})
		deliver(h, cmd)
		if h.chatView != a {
			t.Fatalf("full %v: the focus moved", full)
		}
		if !strings.Contains(printed(b), "+new") || printed(a) != "" || len(a.tr.entries) != 0 {
			t.Errorf("full %v: a %q (%d entries), b %q", full, printed(a), len(a.tr.entries), printed(b))
		}
		if full && (len(b.side.changes) != 1 || a.side.changes != nil) {
			t.Errorf("sidebar changes: a %v, b %v", a.side.changes, b.side.changes)
		}
	}
}

// Each Chat numbers its own Consultations, so two can share a seq: the
// advice lands in the Chat that asked, and its tick re-arms there.
func TestAdviceLandsInItsChat(t *testing.T) {
	h, a, b, _ := twoChats()
	a.adv = &adviseState{seq: 1, running: true}
	b.adv = &adviseState{seq: 1, running: true}
	h.focus(b)
	if _, cmd := h.Update(adviceTickMsg{view: a, seq: 1}); cmd == nil {
		t.Error("a's tick did not re-arm while b had the focus")
	}
	advice := &run.Advice{ID: "c-1", Text: "try the other branch"}
	h.Update(adviceMsg{view: a, seq: 1, advice: advice})
	if a.adv.running || a.adv.advice != advice || !b.adv.running || b.adv.advice != nil || h.chatView != b {
		t.Errorf("a %+v, b %+v", a.adv, b.adv)
	}

	ask := &agent.Ask{}
	b.turn.askAdv = &askAdvice{ask: ask, seq: 1, running: true}
	h.focus(a)
	if _, cmd := h.Update(askAdviceTickMsg{view: b, ask: ask, seq: 1}); cmd == nil {
		t.Error("b's ask advice tick did not re-arm while a had the focus")
	}
}
