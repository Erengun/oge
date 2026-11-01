package cli

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
	for _, want := range []string{"Chats", "1  new", "2  new", "(this one)"} {
		if !strings.Contains(got, want) {
			t.Errorf("picker lacks %q:\n%s", want, got)
		}
	}
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
