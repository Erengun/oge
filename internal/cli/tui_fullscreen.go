package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The full-screen home (ADR-0022, amended): the bare-oge Chat draws on the
// alternate screen. The transcript is a viewport of its own, pinned to the
// bottom, and the live area (what used to be the inline frame: the box, a
// turn's ask, panels) sits under it. Nothing here decides anything: it is a
// view of what the Chat prints, and its keys only move around the screen.
// The layout is after pitago (MIT, see THIRD_PARTY_NOTICES).

// trEntry is one printed block of the transcript. text is what the home
// printed; render, when set, draws it at a width (markdown, tool boxes), with
// expand showing tool output in full.
type trEntry struct {
	text   string
	render func(width int, expand bool) string
}

// trPos is a cell of the transcript: an index into its rendered lines and a
// column in the line.
type trPos struct{ line, col int }

// transcript is the viewport's state. The rendered lines are cached per width
// and expand setting.
type transcript struct {
	entries []trEntry
	expand  bool // Ctrl+O: tool output in full

	lines  []string // entries rendered at width
	rawN   int      // entries rendered
	width  int
	exp    bool
	gen    int   // bumped when every entry changes
	starts []int // the first rendered line of each rendered entry
	genAt  int
	scroll bool // scrolled away from the bottom
	end    int  // while scrolled: the line after the last one shown

	// What the last view laid out, for the mouse.
	top, vh, vw int // the viewport's screen rows and width
	start       int // index of its first line
	chipX0      int // the chip's first column on the viewport's last row (0: none)
	dragging    bool
	sel         *[2]trPos // anchor and head of the selection
	selMoved    bool
	lastReply   string // the agent's latest reply, raw: what alt+y copies
	freshReply  bool   // the next claim starts a new reply
}

func (t *transcript) add(e trEntry) { t.entries = append(t.entries, e) }

// redraw drops the cache from entry i on, for an entry that changed in place:
// the entries before it keep their rendered lines.
func (t *transcript) redraw(i int) {
	if i < 0 || i >= t.rawN {
		return
	}
	t.lines, t.starts, t.rawN = t.lines[:t.starts[i]], t.starts[:i], i
}

// render brings the cached lines up to date at width.
func (t *transcript) render(width int) []string {
	if t.width != width || t.exp != t.expand || t.gen != t.genAt || t.rawN > len(t.entries) {
		t.lines, t.starts, t.rawN, t.width, t.exp, t.genAt = nil, nil, 0, width, t.expand, t.gen
	}
	for ; t.rawN < len(t.entries); t.rawN++ {
		e := t.entries[t.rawN]
		t.starts = append(t.starts, len(t.lines))
		text := e.text
		if e.render != nil {
			text = e.render(width, t.expand)
		}
		t.lines = append(t.lines, strings.Split(ansi.Wrap(text, max(width, 1), ""), "\n")...)
	}
	return t.lines
}

// window is the lines to show in a viewport of vh rows at width: the bottom
// ones, or those above the point the human scrolled to. It sets start.
func (t *transcript) window(width, vh int) []string {
	lines := t.render(width)
	n := len(lines)
	end := n
	if t.scroll {
		end = min(max(t.end, min(vh, n)), n)
		if end >= n {
			t.scroll = false
		}
	}
	t.end = end
	t.start = max(end-vh, 0)
	return lines[t.start:end]
}

// move scrolls by delta lines (negative: up, to older lines).
func (t *transcript) move(delta int) {
	n := len(t.render(t.width))
	if !t.scroll {
		t.end = n
	}
	t.end = min(max(t.end+delta, min(t.vh, n)), n)
	t.scroll = t.end < n
}

func (t *transcript) latest() { t.scroll = false }

// behind is how many lines lie under the viewport.
func (t *transcript) behind() int {
	if !t.scroll {
		return 0
	}
	return len(t.lines) - t.end
}

// text is the plain text of the selection, "" without one.
func (t *transcript) selected() string {
	if t.sel == nil {
		return ""
	}
	a, b := t.sel[0], t.sel[1]
	if b.line < a.line || (b.line == a.line && b.col < a.col) {
		a, b = b, a
	}
	var out []string
	for i := a.line; i <= b.line && i < len(t.lines); i++ {
		l := ansi.Strip(t.lines[i])
		c0, c1 := 0, ansi.StringWidth(l)
		if i == a.line {
			c0 = a.col
		}
		if i == b.line {
			c1 = min(b.col+1, c1)
		}
		out = append(out, strings.TrimRight(ansi.Cut(l, min(c0, c1), c1), " "))
	}
	return strings.Join(out, "\n")
}

// paintSel marks the selected cells of the visible line i (an index into the
// lines) in reverse video.
func (t *transcript) paintSel(i int, line string) string {
	if t.sel == nil {
		return line
	}
	a, b := t.sel[0], t.sel[1]
	if b.line < a.line || (b.line == a.line && b.col < a.col) {
		a, b = b, a
	}
	if i < a.line || i > b.line {
		return line
	}
	w := ansi.StringWidth(line)
	c0, c1 := 0, max(w, 1)
	if i == a.line {
		c0 = a.col
	}
	if i == b.line {
		c1 = b.col + 1
	}
	c1 = min(c1, max(w, 1))
	if c0 >= c1 {
		return line
	}
	mid := ansi.Cut(line, c0, c1)
	if mid == "" {
		return line
	}
	return ansi.Cut(line, 0, c0) + "\x1b[7m" + ansi.Strip(mid) + "\x1b[27m" + ansi.Cut(line, c1, max(w, c1))
}

// Sidebar geometry: it shows from sidebarMinCols columns up (pitago's
// breakpoint), sidebarWidth wide, and Ctrl+T turns it off.
const (
	sidebarWidth   = 36
	sidebarMinCols = 110
)

// sidebarOn reports whether the sidebar is drawn at the terminal's width.
func (h *home) sidebarOn() bool {
	return h.full && !h.sidebarOff && h.termW >= sidebarMinCols
}

// headRows are the rows above the transcript: the header and its rule, so
// clipped content never touches the title.
const headRows = 2

// layout sets the content width and height from the terminal's: the main
// column is the screen less the sidebar, and headRows go to the header.
func (h *home) layout() {
	if !h.full {
		return
	}
	w := h.termW
	if h.sidebarOn() {
		w -= sidebarWidth + 1
	}
	h.width, h.height = w, max(h.termH-headRows, 1)
	if h.run != nil {
		h.run.width, h.run.height = h.width, h.height
	}
	if h.diff != nil {
		h.diff.resize(h.width)
	}
}

// header is the top row: the title.
func (h *home) header(width int) string {
	st := h.st
	title := h.title
	first, rest, ok := strings.Cut(title, " · ")
	line := " " + st.bold(first)
	if ok {
		line += st.dim(" · " + rest)
	}
	return ansi.Truncate(line, width, "…")
}

// welcome is the empty transcript's block.
func (h *home) welcome() []string {
	st := h.st
	ready := st.ok("●") + " " + st.bold("ready")
	if h.turn != nil {
		ready = st.accent("●") + " " + st.bold("working")
	}
	hints := "/ commands · ! shell · ? keys"
	if h.termW >= sidebarMinCols {
		hints += " · ctrl+t sidebar"
	}
	out := []string{"", "  " + st.accent(st.bold("oge")) + "  " + ready}
	if len(h.setup) > 0 {
		// What the next turn uses, in a line: agent · mode · verifier.
		out = append(out, "  "+st.dim(strings.Replace(h.setup[0], "implement ", "", 1)))
	}
	return append(out, "  "+st.dim(hints), "")
}

// compose lays out the screen: the header, the transcript viewport, the live
// area at the bottom, and the sidebar beside them.
func (h *home) compose(v tea.View) tea.View {
	t := h.tr
	W, H := h.termW, h.termH
	if W <= 0 || H <= 0 {
		W, H = 80, 24
	}
	mainW := h.width
	if mainW <= 0 {
		mainW = W
	}
	live := strings.Split(strings.TrimSuffix(v.Content, "\n"), "\n")
	if strings.TrimSpace(v.Content) == "" {
		live = nil
	}
	curX, curY := -1, -1
	if v.Cursor != nil {
		curX, curY = v.Cursor.X, v.Cursor.Y
	}
	// The live area keeps its bottom rows when it is taller than the screen.
	if room := max(H-headRows, 0); len(live) > room {
		drop := len(live) - room
		live = live[drop:]
		curY -= drop
	}
	vh := max(H-headRows-len(live), 0)
	t.top, t.vh, t.vw, t.chipX0 = headRows, vh, mainW, 0
	var body []string
	if len(t.entries) == 0 {
		body = h.welcome()
		if len(body) > vh {
			body = body[:vh]
		}
		t.start = 0
	} else {
		body = unclip(t.window(mainW, vh), t.start)
		if t.sel != nil {
			for i := range body {
				body[i] = t.paintSel(t.start+i, body[i])
			}
		}
	}
	rows := make([]string, 0, H)
	rows = append(rows, h.header(mainW), h.st.border(strings.Repeat("─", mainW)))
	rows = append(rows, body...)
	for len(rows) < headRows+vh {
		rows = append(rows, "")
	}
	if n := t.behind(); n > 0 && vh > 0 {
		chip := h.st.accent(" ↓ " + plural(n, "line") + " below · jump to latest (end) ")
		w := ansi.StringWidth(ansi.Strip(chip))
		last := headRows + vh - 1
		x0 := max(mainW-w-1, 0)
		rows[last] = ansi.Truncate(rows[last], x0, "") + strings.Repeat(" ", max(x0-ansi.StringWidth(rows[last]), 0)) + chip
		t.chipX0 = x0 + 1
	}
	if toast := h.toastView(); len(toast) > 0 && vh > 0 {
		w := ansi.StringWidth(ansi.Strip(toast[0]))
		x0 := max(mainW-w-1, 0)
		for i, tl := range toast[:min(len(toast), vh)] {
			r := headRows + i
			rows[r] = ansi.Truncate(rows[r], x0, "") + strings.Repeat(" ", max(x0-ansi.StringWidth(rows[r]), 0)) + tl
		}
	}
	rows = append(rows, live...)
	for len(rows) < H {
		rows = append(rows, "")
	}
	rows = rows[:H]
	var side []string
	if h.sidebarOn() {
		side = h.sidebar(sidebarWidth, H)
	}
	var b strings.Builder
	for i, r := range rows {
		r = ansi.Truncate(r, mainW, "…")
		if side != nil {
			r += strings.Repeat(" ", max(mainW-ansi.StringWidth(r), 0)) + " " + side[i]
		}
		b.WriteString(r)
		if i < len(rows)-1 {
			b.WriteByte('\n')
		}
	}
	out := tea.NewView(b.String())
	out.AltScreen = true
	out.MouseMode = tea.MouseModeCellMotion
	if v.Cursor != nil && curY >= 0 {
		c := tea.NewCursor(curX, headRows+vh+curY)
		c.Blink = false
		out.Cursor = c
	}
	return out
}

// fullKey handles the keys that only move around the full screen. They
// never reach an ask, a Gate or a delivery: none of them decides anything.
func (h *home) fullKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if h.keys != nil || h.picker != nil || h.diff != nil || h.panel != nil ||
		h.search != nil || (h.run != nil && h.run.expanded) {
		return nil, false
	}
	t := h.tr
	key := msg.String()
	// A key the keybindings file gave to an action is that action's, not ours.
	ctx := ctxComposer
	if h.run != nil {
		ctx = ctxRun
	}
	switch key {
	case "ctrl+t", "ctrl+o", "alt+y", "ctrl+end":
		if h.kb.actionFor(ctx, key) != "" {
			return nil, false
		}
	}
	switch key {
	case "pgup":
		t.move(-max(t.vh-2, 1))
	case "pgdown":
		t.move(max(t.vh-2, 1))
	case "ctrl+end":
		t.latest()
	case "end":
		if !t.scroll || !h.comp.empty() {
			return nil, false
		}
		t.latest()
	case "ctrl+t":
		h.sidebarOff = !h.sidebarOff
		h.layout()
	case "ctrl+o":
		if h.run != nil {
			return nil, false // the Run's own expand
		}
		t.expand = !t.expand
	case "alt+y":
		return h.yankReply(), true
	default:
		return nil, false
	}
	return nil, true
}

// fullMouse handles the mouse over the transcript: the wheel scrolls it, a
// drag selects within it (clamped to the pane) and copies on release.
func (h *home) fullMouse(msg tea.Msg) (tea.Cmd, bool) {
	t := h.tr
	clamp := func(m tea.Mouse) trPos {
		y := min(max(m.Y-t.top, 0), max(t.vh-1, 0))
		x := min(max(m.X, 0), max(t.vw-1, 0))
		return trPos{t.start + y, x}
	}
	inPane := func(m tea.Mouse) bool { return m.Y >= t.top && m.Y < t.top+t.vh && m.X < t.vw }
	switch m := msg.(type) {
	case tea.MouseWheelMsg:
		if h.menu != nil || h.picker != nil || h.diff != nil {
			return h.wheel(m), true
		}
		switch m.Button {
		case tea.MouseWheelUp:
			t.move(-3)
		case tea.MouseWheelDown:
			t.move(3)
		}
		return nil, true
	case tea.MouseClickMsg:
		mm := m.Mouse()
		if mm.Button != tea.MouseLeft || !inPane(mm) {
			return nil, true
		}
		if t.scroll && t.chipX0 > 0 && mm.Y == t.top+t.vh-1 && mm.X >= t.chipX0-1 {
			t.latest()
			return nil, true
		}
		p := clamp(mm)
		t.sel, t.dragging, t.selMoved = &[2]trPos{p, p}, true, false
		return nil, true
	case tea.MouseMotionMsg:
		if !t.dragging || t.sel == nil {
			return nil, true
		}
		mm := m.Mouse()
		p := clamp(mm)
		t.sel[1] = p
		t.selMoved = true
		// Dragging past the pane's edge scrolls it.
		switch {
		case mm.Y < t.top:
			t.move(-1)
		case mm.Y >= t.top+t.vh:
			t.move(1)
		}
		return nil, true
	case tea.MouseReleaseMsg:
		if !t.dragging {
			return nil, true
		}
		t.dragging = false
		if !t.selMoved {
			t.sel = nil
			return nil, true
		}
		text := t.selected()
		if text == "" {
			t.sel = nil
			return nil, true
		}
		return h.copyText(text, "selection"), true
	}
	return nil, false
}

// fullUpdate takes the messages that are about the screen itself; ok is
// false for the rest, which the home handles as it always did.
func (h *home) fullUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case toastGoneMsg:
		if h.toast != nil && h.toast.seq == m.seq {
			h.toast = nil
		}
		return nil, true
	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return h.fullMouse(msg)
	case tea.KeyPressMsg:
		h.tr.sel = nil
		return h.fullKey(m)
	}
	return nil, false
}

// noteReply keeps the agent's words as written, a turn's claims joined, for
// alt+y; what the transcript draws of them never is the source.
func (h *home) noteReply(text string) {
	if h.tr == nil {
		return
	}
	t := h.tr
	if t.freshReply || t.lastReply == "" {
		t.lastReply, t.freshReply = text, false
		return
	}
	t.lastReply += "\n\n" + text
}

// leaveTranscript writes what the session printed to w, on the screen the
// terminal returned to, so quitting does not lose it.
func (h *home) leaveTranscript(w io.Writer) {
	if len(h.tr.entries) == 0 {
		return
	}
	width := h.termW
	if width <= 0 {
		width = 80
	}
	t := &transcript{entries: h.tr.entries, expand: h.tr.expand}
	fmt.Fprintln(w, strings.Join(t.render(width), "\n"))
}

// unclip blanks the closing border lines at the top of the viewport whose
// block (a box, a table) started above it: a border fragment floating under
// the header reads as a glitch. The line
// count stays, so scrolling and selection are unaffected.
func unclip(body []string, start int) []string {
	if start == 0 {
		return body
	}
	body = slices.Clone(body) // the lines are the transcript's cache
	// Only a closing border line (╰…╯) is the tail of a block; a "│ code"
	// row is content and stays.
	for i, l := range body {
		t := strings.TrimLeft(ansi.Strip(l), " ")
		if !strings.HasPrefix(t, "╰") && !strings.HasPrefix(t, "└") {
			break
		}
		body[i] = ""
	}
	return body
}
