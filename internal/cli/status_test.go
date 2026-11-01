package cli

import (
	"regexp"
	"strings"
	"testing"
)

// oge status prints the brief since the human's last act, read from the
// records alone, and exits 0 even when there is nothing to say.
func TestStatusBrief(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, "", "status")
	if code != ExitOK || out != "nothing to report: no Chats or Runs in this repository yet\n" {
		t.Fatalf("empty: exit %d: %q %s", code, out, errOut)
	}

	if code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended"); code != ExitOK {
		t.Fatalf("run: exit %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = f.run(t, "", "status")
	want := regexp.MustCompile(`^since you last acted \(\dm\): ✓ 1 accepted\n  Run \S+ fix Add · ✓ 1 accepted\n$`)
	if code != ExitOK || !want.MatchString(out) {
		t.Fatalf("after an Accepted Run: exit %d: %q %s", code, out, errOut)
	}
	if strings.Contains(out, "fixing Add") {
		t.Errorf("the agent's text reached the brief: %q", out)
	}

	if code, _, errOut := f.run(t, "", "status", "now"); code != ExitRefused || !strings.Contains(errOut, "no arguments") {
		t.Errorf("status now: exit %d: %s", code, errOut)
	}
}
