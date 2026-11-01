package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/herdr"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// The attention router (docs/ux/desk.md, "Attention levels"): a fixed
// table from what happened in a Chat or a Run to how loudly the human
// hears of it, on Matthews et al.'s scale. No model decides any of it.

// attnEvent is what happened, in Öge's own terms.
type attnEvent int

const (
	evNone             attnEvent = iota
	evToolStep                   // a tool step or agent text
	evSettled                    // a turn settled with no change
	evAccepted                   // a cut or a Run Accepted
	evCheckFailed                // a Check failed
	evRunEnded                   // a Run ended other than Accepted
	evGate                       // a Gate or a delivery choice opened
	evToolAsk                    // the agent asks to use a tool
	evAskUser                    // the agent asks the human a question
	evStaleGate                  // a Gate or an ask open past [desk] stale_gate
	evApprovalExpiring           // an approval about to expire
)

// level is how loudly an event reaches the human, quietest first.
type level int

const (
	ignore      level = iota // nothing outside the focused Chat
	changeBlind              // the glyph changes, nothing else
	makeAware                // glyph and header count; held while typing
	interrupt                // a toast at the next breakpoint
	demand                   // the bell / OSC now, even while typing
)

// levelOf is the router: the level for e. It is the whole table.
func levelOf(e attnEvent) level {
	switch e {
	case evSettled:
		return changeBlind
	case evAccepted, evCheckFailed, evRunEnded:
		return makeAware
	case evGate, evToolAsk, evAskUser:
		return interrupt
	case evStaleGate, evApprovalExpiring:
		return demand
	}
	return ignore
}

// glyphEvent is what a Chat's glyph moving from before to after says
// happened: the glyph already holds the Chat's exact state. Going quiet,
// a ✓ seen and anything back to idle are no news.
func glyphEvent(v *chatView, before, after string) attnEvent {
	if before == after {
		return evNone
	}
	switch after {
	case "▲":
		switch {
		case v.cutGate != nil, v.stale != nil:
			return evGate
		case v.turn != nil && len(v.turn.asks) > 0 && v.turn.asks[0].Questions != nil:
			return evAskUser
		}
		return evToolAsk
	case "✗":
		return evCheckFailed
	case "✓":
		return evAccepted
	case "○":
		if before == "●" || before == "?" {
			return evSettled
		}
	}
	return evNone
}

// parked is an item held while the human types, for the toast that
// releases it.
type parked struct {
	v  *chatView
	ev attnEvent
}

// staleTickMsg wakes the router when a Chat's Gate or ask turns stale.
type staleTickMsg struct{}

// staleAfter is [desk] stale_gate.
func (h *home) staleAfter() time.Duration {
	if h.deskStale > 0 {
		return h.deskStale
	}
	return pipeline.DefaultDeskStaleGate
}

// composing reports whether the focused box holds a message being
// written: everything below demand waits until it is sent or cleared.
func (h *home) composing() bool {
	return h.run == nil && h.comp != nil && !h.comp.empty()
}

// attend runs after every update: it routes what changed in each Chat,
// holds or delivers it, rings for a stale Gate, and tells herdr the
// Desk's state. It never moves the focus.
func (h *home) attend() tea.Cmd {
	now := h.clock()
	var cmds []tea.Cmd
	var fresh []parked
	for _, v := range h.views {
		mark, _ := h.mark(v)
		if mark == "▲" && v.attnMark != "▲" {
			v.askedAt, v.rang = now, false
			if h.redraw != nil {
				cmds = append(cmds, h.redraw(h.staleAfter(), func(time.Time) tea.Msg { return staleTickMsg{} }))
			}
		}
		ev := glyphEvent(v, v.attnMark, mark)
		v.attnMark = mark
		if mark == "▲" && !v.rang && !v.askedAt.IsZero() && now.Sub(v.askedAt) >= h.staleAfter() {
			v.rang = true
			cmds = append(cmds, h.ring(v))
		}
		// The focused Chat shows its own news where it happens.
		if v == h.chatView || levelOf(ev) < makeAware {
			continue
		}
		fresh = append(fresh, parked{v, ev})
	}
	for _, p := range fresh {
		switch {
		case h.composing():
			h.parked = append(h.parked, p)
		case levelOf(p.ev) == interrupt:
			h.parked = append(h.parked, p) // a breakpoint now: delivered below
		}
	}
	h.parked = slices.DeleteFunc(h.parked, func(p parked) bool {
		g, ok := heldGlyph[p.ev]
		return ok && p.v.attnMark != g
	})
	// A toast on screen is not replaced: what waits goes when it does.
	if len(h.parked) > 0 && !h.composing() && h.toast == nil {
		cmds = append(cmds, h.release())
	}
	h.reportDesk()
	return tea.Batch(cmds...)
}

// heldGlyph is the glyph that keeps a held item true: an ask answered, a
// turn ended or a ✓ seen while it waited needs no word.
var heldGlyph = map[attnEvent]string{
	evGate: "▲", evToolAsk: "▲", evAskUser: "▲", evCheckFailed: "✗", evAccepted: "✓",
}

// release shows what was held as one toast, the human's part first. A
// Chat closed or focused since needs no word.
func (h *home) release() tea.Cmd {
	items := slices.DeleteFunc(h.parked, func(p parked) bool {
		return p.v == h.chatView || !slices.Contains(h.views, p.v)
	})
	h.parked = nil
	if len(items) == 0 {
		return nil
	}
	rank := func(e attnEvent) int {
		switch {
		case levelOf(e) == interrupt:
			return 0
		case e == evCheckFailed:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(items, func(a, b parked) int { return rank(a.ev) - rank(b.ev) })
	parts := make([]string, 0, len(items)+1)
	if len(items) > 1 {
		parts = append(parts, strconv.Itoa(len(items))+" new")
	}
	for _, p := range items {
		parts = append(parts, attnLine(p))
	}
	cmd := h.notice(strings.Join(parts, " · "), false)
	h.toast.plain = true
	return cmd
}

// attnLine is one held item in the toast: Öge's fixed words with the
// Chat's number and title. Titles stay inside the TUI.
func attnLine(p parked) string {
	what := map[attnEvent]string{
		evGate: "▲ %d %s needs you", evToolAsk: "▲ %d %s asks", evAskUser: "▲ %d %s asks you",
		evCheckFailed: "✗ %d %s Check failed", evAccepted: "✓ %d %s accepted",
	}[p.ev]
	if what == "" {
		what = "· %d %s"
	}
	return fmt.Sprintf(what, p.v.num, ansi.Truncate(clean(p.v.caption()), 24, "…"))
}

// heldNote is the box's `N held`, "" with nothing held.
func (h *home) heldNote() string {
	if len(h.parked) == 0 {
		return ""
	}
	return strconv.Itoa(len(h.parked)) + " held"
}

// ring is the demand for v's stale Gate or ask: the bell and the OSC
// notification, as [notify] attention allows. The text is fixed and names
// the Chat's number only.
func (h *home) ring(v *chatView) tea.Cmd {
	if levelOf(evStaleGate) < demand || h.notify == nil || !h.notify.attention {
		return nil
	}
	return tea.Raw(h.notify.sequence(staleText(v.num)))
}

func staleText(num int) string {
	return "oge: Chat " + strconv.Itoa(num) + " is waiting for you"
}

// deskState is the Desk's state rolled up for herdr: blocked over working
// over idle. herdr has no done, so a ✓ is idle and never hides a turn
// that runs.
func deskState(marks []string) herdr.State {
	switch {
	case slices.Contains(marks, "▲"):
		return herdr.Blocked
	case slices.Contains(marks, "●"), slices.Contains(marks, "?"):
		return herdr.Working
	}
	return herdr.Idle
}

// reportDesk tells herdr the Desk's state when it changes. A Run in
// flight reports its own on the same pane, so once it ends the Desk's
// state is told again.
func (h *home) reportDesk() {
	if h.herdr == nil {
		return
	}
	if h.run != nil {
		h.herdrState = ""
		return
	}
	marks := make([]string, 0, len(h.views))
	for _, v := range h.views {
		marks = append(marks, v.attnMark)
	}
	s := deskState(marks)
	if s == h.herdrState || h.herdrState == "" && s == herdr.Idle {
		return
	}
	h.herdrState = s
	msg := ""
	if s == herdr.Blocked {
		msg = notifyWaiting
	}
	h.herdr.Report(s, msg)
}

// runEvent is a Run's end as the router knows it.
func runEvent(res *run.Result, failed bool) attnEvent {
	if res != nil && !failed && res.Outcome == run.Accepted {
		return evAccepted
	}
	return evRunEnded
}
