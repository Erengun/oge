package cli

import (
	"errors"
	"slices"
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
// next Ctrl+C, and only the very next key, goes on to quit, through the
// quit ask. A stopped turn's second Ctrl+C asks the same.
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
	if h.Update(ctrlC); h.quit || h.quitAsk != 1 {
		t.Fatalf("a second ctrl+c in a row: quit %v, ask %d", h.quit, h.quitAsk)
	}
	if _, cmd := h.Update(runeKey('c')); !h.quit || cmd == nil || h.keep {
		t.Fatalf("c at the ask: quit %v, keep %v", h.quit, h.keep)
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
	if h.quit || h.quitAsk == 0 {
		t.Error("a second ctrl+c didn't go on to the quit ask")
	}

	// With nothing else at work, one is enough to ask; with no Chat
	// open, to quit, as before.
	h = deskHome()
	h.Update(ctrlC)
	if h.quit || h.quitAsk != 1 {
		t.Error("ctrl+c on an empty box with one Chat open didn't ask")
	}
	h = deskHome()
	h.chat = &homeChat{}
	h.Update(ctrlC)
	if !h.quit {
		t.Error("ctrl+c on an empty box with no Chat open didn't quit")
	}
}

// Quitting with Chats open asks whether to keep them (docs/ux/away.md,
// A3). Enter alone, ctrl+c or any other key decides nothing; esc goes back; k
// keeps them, c closes them.
func TestQuitAsksToKeepOpenChats(t *testing.T) {
	h := deskHome()
	submit(h, "/new")
	h.chat = &homeChat{c: &run.Chat{}}
	submit(h, "/quit")
	if h.quit || h.quitAsk != 2 {
		t.Fatalf("/quit with two Chats open: quit %v, ask %d", h.quit, h.quitAsk)
	}
	if got := ansi.Strip(h.View().Content); !strings.Contains(got, "Keep 2 Chats for next time? k keep · c close · esc cancel") {
		t.Errorf("the ask isn't on the screen:\n%s", got)
	}
	for _, k := range []tea.KeyPressMsg{enter(), runeKey('y'), runeKey('q'), {Code: tea.KeySpace}, ctrlKey('c')} {
		if h.Update(k); h.quit || h.quitAsk == 0 {
			t.Fatalf("%q at the ask: quit %v, ask %d", k.String(), h.quit, h.quitAsk)
		}
	}
	if h.comp.text() != "" {
		t.Errorf("keys at the ask reached the box: %q", h.comp.text())
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.quit || h.quitAsk != 0 || strings.Contains(ansi.Strip(h.View().Content), "for next time?") {
		t.Fatalf("esc: quit %v, ask %d", h.quit, h.quitAsk)
	}
	submit(h, "/quit")
	if _, cmd := h.Update(runeKey('k')); !h.quit || !h.keep || cmd == nil {
		t.Fatalf("k: quit %v, keep %v", h.quit, h.keep)
	}

	// With no Chat open, /quit quits as it always has.
	h = testHome(80)
	submit(h, "/quit")
	if !h.quit || h.quitAsk != 0 {
		t.Errorf("/quit with no Chat: quit %v, ask %d", h.quit, h.quitAsk)
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

// glyph follows the Chat's exact state, on an injected clock.
func TestGlyphFollowsExactState(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	accepted := func(v *chatView) { v.last, v.side.outcome, v.doneAt = &lastRun{}, run.Accepted, t0 }
	for _, c := range []struct {
		name string
		set  func(*chatView)
		now  time.Time
		want string
	}{
		{"idle", func(*chatView) {}, t0, "○ idle"},
		{"turn", func(v *chatView) { v.turn, v.lastEvent = &chatTurn{}, t0 }, t0.Add(time.Minute), "● working"},
		{"verify", func(v *chatView) { v.verify = &chatVerify{} }, t0, "● working"},
		{"quiet", func(v *chatView) { v.turn, v.lastEvent = &chatTurn{}, t0 }, t0.Add(pipeline.DefaultDeskQuiet), "? quiet"},
		{"quiet verify alone is working", func(v *chatView) { v.verify, v.lastEvent = &chatVerify{}, t0 }, t0.Add(time.Hour), "● working"},
		{"ask", func(v *chatView) { v.turn = &chatTurn{asks: []*agent.Ask{{}}} }, t0, "▲ needs you"},
		{"quiet ask needs you", func(v *chatView) { v.turn, v.lastEvent = &chatTurn{asks: []*agent.Ask{{}}}, t0 }, t0.Add(time.Hour), "▲ needs you"},
		{"gate", func(v *chatView) { v.cutGate = &gateState{} }, t0, "▲ needs you"},
		{"stale", func(v *chatView) { v.stale = &staleAsk{} }, t0, "▲ needs you"},
		{"feedback held", func(v *chatView) { v.pendingFB = &pendingFeedback{} }, t0, "✗ check failed"},
		{"failed verdict", func(v *chatView) { v.side.outcome = run.Failed }, t0, "✗ check failed"},
		{"working after a failed verdict", func(v *chatView) { v.side.outcome, v.turn, v.lastEvent = run.Failed, &chatTurn{}, t0 }, t0, "● working"},
		{"gate over failed", func(v *chatView) { v.side.outcome, v.cutGate = run.Failed, &gateState{} }, t0, "▲ needs you"},
		{"done in the background", accepted, t0.Add(time.Hour), "✓ done"},
		{"done, focused under 1s", func(v *chatView) { accepted(v); v.focusAt = t0 }, t0.Add(seenAfter - 1), "✓ done"},
		{"done, focused 1s", func(v *chatView) { accepted(v); v.focusAt = t0 }, t0.Add(seenAfter), "○ idle"},
		{"done after a long focus", func(v *chatView) { accepted(v); v.focusAt = t0.Add(-time.Hour) }, t0.Add(seenAfter - 1), "✓ done"},
		{"done, a key pressed", func(v *chatView) { accepted(v); v.seen = true }, t0, "○ idle"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := &chatView{repoCtx: &repoCtx{}}
			c.set(v)
			if mark, coarse := glyph(v, c.now, pipeline.DefaultDeskQuiet); mark+" "+coarse != c.want {
				t.Errorf("glyph = %s %s, want %s", mark, coarse, c.want)
			}
		})
	}
}

// A turn's agent events keep its Chat working; past [desk] quiet without
// one, it shows quiet.
func TestGlyphQuietAfterTheLastEvent(t *testing.T) {
	h := deskHome()
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	h.turn = &chatTurn{}
	h.lastEvent = now
	now = now.Add(pipeline.DefaultDeskQuiet)
	if m, _ := glyph(h.chatView, now, pipeline.DefaultDeskQuiet); m != "?" {
		t.Fatalf("glyph = %s after %s with no event, want ?", m, pipeline.DefaultDeskQuiet)
	}
	h.Update(chatEvMsg{view: h.chatView, ev: agent.Event{}, ok: true})
	if m, _ := glyph(h.chatView, now, pipeline.DefaultDeskQuiet); m != "●" {
		t.Fatalf("glyph = %s right after an event, want ●", m)
	}
}

// With several Chats open the header tallies them, leaving out each zero
// count; with one, it says nothing new.
func TestHeaderCountsTheChats(t *testing.T) {
	h := deskHome()
	h.title = "oge · repo"
	if got := ansi.Strip(h.header(200)); strings.Contains(got, "working") || strings.Contains(got, "needs you") {
		t.Fatalf("one Chat: header = %q, want no counts", got)
	}
	h.turn = &chatTurn{}
	h.lastEvent = h.clock()
	b := &chatView{repoCtx: &repoCtx{}, num: 2, cutGate: &gateState{}}
	c := &chatView{repoCtx: &repoCtx{}, num: 3, verify: &chatVerify{}}
	d := &chatView{repoCtx: &repoCtx{}, num: 4}
	h.views = append(h.views, b, c, d)
	got := ansi.Strip(h.header(200))
	if !strings.Contains(got, "▲1 needs you · ●2 working") || strings.Contains(got, "✓") {
		t.Fatalf("header = %q, want ▲1 needs you · ●2 working and no ✓", got)
	}
	d.last, d.side.outcome, d.doneAt = &lastRun{}, run.Accepted, h.clock()
	b.cutGate = nil
	if got := ansi.Strip(h.header(200)); !strings.Contains(got, "●2 working · ✓1") || strings.Contains(got, "▲") {
		t.Fatalf("header = %q, want ●2 working · ✓1 and no ▲", got)
	}
}

// A key pressed in the focused Chat counts its ✓ as seen.
func TestKeySeesTheDoneChat(t *testing.T) {
	h := deskHome()
	h.last, h.side.outcome, h.doneAt = &lastRun{}, run.Accepted, h.clock()
	if m, _ := glyph(h.chatView, h.clock(), pipeline.DefaultDeskQuiet); m != "✓" {
		t.Fatalf("glyph = %s, want ✓", m)
	}
	h.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m, _ := glyph(h.chatView, h.clock(), pipeline.DefaultDeskQuiet); m != "○" {
		t.Fatalf("glyph = %s after a key, want ○", m)
	}
}

// A ✓ looked at for long enough stays cleared after the human switches
// away: leaving the Chat doesn't make it unseen again.
func TestDoneStaysSeenAfterSwitchingAway(t *testing.T) {
	h := deskHome()
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	first := h.chatView
	other := &chatView{repoCtx: &repoCtx{}, num: 2}
	h.views = append(h.views, other)
	h.focus(other)
	first.last, first.side.outcome, first.doneAt = &lastRun{}, run.Accepted, now
	h.focus(first)
	now = now.Add(5 * time.Second)
	if m, _ := glyph(first, now, pipeline.DefaultDeskQuiet); m != "○" {
		t.Fatalf("glyph = %s after 5s in view, want ○", m)
	}
	h.focus(other)
	if m, _ := glyph(first, now, pipeline.DefaultDeskQuiet); m != "○" {
		t.Fatalf("glyph = %s after switching away, want ○ (it was seen)", m)
	}
}

// railHome is a full-screen home w columns wide with Chats 1 to n open,
// Chat 1 focused, on a clock the test moves.
func railHome(w, n int) (*home, *time.Time) {
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	h := fullHome(w, 24)
	h.now = func() time.Time { return now }
	h.num, h.focusAt = 1, now
	h.views = []*chatView{h.chatView}
	for i := 2; i <= n; i++ {
		h.views = append(h.views, &chatView{repoCtx: &repoCtx{}, num: i, comp: newComposer(), tr: &transcript{}})
	}
	return h, &now
}

// railCol is the rail's column of the screen, its rows trimmed.
func railCol(h *home) []string {
	var out []string
	for _, l := range screenLines(h) {
		out = append(out, strings.TrimRight(ansi.Cut(ansi.Strip(l), 0, railWidth), " "))
	}
	return out
}

// railNums are the Chat numbers in the rail's order, top to bottom.
func railNums(h *home) []int {
	var out []int
	for _, l := range railCol(h)[1:] {
		for _, f := range strings.Fields(strings.Trim(l, "│")) {
			n, err := strconv.Atoi(strings.TrimRight(f, "▲✗?●✓○◀"))
			if err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// The rail at 120 columns: Chats at work or waiting on you get two rows,
// the coarse state and its age; done and idle ones fold below a rule.
func TestRailSnapshot(t *testing.T) {
	h, now := railHome(120, 5)
	t0 := *now
	h.name, h.turn, h.side.since, h.lastEvent = "make the parser accept trailing commas", &chatTurn{}, t0, t0
	two, three, four, five := h.views[1], h.views[2], h.views[3], h.views[4]
	two.name, two.turn, two.lastEvent = "auth", &chatTurn{asks: []*agent.Ask{{}}}, t0
	three.name, three.side.outcome, three.last, three.doneAt = "docs", run.Accepted, &lastRun{}, t0
	four.name, four.side.outcome, four.doneAt = "ci-flake", run.Failed, t0
	five.name = "spare"
	*now = t0.Add(3*time.Minute - time.Second)
	golden(t, "desk-rail-120", h.View().Content)
	if h.width != 120-railWidth-1-sidebarWidth-1 {
		t.Errorf("main column %d wide", h.width)
	}
	for i, l := range screenLines(h) {
		if w := ansi.StringWidth(l); w > 120 {
			t.Errorf("row %d is %d columns", i, w)
		}
	}
}

// Rows keep number order whatever the Chats do, and a /close leaves the
// others' numbers and order alone.
func TestRailOrderIsStable(t *testing.T) {
	h, now := railHome(120, 4)
	h.chat = &homeChat{c: &run.Chat{}}
	for _, v := range h.views[1:] {
		v.turn, v.lastEvent = &chatTurn{}, *now
	}
	h.turn, h.lastEvent = &chatTurn{}, *now
	want := []int{1, 2, 3, 4}
	if got := railNums(h); !slices.Equal(got, want) {
		t.Fatalf("rail %v, want %v", got, want)
	}
	// Chat 3 asks, Chat 2 goes quiet, Chat 1 fails its Check: still 1 to 4.
	h.views[2].turn.asks = []*agent.Ask{{}}
	h.views[1].lastEvent = now.Add(-time.Hour)
	h.views[3].turn.asks = []*agent.Ask{{}}
	if got := railNums(h); !slices.Equal(got, want) {
		t.Errorf("after state changes: rail %v, want %v", got, want)
	}
	h.Update(alt('2')) // quiet, not asking: the box takes /close
	h.views[1].chat = nil
	submit(h, "/close")
	if got := railNums(h); !slices.Equal(got, []int{1, 3, 4}) || h.num != 3 {
		t.Errorf("after closing 2: rail %v, focused %d", got, h.num)
	}
	// A Chat that settles folds below the rule; the others keep their order.
	h.views[0].turn = nil
	col := strings.Join(railCol(h), "\n")
	if got := railNums(h); !slices.Equal(got, []int{3, 4, 1}) || !strings.Contains(col, "│1○ ") {
		t.Errorf("after 1 settles: rail %v\n%s", got, col)
	}
}

// Without colour, the rail paints nothing.
func TestRailNoColour(t *testing.T) {
	h, _ := railHome(120, 3)
	h.views[1].turn = &chatTurn{asks: []*agent.Ask{{}}}
	h.views[2].cutGate = &gateState{}
	if got := h.View().Content; strings.Contains(got, "\x1b") || !strings.Contains(got, "DESK") {
		t.Errorf("rail without colour:\n%q", got)
	}
}

// Clicking a Chat's row focuses it; a click in the transcript still lands
// where it points, past the rail.
func TestRailClickSwitchesChat(t *testing.T) {
	h, _ := railHome(120, 3)
	h.views[1].turn = &chatTurn{}
	col := railCol(h)
	y := slices.IndexFunc(col, func(l string) bool { return strings.HasPrefix(l, "│2 ●") })
	if y < 0 {
		t.Fatalf("no row for Chat 2:\n%s", strings.Join(col, "\n"))
	}
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: y + 1}) // its second row
	if h.num != 2 {
		t.Fatalf("a click on Chat 2's row focused %d", h.num)
	}
	col = railCol(h)
	y = slices.IndexFunc(col, func(l string) bool { return strings.Contains(l, "3○") })
	x := ansi.StringWidth(col[y][:strings.Index(col[y], "3○")])
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if h.num != 3 {
		t.Fatalf("a click on Chat 3 in the fold focused %d", h.num)
	}
	h.tr.add(trEntry{text: "hello"})
	h.View()
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: railWidth + 1, Y: headRows})
	if h.tr.sel == nil || h.tr.sel[0].col != 0 || h.num != 3 {
		t.Errorf("a click at the pane's first column: sel %v, focused %d", h.tr.sel, h.num)
	}
}

// The rail needs 100 columns and a second Chat, and ctrl+t hides it with
// the sidebar.
func TestRailNeedsRoomAndToggles(t *testing.T) {
	for _, w := range []int{80, 99} {
		if h, _ := railHome(w, 3); h.railOn() || strings.Contains(h.View().Content, "DESK") || h.width != w {
			t.Errorf("a rail at %d columns", w)
		}
	}
	if h, _ := railHome(120, 1); h.railOn() || strings.Contains(h.View().Content, "DESK") {
		t.Error("a rail with one Chat")
	}
	h, _ := railHome(100, 2)
	if !strings.Contains(h.View().Content, "DESK") || h.width != 100-railWidth-1 {
		t.Fatalf("no rail at 100 columns, or main column %d", h.width)
	}
	if c := h.View().Cursor; c == nil || c.X <= railWidth {
		t.Errorf("the cursor isn't past the rail: %+v", c)
	}
	h.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if strings.Contains(h.View().Content, "DESK") || h.width != 100 {
		t.Error("ctrl+t did not hide the rail")
	}
}

// /new with max_active Chats working opens the Chat and says so once,
// in place of the usual note; later ones say nothing more.
func TestNewPastTheCapWarnsOnce(t *testing.T) {
	h := deskHome()
	h.turn = &chatTurn{}
	for range 2 {
		h.views = append(h.views, &chatView{repoCtx: &repoCtx{}, num: len(h.views) + 1, turn: &chatTurn{}})
	}
	submit(h, "/new")
	if len(h.views) != 4 || h.num != 4 || !strings.Contains(h.note, "3 Chats are working") || strings.Contains(h.note, "again") {
		t.Fatalf("first /new past the cap: views %v, focused %d, note %q", nums(h), h.num, h.note)
	}
	h.turn = &chatTurn{}
	h.chat.c = &run.Chat{}
	submit(h, "/new")
	if len(h.views) != 5 || h.num != 5 || strings.Contains(h.note, "research") {
		t.Errorf("a later /new: views %v, focused %d, note %q", nums(h), h.num, h.note)
	}
	// Under the cap, and with a cap set higher, nothing is said.
	h = deskHome()
	h.deskMax = 5
	h.turn = &chatTurn{}
	h.views = append(h.views, &chatView{repoCtx: &repoCtx{}, num: 2, turn: &chatTurn{}}, &chatView{repoCtx: &repoCtx{}, num: 3, turn: &chatTurn{}})
	submit(h, "/new")
	if len(h.views) != 4 || h.capNoted {
		t.Errorf("max_active 5: views %v, noted %v", nums(h), h.capNoted)
	}
}

// A turn sent after a failed Check answers its ✗: once that turn ends
// without a cut the Chat is idle, not ✗ again. A new cut brings it back.
func TestFailedCheckAnsweredByATurn(t *testing.T) {
	h := deskHome()
	h.hist, h.sel = &history{}, &setupState{}
	h.last, h.side.outcome, h.doneAt = &lastRun{}, run.Failed, h.clock()
	if m, _ := h.mark(h.chatView); m != "✗" {
		t.Fatalf("glyph = %s, want ✗", m)
	}
	submit(h, "why did it fail?")
	if h.turn == nil || !h.answered {
		t.Fatal("the message started no turn")
	}
	h.turn = nil // it ended; no cut
	if m, _ := h.mark(h.chatView); m != "○" {
		t.Errorf("glyph = %s after a turn answered the failure, want ○", m)
	}
	if text, _ := h.attention(); !strings.Contains(text, "Check failed") {
		t.Errorf("the sidebar forgot the failure: %q", text)
	}
}

// An Accepted cut that was applied is not done-unseen.
func TestDoneIsNotAnAppliedCut(t *testing.T) {
	applied := false
	v := &chatView{repoCtx: &repoCtx{}, last: &lastRun{id: "r1"}, chat: &homeChat{delivered: func(id string) bool { return applied && id == "r1" }}}
	v.side.outcome = run.Accepted
	now := time.Now()
	if m, _ := glyph(v, now, pipeline.DefaultDeskQuiet); m != "✓" {
		t.Fatalf("glyph = %s, want ✓", m)
	}
	applied = true
	if m, _ := glyph(v, now, pipeline.DefaultDeskQuiet); m != "○" {
		t.Errorf("glyph = %s for an applied cut, want ○", m)
	}
}

// redraws records the redraws the home asks for, firing none.
type redraws struct {
	d   []time.Duration
	msg []tea.Msg
}

func (r *redraws) after(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
	r.d = append(r.d, d)
	r.msg = append(r.msg, fn(time.Time{}))
	return func() tea.Msg { return nil }
}

// A Chat whose agent goes silent shows ? with no key pressed: one redraw
// is asked for at the quiet deadline, and the frame after it shows it.
func TestQuietIsDrawnWithNoMessage(t *testing.T) {
	h, now := railHome(120, 2)
	r := &redraws{}
	h.redraw = r.after
	two := h.views[1]
	two.turn, two.lastEvent = &chatTurn{}, *now
	h.Update(chatEvMsg{view: two, ev: agent.Event{}, ok: true})
	if len(r.d) != 1 || r.d[0] != pipeline.DefaultDeskQuiet {
		t.Fatalf("redraws %v, want one in %s", r.d, pipeline.DefaultDeskQuiet)
	}
	h.Update(chatEvMsg{view: two, ev: agent.Event{}, ok: true})
	if len(r.d) != 1 {
		t.Fatalf("a second event asked for another redraw: %v", r.d)
	}
	if col := strings.Join(railCol(h), "\n"); !strings.Contains(col, "2 ●") {
		t.Fatalf("rail before the deadline:\n%s", col)
	}
	*now = now.Add(pipeline.DefaultDeskQuiet)
	h.Update(r.msg[0])
	if col := strings.Join(railCol(h), "\n"); !strings.Contains(col, "2 ?") {
		t.Errorf("rail after the quiet redraw:\n%s", col)
	}
	if got := ansi.Strip(h.header(200)); strings.Contains(got, "working") {
		t.Errorf("header after the quiet redraw: %q", got)
	}
	if len(r.d) != 1 {
		t.Errorf("a quiet turn asked for more redraws: %v", r.d)
	}
}

// A ✓ in view for a second clears on screen with no key pressed.
func TestSeenIsDrawnWithNoMessage(t *testing.T) {
	h, now := railHome(120, 2)
	r := &redraws{}
	h.redraw = r.after
	two := h.views[1]
	two.name, two.last, two.side.outcome, two.doneAt = "docs", &lastRun{}, run.Accepted, *now
	h.Update(alt('2'))
	if h.num != 2 || len(r.d) != 1 || r.d[0] != seenAfter {
		t.Fatalf("focused %d, redraws %v, want one in %s", h.num, r.d, seenAfter)
	}
	if got := ansi.Strip(h.header(200)); !strings.Contains(got, "✓1") {
		t.Fatalf("header before a second in view: %q", got)
	}
	*now = now.Add(seenAfter)
	h.Update(r.msg[0])
	if got := ansi.Strip(h.header(200)); strings.Contains(got, "✓") {
		t.Errorf("header after the seen redraw: %q", got)
	}
}

// Passing through a ✓ Chat for less than a second leaves it unseen: the
// key that leaves it is not a key in it.
func TestSwitchingThroughKeepsTheDoneUnseen(t *testing.T) {
	h, now := railHome(120, 3)
	two := h.views[1]
	two.last, two.side.outcome, two.doneAt = &lastRun{}, run.Accepted, *now
	h.Update(alt('2'))
	*now = now.Add(seenAfter / 2)
	h.Update(alt('1'))
	if m, _ := h.mark(two); h.num != 1 || m != "✓" {
		t.Errorf("focused %d, Chat 2 is %s after half a second in view, want ✓", h.num, m)
	}
	h.Update(alt('2'))
	h.Update(alt('3'))
	if m, _ := h.mark(two); h.num != 3 || m != "✓" {
		t.Errorf("focused %d, Chat 2 is %s after passing through, want ✓", h.num, m)
	}
}

// Advice the human sends after a failed Check is working while it runs;
// a send-back still held is ✗ again once it ends with no cut. Advice sent
// on its own answers nothing: the failure stays news.
func TestFailedCheckAndAdviceTurns(t *testing.T) {
	h := deskHome()
	c := &run.Chat{}
	h.last, h.side.outcome, h.doneAt = &lastRun{}, run.Failed, h.clock()
	h.pendingFB = &pendingFeedback{text: "1 failing"}
	h.sendAdviceTurn(c, &run.Advice{ID: "a1", Text: "try the other path"}, false, true)
	if m, _ := h.mark(h.chatView); h.turn == nil || m != "●" {
		t.Fatalf("glyph = %s during the advice turn, want ●", m)
	}
	h.turn = nil // it ended; no cut
	if m, _ := h.mark(h.chatView); m != "✗" {
		t.Errorf("glyph = %s with the send-back still held, want ✗", m)
	}

	h = deskHome()
	h.last, h.side.outcome, h.doneAt = &lastRun{}, run.Failed, h.clock()
	h.sendAdviceTurn(c, &run.Advice{ID: "a2", Text: "try the other path"}, true, false)
	if m, _ := h.mark(h.chatView); h.turn == nil || m != "●" {
		t.Fatalf("glyph = %s during the automatic advice turn, want ●", m)
	}
	h.turn = nil
	if m, _ := h.mark(h.chatView); m != "✗" {
		t.Errorf("glyph = %s after an automatic turn with no cut, want ✗", m)
	}
}

// The chain's automatic Consultation under [advisor] auto = "ask" waits
// for ↵: the human sending it answers the failure.
func TestAcceptedAutoAdviceAnswersTheFailure(t *testing.T) {
	h := deskHome()
	c := h.chat.c
	h.last, h.side.outcome, h.doneAt = &lastRun{}, run.Failed, h.clock()
	h.adv = &adviseState{chat: c, advice: &run.Advice{ID: "a1", Text: "try the other path"}, shown: true, auto: true, mode: pipeline.AdvisorAutoAsk}
	h.Update(enter())
	if h.turn == nil || !h.answered {
		t.Fatalf("↵ on the advice: turn %v, answered %v", h.turn, h.answered)
	}
	h.turn = nil
	if m, _ := h.mark(h.chatView); m != "○" {
		t.Errorf("glyph = %s after the human sent the advice, want ○", m)
	}
}

// Öge's own feedback turn ([chat] feedback = "on") answers nothing either;
// the human's send-back does.
func TestFeedbackTurnsAndTheFailure(t *testing.T) {
	h := deskHome()
	h.last, h.side.outcome, h.doneAt = &lastRun{}, run.Failed, h.clock()
	h.feedbackSubmit("1 failing", nil, nil)
	h.turn = nil
	if m, _ := h.mark(h.chatView); m != "✗" {
		t.Errorf("glyph = %s after the automatic feedback turn, want ✗", m)
	}
	h.pendingFB = &pendingFeedback{text: "1 failing"}
	submit(h, "/feedback")
	if h.turn == nil || !h.answered {
		t.Fatal("/feedback started no turn")
	}
	h.turn = nil
	if m, _ := h.mark(h.chatView); m != "○" {
		t.Errorf("glyph = %s after the human's send-back, want ○", m)
	}
}

func runeKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// A ✓ seen is written to desk.log once, so it stays seen across a
// restart (G1).
func TestSeenIsRecordedOnce(t *testing.T) {
	private := t.TempDir()
	d, err := run.OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Release()
	h := deskHome()
	h.chat = &homeChat{c: &run.Chat{ID: "c1"}, desk: d}
	h.last, h.side.outcome, h.doneRun, h.doneAt = &lastRun{}, run.Accepted, "r1", h.clock()
	h.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if h.seenRun != "" {
		t.Fatal("recorded seen before the human looked")
	}
	h.Update(runeKey('x'))
	h.Update(runeKey('y'))
	if h.seenRun != "r1" {
		t.Fatalf("seenRun %q", h.seenRun)
	}
	d.Close(true)
	recs, err := run.ReadDesk(private)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, r := range recs {
		if r.Type == run.RecSeen {
			seen = append(seen, string(r.Data))
		}
	}
	if len(seen) != 1 || seen[0] != `{"chat":"c1","upto":"r1"}` {
		t.Errorf("Seen records %v", seen)
	}
}

// A rail click while the quit ask is open moves nothing and the ask stays
// on the screen; the keys are still the ask's.
func TestQuitAskSurvivesARailClick(t *testing.T) {
	h, _ := railHome(120, 3)
	h.views[1].turn = &chatTurn{}
	h.quitAsk = 3 // asked, whatever the Chats' own state
	col := railCol(h)
	y := slices.IndexFunc(col, func(l string) bool { return strings.HasPrefix(l, "│2 ●") })
	if y < 0 {
		t.Fatalf("no row for Chat 2:\n%s", strings.Join(col, "\n"))
	}
	before := h.num
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: y + 1})
	h.Update(tea.PasteMsg{Content: "can you"})
	if h.num != before || h.quitAsk == 0 || h.quit {
		t.Errorf("click at the ask: focused %d (was %d), ask %d, quit %v", h.num, before, h.quitAsk, h.quit)
	}
	if got := ansi.Strip(h.View().Content); !strings.Contains(got, "for next time?") {
		t.Errorf("the ask left the screen:\n%s", got)
	}
}
