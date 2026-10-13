package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

func TestNotifierSequences(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		n    notifier
		want string
	}{
		{"plain", notifier{bell: true}, "\a\x1b]9;m\a"},
		{"no bell", notifier{}, "\x1b]9;m\a"},
		{"osc 777", notifier{bell: true, osc777: true}, "\a\x1b]777;notify;oge;m\a"},
		{"tmux passthrough", notifier{bell: true, tmux: true}, "\a\x1bPtmux;\x1b\x1b]9;m\a\x1b\\"},
		{"screen keeps only the bell", notifier{bell: true, screen: true}, "\a"},
	} {
		if got := c.n.sequence("m"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The texts are fixed: whatever the Run wrote never reaches them.
func TestNotifyTextsAreFixed(t *testing.T) {
	t.Parallel()
	got := []string{notifyWaiting, notifyEnded(run.Accepted), notifyEnded(run.Rejected), notifyEnded(run.InfrastructureStop), notifyEnded("")}
	want := []string{"oge: waiting for you", "oge: Run accepted", "oge: Run ended: rejected", "oge: Run ended: infrastructure stop", "oge: Run ended: stopped"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("text %d = %q, want %q", i, got[i], want[i])
		}
	}
	m := newModel(task.Parse("steal $(secret) \a\x1b]9;evil\a\n"), homeFrozen(), newStyles(false), time.Now)
	m.notify = &notifier{attention: true, runEnd: true, bell: true}
	m.runEnded(&run.Result{Outcome: run.Accepted, Why: []string{"agent says \x1b]9;evil\a"}}, false)
	if s := strings.Join(m.alerts, ""); strings.Contains(s, "evil") || strings.Contains(s, "secret") {
		t.Errorf("alert carries agent or Task text: %q", s)
	}
}

func TestNotifierOffWhenSilentOrDisabled(t *testing.T) {
	t.Parallel()
	env := Env{Getenv: func(string) string { return "" }}
	for _, f := range []runFlags{{unattended: true}, {dryRun: true}, {plain: true}} {
		if notifierFor(env, f, nil) != nil {
			t.Errorf("%+v notifies", f)
		}
	}
	m := newModel(task.Parse("x\n"), homeFrozen(), newStyles(false), time.Now)
	m.needsYou()
	m.runEnded(nil, true)
	if m.flushAlerts() != nil {
		t.Error("a model with no notifier rings")
	}
}

func writeNotifyConfig(t *testing.T, f *runFixture, body string) {
	t.Helper()
	p := filepath.Join(f.vars["XDG_CONFIG_HOME"], filepath.FromSlash("oge/config.toml"))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("schema = 1\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A Gate and the Run's end ring the bell and send OSC 9, fixed text only.
func TestHomeNotifiesAtTheGateAndAtTheEnd(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake", "--confirm")
	s.composer()
	s.type_("fix Add\r")
	s.expect("Result gate")
	s.expect("\a\x1b]9;oge: waiting for you\a")
	s.type_("t\r")
	s.ended()
	s.expect("\a\x1b]9;oge: Run accepted\a")
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}

func TestHomeStaysSilentWithNotifyOff(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeNotifyConfig(t, f, "[notify]\nattention = false\nrun_end = false\n")
	s := startHomeSession(t, f, "--fast", "--agent", "fake", "--confirm")
	s.composer()
	s.type_("fix Add\r")
	s.expect("Result gate")
	s.type_("t\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if out := s.out.String(); strings.Contains(out, "\x1b]9;") || strings.Contains(out, "\a") {
		t.Errorf("notified with [notify] off:\n%q", out)
	}
}
