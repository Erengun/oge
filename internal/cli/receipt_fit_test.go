package cli

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/receipt"
	"github.com/erengun/oge/internal/run"
)

// /copy puts the unwrapped Receipt on the clipboard, though the screen's
// copy wraps at the terminal's width (#314).
func TestCopyIsNotHardWrapped(t *testing.T) {
	h := testHome(80)
	long := "Time          " + strings.Repeat("stage 1s · ", 12) + "your attention 1s"
	h.last = &lastRun{
		receipt: func() string { return "Time          stage 1s\n              · your attention 1s\n" },
		copy:    func() string { return long + "\n" },
	}
	seq, n := h.copySeq()
	if want := base64.StdEncoding.EncodeToString([]byte(long)); seq != "\x1b]52;c;"+want+"\a" || n != 1 {
		t.Errorf("seq %q n %d: the clipboard text is wrapped", seq, n)
	}
}

// The configuration warning under the Receipt wraps under its text.
func TestReceiptWarningWrapsAtTheWidth(t *testing.T) {
	var b strings.Builder
	r := &renderer{w: &b, width: func() int { return 80 }, warn: "test_config is empty: setup and Candidate code can change go.mod/go.sum that the Check reads"}
	r.receipt(&receipt.Receipt{}, &run.Result{})
	var got []string
	for _, l := range strings.Split(b.String(), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("row is %d columns: %q", w, l)
		}
		if strings.HasPrefix(l, "! ") || strings.HasPrefix(l, "  ") && len(got) > 0 {
			got = append(got, strings.TrimSpace(strings.TrimPrefix(l, "!")))
		}
	}
	if len(got) < 2 || strings.Join(got, " ") != r.warn {
		t.Errorf("warning not wrapped whole: %q", got)
	}
}
