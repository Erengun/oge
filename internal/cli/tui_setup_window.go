package cli

import "fmt"

// minPanelHeight is the shortest terminal the setup panel is drawn on (#212).
const minPanelHeight = 12

// windowLines fits a panel into budget lines. lines[0] is the header and
// always stays; [lo, hi] (indices into lines) is the focus and always stays
// visible; the rest scrolls, with a marker line counting what is hidden above
// and below. The window is stateless: it centres the focus where it can.
func windowLines(lines []string, lo, hi, budget int, up, down func(n int) string) []string {
	if budget <= 0 || len(lines) <= budget {
		return lines
	}
	body := lines[1:]
	lo, hi = lo-1, hi-1 // indices into body
	size := max(budget-1, 1)
	// visible returns the content window [a, b) when it starts at a.
	visible := func(a int) (int, int) {
		room := size
		if a > 0 {
			room--
		}
		b := min(a+room, len(body))
		if b < len(body) {
			b = max(b-1, a)
		}
		return a, b
	}
	// prefer the window that centres the focus, but never leave blank room at the bottom
	want := min(max(lo-(size-(hi-lo+1))/2, 0), max(len(body)-(size-1), 0))
	best, bestDist := -1, 0
	for a := 0; a < len(body); a++ {
		s, e := visible(a)
		if lo < s || hi >= e {
			continue
		}
		d := max(a-want, want-a)
		if best < 0 || d < bestDist {
			best, bestDist = a, d
		}
	}
	if best < 0 { // a focus taller than the window: show its start
		best = max(lo-1, 0)
	}
	a, b := visible(best)
	out := []string{lines[0]}
	if a > 0 {
		out = append(out, up(a))
	}
	out = append(out, body[a:b]...)
	if b < len(body) {
		out = append(out, down(len(body)-b))
	}
	return out
}

func (p *setupPanel) moreUp(n int) string {
	return p.st.dim(fmt.Sprintf("   ↑ %d more", n))
}

func (p *setupPanel) moreDown(n int) string {
	return p.st.dim(fmt.Sprintf("   ↓ %d more", n))
}
