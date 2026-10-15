package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testSecret is built at run time so no key-like literal sits in the source.
func testSecret() string { return "sk-" + strings.Repeat("a1", 12) }

func TestExportWritesRedactedPrivateFile(t *testing.T) {
	h := testHome(70)
	h.exportDir = filepath.Join(t.TempDir(), "exports")
	h.now = func() time.Time { return time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC) }
	h.logScroll("task: use " + testSecret())
	h.logScroll("\x1b[1mReceipt\x1b[0m ok")
	cmd, problem := h.exportCmd(nil)
	if problem != "" || cmd == nil {
		t.Fatalf("problem %q cmd %v", problem, cmd)
	}
	path := filepath.Join(h.exportDir, "oge-transcript-20261013-010203.txt")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, testSecret()) || !strings.Contains(got, "[REDACTED:") || strings.Contains(got, "\x1b") || !strings.Contains(got, "Receipt ok") {
		t.Errorf("export = %q", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
	if s := fmt.Sprint(cmd()); !strings.Contains(s, path) {
		t.Errorf("printed %q lacks the path", s)
	}
}

func TestExportEmpty(t *testing.T) {
	_, problem := testHome(70).exportCmd(nil)
	if !strings.Contains(problem, "nothing to export") {
		t.Errorf("problem %q", problem)
	}
}

func TestCopyIsRedactedBase64AndTmuxWrapped(t *testing.T) {
	h := testHome(70)
	h.last = &lastRun{receipt: func() string { return "\x1b[1mReceipt\x1b[0m\nkey " + testSecret() + "\n" }}
	seq, n := h.copySeq()
	want := base64.StdEncoding.EncodeToString([]byte(redacted("Receipt\nkey " + testSecret())))
	if seq != "\x1b]52;c;"+want+"\a" || n != 2 {
		t.Errorf("seq %q n %d", seq, n)
	}
	if strings.Contains(seq, testSecret()) {
		t.Error("secret in the sequence")
	}
	dec, _ := base64.StdEncoding.DecodeString(want)
	if strings.Contains(string(dec), testSecret()) || !strings.Contains(string(dec), "[REDACTED:") {
		t.Errorf("payload %q", dec)
	}
	h.tmux = true
	seq, _ = h.copySeq()
	if seq != "\x1bPtmux;\x1b\x1b]52;c;"+want+"\a\x1b\\" {
		t.Errorf("tmux seq %q", seq)
	}
}

func TestCopyWithoutReceipt(t *testing.T) {
	cmd, problem := testHome(70).copyCmd(nil)
	if problem != "" || !strings.Contains(fmt.Sprint(cmd()), "nothing to copy yet") {
		t.Errorf("problem %q", problem)
	}
}

func TestHelpListsExportAndCopy(t *testing.T) {
	for _, c := range []string{"/export", "/copy"} {
		if !strings.Contains(advancedHelpText(nil), c) {
			t.Errorf("/help lacks %s", c)
		}
	}
}

func TestExportNeverFallsBackToTheWorkingDirectory(t *testing.T) {
	h := testHome(70)
	h.logScroll("x")
	if cmd, problem := h.exportCmd(nil); cmd != nil || !strings.Contains(problem, "no private state dir") {
		t.Errorf("cmd %v problem %q", cmd, problem)
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.exportDir = filepath.Join(file, "exports") // a file where the dir should be
	if cmd, problem := h.exportCmd(nil); cmd != nil || !strings.Contains(problem, "no private state dir") {
		t.Errorf("cmd %v problem %q", cmd, problem)
	}
}

func TestExportCollisionGetsASuffix(t *testing.T) {
	h := testHome(70)
	h.exportDir = t.TempDir()
	h.now = func() time.Time { return time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC) }
	h.logScroll("x")
	for range 3 {
		if _, problem := h.exportCmd(nil); problem != "" {
			t.Fatal(problem)
		}
	}
	for _, n := range []string{"", "-2", "-3"} {
		if _, err := os.Stat(filepath.Join(h.exportDir, "oge-transcript-20261013-010203"+n+".txt")); err != nil {
			t.Error(err)
		}
	}
}

func TestScrollIsCapped(t *testing.T) {
	h := testHome(70)
	for i := range maxScroll + 10 {
		h.logScroll(fmt.Sprint("l", i))
	}
	if len(h.scroll) != maxScroll || h.scroll[len(h.scroll)-1] != fmt.Sprint("l", maxScroll+9) {
		t.Errorf("len %d", len(h.scroll))
	}
}

func TestCopyTooLongIsPrintedNotSent(t *testing.T) {
	h := testHome(70)
	h.last = &lastRun{receipt: func() string { return strings.Repeat("a", maxOSC52) }}
	if seq, _ := h.copySeq(); seq != "" {
		t.Error("sent an oversize payload")
	}
	if cmd, _ := h.copyCmd(nil); cmd == nil {
		t.Fatal("printed nothing")
	}
	if s := strings.Join(h.scroll, "\n"); !strings.Contains(s, "too long for the clipboard") {
		t.Errorf("printed %.80q", s)
	}
}

// A print taller than the room above the frame goes in pieces that each
// fit, or insertAbove lands it over the frame (#247).
func TestFitPieces(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	text := strings.Join(lines, "\n")
	pieces := fitPieces(text, 80, 24, 9) // room: 14 rows
	if got := strings.Join(pieces, "\n"); got != text {
		t.Fatalf("the pieces don't make the text:\n%s", got)
	}
	for _, p := range pieces {
		if n := strings.Count(p, "\n") + 1; n > 14 {
			t.Errorf("a piece of %d rows, room 14", n)
		}
	}
	if len(pieces) != 3 {
		t.Errorf("%d pieces, want 3", len(pieces))
	}
	// A wide line counts the rows it wraps to.
	wide := strings.Repeat("x", 200) // 3 rows at width 80
	if p := fitPieces(wide+"\n"+wide+"\nend", 80, 10, 3); len(p) != 2 {
		t.Errorf("wrapped rows: %d pieces, want 2", len(p))
	}
	// Unknown height or no room: whole, or one line at a time.
	if p := fitPieces(text, 80, 0, 9); len(p) != 1 {
		t.Errorf("unknown height: %d pieces", len(p))
	}
	if p := fitPieces("a\nb", 80, 5, 9); len(p) != 2 {
		t.Errorf("no room: %d pieces, want one line each", len(p))
	}
}

// A print is sized against the taller of the frame on screen and the next
// one: at a stop the model already draws the short frame while the tall
// running one is still on screen (#188).
func TestFrameRowsIsTheTallerFrame(t *testing.T) {
	h := testHome(80)
	next := rows(h.View().Content)
	if got := h.frameRows(); got != next {
		t.Fatalf("frameRows %d, want the composer's %d", got, next)
	}
	h.shown = 23 // a tall Run frame still on screen
	if got := h.frameRows(); got != 23 {
		t.Errorf("frameRows %d under a 23-row frame, want 23", got)
	}
	if h.shown != 23 {
		t.Errorf("frameRows changed what is on screen to %d", h.shown)
	}
}
