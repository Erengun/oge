package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// Several Chats (docs/ux/desk.md, slice 3): /new opens another Chat while
// this one keeps working, /close ends the focused one, and /chats or
// alt+1…9 switch between them. A Chat keeps its number for its whole life.

// newCmd is /new: another Chat, focused, while this one goes on. A Chat
// that has opened nothing yet is only started afresh, as /clear does.
// /new <path> opens it in the repo at path (docs/ux/desk.md, Projects
// P2); an idle Chat moves there instead.
func (h *home) newCmd(args []string) (tea.Cmd, string) {
	if len(args) > 1 {
		return nil, usageOf("/new")
	}
	rc := h.repoCtx
	if len(args) == 1 {
		root, problem := projectRoot(args[0], h.root, h.getenv)
		if problem != "" {
			return nil, problem
		}
		if repoKey(root) != repoKey(h.root) {
			if h.repoFor == nil {
				return nil, "/new: this session can't open another repo"
			}
			var lines []string
			if rc, lines = h.repoFor(root); rc == nil {
				return nil, strings.Join(lines, "; ")
			}
			defer func() { h.msg = append(h.msg, lines...) }() // its config's problem, under its box
		}
	}
	if h.chat == nil || (h.turn == nil && h.verify == nil && h.chat.get() == nil) {
		cmd := h.resetView()
		if rc != h.repoCtx {
			h.moveTo(rc)
		}
		return cmd, ""
	}
	h.openView()
	return nil, ""
}

// openView opens another Chat, focused, and says the old one stays open:
// /new's, and the Inbox's for a task.
func (h *home) openView() {
	n, old, v := h.working(), h.num, h.newView()
	if rc != h.repoCtx {
		v = h.newViewIn(rc)
	}
	h.views = append(h.views, v)
	h.switchTo(v)
	h.note = fmt.Sprintf("Chat %d stays open · /chats switches back", old)
	if n >= h.maxActive() && !h.capNoted {
		// Said once, in place of that: the Chat opens all the same.
		h.capNoted = true
		h.note = fmt.Sprintf("%d Chats are working; research says about 3 is what one person can keep track of", n)
	}
}

// moveTo puts the focused Chat, idle and opened nothing, in the repo rc:
// its Chat, header, setup and history are rc's from now on.
func (h *home) moveTo(rc *repoCtx) {
	if h.chat != nil {
		h.closed = append(h.closed, h.chat) // quitting waits for its closing
	}
	h.repoCtx, h.chat = rc, rc.hc.clone()
	if rc.sel != nil {
		h.side.frozen = rc.sel.lastFrozen
	}
	h.navigating = false
}

// working counts the open Chats with a turn or a check in flight.
func (h *home) working() int {
	n := 0
	for _, v := range h.views {
		if v.turn != nil || v.verify != nil {
			n++
		}
	}
	return n
}

// maxActive is [desk] max_active: past it, /new says once that more
// working Chats are more than one person tracks, and never refuses.
func (h *home) maxActive() int {
	if h.deskMax > 0 {
		return h.deskMax
	}
	return pipeline.DefaultDeskMaxActive
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
	case "/advise", "/new", "/chats", "/close", "/inbox":
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
	mark, state := d.h.mark(v)
	line := fmt.Sprintf("%d %s %s  %s", v.num, mark, clean(v.caption()), d.h.st.dim(state))
	if v == d.h.chatView {
		line += d.h.st.dim("  (this one)")
	}
	if i == m.Index() {
		fmt.Fprint(w, " "+d.h.st.accent("> ")+line)
		return
	}
	fmt.Fprint(w, "   "+line)
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

// quietAfter is [desk] quiet: how long a turn may go without an agent
// event before its Chat shows quiet rather than working.
func (h *home) quietAfter() time.Duration {
	if h.deskQuiet > 0 {
		return h.deskQuiet
	}
	return pipeline.DefaultDeskQuiet
}

// mark is v's glyph now.
func (h *home) mark(v *chatView) (mark, coarse string) {
	return glyph(v, h.clock(), h.quietAfter())
}

// seenAfter is how long a Chat holds the focus before its ✓ counts as
// seen; a key pressed in it counts at once (docs/ux/desk.md, G1).
const seenAfter = time.Second

// glyph is the Chat's exact state for the rail and the header counts: the
// mark and its coarse word. The human's part comes first, then a failed
// Check, then a turn gone quiet for quiet (never shown as working), work,
// an Accepted cut neither applied nor seen yet, and idle. It names no tool.
//
// A failed Check is news until the human answers it: a message, send back
// or advice they sent after the cut (an automatic turn answers nothing).
// What that turn leads to is shown instead, so a turn that ends without a
// cut leaves the Chat idle, not ✗ again; the sidebar still says the Check
// failed until the next cut. A send-back still held is ✗ whenever no turn
// or check runs, as the status line says.
func glyph(v *chatView, now time.Time, quiet time.Duration) (mark, coarse string) {
	switch {
	case v.cutGate != nil, v.stale != nil, v.turn != nil && len(v.turn.asks) > 0:
		return "▲", "needs you"
	case v.turn == nil && v.verify == nil && (v.pendingFB != nil || v.side.outcome == run.Failed && !v.answered):
		return "✗", "check failed"
	case v.turn != nil && !v.lastEvent.IsZero() && now.Sub(v.lastEvent) >= quiet:
		return "?", "quiet"
	case v.turn != nil, v.verify != nil:
		return "●", "working"
	case v.last != nil && v.side.outcome == run.Accepted && !v.seenBy(now) && !v.cutDelivered():
		return "✓", "done"
	}
	return "○", "idle"
}

// deskTickMsg redraws the screen at when, the moment v's glyph changes
// with no message to show it: a turn gone quiet, a ✓ in view seen.
type deskTickMsg struct {
	view *chatView
	at   time.Time
}

// redrawAt is when v's glyph next changes on its own: its turn going
// quiet, or its ✓ seen while it has the focus. Zero for never.
func (h *home) redrawAt(v *chatView, now time.Time) time.Time {
	var at time.Time
	if v.turn != nil && !v.lastEvent.IsZero() {
		if q := v.lastEvent.Add(h.quietAfter()); q.After(now) {
			at = q
		}
	}
	if !v.focusAt.IsZero() {
		if m, _ := glyph(v, now, h.quietAfter()); m == "✓" {
			if s := later(v.focusAt, v.doneAt).Add(seenAfter); at.IsZero() || s.Before(at) {
				at = s
			}
		}
	}
	return at
}

// deskTick is the one redraw the Chat being handled needs next, when no
// earlier one is due: nothing animates, the screen is only drawn again at
// that moment.
func (h *home) deskTick() tea.Cmd {
	v := h.chatView
	if h.redraw == nil || v == nil {
		return nil
	}
	now := h.clock()
	at := h.redrawAt(v, now)
	if at.IsZero() || v.tickAt.After(now) && !v.tickAt.After(at) {
		return nil
	}
	v.tickAt = at
	return h.redraw(at.Sub(now), func(time.Time) tea.Msg { return deskTickMsg{view: v, at: at} })
}

// withTick adds a deskTick to cmd.
func withTick(cmd, tick tea.Cmd) tea.Cmd {
	if tick == nil {
		return cmd
	}
	return tea.Batch(cmd, tick)
}

// seenBy reports whether the human has seen the Chat's Accepted cut by
// now: a key pressed in it, or the focus held for seenAfter since it was
// both focused and done.
func (v *chatView) seenBy(now time.Time) bool {
	if v.seen {
		return true
	}
	if v.focusAt.IsZero() {
		return false
	}
	return now.Sub(later(v.focusAt, v.doneAt)) >= seenAfter
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// deskCounts is the header's tally over the open Chats, with several
// open: ▲N needs you · ●N working · ✓N, each zero count left out.
func (h *home) deskCounts() string {
	if len(h.views) < 2 {
		return ""
	}
	n := map[string]int{}
	for _, v := range h.views {
		m, _ := h.mark(v)
		n[m]++
	}
	st := h.st
	var parts []string
	if n["▲"] > 0 {
		parts = append(parts, st.accent(fmt.Sprintf("▲%d needs you", n["▲"])))
	}
	if n["●"] > 0 {
		parts = append(parts, fmt.Sprintf("●%d working", n["●"]))
	}
	if n["✓"] > 0 {
		parts = append(parts, st.ok(fmt.Sprintf("✓%d", n["✓"])))
	}
	return strings.Join(parts, st.dim(" · "))
}

// Rail geometry (docs/ux/desk.md, Layout): a column railWidth wide at
// the screen's left, from railMinCols columns up, with several Chats open.
// Ctrl+T turns it off with the sidebar.
const (
	railWidth   = 14
	railMinCols = 100
)

// railOn reports whether the rail is drawn. With one Chat it would say
// nothing the screen doesn't, as the header counts don't.
func (h *home) railOn() bool {
	return h.full && !h.sidebarOff && h.termW >= railMinCols && len(h.views) > 1
}

// railHit is where the rail drew a Chat: screen row y, columns x0 to x1.
type railHit struct {
	y, x0, x1 int
	v         *chatView
}

// rail draws the column, height rows tall, each railWidth wide. Chats
// that need you, failed, or are at work take two rows each, number and
// glyph and title, then the coarse state and its age; done and idle ones
// fold below a rule, a number and a glyph each. Both keep number order:
// a row never moves for another Chat's sake. Nothing animates and no
// tool is named.
func (h *home) rail(height int) []string {
	st := h.st
	const inner = railWidth - 2
	now, quiet := h.clock(), h.quietAfter()
	paint := func(mark string) string {
		switch mark {
		case "▲":
			return st.accent(mark)
		case "✗":
			return st.bad(mark)
		case "?":
			return st.warn(mark)
		case "✓":
			return st.ok(mark)
		case "○":
			return st.dim(mark)
		}
		return mark
	}
	var body []string
	var hits []railHit
	var folded []*chatView
	for _, v := range h.views {
		mark, coarse := glyph(v, now, quiet)
		if mark == "✓" || mark == "○" {
			folded = append(folded, v)
			continue
		}
		head := strconv.Itoa(v.num) + " " + mark + " "
		room := inner - ansi.StringWidth(head)
		tail := ""
		if v == h.chatView {
			tail, room = "◀", room-1
		}
		title := ansi.Truncate(clean(v.caption()), max(room, 0), "…")
		title += strings.Repeat(" ", max(room-ansi.StringWidth(title), 0))
		if v == h.chatView {
			title = st.bold(title)
		}
		y := len(body)
		body = append(body, strconv.Itoa(v.num)+" "+paint(mark)+" "+title+st.accent(tail), st.dim(railState(coarse, railAge(now, v.since(mark)), inner)))
		hits = append(hits, railHit{y, 0, inner, v}, railHit{y + 1, 0, inner, v})
	}
	if len(folded) > 0 {
		if len(body) > 0 {
			body = append(body, st.border(strings.Repeat("─", inner)))
		}
		line, w := "", 0
		for _, v := range folded {
			mark, _ := glyph(v, now, quiet)
			tok, painted := strconv.Itoa(v.num)+mark, strconv.Itoa(v.num)+paint(mark)
			if v == h.chatView {
				tok, painted = tok+"◀", painted+st.accent("◀")
			}
			tw := ansi.StringWidth(tok)
			if w > 0 && w+1+tw > inner {
				body = append(body, line)
				line, w = "", 0
			}
			if w > 0 {
				line, w = line+" ", w+1
			}
			hits = append(hits, railHit{len(body), w, w + tw, v})
			line, w = line+painted, w+tw
		}
		body = append(body, line)
	}

	// The box: the top border names it; rows and hits move in by one.
	b := st.border
	out := make([]string, 0, height)
	out = append(out, b("╭ DESK "+strings.Repeat("─", railWidth-8)+"╮"))
	for i := 0; i < height-2; i++ {
		l := ""
		if i < len(body) {
			l = ansi.Truncate(body[i], inner, "…")
		}
		out = append(out, b("│")+l+strings.Repeat(" ", max(inner-ansi.StringWidth(l), 0))+b("│"))
	}
	out = append(out, b("╰"+strings.Repeat("─", railWidth-2)+"╯"))
	h.railHits = h.railHits[:0]
	for _, hit := range hits {
		if hit.y < height-2 {
			h.railHits = append(h.railHits, railHit{hit.y + 1, hit.x0 + 1, hit.x1 + 1, hit.v})
		}
	}
	return out[:min(height, len(out))]
}

// railState is a rail row's second line: the coarse state and its age,
// indented under the title while they fit in width, the age dropped
// when even the word alone barely fits.
func railState(coarse, age string, width int) string {
	s := coarse
	if age != "" {
		s += " " + age
	}
	switch {
	case ansi.StringWidth(s)+2 <= width:
		return "  " + s
	case ansi.StringWidth(s) <= width:
		return s
	}
	return coarse
}

// railAge is how long a state has held, coarse: minutes, hours, days;
// nothing under a minute, and nothing for an unknown start.
func railAge(now, since time.Time) string {
	if since.IsZero() {
		return ""
	}
	switch d := now.Sub(since); {
	case d < time.Minute:
		return ""
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
}

// since is when v's glyph mark began, as near as the Chat knows: its turn's
// start for work, its cut's end for done and failed, and its last agent
// event for a quiet turn or an ask.
func (v *chatView) since(mark string) time.Time {
	switch mark {
	case "●":
		return v.side.since
	case "✓", "✗":
		return v.doneAt
	}
	return v.lastEvent
}

// railClick is a left click at x, y on the rail: the Chat drawn there
// takes the focus, as alt+N would give it, and only when alt+N could.
func (h *home) railClick(x, y int) {
	if h.overlaid() || h.run != nil || h.job != nil {
		return
	}
	for _, hit := range h.railHits {
		if hit.y == y && x >= hit.x0 && x < hit.x1 && slices.Contains(h.views, hit.v) {
			h.switchTo(hit.v)
			return
		}
	}
}
