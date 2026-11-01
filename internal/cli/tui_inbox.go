package cli

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/task"
)

// The Inbox (docs/ux/desk.md, "Inbox"): ctrl+space or /inbox. One card
// at a time for what the open Chats want from the human: ▲ first, then
// ✗, then ✓, oldest first within each. It holds no list of its own: the
// cards are the Chats' glyphs, so a card goes when its glyph does (the
// Gate answered, the failure answered by a turn, the ✓ seen).
//
// Enter alone decides nothing here. The only Enter that does anything
// sends a task typed in the Inbox, which opens a new Chat.

// inboxState is the Inbox while it is open.
type inboxState struct {
	cur  *chatView // the Chat whose card is shown
	mark string    // its glyph when it was shown
	at   time.Time // when that glyph began, for its place in the order
	// comp is a task being typed: Enter sends it to a new Chat. While it
	// holds text every key is typing, none is a card's action.
	comp *composer
	note string // what the last action said
	// shown is when cur's card came on screen (G1: a ✓ shown about a
	// second counts as seen); unread is a ✓ the human marked unread with u,
	// which the Inbox then leaves unread.
	shown  time.Time
	ticked time.Time // the shown its redraw was asked for
	unread *chatView
}

// inboxItem is one card: a Chat, its glyph and when that began.
type inboxItem struct {
	v    *chatView
	mark string
	at   time.Time
}

func inboxRank(mark string) int {
	return strings.Index("▲✗✓", mark) / len("▲")
}

// inboxItems are the cards, in the Inbox's order.
func (h *home) inboxItems() []inboxItem {
	var out []inboxItem
	for _, v := range h.views {
		m, _ := h.mark(v)
		if m != "▲" && m != "✗" && m != "✓" {
			continue
		}
		at := v.doneAt
		if m == "▲" {
			at = v.askedAt
			if at.IsZero() {
				at = v.lastEvent
			}
		}
		out = append(out, inboxItem{v, m, at})
	}
	slices.SortStableFunc(out, inboxOrder)
	return out
}

func inboxOrder(a, b inboxItem) int {
	return cmp.Or(inboxRank(a.mark)-inboxRank(b.mark), a.at.Compare(b.at), a.v.num-b.v.num)
}

// inboxCount is N for `^space inbox (N)`.
func (h *home) inboxCount() int { return len(h.inboxItems()) }

// cards are the Inbox's cards as shown: the items, and the card on screen
// while it stays, a ✓ seen by being shown included (G1).
func (h *home) cards() []inboxItem {
	items := h.inboxItems()
	in := h.inbox
	if in == nil || in.cur == nil || !slices.Contains(h.views, in.cur) {
		return items
	}
	if slices.ContainsFunc(items, func(it inboxItem) bool { return it.v == in.cur }) {
		return items
	}
	if in.mark == "✓" && in.cur.seen {
		items = append(items, inboxItem{in.cur, in.mark, in.at})
		slices.SortStableFunc(items, inboxOrder)
	}
	return items
}

// card is the card shown, and its place among the cards; ok is false for
// an empty Inbox. A card gone since takes the first one's place.
func (h *home) card() (it inboxItem, i, n int, ok bool) {
	cs := h.cards()
	if len(cs) == 0 {
		return inboxItem{}, 0, 0, false
	}
	i = max(slices.IndexFunc(cs, func(c inboxItem) bool { return c.v == h.inbox.cur }), 0)
	return cs[i], i, len(cs), true
}

// inboxKeys are the keys that open the Inbox: ctrl+space, which some
// terminals send as NUL, read as ctrl+@.
func inboxKey(key string) bool { return key == "ctrl+space" || key == "ctrl+@" }

// canOpenInbox: as a rail click, never over a panel, a Run or a shell.
func (h *home) canOpenInbox() bool {
	return h.inbox == nil && !h.overlaid() && h.run == nil && h.job == nil
}

// openInbox opens the Inbox on its first card.
func (h *home) openInbox() {
	h.inbox = &inboxState{comp: h.composer()}
	h.showCard()
}

// showCard settles which card is on screen; a new one starts its
// seen-clock.
func (h *home) showCard() {
	in := h.inbox
	it, _, _, ok := h.card()
	if !ok {
		in.cur, in.mark = nil, ""
		return
	}
	if it.v != in.cur || it.mark != in.mark {
		in.cur, in.mark, in.at, in.shown = it.v, it.mark, it.at, h.clock()
	}
}

// inboxCmd is /inbox.
func (h *home) inboxCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/inbox")
	}
	h.openInbox()
	return nil, ""
}

// inboxSeenMsg is the moment the ✓ card shown at shown has been on
// screen seenAfter.
type inboxSeenMsg struct {
	v     *chatView
	shown time.Time
}

// inboxTick asks for the redraw that marks a ✓ card seen, once per card
// shown. Nil when nothing is due.
func (h *home) inboxTick() tea.Cmd {
	in := h.inbox
	if in == nil {
		return nil
	}
	h.showCard()
	if in.cur == nil || in.mark != "✓" || in.cur.seen || in.unread == in.cur || h.redraw == nil || in.ticked.Equal(in.shown) {
		return nil
	}
	in.ticked = in.shown // one redraw per card shown
	v, shown := in.cur, in.shown
	return h.redraw(seenAfter, func(time.Time) tea.Msg { return inboxSeenMsg{v, shown} })
}

// inboxSeen marks the ✓ card seen when it is still on screen.
func (h *home) inboxSeen(m inboxSeenMsg) {
	if in := h.inbox; in != nil && in.cur == m.v && in.shown.Equal(m.shown) && in.unread != m.v {
		m.v.seen = true
	}
}

// inboxInput takes a key or a paste while the Inbox is open; ok is false
// for a message that isn't for it.
func (h *home) inboxInput(msg tea.Msg) (tea.Cmd, bool) {
	in := h.inbox
	switch m := msg.(type) {
	case tea.PasteMsg:
		in.comp.change("paste", func() { in.comp.insertPaste(m.Content) })
		return nil, true
	case tea.KeyPressMsg:
		return h.inboxKeyPress(m), true
	}
	return nil, false
}

func (h *home) inboxKeyPress(msg tea.KeyPressMsg) tea.Cmd {
	in := h.inbox
	key := msg.String()
	in.note = ""
	switch {
	case key == "esc" || key == "ctrl+c" || inboxKey(key):
		h.inbox = nil
		return nil
	case !in.comp.empty():
		if key == "enter" {
			return h.inboxTask()
		}
		in.comp.key(msg)
		return nil
	}
	it, i, n, ok := h.card()
	v := it.v
	switch key {
	case "enter":
		return nil // Enter alone decides nothing (ADR-0022)
	case "tab", "shift+tab":
		if !ok {
			return nil
		}
		step := 1
		if key == "shift+tab" {
			step = n - 1
		}
		next := h.cards()[(i+step)%n]
		in.cur, in.mark, in.at, in.shown = next.v, next.mark, next.at, h.clock()
		return nil
	case "o":
		if ok {
			h.inbox = nil
			h.seeOn(v)
		}
		return nil
	case "s":
		if ok && it.mark == "✗" {
			cmd, note := h.on(v, h.sendBack)
			in.note = note
			return cmd
		}
	case "a":
		if ok && it.mark == "✗" {
			// /advise needs the question: the box holds it, the human asks.
			h.inbox = nil
			h.seeOn(v)
			if h.comp.empty() {
				h.comp.set("/advise ")
			}
			return nil
		}
	case "d":
		if ok && it.mark == "✓" && v.last != nil {
			h.inbox = nil
			h.seeOn(v)
			return h.actNow("d", "")
		}
	case "u":
		if ok && it.mark == "✓" {
			v.seen = false
			in.unread = v
			in.note = "left unread"
			return nil
		}
	}
	// Any other key starts a task; a card's key on a card without that
	// action types too, as the hint offers only the card's own keys.
	in.comp.key(msg)
	return nil
}

// seeOn focuses v: the switch the human asked for from the Inbox. The
// letter may have begun a word: what follows it, until the typing
// pauses for actionsArm, reaches no ask, stale choice or Gate (see
// inboxCarry).
func (h *home) seeOn(v *chatView) {
	if slices.Contains(h.views, v) {
		h.switchTo(v)
		h.inboxArm = h.clock()
	}
}

// inboxCarry reports whether k was typed on from an Inbox letter into
// the Chat it focused, and so is dropped: while that Chat has an ask, a
// stale choice or a Gate open, as queueKey drops keys under a Gate that
// opened during typing. Each dropped key restarts the window, so a word
// typed on at any speed is dropped whole; the first key after a pause of
// actionsArm is the Chat's. Ctrl+C is never dropped, nor the Inbox's own
// key.
func (h *home) inboxCarry(k tea.KeyPressMsg) bool {
	if h.inboxArm.IsZero() || h.clock().Sub(h.inboxArm) >= actionsArm || k.String() == "ctrl+c" || inboxKey(k.String()) {
		return false
	}
	// Only a key that could type a word or decide something carries: the
	// human can scroll the transcript above the ask at once.
	if k.Text == "" && k.String() != "enter" {
		return false
	}
	switch {
	case h.stale != nil, h.cutGate != nil,
		h.verify != nil && h.verify.gate != nil, h.run != nil && h.run.gate != nil,
		h.turn != nil && len(h.turn.asks) > 0:
		h.inboxArm = h.clock()
		return true
	}
	return false
}

// on runs f as Chat v's, as route runs a background Chat's message: the
// Inbox stays, and the card goes once its glyph does.
func (h *home) on(v *chatView, f func() (tea.Cmd, string)) (tea.Cmd, string) {
	if !slices.Contains(h.views, v) {
		return nil, ""
	}
	prev, bg := h.chatView, h.background
	h.chatView, h.background = v, v != prev
	defer func() { h.chatView, h.background = prev, bg }()
	return f()
}

// inboxTask sends the task typed in the Inbox as a new Chat's first
// message, in the focused Chat's project. A focused Chat with nothing
// opened yet takes it itself. A slash or shell line is left in the new
// Chat's box, unsent: the Inbox only starts tasks.
func (h *home) inboxTask() tea.Cmd {
	text := task.StripComments(h.inbox.comp.expanded())
	h.inbox = nil
	if task.IsEmpty(text) {
		return nil
	}
	fresh := h.turn == nil && h.verify == nil && h.comp.empty() && (h.chat == nil || h.chat.get() == nil)
	if !fresh {
		h.openView()
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!") {
		h.comp.set(text)
		return nil
	}
	if n := hiddenCount(text); n > 0 {
		h.comp.set(text)
		h.hidden = &hiddenPrompt{text: text, n: n}
		return nil
	}
	_, cmd := h.submit(text)
	return cmd
}

// wanted is what the card's Chat wants of the human, in Öge's fixed words
// (the failing count and criterion ids are Öge's too).
func (h *home) wanted(it inboxItem) string {
	v := it.v
	switch it.mark {
	case "▲":
		switch {
		case v.cutGate != nil, v.verify != nil && v.verify.gate != nil:
			return "a Gate is waiting for you · o, then /apply"
		case v.stale != nil:
			return "newer edits, unchecked: apply the verified cut or wait"
		case v.turn != nil && len(v.turn.asks) > 0 && v.turn.asks[0].Questions != nil:
			return "a question is waiting for your answer"
		case v.turn != nil && len(v.turn.asks) > 0:
			return "Allow " + clean(v.turn.asks[0].Tool) + "?"
		}
		return "needs you"
	case "✗":
		if v.pendingFB != nil {
			first, _, _ := strings.Cut(v.pendingFB.text, "\n")
			if first = strings.TrimSpace(clean(first)); first != "" {
				return "the Check failed: " + first
			}
		}
		return "the Check failed"
	}
	return "Accepted, not applied"
}

// project is the Chat's project as the card names it: the repository
// the home was opened in.
func (h *home) project(*chatView) string {
	if parts := strings.Split(h.title, " · "); len(parts) > 1 {
		return parts[1]
	}
	return ""
}

// inboxLines draws the Inbox in the live area: the card, its keys, and
// the box a task is typed in. Its cursor is in that box.
func (h *home) inboxLines() (lines []string, curRow, curCol int) {
	st := h.st
	width := h.width
	if width <= 0 {
		width = 80
	}
	fit := func(s string) string { return ansi.Truncate(s, width, "…") }
	h.showCard()
	it, i, n, ok := h.card()
	head := " " + st.bold("Inbox")
	if ok {
		head += st.dim(" · " + strconv.Itoa(i+1) + " of " + strconv.Itoa(n))
	}
	lines = append(lines, "", fit(head))
	keys := "esc close"
	if !ok {
		lines = append(lines, fit(" "+st.dim("nothing needs you")))
	} else {
		v := it.v
		paint := map[string]func(string) string{"▲": st.accent, "✗": st.bad, "✓": st.ok}[it.mark]
		id := []string{strconv.Itoa(v.num) + " " + clean(v.caption())}
		if p := h.project(v); p != "" {
			id = append(id, p)
		}
		if a, _ := v.side.binding(); a != "" {
			id = append(id, clean(a))
		}
		if !it.at.IsZero() {
			id = append(id, "waiting "+elapsed(h.clock().Sub(it.at)))
		}
		lines = append(lines, fit(" "+paint(it.mark)+" "+strings.Join(id, st.dim(" · "))))
		lines = append(lines, fit("   "+h.wanted(it)))
		// The Evidence: the Verdict and the files, as the sidebar has them.
		if s := v.side.verdict; s != "" {
			lines = append(lines, fit("   "+st.dim("verdict ")+clean(s)))
		}
		const most = 4
		for j, f := range v.side.changes {
			if j == most {
				lines = append(lines, fit("   "+st.dim(fmt.Sprintf("… +%d more", len(v.side.changes)-most))))
				break
			}
			cs := counts(st, f.add, f.del)
			lines = append(lines, fit("   "+clean(f.path)+" "+cs))
		}
		if v.side.verdict == "" && len(v.side.changes) == 0 {
			lines = append(lines, fit("   "+st.dim("no Evidence yet")))
		}
		switch it.mark {
		case "▲":
			keys = "o open"
		case "✗":
			keys = "s send back · a advise · o open"
		case "✓":
			keys = "d diff · o open · u unread"
		}
		if n > 1 {
			keys += " · tab next"
		}
		keys += " · esc close"
	}
	lines = append(lines, fit(" "+st.dim(keys)))
	if h.inbox.note != "" {
		lines = append(lines, fit(" "+st.dim(h.inbox.note)))
	}
	// The task box.
	c := h.inbox.comp
	inner := max(width-4-promptWidth, 1)
	rows, cr, cc := c.layout(inner)
	lines = append(lines, st.border("╭"+strings.Repeat("─", max(width-2, 0))+"╮"))
	top := len(lines)
	for j, r := range rows {
		pre := "  "
		if j == 0 {
			pre = st.accent("❯") + " "
		}
		if c.empty() && j == 0 {
			r = st.dim("type a task · ⏎ opens a new Chat")
		}
		r = ansi.Truncate(r, inner, "…")
		lines = append(lines, st.border("│")+" "+pre+r+strings.Repeat(" ", max(inner-ansi.StringWidth(r), 0))+" "+st.border("│"))
	}
	lines = append(lines, st.border("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
	return lines, top + cr, 2 + promptWidth + cc
}

// inboxView is the Inbox as the home's view.
func (h *home) inboxView() tea.View {
	lines, row, col := h.inboxLines()
	v := tea.NewView(strings.Join(lines, "\n"))
	c := tea.NewCursor(col, row)
	c.Blink = false
	v.Cursor = c
	return v
}

// inboxHint is `^space inbox (N)` for the box's status: "" with no card,
// with one Chat open, whose news is on screen already, and while a Run or
// a shell command keeps ctrl+space from opening it.
func (h *home) inboxHint() string {
	if len(h.views) < 2 || h.run != nil || h.job != nil {
		return ""
	}
	if n := h.inboxCount(); n > 0 {
		return "^space inbox (" + strconv.Itoa(n) + ")"
	}
	return ""
}
