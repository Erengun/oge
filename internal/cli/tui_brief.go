package cli

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/run"
)

// The home's part of the return brief (docs/ux/away.md, slice 7): the
// away window, the brief the Inbox shows as its first card (#336b), and
// the header's "while you were away" line.

// awayMin is the shortest absence the brief reports.
const awayMin = 10 * time.Minute

// awayState is the home's away window.
type awayState struct {
	// last is the last key or mouse event's time, on the home's clock.
	last time.Time
	// back is the brief of the absence the human came back from, built
	// once on the key that ended it; nil once seen or none.
	back *run.Brief
	// private is the state root's private directory, where the Runs'
	// Ledgers are: "" reads no Run.
	private string
}

// noteInput moves the away window on a key or mouse event. One that ends
// an absence of awayMin or more builds that absence's brief.
func (h *home) noteInput(msg tea.Msg) {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg:
	default:
		return
	}
	now := h.clock()
	if last := h.away.last; !last.IsZero() && now.Sub(last) >= awayMin {
		b := h.buildBrief(awayWindow(last, now))
		h.away.back = &b
	}
	h.away.last = now
}

// awayWindow is the window from the last input to now. The machine's
// sleep is the gap between the wall clock and the monotonic one, which
// stops while it sleeps; times without a monotonic reading (an injected
// clock) show none.
func awayWindow(from, to time.Time) run.BriefWindow {
	w := run.BriefWindow{From: from, To: to, Basis: run.BasisAway}
	if s := to.Round(0).Sub(from.Round(0)) - to.Sub(from); s >= run.SleepFloor {
		w.Slept = s
	}
	return w
}

// brief is the return brief for the Inbox's first card: the absence the
// human came back from, else the one under way. ok is false when
// neither is awayMin long.
func (h *home) brief() (b run.Brief, ok bool) {
	if h.away.back != nil {
		return *h.away.back, true
	}
	now := h.clock()
	if h.away.last.IsZero() || now.Sub(h.away.last) < awayMin {
		return run.Brief{}, false
	}
	return h.buildBrief(awayWindow(h.away.last, now)), true
}

// clearBrief drops the brief of the absence the human came back from:
// they sent a message, or the Inbox showed it.
func (h *home) clearBrief() { h.away.back = nil }

// briefLine is the header's line for the absence the human came back
// from: "while you were away · 2h 14m · ▲1 ✗1 ✓2"; "" when there is none.
func (h *home) briefLine() string {
	b := h.away.back
	if b == nil {
		return ""
	}
	line := "while you were away · " + run.Span(b.Away())
	if b.Slept > 0 {
		line += " · slept " + run.Span(b.Slept)
	}
	if c := b.Counts(); c != "" {
		line += " · " + c
	}
	return line
}

// buildBrief builds the brief over w from the open Chats' logs and the
// repository's newest Runs.
func (h *home) buildBrief(w run.BriefWindow) run.Brief {
	var chats []run.BriefChat
	for _, v := range h.views {
		if v.chat == nil {
			continue
		}
		c, _ := v.chat.current()
		if c == nil {
			continue
		}
		log, err := run.ReadBriefLog(c.ID, c.Dir)
		if err != nil {
			continue
		}
		bc := run.BriefChat{BriefLog: log, N: v.num, Title: v.name}
		if v.turn != nil {
			bc.Asks = len(v.turn.asks)
		}
		chats = append(chats, bc)
	}
	var runs []run.BriefRun
	if h.away.private != "" && h.root != "" {
		runs, _ = repoRuns(h.away.private, h.root)
	}
	return run.BuildBrief(w, chats, runs)
}
