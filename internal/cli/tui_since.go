package cli

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/run"
)

// The since-you-left card (docs/ux/desk.md, "Since-you-left card"): when
// the human focuses a Chat that heard from its agent or its Check while
// they were elsewhere, a card pinned at the top of its transcript says
// what happened, in time order, from their last message to the open ask.
// It takes no key: typing goes to the box, and the card goes on send or
// scroll (Parnin & DeLine 2010; Altmann & Trafton 2002).

// sinceRing is how many event lines a Chat keeps while unfocused.
const sinceRing = 12

// sinceState is what a Chat recorded while another one had the focus.
type sinceState struct {
	lines []string  // one per event, oldest first, at most sinceRing
	more  int       // lines dropped off the front of the ring
	left  time.Time // when the Chat lost the focus
	you   string    // the human's last message in it
	// What attend saw last, to tell what is new.
	steps          int
	check, verdict string
}

// leave starts v's record: the human has moved to another Chat.
func (v *chatView) leave(now time.Time) {
	v.away.lines, v.away.more = nil, 0
	v.away.left = now
}

// collapseSince folds the focused Chat's card: sent, or scrolled.
func (h *home) collapseSince() {
	h.away.lines, h.away.more = nil, 0
}

// noteSince records what is new in v since attend last looked: a tool
// step, a Check's step, a Verdict. Only an unfocused Chat keeps lines;
// every Chat keeps its last look, so its record starts from now.
func (h *home) noteSince(v *chatView) {
	s := &v.away
	add := func(line string) {
		if v == h.chatView || (len(s.lines) > 0 && s.lines[len(s.lines)-1] == line) {
			return
		}
		if len(s.lines) == sinceRing {
			s.lines, s.more = s.lines[1:], s.more+1
		}
		s.lines = append(s.lines, line)
	}
	if v.turn != nil && !v.turn.oge && v.turn.text != "" {
		s.you = v.turn.text
	}
	if v.side.steps > s.steps && v.side.step != "" {
		add("● " + v.side.step)
	}
	s.steps = v.side.steps
	check := ""
	if v.verify != nil {
		check = v.verify.step
	}
	if check != s.check && check != "" {
		add("● " + check)
	}
	s.check = check
	if v.side.verdict != s.verdict && v.side.verdict != "" {
		mark := "·"
		switch v.side.outcome {
		case run.Accepted:
			mark = "✓"
		case run.Failed, run.Refused:
			mark = "✗"
		}
		add(mark + " " + v.side.verdict)
	}
	s.verdict = v.side.verdict
}

// sinceCard is the focused Chat's card at width, nil when it has none:
// the human's last message, the events in time order, the open ask last.
func (h *home) sinceCard(width int) []string {
	s := &h.away
	if len(s.lines) == 0 || width < 20 {
		return nil
	}
	st := h.st
	inner := width - 4
	row := func(s string) string {
		s = ansi.Truncate(s, inner, "…")
		return st.border("│") + " " + s + strings.Repeat(" ", max(inner-ansi.StringWidth(s), 0)) + " " + st.border("│")
	}
	paint := func(line string) string {
		mark, rest, _ := strings.Cut(line, " ")
		switch mark {
		case "✓":
			mark = st.ok(mark)
		case "✗":
			mark = st.bad(mark)
		case "▲":
			mark = st.accent(mark)
		default:
			mark = st.dim(mark)
		}
		return mark + "  " + clean(rest)
	}
	title := " since you left "
	if age := railAge(h.clock(), s.left); age != "" {
		title = " since you left · " + age + " "
	}
	out := []string{st.border("╭─") + st.bold(title) + st.border(strings.Repeat("─", max(width-3-ansi.StringWidth(title), 0))+"╮")}
	if you, _, _ := strings.Cut(strings.TrimSpace(s.you), "\n"); you != "" {
		out = append(out, row(st.dim("you")+"  "+clean(you)))
	}
	if s.more > 0 {
		out = append(out, row(st.dim("… "+strconv.Itoa(s.more)+" earlier")))
	}
	for _, l := range s.lines {
		out = append(out, row(paint(l)))
	}
	if m, _ := h.mark(h.chatView); m == "▲" {
		out = append(out, row(paint("▲ "+h.wanted(inboxItem{v: h.chatView, mark: m}))))
	}
	foot := " collapses on send or scroll "
	if ansi.StringWidth(foot)+4 > width {
		foot = ""
	}
	out = append(out, st.border("╰"+strings.Repeat("─", max(width-3-ansi.StringWidth(foot), 0)))+st.dim(foot)+st.border("─╯"))
	return out
}
