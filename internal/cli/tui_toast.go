package cli

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// A toast is a notice that passes: it shows over the transcript's top right
// for a few seconds and goes by itself. It never holds a question: an ask, a
// Gate or an apply is answered where it is shown, not in a toast.
type toast struct {
	text string
	bad  bool
	seq  int
}

const toastFor = 3 * time.Second

type toastGoneMsg struct{ seq int }

// notice shows text as a toast; the command removes it again.
func (h *home) notice(text string, bad bool) tea.Cmd {
	h.toastSeq++
	seq := h.toastSeq
	h.toast = &toast{text: clean(text), bad: bad, seq: seq}
	return tea.Tick(toastFor, func(time.Time) tea.Msg { return toastGoneMsg{seq} })
}

func (h *home) toastView() string {
	t := h.toast
	if t == nil {
		return ""
	}
	st := h.st
	glyph := st.ok("✓")
	if t.bad {
		glyph = st.bad("✗")
	}
	text := ansi.Truncate(t.text, max(h.width-8, 8), "…")
	return st.border("▏") + " " + glyph + " " + text + " "
}

// copyText puts text on the clipboard (OSC 52, redacted like /copy) and says
// so in a toast.
func (h *home) copyText(text, what string) tea.Cmd {
	text = strings.TrimRight(redacted(text), "\n")
	if text == "" {
		return nil
	}
	seq := osc52(text, h.tmux)
	if len(seq) > maxOSC52+64 {
		return h.notice("too long for the clipboard: /export it", true)
	}
	n := strings.Count(text, "\n") + 1
	return tea.Sequence(tea.Raw(seq), h.notice("copied "+what+" ("+plural(n, "line")+")", false))
}

// yankReply copies the agent's latest reply, as it was written: the raw
// text, never what the transcript drew of it.
func (h *home) yankReply() tea.Cmd {
	if h.tr.lastReply == "" {
		return h.notice("no reply to copy yet", true)
	}
	return h.copyText(h.tr.lastReply, "the last reply")
}
