package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Several Chats (docs/ux/desk.md, slice 3): /new opens another Chat while
// this one keeps working, /close ends the focused one, and /chats or
// alt+1…9 switch between them. A Chat keeps its number for its whole life.

// newCmd is /new: another Chat, focused, while this one goes on. A Chat
// that has opened nothing yet is only started afresh, as /clear does.
func (h *home) newCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/new")
	}
	if h.chat == nil || (h.turn == nil && h.verify == nil && h.chat.get() == nil) {
		return h.resetView(), ""
	}
	old, v := h.num, h.newView()
	h.views = append(h.views, v)
	h.switchTo(v)
	h.note = fmt.Sprintf("Chat %d stays open · /chats switches back", old)
	return nil, ""
}

// clearCmd is /clear: this Chat starts afresh; its old Session closes.
func (h *home) clearCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/clear")
	}
	return h.resetView(), ""
}

// closeCmd is /close: the focused Chat ends, and the nearest one is
// focused; the last one leaves an empty Chat behind. No other Chat is
// renumbered.
func (h *home) closeCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/close")
	}
	v := h.chatView
	cmd := h.resetView()
	if v.chat != nil {
		h.closed = append(h.closed, v.chat) // quitting waits for its closing
	}
	i := slices.Index(h.views, v)
	if i < 0 {
		return cmd, ""
	}
	h.views = slices.Delete(h.views, i, i+1)
	if len(h.views) == 0 {
		h.views = append(h.views, h.newView())
	}
	h.switchTo(h.views[min(i, len(h.views)-1)])
	return cmd, ""
}

// resetView starts the focused Chat afresh: the next message opens a new
// Session, and the old one's cut in flight ends unshown.
func (h *home) resetView() tea.Cmd {
	h.last, h.thread = nil, nil
	h.side.verdict, h.side.outcome, h.side.changes, h.side.tokens, h.side.cost = "", "", nil, nil, nil
	h.side.took, h.side.turns, h.side.steps, h.side.ctx = 0, 0, 0, nil
	h.unchecked, h.uncheckedChanged = false, 0
	h.steps = nil // a result of the old Session hangs on no new step
	h.name = ""
	if v := h.verify; v != nil {
		v.cancel()
		h.verify = nil
	}
	h.actions = false
	h.pendingFB = nil
	if h.adv != nil {
		h.dismissAdvice(h.adv)
		h.adv.cancel()
		h.adv = nil
	}
	h.dropAdviceEdit()
	cmd := h.chat.reset(h.chatView)
	h.resetting = cmd != nil
	return cmd
}

// leave reports whether Ctrl+C quits now. Quitting stops every Chat, so
// with another one at work the first press only says so, and a second
// one in a row quits.
func (h *home) leave() bool {
	var busy []string
	for _, v := range h.views {
		if v != h.chatView && (v.turn != nil || v.verify != nil) {
			busy = append(busy, strconv.Itoa(v.num))
		}
	}
	if h.quitArmed || len(busy) == 0 {
		return true
	}
	h.quitArmed = true
	line := "Chat " + busy[0] + " is working · ctrl+c again quits and stops it"
	if len(busy) > 1 {
		line = "Chats " + strings.Join(busy, ", ") + " are working · ctrl+c again quits and stops them"
	}
	h.msg = append(h.msg, line)
	return false
}

// slashDuringTurn reports whether a command runs while the focused
// Chat's turn is in flight, rather than being queued as a message.
func slashDuringTurn(word string) bool {
	switch word {
	case "/advise", "/new", "/chats", "/close":
		return true
	}
	return false
}

// altNum is the Chat number alt+1…9 asks for; 0 for any other key.
func altNum(key string) int {
	d, ok := strings.CutPrefix(key, "alt+")
	if !ok || len(d) != 1 || d[0] < '1' || d[0] > '9' {
		return 0
	}
	n, _ := strconv.Atoi(d)
	return n
}

// switchKey is alt+N: it focuses Chat N, when there is one. A key the
// keybindings file gave to an action is that action's. A shell command
// running is the focused Chat's, its output too: no switch until it ends.
func (h *home) switchKey(key string) bool {
	n := altNum(key)
	if n == 0 || h.run != nil || h.kb.actionFor(ctxComposer, key) != "" {
		return false
	}
	if h.job != nil {
		return true
	}
	if v := h.viewNum(n); v != nil {
		h.switchTo(v)
	}
	return true
}

// chatsCmd is /chats: a picker over the open Chats, at any width.
func (h *home) chatsCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/chats")
	}
	items := make([]list.Item, len(h.views))
	sel := 0
	for i, v := range h.views {
		items[i] = chatItem{v}
		if v == h.chatView {
			sel = i
		}
	}
	l := list.New(items, chatDelegate{h}, 100, min(len(items), max(h.height-8, 3))+4)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	l.Select(sel)
	h.chats = &l
	return nil, ""
}

type chatItem struct{ v *chatView }

func (i chatItem) FilterValue() string { return i.v.caption() }

type chatDelegate struct{ h *home }

func (chatDelegate) Height() int                         { return 1 }
func (chatDelegate) Spacing() int                        { return 0 }
func (chatDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d chatDelegate) Render(w io.Writer, m list.Model, i int, it list.Item) {
	v := it.(chatItem).v
	glyph, state := v.state()
	line := fmt.Sprintf("%d %s %s  %s", v.num, glyph, clean(v.caption()), d.h.st.dim(state))
	if v == d.h.chatView {
		line += d.h.st.dim("  (this one)")
	}
	if i == m.Index() {
		fmt.Fprint(w, " "+d.h.st.accent("> ")+line)
		return
	}
	fmt.Fprint(w, "   "+line)
}

// state is the Chat's coarse state, glyph and word, as /chats shows it:
// waiting on the human first, then at work, else idle. It is static and
// names no tool (docs/ux/desk.md).
func (v *chatView) state() (glyph, word string) {
	switch {
	case v.turn != nil && len(v.turn.asks) > 0, v.stale != nil, v.pendingFB != nil:
		return "▲", "needs you"
	case v.turn != nil, v.verify != nil:
		return "●", "working"
	}
	return "○", "idle"
}

// chatsKey is a key while /chats is open: Enter or a Chat's number
// focuses it; Esc closes the picker.
func (h *home) chatsKey(msg tea.KeyPressMsg) {
	key := msg.String()
	if n, err := strconv.Atoi(key); err == nil {
		if v := h.viewNum(n); v != nil {
			h.chats = nil
			h.switchTo(v)
		}
		return
	}
	switch key {
	case "esc", "ctrl+c":
		h.chats = nil
	case "enter":
		if it, ok := h.chats.SelectedItem().(chatItem); ok && slices.Contains(h.views, it.v) {
			h.chats = nil
			h.switchTo(it.v)
		}
	default:
		l, _ := h.chats.Update(msg)
		h.chats = &l
	}
}

// chatsLines draws /chats under the box.
func (h *home) chatsLines() []string {
	st := h.st
	var out []string
	for _, l := range strings.Split(h.chats.View(), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, ansi.Truncate(strings.TrimRight(l, " "), max(h.width, 20), "…"))
		}
	}
	return append(out, " "+st.dim("⏎ or a number switches · esc"))
}
