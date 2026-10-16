package cli

import (
	tea "charm.land/bubbletea/v2"
	"strings"

	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/run"
)

// labeller is home.label: it labels the Run id names (an id or the start of
// one), in this repository, with text; empty text clears the label. It
// returns the label as stored and, when it can't, why. The label is the
// human's own words beside the Run, never Ledger data (#177).
func labeller(env Env, root string) func(id, text string) (string, string) {
	return func(id, text string) (string, string) {
		var b strings.Builder
		q := env
		q.Stderr = &b
		r, code := findRun(q, root, id)
		if code != ExitOK {
			return "", strings.TrimSpace(clean(b.String()))
		}
		label, err := run.WriteLabel(r.Dir, text)
		if err != nil {
			return "", "/label: " + clean(err.Error())
		}
		r.Label = label
		return label, ""
	}
}

// labelCmd is /label <text> for the latest Run this session ended, or
// /label <run> <text> for any Run of this repository.
func (h *home) labelCmd(args []string) (tea.Cmd, string) {
	id := ""
	if len(args) > 0 && runIDShape.MatchString(args[0]) {
		id, args = args[0], args[1:]
	}
	if id == "" && h.last == nil {
		return nil, "/label: no Run in this session; give a Run id: " + usageOf("/label")
	}
	if id == "" {
		id = h.last.id
	}
	if h.label == nil || id == "" {
		return nil, "/label: no Runs to label here"
	}
	label, problem := h.label(id, strings.Join(args, " "))
	if problem != "" {
		return nil, problem
	}
	if label == "" {
		return h.fit("label cleared on Run " + clean(id)), ""
	}
	return h.fit("labelled Run " + clean(id) + ": " + clean(label)), ""
}

// runLine is a Run as /runs lists it: id, outcome, when, the Task's first
// line, and the human's label if it has one.
func runLine(r *delivery.Run) string {
	line := r.ID + "  " + padRight(outcomeWord(r), 10) + "  " + runWhen(r.ID) + "  "
	if r.Label != "" {
		line += "[" + clean(r.Label) + "] "
	}
	line += clean(r.Title)
	if r.Torn {
		line += "  (ledger torn)"
	}
	return line
}

func padRight(s string, n int) string {
	if d := n - len([]rune(s)); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
