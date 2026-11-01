//go:build unix

package cli

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
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

// A pager or $EDITOR has the terminal: its Ctrl+C (a SIGINT to the process
// group, as less leaves ISIG on) is its own and never ends the session.
func TestBareOgeInterruptInPagerOrEditorIsTheirs(t *testing.T) {
	// Sequential: it signals the test process.
	f := newRunFixture(t)
	interrupt := func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Error(err)
		}
		time.Sleep(200 * time.Millisecond) // let the signal arrive while it runs
	}
	// The viewer would take /diff; a terminal too short for it leaves the
	// pager, which is what this test is about.
	old := diffMinHeight
	diffMinHeight = 1 << 20
	t.Cleanup(func() { diffMinHeight = old })
	paged := make(chan struct{}, 1)
	f.edit = func(path string) error {
		interrupt()
		return os.WriteFile(path, []byte("fix Add\n"), 0o644)
	}
	s := startHomeSessionWith(t, f, func(env *Env) {
		env.Page = func(string) error { interrupt(); paged <- struct{}{}; return nil }
	}, "--fast", "--agent", "fake")
	s.composer()
	s.type_("\x05") // Ctrl+E
	s.expect("fix Add")
	s.type_("\r")
	s.ended()
	s.type_("/diff\r")
	select {
	case <-paged:
	case <-time.After(20 * time.Second):
		t.Fatal("the pager never ran")
	}
	s.type_("still here")
	s.expect("still here")
	// clear it, then leave
	if code := s.quitClosing("\x03\x03"); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if n := len(runDirs(t, f)); n != 1 {
		t.Errorf("%d Runs", n)
	}
	if strings.Contains(s.errOut.String(), "killed") {
		t.Errorf("stderr: %s", s.errOut.String())
	}
}
