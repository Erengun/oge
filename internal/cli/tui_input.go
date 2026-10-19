package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The full-screen input (ADR-0022, amended): a bordered box with a ❯ prompt,
// and the status line inside its bottom border, as pitago draws it: the
// state, the keys that work now, and what the next turn will use on the
// right. The popup above it lists the slash commands with their
// descriptions. Both only show and navigate: the keys they name are the
// composer's own, and none of them decides an ask, a Gate or an apply.

// promptWidth is the columns of "❯ ".
const promptWidth = 2

// fullBox is the composer's box on the full screen at width, with the
// cursor's row in it and its column (counted from the text, as boxLines'
// callers expect: they add the border and its space).
func (h *home) fullBox(width int, blank string) (lines []string, curRow, curCol int) {
	st := h.st
	inner := max(width-4, 1)
	h.comp.sel, h.comp.selPaint = h.selection(), st.selected
	rows, cr, cc := h.comp.layout(max(inner-promptWidth, 1))
	empty := len(h.comp.lines) == 1 && len(h.comp.lines[0]) == 0
	// Two text rows where the screen has the room, one on a short one.
	minRows := 1
	if h.termH >= 30 {
		minRows = 2
	}
	for len(rows) < minRows {
		rows = append(rows, "")
	}
	border := st.accent
	top := "╭" + strings.Repeat("─", max(width-2, 0)) + "╮"
	if _, shell := shellLine(h.comp.expanded()); shell || h.job != nil {
		label := " shell "
		border = st.warn // not a Task: it never reaches an agent
		top = "╭─" + label + strings.Repeat("─", max(width-3-len(label), 0)) + "╮"
	}
	lines = append(lines, border(top))
	for i, r := range rows {
		body := r
		pre := "  "
		if i == 0 {
			pre = st.accent("❯") + " "
		}
		if empty && i == 0 {
			body = blank
			if h.job != nil {
				body = st.dim("running… (ctrl+c to stop)")
			}
		}
		body = ansi.Truncate(body, max(inner-promptWidth, 1), "…")
		pad := strings.Repeat(" ", max(inner-promptWidth-ansi.StringWidth(ansi.Strip(body)), 0))
		lines = append(lines, border("│")+" "+pre+body+pad+" "+border("│"))
	}
	lines = append(lines, h.statusBorder(width, border))
	return lines, cr, cc + promptWidth
}

// statusBorder is the box's bottom border with the status in it.
func (h *home) statusBorder(width int, border func(string) string) string {
	st := h.st
	avail := max(width-2, 0) // between the corners
	left, right := h.statusParts()
	// What is cut to fit goes from the right: the Launch profile first, then
	// the keys, never the state.
	fit := func() (string, string) {
		for {
			l, r := strings.Join(left, " · "), right
			w := ansi.StringWidth(l) + ansi.StringWidth(r)
			if r != "" {
				w += 3
			}
			if w+2 <= avail || (len(left) <= 1 && r == "") {
				return l, r
			}
			if len(left) > 2 {
				left = left[:len(left)-1]
				continue
			}
			if r != "" {
				right = ""
				continue
			}
			left = left[:len(left)-1]
		}
	}
	l, r := fit()
	var b strings.Builder
	b.WriteString(border("╰─") + " ")
	used := 3
	for i, p := range strings.Split(l, " · ") {
		if i > 0 {
			b.WriteString(st.dim(" · "))
			used += 3
		}
		b.WriteString(p)
		used += ansi.StringWidth(p)
	}
	b.WriteString(" ")
	used++
	if r != "" {
		gap := max(avail-used-ansi.StringWidth(r)-2, 1)
		b.WriteString(border(strings.Repeat("─", gap)) + " " + st.dim(r) + " ")
		used += gap + ansi.StringWidth(r) + 2
	}
	b.WriteString(border(strings.Repeat("─", max(width-1-used, 0)) + "╯"))
	return ansi.Truncate(b.String(), width, "")
}

// statusParts is the status: the state and the keys that work now (each
// painted), and the Launch profile that goes to the right.
func (h *home) statusParts() (left []string, right string) {
	st := h.st
	submit := keyBinding{act: "composer.submit"}.label(h.kb)
	state := st.ok("●") + " " + st.bold("ready")
	var keys []string
	switch {
	case h.turn != nil && h.ask() != nil:
		state = st.warn("●") + " " + st.bold("waiting for you")
		keys = []string{"answer above"}
	case h.turn != nil:
		state = st.accent("●") + " " + st.bold("working")
		keys = []string{"esc interrupt", submit + " queue"}
	case h.run != nil:
		state = st.accent("●") + " " + st.bold("running")
		keys = []string{h.kb.expandLabel() + " details", "ctrl+c cancel"}
	case h.job != nil:
		state = st.accent("●") + " " + st.bold("shell")
		keys = []string{"ctrl+c stop"}
	default:
		if _, shell := shellLine(h.comp.expanded()); shell {
			keys = []string{submit + " run shell", "ctrl+j newline"}
		} else {
			action := "send"
			if h.chat == nil {
				action = "run"
			}
			keys = []string{submit + " " + action, "/ commands", "! shell", "tab setup"}
		}
	}
	left = []string{state}
	for _, k := range keys {
		left = append(left, st.dim(k))
	}
	if len(h.setup) > 0 {
		right = strings.Replace(h.setup[0], "implement ", "", 1)
	}
	return left, right
}

// menuLinesFull is the slash popup for the full screen, drawn above the box:
// each row is "/command — description", the highlighted one marked ▸, with a
// counter and what the keys do. Its height is capped to the rows the screen
// can spare.
func (h *home) menuLinesFull() []string {
	st := h.st
	items := h.menu.Items()
	n := len(items)
	room := max(min(8, h.termH-14), 3)
	idx := h.menu.Index()
	first := min(max(idx-room+1, 0), max(n-room, 0))
	last := min(first+room, n)
	var out []string
	for i := first; i < last; i++ {
		c := items[i].(slashItem).cmd
		if i == idx {
			out = append(out, " "+st.accent("▸")+" "+st.bold(c.usage)+st.dim(" — "+c.desc))
		} else {
			out = append(out, "   "+c.usage+st.dim(" — "+c.desc))
		}
	}
	for i, l := range out {
		out[i] = ansi.Truncate(l, max(h.width-1, 8), "…")
	}
	if last < n {
		out = append(out, " "+st.dim(fmt.Sprintf("…(+%d below)", n-last)))
	}
	return append(out, " "+st.dim(fmt.Sprintf("(%d/%d) Tab complete · Enter accept · Esc close", idx+1, n)))
}
