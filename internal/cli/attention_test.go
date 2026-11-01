package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/herdr"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// The router is a fixed table (docs/ux/desk.md, "Attention levels").
func TestAttentionTable(t *testing.T) {
	for _, c := range []struct {
		ev   attnEvent
		want level
	}{
		{evNone, ignore},
		{evToolStep, ignore},
		{evSettled, changeBlind},
		{evAccepted, makeAware},
		{evCheckFailed, makeAware},
		{evRunEnded, makeAware},
		{evGate, interrupt},
		{evToolAsk, interrupt},
		{evAskUser, interrupt},
		{evStaleGate, demand},
		{evApprovalExpiring, demand},
	} {
		if got := levelOf(c.ev); got != c.want {
			t.Errorf("levelOf(%d) = %d, want %d", c.ev, got, c.want)
		}
	}
}

// What a Chat's glyph moving says happened.
func TestGlyphEvent(t *testing.T) {
	ask := &chatView{turn: &chatTurn{asks: []*agent.Ask{{Tool: "Bash"}}}}
	question := &chatView{turn: &chatTurn{asks: []*agent.Ask{{Questions: []agent.AskQuestion{{}}}}}}
	gate := &chatView{cutGate: &gateState{}}
	for _, c := range []struct {
		name          string
		v             *chatView
		before, after string
		want          attnEvent
	}{
		{"tool ask", ask, "●", "▲", evToolAsk},
		{"question", question, "●", "▲", evAskUser},
		{"gate", gate, "○", "▲", evGate},
		{"still asking", ask, "▲", "▲", evNone},
		{"check failed", &chatView{}, "●", "✗", evCheckFailed},
		{"accepted", &chatView{}, "●", "✓", evAccepted},
		{"settled, no change", &chatView{}, "●", "○", evSettled},
		{"quiet settled", &chatView{}, "?", "○", evSettled},
		{"gone quiet", &chatView{}, "●", "?", evNone},
		{"seen", &chatView{}, "✓", "○", evNone},
		{"answered", &chatView{}, "▲", "●", evNone},
		{"first look", &chatView{}, "", "○", evNone},
	} {
		if got := glyphEvent(c.v, c.before, c.after); got != c.want {
			t.Errorf("%s: %s→%s = %d, want %d", c.name, c.before, c.after, got, c.want)
		}
	}
}

// attnHome is a full-screen home with Chats 1 (focused, with a Session)
// and 2, "parser", whose turn is in flight.
func attnHome(t *testing.T) (h *home, two *chatView, now *time.Time) {
	t.Helper()
	h, now = railHome(120, 2)
	h.chat = &homeChat{c: &run.Chat{}}
	h.hist, h.sel = &history{}, &setupState{}
	two = h.views[1]
	two.name, two.turn, two.lastEvent = "parser", &chatTurn{}, *now
	h.Update(chatEvMsg{view: two, ok: true})
	return h, two, now
}

func askIn(v *chatView) tea.Msg {
	return chatEvMsg{view: v, ok: true, ev: agent.Event{Kind: agent.HostRequest, Ask: &agent.Ask{Tool: "Bash", Target: "ls", Detail: "ls"}}}
}

func boxTop(h *home) string {
	lines := screenLines(h)
	for i := len(lines) - 1; i >= 0; i-- {
		if l := ansi.Strip(lines[i]); strings.Contains(l, "╭") && !strings.Contains(l, "DESK") {
			return l
		}
	}
	return ""
}

// A Gate or ask in a background Chat while the human types in another is
// held: the box says so, no toast passes and the focus stays. Sending
// the message releases it as one toast.
func TestBackgroundAskHeldWhileTypingReleasedOnSend(t *testing.T) {
	h, two, _ := attnHome(t)
	one := h.chatView
	typeText(h, "a half-written thought")
	h.Update(askIn(two))
	if h.chatView != one {
		t.Fatalf("the focus moved to Chat %d", h.num)
	}
	if m, _ := h.mark(two); m != "▲" {
		t.Fatalf("Chat 2's glyph = %s, want ▲", m)
	}
	if h.toast != nil {
		t.Fatalf("a toast passed while typing: %q", h.toast.text)
	}
	if top := boxTop(h); !strings.Contains(top, "1 held") {
		t.Fatalf("box top %q, want 1 held", top)
	}
	h.Update(enter())
	if h.chatView != one || h.turn == nil {
		t.Fatalf("the message didn't go from Chat 1 (focused %d)", h.num)
	}
	if h.toast == nil || h.toast.text != "▲ 2 parser asks" {
		t.Fatalf("toast after send: %+v, want ▲ 2 parser asks", h.toast)
	}
	if top := boxTop(h); strings.Contains(top, "held") {
		t.Errorf("box top %q after the release", top)
	}
}

// Several held items release as one toast on a cleared box, the human's
// part first; a pause in typing releases nothing.
func TestHeldItemsReleaseAsOneToastOnClear(t *testing.T) {
	h, now := railHome(120, 4)
	two, three, four := h.views[1], h.views[2], h.views[3]
	two.name, three.name, four.name = "parser", "docs", "lexer"
	two.turn, three.turn, four.turn = &chatTurn{}, &chatTurn{}, &chatTurn{}
	h.Update(chatEvMsg{view: two, ok: true})
	typeText(h, "draft")
	// Chat 3's cut is Accepted, then Chat 2's Check fails, then 4 asks.
	three.turn, three.last, three.side.outcome, three.doneAt = nil, &lastRun{}, run.Accepted, *now
	h.Update(deskTickMsg{view: three})
	two.turn, two.side.outcome = nil, run.Failed
	h.Update(deskTickMsg{view: two})
	h.Update(askIn(four))
	if len(h.parked) != 3 || h.toast != nil {
		t.Fatalf("held %d, toast %+v", len(h.parked), h.toast)
	}
	*now = now.Add(time.Hour) // a long pause
	h.Update(staleTickMsg{})
	if h.toast != nil || h.comp.text() != "draft" {
		t.Fatalf("a pause released: toast %+v", h.toast)
	}
	h.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !h.comp.empty() {
		t.Fatalf("ctrl+c left %q", h.comp.text())
	}
	want := "3 new · ▲ 4 lexer asks · ✗ 2 parser Check failed · ✓ 3 docs accepted"
	if h.toast == nil || h.toast.text != want || !h.toast.plain {
		t.Fatalf("toast %+v, want %q", h.toast, want)
	}
}

// A held item no longer true when the box empties is dropped: the ask
// was answered or the turn ended while the human typed.
func TestHeldAskGoneIsNotReleased(t *testing.T) {
	h, two, _ := attnHome(t)
	typeText(h, "draft")
	h.Update(askIn(two))
	if len(h.parked) != 1 {
		t.Fatalf("held %d", len(h.parked))
	}
	two.turn = nil
	h.Update(deskTickMsg{view: two})
	if top := boxTop(h); strings.Contains(top, "held") {
		t.Errorf("box top %q after the ask went", top)
	}
	h.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if h.toast != nil {
		t.Errorf("toast for a gone ask: %q", h.toast.text)
	}
}

// A toast on screen is not replaced by the release; the held items go
// when it does.
func TestReleaseWaitsForAToast(t *testing.T) {
	h, two, _ := attnHome(t)
	typeText(h, "draft")
	h.Update(askIn(two))
	h.notice("copied the last reply (1 line)", false)
	seq := h.toast.seq
	h.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if h.toast == nil || h.toast.seq != seq || len(h.parked) != 1 {
		t.Fatalf("toast %+v, held %d", h.toast, len(h.parked))
	}
	h.Update(toastGoneMsg{seq})
	if h.toast == nil || h.toast.text != "▲ 2 parser asks" || len(h.parked) != 0 {
		t.Errorf("toast %+v, held %d after the first went", h.toast, len(h.parked))
	}
}

// With an empty box, a background ask is a toast at once; a background
// Accepted cut is only its glyph. The focus never moves.
func TestBackgroundAskToastsAtABreakpoint(t *testing.T) {
	h, two, now := attnHome(t)
	one := h.chatView
	h.Update(askIn(two))
	if h.chatView != one || h.toast == nil || h.toast.text != "▲ 2 parser asks" || len(h.parked) != 0 {
		t.Fatalf("focused %d, toast %+v, held %d", h.num, h.toast, len(h.parked))
	}
	h.toast = nil
	two.turn, two.last, two.side.outcome, two.doneAt = nil, &lastRun{}, run.Accepted, *now
	h.Update(deskTickMsg{view: two})
	if h.toast != nil || h.chatView != one {
		t.Errorf("an Accepted cut toasted %+v, focused %d", h.toast, h.num)
	}
}

// A Gate or ask open past [desk] stale_gate rings now, even while the
// human types, once, naming the Chat's number only.
func TestStaleAskRingsWhileComposing(t *testing.T) {
	h, two, now := attnHome(t)
	h.notify = &notifier{attention: true, bell: true}
	r := &redraws{}
	h.redraw = r.after
	h.deskStale = 2 * time.Minute
	typeText(h, "still typing")
	h.Update(askIn(two))
	asked := false
	for i, m := range r.msg {
		if _, ok := m.(staleTickMsg); ok && r.d[i] == 2*time.Minute {
			asked = true
		}
	}
	if !asked {
		t.Fatalf("no stale tick asked for: %v", r.d)
	}
	_, cmd := h.Update(staleTickMsg{})
	if raw := rawOf(cmd); raw != "" {
		t.Fatalf("rang before stale_gate: %q", raw)
	}
	*now = now.Add(2 * time.Minute)
	_, cmd = h.Update(staleTickMsg{})
	raw := rawOf(cmd)
	if !strings.Contains(raw, "\a") || !strings.Contains(raw, "oge: Chat 2 (parser) is waiting for you") {
		t.Fatalf("stale ring %q", raw)
	}
	if h.comp.text() != "still typing" || h.chatView.num != 1 {
		t.Errorf("the ring touched the box %q or focus %d", h.comp.text(), h.num)
	}
	if _, cmd = h.Update(staleTickMsg{}); rawOf(cmd) != "" {
		t.Errorf("rang twice")
	}
}

// rawOf is what cmd writes raw to the terminal, run to the end.
func rawOf(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	var out strings.Builder
	switch m := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range m {
			out.WriteString(rawOf(c))
		}
	case tea.RawMsg:
		out.WriteString(m.Msg.(string))
	}
	return out.String()
}

// herdr hears the Desk's rolled-up state when it changes: working while a
// Chat turn runs, blocked on an ask, idle once all is quiet.
func TestHerdrHearsTheDesk(t *testing.T) {
	h, _ := railHome(120, 2)
	f := &fakeReporter{}
	h.herdr = f
	two := h.views[1]
	two.turn, two.lastEvent = &chatTurn{}, h.clock()
	h.Update(chatEvMsg{view: two, ok: true})
	h.Update(chatEvMsg{view: two, ok: true})
	h.Update(askIn(two))
	two.turn = nil
	h.Update(deskTickMsg{view: two})
	want := []string{"working|", "blocked|" + notifyWaiting, "idle|"}
	if strings.Join(f.got, ",") != strings.Join(want, ",") {
		t.Errorf("herdr got %v, want %v", f.got, want)
	}
}

// A Run started from the home reports on the same pane and ends idle;
// after it the Desk's state is told again.
func TestHerdrHearsTheDeskAfterARun(t *testing.T) {
	h, two, _ := attnHome(t)
	f := &fakeReporter{}
	h.herdr = f
	h.Update(askIn(two))
	h.run = &model{}
	h.Update(staleTickMsg{})
	f.Report(herdr.Working, "")
	f.Report(herdr.Idle, "")
	h.run = nil
	h.Update(staleTickMsg{})
	if n := len(f.got); n == 0 || f.got[n-1] != "blocked|"+notifyWaiting {
		t.Errorf("herdr got %v, want blocked again after the Run", f.got)
	}
}

func TestDeskStateRollup(t *testing.T) {
	for _, c := range []struct {
		marks []string
		want  herdr.State
	}{
		{nil, herdr.Idle},
		{[]string{"○", "●"}, herdr.Working},
		{[]string{"?"}, herdr.Working},
		{[]string{"●", "✓"}, herdr.Working}, // done is idle: a turn shows over it
		{[]string{"✓"}, herdr.Idle},
		{[]string{"✓", "▲", "●"}, herdr.Blocked},
		{[]string{"✗"}, herdr.Idle},
	} {
		if got := deskState(c.marks); got != c.want {
			t.Errorf("%v = %s, want %s", c.marks, got, c.want)
		}
	}
}

func TestStaleGateDefault(t *testing.T) {
	if (&home{}).staleAfter() != pipeline.DefaultDeskStaleGate {
		t.Error("stale_gate default")
	}
}

// Typing a follow-up into the queue box during a Run holds what other
// Chats bring, as the composer does (U3).
func TestQueueBoxHoldsDuringARun(t *testing.T) {
	h, two, _ := attnHome(t)
	h.run = &model{}
	q := h.composer()
	q.set("a follow-up for after the Run")
	h.next = &queued{comp: q}
	if !h.composing() {
		t.Fatal("a non-empty queue box during a Run isn't composing")
	}
	h.next.comp.set("")
	if h.composing() {
		t.Fatal("an empty queue box during a Run is composing")
	}
	h.run, h.next = nil, nil
	_ = two
}
