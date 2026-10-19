package receipt_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/receipt"
)

// A text that starts with a separator keeps it when it wraps.
func TestWrapKeepsALeadingSeparator(t *testing.T) {
	rows := receipt.Wrap(" · "+strings.Repeat("word ", 12), 30)
	if len(rows) < 2 || !strings.HasPrefix(rows[0], "· ") {
		t.Errorf("rows %q", rows)
	}
}

// timeRows is the Time line of text and the continuation rows under it.
func timeRows(text string) []string {
	var rows []string
	in := false
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(l, "Time "):
			in = true
			rows = append(rows, strings.TrimSpace(strings.TrimPrefix(l, "Time")))
		case in && strings.HasPrefix(l, strings.Repeat(" ", 14)) && strings.TrimSpace(l) != "":
			rows = append(rows, strings.TrimSpace(l))
		default:
			in = false
		}
	}
	return rows
}

// A Time line wider than the terminal wraps at its separators under the
// text, and loses nothing (#314).
func TestFitWrapsTimeLine(t *testing.T) {
	r := &receipt.Receipt{Time: receipt.Time{
		TotalMs: 754000,
		Stages: []receipt.StageTime{
			{Stage: "preflight", Ms: 12000}, {Stage: "implement", Ms: 401000},
			{Stage: "Check", Ms: 221000}, {Stage: "QA", Ms: 90000},
		},
		AttentionMs: 36000,
	}}
	const width = 80
	whole := timeRows(r.Text(receipt.Paint{}))
	if len(whole) != 1 || ansi.StringWidth("Time          "+whole[0]) <= width {
		t.Fatalf("the Time line should be one row wider than %d columns: %q", width, whole)
	}
	fit := r.Fit(width).Text(receipt.Paint{})
	for _, l := range strings.Split(fit, "\n") {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("row is %d columns, past %d: %q", w, width, l)
		}
	}
	rows := timeRows(fit)
	if len(rows) < 2 {
		t.Fatalf("the Time line did not wrap: %q", rows)
	}
	// Each continuation row opens with the separator it broke at.
	if got := strings.Join(rows, " "); got != whole[0] {
		t.Errorf("wrapped Time lost text:\n got %q\nwant %q", got, whole[0])
	}
	if !strings.HasSuffix(rows[len(rows)-1], "your attention 36s") {
		t.Errorf("your attention clipped: %q", rows)
	}
}
