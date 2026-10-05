package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
)

// lateAdapter fixes Add, then never settles on its own; asked to
// interrupt, it settles as if the turn had completed.
type lateAdapter struct{}

type lateSession struct{ events chan agent.Event }

func (lateAdapter) Open(_ context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	if err := os.WriteFile(filepath.Join(spec.Workspace, "add.go"), []byte(fixed), 0o644); err != nil {
		return nil, err
	}
	return &lateSession{events: make(chan agent.Event, 8)}, nil
}
func (s *lateSession) Process() agent.Process     { return agent.Process{} }
func (s *lateSession) Events() <-chan agent.Event { return s.events }
func (s *lateSession) Send(agent.Turn) error {
	s.events <- agent.Event{Kind: agent.TurnAccepted}
	return nil
}
func (s *lateSession) Interrupt() error {
	s.events <- agent.Event{Kind: agent.TurnSettled, Exit: "done"}
	return nil
}
func (s *lateSession) Close() error { return nil }

// A turn that settles only after the stage timeout fired stays a timeout:
// no Candidate, no Check, no Verdict.
func TestRunStageTimeoutWinsOverALateResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(fxConfig+"[pipelines.default.limits]\nstage_timeout = \"1s\"\n"))
	gitIn(t, f.repo, "commit", "-q", "-am", "short stage timeout")
	var stdout, stderr bytes.Buffer
	env := Env{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Dir: f.repo,
		Interactive: func() bool { return false }, LookPath: exec.LookPath,
		Edit: func(string) error { return nil }, GOOS: runtime.GOOS, Version: "test", Getenv: os.Getenv,
		Agents: map[string]agent.Adapter{"fake": lateAdapter{}}, CacheSeedTemplate: testSeed,
	}
	code := Main(env, []string{"fix Add", "--fast", "--agent", "fake", "--unattended"})
	out := stdout.String()
	if code != ExitInfra || !strings.Contains(out, "Attempt failed: timeout") || strings.Contains(out, "✓ Accepted") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, stderr.String())
	}
	for _, rec := range recordTypes(t, f.onlyRun(t)) {
		if rec == run.RecCheckStarted || rec == run.RecVerdict {
			t.Errorf("a timed-out Attempt reached %s", rec)
		}
	}
}
