package cli

import (
	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/run"
)

// Leaving the Desk (docs/ux/away.md, A3): quitting with Chats open asks
// whether to keep them for next time or close them. A signal can't ask;
// it keeps them (tui_home.go).

// quitOrAsk quits, or, with a Chat open, asks first.
func (h *home) quitOrAsk() tea.Cmd {
	n := h.openChats()
	if n == 0 {
		h.quit = true
		return tea.Quit
	}
	h.quitAsk = n
	h.msg = []string{quitAskLine(n)}
	return nil
}

func quitAskLine(n int) string {
	return "Keep " + plural(n, "Chat") + " for next time? k keep · c close · esc cancel"
}

// openChats counts the Chats a first message opened, or is opening.
func (h *home) openChats() int {
	n := 0
	for _, v := range h.views {
		if v.chat != nil && (v.chat.get() != nil || v.turn != nil) {
			n++
		}
	}
	return n
}

// quitAskKey answers the ask: k keeps the Chats, Workspaces and all; c
// closes them as /close does; esc goes back. Any other key, Enter and a
// repeated ctrl+c too, does nothing (ADR-0022).
func (h *home) quitAskKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "k", "K":
		h.keep = true
		fallthrough
	case "c", "C":
		h.quitAsk, h.msg = 0, nil
		h.quit = true
		return h, tea.Quit
	case "esc":
		h.quitAsk, h.msg = 0, nil
	}
	return h, nil
}

// recordSeen writes Seen for each Chat whose ✓ the human has seen since
// the last update, so it stays seen across a restart (G1).
func (h *home) recordSeen() {
	now := h.clock()
	for _, v := range h.views {
		if v.chat == nil || v.doneRun == "" || v.seenRun == v.doneRun || v.side.outcome != run.Accepted || !v.seenBy(now) {
			continue
		}
		if c := v.chat.get(); c != nil {
			v.seenRun = v.doneRun
			v.chat.desk.Seen(c.ID, v.doneRun)
		}
	}
}
