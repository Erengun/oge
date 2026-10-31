package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/agent"
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
