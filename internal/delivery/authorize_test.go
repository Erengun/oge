package delivery

import (
	"strings"
	"testing"

	"github.com/erengun/oge/internal/run"
)

// Accepted delivers by default; Overridden and Rejected only with the
// flag named after their outcome; every other outcome never (ADR-0015).
func TestAuthorizeMatchesTheFlagToTheOutcome(t *testing.T) {
	for _, c := range []struct {
		outcome run.Outcome
		parked  bool
		flag    string
		refusal string // "" when allowed
	}{
		{run.Accepted, false, "", ""},
		{run.Accepted, false, FlagRejected, "is Accepted; --rejected is only for a Rejected Run"},
		{run.Overridden, false, "", "is Overridden, not Accepted; pass --overridden"},
		{run.Overridden, false, FlagOverridden, ""},
		{run.Overridden, false, FlagRejected, "--rejected is only for a Rejected Run (use --overridden)"},
		{run.Rejected, false, "", "is Rejected, not Accepted; pass --rejected"},
		{run.Rejected, false, FlagRejected, ""},
		{run.Rejected, false, FlagOverridden, "(use --rejected)"},
		{run.Cancelled, false, FlagRejected, "ended Cancelled; only Accepted, Overridden (--overridden) and Rejected (--rejected)"},
		{run.Infeasible, false, "", "ended Infeasible"},
		{"", true, "", "is parked at a Gate"},
		{"", false, "", "stopped without an outcome"},
	} {
		r := &Run{ID: "r1", Dir: t.TempDir(), Candidate: "c0ffee", Outcome: c.outcome, Parked: c.parked}
		err := Authorize(r, c.flag)
		switch {
		case c.refusal == "" && err != nil:
			t.Errorf("%s %q: refused: %v", c.outcome, c.flag, err)
		case c.refusal != "" && (err == nil || !IsRefused(err) || !strings.Contains(err.Error(), c.refusal)):
			t.Errorf("%s %q: got %v, want a refusal with %q", c.outcome, c.flag, err, c.refusal)
		}
	}
	if err := Authorize(&Run{ID: "r1", Dir: t.TempDir(), Outcome: run.Accepted}, ""); err == nil || !strings.Contains(err.Error(), "no Candidate") {
		t.Errorf("a Run without a Candidate: %v", err)
	}
}

func TestSafePathRefusesWhatCouldLeaveTheTree(t *testing.T) {
	for _, p := range []string{"", "/etc/passwd", "../x", "a/../../x", ".git/config", "sub/.GIT/hooks/x", "a//b"} {
		if safePath(p) == nil {
			t.Errorf("%q passed", p)
		}
	}
	for _, p := range []string{"a.go", "dir/b.txt", ".github/workflows/ci.yml", ".gitignore"} {
		if err := safePath(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}
