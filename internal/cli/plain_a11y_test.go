package cli

import (
	"strings"
	"testing"
)

// assertScreenReaderSafe fails when plain output holds anything a screen
// reader or a pipe would trip on: terminal control, carriage-return
// redraws, or box-drawing and arrow decoration (#173).
func assertScreenReaderSafe(t *testing.T, out string) {
	t.Helper()
	if strings.ContainsRune(out, '\x1b') {
		t.Errorf("plain output has an ESC sequence:\n%q", out)
	}
	if strings.ContainsRune(out, '\r') {
		t.Errorf("plain output has a carriage return:\n%q", out)
	}
	for _, r := range out {
		if (r >= 0x2500 && r <= 0x257F) || r == '▶' {
			t.Errorf("plain output has the decorative character %q:\n%s", r, out)
			break
		}
	}
}

// --plain is flat text: no control codes, no redraws, no box drawing, and
// every status glyph is followed by a word.
func TestPlainIsScreenReaderSafe(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	f.attended("q\n")
	code, out, errOut := f.run(t, cheatScript, "fix Add", "--fast", "--agent", "fake", "--plain", "-v")
	if code != ExitCancelled {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	assertScreenReaderSafe(t, out)
	if !strings.Contains(out, "Result        ■ Cancelled") {
		t.Errorf("the status glyph lacks its word:\n%s", out)
	}

	code, out, errOut = runIn(t, newRunFixture(t).repo, "--dry-run", "-v", "fix it")
	if code != ExitOK && code != ExitRefused {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	assertScreenReaderSafe(t, out)
}
