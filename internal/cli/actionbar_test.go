package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/run"
)

// barHarness is an action bar whose actions only say what they did.
func barHarness(keys string, color bool) (*actionBar, *bytes.Buffer, *[]string) {
	var out bytes.Buffer
	var did []string
	b := &actionBar{in: strings.NewReader(keys), out: &out, st: newStyles(color)}
	b.apply = func() string {
		did = append(did, "apply")
		b.applied = true
		return "Applied Candidate 6d1231d to your working tree (1 file changed). Nothing was committed or staged."
	}
	b.diff = func() { did = append(did, "diff"); out.WriteString("(the paged diff)\n") }
	b.receipt = func() { did = append(did, "receipt"); out.WriteString("ACCEPTED   Candidate 6d1231d · Oracle v0\n") }
	return b, &out, &did
}

func TestActionBarFrames(t *testing.T) {
	b, _, _ := barHarness("", false)
	golden(t, "action-bar", b.render()+"\n")
	b.applied = true
	golden(t, "action-bar-applied", b.render()+"\n")
	c, _, _ := barHarness("", true)
	golden(t, "action-bar-color", c.render()+"\n")
}

// d opens the diff and comes back to the bar; r shows the summary; a
// applies once, after which the bar no longer offers it; q leaves.
func TestActionBarKeys(t *testing.T) {
	b, out, did := barHarness("xdraaq", false)
	b.run()
	if got := strings.Join(*did, ","); got != "diff,receipt,apply" {
		t.Errorf("actions: %s", got)
	}
	var screen vt
	screen.write(out.Bytes())
	golden(t, "action-bar-session", screen.String())
}

func TestActionBarLeavesOnEnterAndAtTheEndOfInput(t *testing.T) {
	for _, keys := range []string{"\na", "\ra", "qa", "\x03a", ""} {
		b, out, did := barHarness(keys, false)
		b.run()
		if len(*did) != 0 {
			t.Errorf("%q: %v after leaving", keys, *did)
		}
		var screen vt
		screen.write(out.Bytes())
		if s := strings.TrimSpace(screen.String()); s != "" {
			t.Errorf("%q: the bar stayed on the screen: %q", keys, s)
		}
	}
}

// The live view offers the bar only after an Accepted Run: there is no
// one-key apply for any other outcome (ADR-0015).
func TestNoActionBarUnlessAccepted(t *testing.T) {
	for _, o := range []run.Outcome{run.Rejected, run.Overridden, run.Cancelled, run.Parked, run.InfrastructureStop} {
		var out bytes.Buffer
		env := Env{Stdin: strings.NewReader("a"), Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" }}
		h := newTUIHarness(t, false, 80)
		u := &tui{in: env.Stdin, out: &out, plain: &renderer{w: &out, frozen: h.f}, m: h.m}
		if code := afterRun(env, runFlags{}, u, t.TempDir(), &run.Result{Outcome: o, Candidate: "6d1231d9f00d"}); code != ExitOK || out.Len() != 0 {
			t.Errorf("%s: exit %d, %q", o, code, out.String())
		}
	}
}
