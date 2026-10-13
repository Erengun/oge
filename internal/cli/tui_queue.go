package cli

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/task"
)

// The next-Task queue (#161, D-2): while a Run is in progress and no Gate is
// open, typing edits a small "next ›" box under the Run's frame. Nothing is
// sent to the running stage. When the Run ends the text moves into the
// composer and waits for Enter: it is never started by itself, because the
// action row must be handled first and a follow-up must not build on a
// Candidate the human hasn't acted on. Slash commands, `!` and the hidden
// character check all apply at that later Enter, not here.

const (
	queuePrefix   = "next › "
	queuedPrefix  = "queued › "
	queueReadyMsg = "queued Task ready: Enter starts it"
)

// queued is the next Task being typed (or kept) during a Run.
type queued struct {
	comp *composer
	kept bool // Enter marked it queued
}

// queueKey takes a message during a Run and reports whether the queue box
// consumed it. A Gate owns every key while it is open (the queued text stays,
// hidden); the expanded view keeps its scrolling and search keys; Esc and
// Ctrl+O are the Run's; Ctrl+C clears the text first and otherwise cancels
// the Run, as today.
func (h *home) queueKey(msg tea.Msg) bool {
	r := h.run
	if r.gate != nil {
		// Keys typed into the queue must not answer a Gate that opened under
		// them (a stray "quit⏎" or "t⏎"): they are dropped for actionsArm.
		// Ctrl+C is never dropped.
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
			return false
		}
		return !h.gateArm.IsZero() && h.clock().Sub(h.gateArm) < actionsArm
	}
	if r.finished {
		return false
	}
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if r.expanded {
			return false
		}
		h.queueComp().insertPaste(msg.Content)
		h.next.kept = false
		return true
	case tea.KeyPressMsg:
		key := msg.String()
		has := h.next != nil && !h.next.comp.empty()
		if r.expanded {
			return false
		}
		switch resolve(ctxRun, key) {
		case "ctrl+c":
			if !has || r.cancelling {
				return false
			}
			h.next = nil // the first Ctrl+C clears the Task, not the Run
			return true
		case "esc", actExpand:
			return false
		case actCancelQueue:
			h.next = nil
			return true
		case "enter":
			if has && !h.next.comp.backslashNewline() {
				h.next.kept = true
			}
			return true
		}
		c := h.queueComp()
		c.key(msg)
		h.next.kept = false
		return true
	}
	return false
}

func (h *home) queueComp() *composer {
	if h.next == nil {
		h.next = &queued{comp: newComposer()}
	}
	return h.next.comp
}

// queueView is the box under the Run's frame: nothing until something is
// typed, and nothing while a Gate is open.
func (h *home) queueView() tea.View {
	v := h.run.View()
	frame := v.Content
	if h.next == nil || h.next.comp.empty() || h.run.gate != nil || h.run.finished {
		return v
	}
	st := h.st
	width := h.width
	if width <= 0 {
		width = 80
	}
	if h.next.kept {
		first, _, _ := strings.Cut(h.next.comp.expanded(), "\n")
		line := st.dim(queuedPrefix) + clean(first)
		v.Content = frame + ansi.Truncate(line, width, "…") + "\n" +
			st.dim("  ctrl+x to cancel · starts after this Run, when you press Enter") + "\n"
		return v
	}
	rows, cr, cc := h.next.comp.layout(max(width-ansi.StringWidth(queuePrefix), 1))
	var b strings.Builder
	b.WriteString(frame)
	for i, row := range rows {
		pre := strings.Repeat(" ", ansi.StringWidth(queuePrefix))
		if i == 0 {
			pre = st.dim(queuePrefix)
		}
		b.WriteString(ansi.Truncate(pre+row, width, "…") + "\n")
	}
	b.WriteString(st.dim("  queued: starts after this Run, Enter to keep") + "\n")
	v.Content = b.String()
	c := tea.NewCursor(ansi.StringWidth(queuePrefix)+cc, strings.Count(frame, "\n")+cr)
	c.Blink = false
	v.Cursor = c
	return v
}

// takeQueue moves the queued text into the fresh composer when the Run ends.
// It is only put there: Enter starts it, and a second Enter that was already
// on its way is dropped for the same moment the delivery commands drop theirs.
func (h *home) takeQueue() {
	q := h.next
	h.next = nil
	if q == nil || q.comp.empty() {
		return
	}
	text := q.comp.expanded()
	h.comp.set(text)
	h.queuedText = text
	h.armText, h.armAt = strings.TrimSpace(task.StripComments(text)), h.clock()
	h.note = queueReadyMsg
}

// composerFree reports whether the composer has nothing the human typed
// themselves: empty, or holding only the queued Task as it was moved in
// (cursor at its end), so ↓ and the action row work as today.
func (h *home) composerFree() bool {
	if h.comp.empty() {
		return true
	}
	return h.queuedText != "" && h.comp.expanded() == h.queuedText && h.comp.row == len(h.comp.lines)-1
}
