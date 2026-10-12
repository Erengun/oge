//go:build unix

package cli

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

// An interrupt while composing ends the session cleanly, without a Run to
// stop.
func TestBareOgeInterruptedWhileComposing(t *testing.T) {
	// Sequential: it signals the test process.
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if strings.Contains(s.errOut.String(), "killed") {
		t.Errorf("stderr: %s", s.errOut.String())
	}
}
