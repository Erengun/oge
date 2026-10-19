package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContextUsePercent(t *testing.T) {
	for _, c := range []struct {
		use  ContextUse
		want int
	}{
		{ContextUse{126000, 200000}, 63},
		{ContextUse{6051, 200000}, 3},
		{ContextUse{300000, 200000}, 100},
		{ContextUse{5, 0}, 0},
	} {
		if got := c.use.Percent(); got != c.want {
			t.Errorf("%+v = %d%%, want %d%%", c.use, got, c.want)
		}
	}
}

func TestClipOutputKeepsLinesAndMarksTheCut(t *testing.T) {
	if got := ClipOutput("a\r\nb  \n\n"); got != "a\nb" {
		t.Errorf("trimmed = %q", got)
	}
	long := strings.Repeat("x\n", OutputMaxLines+5)
	got := ClipOutput(long)
	if n := strings.Count(got, "\n"); n != OutputMaxLines || !strings.HasSuffix(got, "\n…") {
		t.Errorf("line cut: %d breaks, tail %q", n, got[max(len(got)-4, 0):])
	}
	big := ClipOutput(strings.Repeat("é", OutputMaxBytes))
	if !utf8.ValidString(big) || len(big) > OutputMaxBytes+8 || !strings.HasSuffix(big, "\n…") {
		t.Errorf("byte cut: %d bytes, valid=%v", len(big), utf8.ValidString(big))
	}
}
