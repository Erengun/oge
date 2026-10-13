package cli

import (
	"fmt"
	"strings"
	"testing"
)

func numbered(n int) []string {
	l := []string{"head"}
	for i := 1; i < n; i++ {
		l = append(l, fmt.Sprintf("r%d", i))
	}
	return l
}

func marks(n int) string { return fmt.Sprintf("^%d", n) }
func markd(n int) string { return fmt.Sprintf("v%d", n) }

func TestWindowLinesFits(t *testing.T) {
	in := numbered(5)
	if got := windowLines(in, 2, 2, 5, marks, markd); strings.Join(got, ",") != strings.Join(in, ",") {
		t.Errorf("a panel that fits changed: %v", got)
	}
	if got := windowLines(in, 2, 2, 0, marks, markd); len(got) != 5 {
		t.Errorf("no budget should mean no limit: %v", got)
	}
}

func TestWindowLinesKeepsHeaderAndFocus(t *testing.T) {
	for n := 8; n <= 40; n += 7 {
		for budget := 4; budget < n; budget++ {
			for focus := 1; focus < n; focus++ {
				in := numbered(n)
				got := windowLines(in, focus, focus, budget, marks, markd)
				if len(got) > budget {
					t.Fatalf("n=%d budget=%d focus=%d: %d lines", n, budget, focus, len(got))
				}
				if got[0] != "head" {
					t.Fatalf("header lost: %v", got)
				}
				if !strings.Contains(strings.Join(got, ","), fmt.Sprintf("r%d,", focus)+"") &&
					got[len(got)-1] != fmt.Sprintf("r%d", focus) {
					t.Fatalf("n=%d budget=%d: focus r%d not visible: %v", n, budget, focus, got)
				}
			}
		}
	}
}

func TestWindowLinesMarkers(t *testing.T) {
	in := numbered(20)
	top := windowLines(in, 1, 1, 8, marks, markd)
	if top[1] != "r1" || top[len(top)-1] != "v13" {
		t.Errorf("top: %v", top)
	}
	bot := windowLines(in, 19, 19, 8, marks, markd)
	if bot[1] != "^13" || bot[len(bot)-1] != "r19" {
		t.Errorf("bottom: %v", bot)
	}
	mid := windowLines(in, 10, 10, 8, marks, markd)
	if mid[1][0] != '^' || mid[len(mid)-1][0] != 'v' {
		t.Errorf("middle should have both markers: %v", mid)
	}
}

func TestWindowLinesKeepsAnOpenEditor(t *testing.T) {
	in := numbered(20)
	got := strings.Join(windowLines(in, 9, 12, 9, marks, markd), ",")
	for _, w := range []string{"r9", "r10", "r11", "r12"} {
		if !strings.Contains(got, w) {
			t.Errorf("%s missing: %s", w, got)
		}
	}
}
