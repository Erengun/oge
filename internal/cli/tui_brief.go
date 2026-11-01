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
	// last is the last key, mouse, focus-in or resize event's time, on
	// the home's clock.
	last time.Time
	// blurred is when the terminal reported focus lost: away starts then,
	// not ten minutes on. Zero while focused, or when the terminal sends
	// no focus reports.
	blurred time.Time
	// back is the brief of the absence the human came back from, built
	// once on the key that ended it; nil once seen or none.
	back *run.Brief
	// private is the state root's private directory, where the Runs'
	// Ledgers are: "" reads no Run.
	private string
}

// noteInput moves the away window (docs/ux/away.md, §Away). Away starts
// when the terminal reports focus lost, or after awayMin with no key; a
// key, a mouse event, focus regained or a resize (a reattach) ends it.
// One that ends an absence of awayMin or more builds its brief.
func (h *home) noteInput(msg tea.Msg) {
	switch msg.(type) {
	case tea.BlurMsg:
		if h.absence.blurred.IsZero() {
			h.absence.blurred = h.clock()
		}
		return
	case tea.KeyPressMsg, tea.MouseMsg, tea.FocusMsg, tea.WindowSizeMsg:
	default:
		return
	}
	now := h.clock()
	if from := h.awayFrom(); !from.IsZero() && wallSince(from, now) >= awayMin {
		b := h.buildBrief(awayWindow(from, now))
		h.absence.back = &b
	}
	h.absence.last, h.absence.blurred = now, time.Time{}
}

// awayFrom is when the absence under way started: when focus was lost,
// else the last input. Zero before any input.
func (h *home) awayFrom() time.Time {
	if !h.absence.blurred.IsZero() && !h.absence.last.IsZero() {
		return h.absence.blurred
	}
	return h.absence.last
}

// awayWindow is the window from the last input to now. The machine's
// sleep is the gap between the wall clock and the monotonic one, which
// stops while it sleeps; times without a monotonic reading (an injected
// clock) show none.
func awayWindow(from, to time.Time) run.BriefWindow {
	// The window itself is wall clock: it includes the sleep.
	w := run.BriefWindow{From: from.Round(0), To: to.Round(0), Basis: run.BasisAway}
	if s := wallSince(from, to) - to.Sub(from); s >= run.SleepFloor {
		w.Slept = s
	}
	return w
}

// wallSince is to - from on the wall clock, which goes on while the
// machine sleeps: an absence over a closed lid counts in full.
func wallSince(from, to time.Time) time.Duration {
	return to.Round(0).Sub(from.Round(0))
}

// brief is the return brief for the Inbox's first card: the absence the
// human came back from, else the one under way. ok is false when
// neither is awayMin long.
func (h *home) brief() (b run.Brief, ok bool) {
	if h.absence.back != nil {
		return *h.absence.back, true
	}
	now := h.clock()
	from := h.awayFrom()
	if from.IsZero() || wallSince(from, now) < awayMin {
		return run.Brief{}, false
	}
	return h.buildBrief(awayWindow(from, now)), true
}

// clearBrief drops the brief of the absence the human came back from:
// they sent a message, or the Inbox showed it.
func (h *home) clearBrief() { h.absence.back = nil }

// briefLine is the header's line for the absence the human came back
// from: "while you were away · 2h 14m · ▲1 ✗1 ✓2"; "" when there is none.
func (h *home) briefLine() string {
	b := h.absence.back
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
// repository's Runs the window can hold.
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
		// This process writes it: a torn tail is a record mid-write.
		log.Torn = false
		// The Chat log keeps hashes only: the first message's title is what
		// the home holds of it, the human's own words.
		bc := run.BriefChat{BriefLog: log, N: v.num, Title: v.name, First: v.name}
		if v.turn != nil {
			bc.Asks = len(v.turn.asks)
		}
		chats = append(chats, bc)
	}
	var runs []run.BriefRun
	if h.absence.private != "" && h.root != "" {
		runs, _ = runsSince(h.absence.private, h.root, w.From)
		recent, _ := repoRuns(h.absence.private, h.root)
		runs = withRecent(runs, recent)
	}
	return run.BuildBrief(w, chats, runs)
}
