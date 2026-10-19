package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

func TestResumeAParkedRun(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, strayScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	id := filepath.Base(f.onlyRun(t))

	// Unattended again: the Gate re-opens and the Run re-parks.
	code, out, errOut = f.run(t, strayScript, "resume", id, "--unattended")
	if code != ExitParked {
		t.Fatalf("resume: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// Attended: "promote" decides the Gate, the resolved Candidate is
	// checked, and the Run accepts.
	f.attended("promote\n")
	code, out, errOut = f.run(t, strayScript, "resume", id, "--plain")
	if code != ExitOK {
		t.Fatalf("decided resume: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "Accepted") {
		t.Errorf("stdout lacks the Accepted summary:\n%s", out)
	}
}

// A Run whose frozen Pipeline blob is gone can't resume; on a terminal
// the refusal prints on the plain view rather than panicking the live
// one on a nil Pipeline.
func TestResumeAMissingPipelineBlobRefuses(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, _, errOut := f.run(t, strayScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
	dir := f.onlyRun(t)
	id := filepath.Base(dir)

	recs, err := ledger.Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	var frozen string
	for _, r := range recs {
		if r.Type != run.RecRunStarted {
			continue
		}
		var d struct {
			Frozen string `json:"frozen"`
		}
		if err := json.Unmarshal(r.Data, &d); err != nil {
			t.Fatal(err)
		}
		frozen = d.Frozen
	}
	if err := os.Remove(filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(frozen, "sha256:"))); err != nil {
		t.Fatal(err)
	}

	f.interactive = true
	f.setenv("TERM", "xterm-256color")
	code, _, errOut = f.run(t, strayScript, "resume", id)
	if code != ExitRefused || !strings.Contains(errOut, "unreadable") {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
}

func TestResumeRefusesAnEndedRun(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, _, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
	id := filepath.Base(f.onlyRun(t))
	code, out, errOut := f.run(t, strayScript, "resume", id, "--unattended")
	if code != ExitRefused {
		t.Fatalf("resume of an ended Run: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}
