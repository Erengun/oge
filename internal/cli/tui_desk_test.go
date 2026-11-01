package cli

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// deskHome is a full-screen home with one Chat whose Session is open, so
// /new keeps it and opens another.
func deskHome() *home {
	h := testHome(80)
	h.full, h.termW, h.termH = true, 80, 24
	h.num, h.tr = 1, &transcript{}
	h.chat = &homeChat{c: &run.Chat{}}
	h.views = []*chatView{h.chatView}
	return h
}

func alt(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModAlt} }

func nums(h *home) []int {
	var out []int
	for _, v := range h.views {
		out = append(out, v.num)
	}
	return out
}

// /new keeps the Chat it was typed in, Session and all, and focuses a new
// one, numbered next, on a Chat of its own.
func TestNewKeepsTheChatAndOpensAnother(t *testing.T) {
	h := deskHome()
	first := h.chatView
	submit(h, "/new")
	if len(h.views) != 2 || h.chatView == first || h.num != 2 || first.chat.get() == nil || h.resetting {
		t.Fatalf("views %v, focused %d, first's Chat %v", nums(h), h.num, first.chat.get())
	}
	if h.chat == first.chat || h.chat.get() != nil || h.tr == first.tr {
		t.Errorf("the new Chat shares the first one's state")
	}
	// A Chat that has opened nothing is only started afresh.
	submit(h, "/new")
	if len(h.views) != 2 || h.num != 2 {
		t.Errorf("/new on an empty Chat opened another: %v", nums(h))
	}
}

// Numbers never change: not as Chats are switched, not when a middle one
// closes. Closing focuses the nearest; the last leaves an empty Chat.
func TestCloseKeepsTheOthersNumbers(t *testing.T) {
	h := deskHome()
	for range 2 {
		submit(h, "/new")
		h.chat.c = &run.Chat{}
	}
	if got := nums(h); len(got) != 3 || got[2] != 3 {
		t.Fatalf("views %v", got)
	}
	h.Update(alt('2'))
	if h.num != 2 {
		t.Fatalf("alt+2 focused %d", h.num)
	}
	h.chat.c = nil // its Session isn't real; closing has nothing to close
	submit(h, "/close")
	if got := nums(h); len(got) != 2 || got[0] != 1 || got[1] != 3 || h.num != 3 {
		t.Fatalf("after /close: views %v, focused %d", got, h.num)
	}
	if len(h.closed) != 1 {
		t.Errorf("closed %d", len(h.closed))
	}
	h.Update(alt('1'))
	if h.num != 1 {
		t.Errorf("alt+1 focused %d", h.num)
	}
	h.Update(alt('2')) // closed: nothing happens
	if h.num != 1 {
		t.Errorf("alt+2 of a closed Chat focused %d", h.num)
	}
	submit(h, "/new")
	if h.num != 4 {
		t.Errorf("a new Chat after a close is %d, want 4", h.num)
	}
	for _, v := range append([]*chatView(nil), h.views...) {
		h.focus(v)
		v.chat.c = nil
		submit(h, "/close")
	}
	if got := nums(h); len(got) != 1 || got[0] != 1 || h.name != "" {
		t.Errorf("closing every Chat left %v", got)
	}
}

// Each Chat keeps its draft and its scroll: switching away and back loses
// neither.
func TestDraftAndScrollSurviveASwitch(t *testing.T) {
	h := deskHome()
	for i := range 60 {
		h.tr.add(trEntry{text: "line " + strconv.Itoa(i)})
	}
	h.View()
	h.tr.move(-5)
	h.View()
	start := h.tr.start
	if !h.tr.scroll {
		t.Fatal("the transcript didn't scroll")
	}
	submit(h, "/new")
	h.Update(alt('1'))
	typeText(h, "half a thought")
	h.Update(alt('2'))
	if h.comp.text() != "" || h.tr.scroll {
		t.Fatalf("Chat 2: box %q, scrolled %v", h.comp.text(), h.tr.scroll)
	}
	typeText(h, "other")
	h.Update(alt('1'))
	h.View()
	if h.comp.text() != "half a thought" || h.tr.start != start || !h.tr.scroll {
		t.Errorf("back on 1: box %q, start %d (want %d), scrolled %v", h.comp.text(), h.tr.start, start, h.tr.scroll)
	}
	h.Update(alt('2'))
	if h.comp.text() != "other" {
		t.Errorf("back on 2: box %q", h.comp.text())
	}
}

// A Chat is named by its first message, "new" until then.
func TestChatNameIsItsFirstMessage(t *testing.T) {
	h := deskHome()
	h.hist, h.sel = &history{}, &setupState{}
	if h.chatView.caption() != "new" {
		t.Errorf("label %q", h.chatView.caption())
	}
	submit(h, "make the parser accept trailing commas")
	submit(h, "and nested arrays")
	if h.turn == nil || h.chatView.caption() != "make the parser accept trailing commas" {
		t.Errorf("turn %v, label %q", h.turn != nil, h.chatView.caption())
	}
}

// /chats works at 80 columns: a number or Enter switches; Esc closes it.
func TestChatsPicker(t *testing.T) {
	h := deskHome()
	submit(h, "/new")
	submit(h, "/chats")
	if h.chats == nil {
		t.Fatal("/chats didn't open")
	}
	got := ansi.Strip(h.View().Content)
	for _, want := range []string{"Chats", "1 ○ new  idle", "2 ○ new  idle", "(this one)"} {
		if !strings.Contains(got, want) {
			t.Errorf("picker lacks %q:\n%s", want, got)
		}
	}
	// Each row says the Chat's coarse state.
	h.chats = nil
	one := h.viewNum(1)
	one.turn = &chatTurn{}
	submit(h, "/chats")
	if got := ansi.Strip(h.View().Content); !strings.Contains(got, "1 ● new  working") {
		t.Errorf("a Chat at work isn't working in the picker:\n%s", got)
	}
	one.turn.asks = []*agent.Ask{{}}
	if got := ansi.Strip(h.View().Content); !strings.Contains(got, "1 ▲ new  needs you") {
		t.Errorf("a Chat asking isn't needs you in the picker:\n%s", got)
	}
	one.turn = nil
	h.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if h.chats != nil || h.num != 1 {
		t.Errorf("1 in the picker: open %v, focused %d", h.chats != nil, h.num)
	}
	submit(h, "/chats")
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	h.Update(enter())
	if h.chats != nil || h.num != 2 {
		t.Errorf("down, enter: open %v, focused %d", h.chats != nil, h.num)
	}
	submit(h, "/chats")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.chats != nil || h.num != 2 {
		t.Errorf("esc: open %v, focused %d", h.chats != nil, h.num)
	}
}

// A Chat in the background that fails to send keeps its text in its own
// box and says why in its own transcript: the focused Chat's box and the
// lines under it stay as they were.
func TestBackgroundSendErrorStaysInItsChat(t *testing.T) {
	h, a, b, _ := twoChats()
	a.tr, b.tr = &transcript{}, &transcript{}
	b.turn = &chatTurn{text: "fix it"}
	h.comp.set("mine")
	h.Update(chatSentMsg{view: b, err: errors.New("no session")})
	if h.chatView != a || h.comp.text() != "mine" || len(h.msg) != 0 || h.background {
		t.Errorf("focused: box %q, msg %v, background %v", h.comp.text(), h.msg, h.background)
	}
	if b.comp.text() != "fix it" || !strings.Contains(printed(b), "no session") || printed(a) != "" {
		t.Errorf("b: box %q, printed %q; a printed %q", b.comp.text(), printed(b), printed(a))
	}
}

// With another Chat at work, Ctrl+C on an empty box only says so: the
// next Ctrl+C, and only the very next key, quits. A stopped turn's second
// Ctrl+C asks the same.
func TestCtrlCWithAnotherChatWorkingAsksAgain(t *testing.T) {
	h := deskHome()
	first := h.chatView
	submit(h, "/new")
	first.turn = &chatTurn{}
	ctrlC := ctrlKey('c')
	h.Update(ctrlC)
	if h.quit || !strings.Contains(strings.Join(h.msg, "\n"), "Chat 1 is working · ctrl+c again quits") {
		t.Fatalf("one ctrl+c: quit %v, msg %q", h.quit, h.msg)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	h.Update(ctrlC)
	if h.quit {
		t.Fatal("ctrl+c, esc, ctrl+c quit")
	}
	if _, cmd := h.Update(ctrlC); !h.quit || cmd == nil {
		t.Fatalf("a second ctrl+c in a row didn't quit")
	}

	h = deskHome()
	first = h.chatView
	submit(h, "/new")
	first.verify = &chatVerify{cancel: func() {}}
	h.turn = &chatTurn{stopped: true}
	h.Update(ctrlC)
	if h.quit {
		t.Fatal("a stopped turn's ctrl+c quit with Chat 1 verifying")
	}
	if got := ansi.Strip(h.View().Content); !strings.Contains(got, "ctrl+c again quits and stops it") {
		t.Errorf("the turn's screen doesn't say why it didn't quit:\n%s", got)
	}
	h.Update(ctrlC)
	if !h.quit {
		t.Error("a second ctrl+c didn't quit")
	}

	// With nothing else at work, one is enough, as before.
	h = deskHome()
	h.Update(ctrlC)
	if !h.quit {
		t.Error("ctrl+c on an empty box with one Chat didn't quit")
	}
}

// alt+N doesn't move the focus from under the hidden-characters prompt:
// Enter then submits the text to the Chat it was typed in.
func TestSwitchWaitsForTheHiddenPrompt(t *testing.T) {
	h := deskHome()
	submit(h, "/new")
	h.Update(alt('1'))
	h.hidden = &hiddenPrompt{text: "do it\u200b now", n: 1}
	h.Update(alt('2'))
	if h.num != 1 || h.hidden == nil {
		t.Errorf("alt+2 under the prompt: focused %d, prompt open %v", h.num, h.hidden != nil)
	}
	h.hidden = nil
	h.saving = true
	h.Update(alt('2'))
	if h.num != 1 {
		t.Errorf("alt+2 under the save prompt focused %d", h.num)
	}
}

// A Chat whose turn ends in the background runs none of its queued
// commands: their output and prompts are the terminal's. They wait in its
// own box, and the focused Chat is untouched.
func TestQueuedCommandsWaitInABackgroundChat(t *testing.T) {
	h := deskHome()
	first := h.chatView
	submit(h, "/new")
	first.turn = &chatTurn{queued: []string{"!true", "/diff", "and then this"}}
	h.comp.set("mine")
	h.Update(chatEvMsg{view: first, ok: false})
	if h.chatView == first || h.job != nil || h.diff != nil || len(h.msg) != 0 || len(h.tr.entries) != 0 || h.comp.text() != "mine" {
		t.Errorf("focused: job %v, diff %v, msg %q, entries %d, box %q", h.job != nil, h.diff != nil, h.msg, len(h.tr.entries), h.comp.text())
	}
	if first.turn != nil || first.comp.text() != "!true\n\n/diff\n\nand then this" {
		t.Errorf("Chat 1: turn %v, box %q", first.turn != nil, first.comp.text())
	}
}

// A switch in the middle of a history walk gives the box its draft back
// first; what one Chat said under the box, or cleared with Ctrl+C, stays
// with it.
func TestSwitchEndsTheHistoryWalk(t *testing.T) {
	h := deskHome()
	h.hist = &history{entries: []string{"older"}}
	submit(h, "/new")
	h.Update(alt('1'))
	typeText(h, "fix login")
	h.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if h.comp.text() != "older" {
		t.Fatalf("↑ showed %q", h.comp.text())
	}
	h.Update(alt('2'))
	h.Update(alt('1'))
	if h.comp.text() != "fix login" || h.navigating {
		t.Errorf("back on 1: box %q, navigating %v", h.comp.text(), h.navigating)
	}
	h.Update(ctrlKey('c')) // cleared, for ↑ to bring back here
	h.msg = []string{"Chat 1's problem"}
	h.Update(alt('2'))
	if len(h.msg) != 0 {
		t.Errorf("Chat 1's lines under Chat 2's box: %q", h.msg)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if h.comp.text() == "fix login" {
		t.Error("↑ on Chat 2 brought back what Ctrl+C cleared in Chat 1")
	}
}

// /new says the old Chat stays open, and how to get back to it.
func TestNewSaysTheOldChatStaysOpen(t *testing.T) {
	h := deskHome()
	submit(h, "/new")
	if !strings.Contains(h.note, "Chat 1 stays open · /chats switches back") {
		t.Errorf("note %q", h.note)
	}
}

// Quitting leaves every Chat's transcript in the scrollback, in number
// order, each under its name.
func TestLeaveTranscriptWritesEveryChat(t *testing.T) {
	h := deskHome()
	first := h.chatView
	first.name = "parser"
	h.tr.add(trEntry{text: "from one"})
	submit(h, "/new")
	h.tr.add(trEntry{text: "from two"})
	var b strings.Builder
	h.leaveTranscript(&b)
	out := ansi.Strip(b.String())
	i1, i2 := strings.Index(out, "from one"), strings.Index(out, "from two")
	h1, h2 := strings.Index(out, "Chat 1 · parser"), strings.Index(out, "Chat 2 · new")
	if i1 < 0 || i2 < 0 || h1 < 0 || h2 < 0 || !(h1 < i1 && i1 < h2 && h2 < i2) {
		t.Errorf("transcripts out of order or unnamed:\n%s", out)
	}

	// One Chat with anything in it is written as before, unnamed.
	h = deskHome()
	h.tr.add(trEntry{text: "alone"})
	b.Reset()
	h.leaveTranscript(&b)
	if out := ansi.Strip(b.String()); !strings.Contains(out, "alone") || strings.Contains(out, "Chat 1") {
		t.Errorf("one Chat:\n%s", out)
	}
}

// Opening a Chat for the first message holds no lock /new or /close
// waits on, and a Chat opened after a reset is not kept.
func TestOpeningAChatHoldsNoLock(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	h := deskHome()
	h.chat = &homeChat{open: func(*resolved) (*run.Chat, error) {
		close(started)
		<-release
		return nil, errors.New("no agent")
	}}
	hc := h.chat
	_, gen := hc.current()
	_, cmd := h.chatSubmit("fix it", &resolved{frozen: &pipeline.Frozen{}}, nil)
	sent := make(chan tea.Msg, 1)
	go func() {
		m := cmd() // the send alone, or a Sequence ending in it
		if cmds := seqCmds(m); len(cmds) > 0 {
			m = cmds[len(cmds)-1]()
		}
		sent <- m
	}()
	<-started
	done := make(chan struct{})
	go func() {
		submit(h, "/new")
		h.Update(alt('1'))
		submit(h, "/close")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("/new or /close waited for the Chat to open")
	}
	close(release)
	if m, ok := (<-sent).(chatSentMsg); !ok || m.err == nil {
		t.Errorf("sent %#v", m)
	}
	hc.closing.Wait()
	if hc.opened(&run.Chat{}, gen) || hc.get() != nil {
		t.Error("a Chat opened before a reset was kept")
	}
}
