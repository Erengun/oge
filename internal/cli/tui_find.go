package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Search in the expanded Ctrl+O view (#167): `/` opens a line, typing finds,
// Enter jumps to the first match, n/N step through them, { } jump between
// Attempts. Finished items live in the terminal's own scrollback; this is
// only the agent-activity view. Matching is a case-insensitive substring of
// the steps as drawn, which are already escaped (clean), never raw bytes.

// finder is the open search.
type finder struct {
	query  string
	typing bool // the line is taking keys; false once Enter confirmed it
	cur    int  // index into matches
}

// matches are the indices of the steps that contain q.
func matches(steps []string, q string) []int {
	if q == "" {
		return nil
	}
	q = strings.ToLower(q)
	var out []int
	for i, s := range steps {
		if strings.Contains(strings.ToLower(s), q) {
			out = append(out, i)
		}
	}
	return out
}

// stepOf is the step the finder points at, or -1.
func (m *model) stepOf() int {
	if m.find == nil {
		return -1
	}
	ms := matches(m.steps, m.find.query)
	if len(ms) == 0 {
		return -1
	}
	return ms[min(m.find.cur, len(ms)-1)]
}

// findKey handles a key for the search line while it takes text; it reports
// whether it used the key.
func (m *model) findKey(msg tea.KeyPressMsg) bool {
	f := m.find
	if f == nil || !f.typing {
		return false
	}
	switch msg.String() {
	case "esc":
		m.find = nil
	case "enter":
		f.typing, f.cur = false, 0
		m.reveal()
	case "backspace":
		if r := []rune(f.query); len(r) > 0 {
			f.query = string(r[:len(r)-1])
		}
		f.cur = 0
	case "ctrl+o", "ctrl+c":
		return false
	default:
		if msg.Text == "" {
			return true
		}
		f.query += msg.Text
		f.cur = 0
	}
	return true
}

// stepKey is a search key in the expanded view outside the typing line.
func (m *model) searchKey(key string) {
	switch key {
	case "/":
		m.find = &finder{typing: true}
	case "n", "N":
		if m.find == nil {
			return
		}
		ms := matches(m.steps, m.find.query)
		if len(ms) == 0 {
			return
		}
		d := 1
		if key == "N" {
			d = -1
		}
		m.find.cur = (m.find.cur + d + len(ms)) % len(ms)
		m.reveal()
	case "{", "}":
		m.jumpAttempt(key == "}")
	}
}

// jumpAttempt scrolls to the start of the next (or previous) Attempt.
func (m *model) jumpAttempt(forward bool) {
	starts := append([]int{0}, m.marks...)
	end := len(m.steps) - m.off // one past the last visible step
	top := max(end-m.window(m.used), 0)
	to := -1
	if forward {
		for _, s := range starts {
			if s > top {
				to = s
				break
			}
		}
	} else {
		for _, s := range starts {
			if s < top {
				to = s
			}
		}
	}
	if to < 0 {
		return
	}
	m.showAt(to, 0)
}

// reveal scrolls so the current match is in view.
func (m *model) reveal() {
	if i := m.stepOf(); i >= 0 {
		m.showAt(i, m.window(m.used)/2)
	}
}

// showAt scrolls so step i is in view with after steps below it.
func (m *model) showAt(i, after int) {
	limit := m.window(m.used)
	end := min(i+1+after, len(m.steps))
	end = max(end, min(limit, len(m.steps)))
	if i < end-limit {
		end = i + limit
	}
	m.off = len(m.steps) - min(end, len(m.steps))
	m.clampOff()
}

// findLine is the search line shown in place of the scroll hint.
func (m *model) findLine() string {
	f := m.find
	line := "/" + f.query
	if f.typing {
		line += "▏"
	}
	if f.query != "" {
		if ms := matches(m.steps, f.query); len(ms) == 0 {
			line += "  no match"
		} else {
			line += fmt.Sprintf("  %d/%d", min(f.cur, len(ms)-1)+1, len(ms))
		}
	}
	if f.typing {
		return line + " · enter jump · esc close"
	}
	return line + " · n/N next · { } attempt · esc close"
}
