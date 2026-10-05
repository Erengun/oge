package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

// frictionAdapter fixes Add, has one request denied, and then settles
// only if settle is set; otherwise it never settles, and the stage
// timeout ends the Attempt.
type frictionAdapter struct{ settle bool }

type frictionSession struct {
	events chan agent.Event
	settle bool
}

func (a frictionAdapter) Open(_ context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	if err := os.WriteFile(filepath.Join(spec.Workspace, "add.go"), []byte(fixed), 0o644); err != nil {
		return nil, err
	}
	return &frictionSession{events: make(chan agent.Event, 8), settle: a.settle}, nil
}
func (s *frictionSession) Process() agent.Process     { return agent.Process{} }
func (s *frictionSession) Events() <-chan agent.Event { return s.events }
func (s *frictionSession) Send(agent.Turn) error {
	f := agent.Friction{Denied: 1, LostTurns: 1}
	s.events <- agent.Event{Kind: agent.TurnAccepted}
	s.events <- agent.Event{Kind: agent.HostRequest, Friction: &f, Host: &agent.HostDecision{
		Family: agent.Approval, Tool: "Bash", Target: "curl x", Decision: "deny", Rule: "no_interactive_approval", Reason: "Öge denied this: no."}}
	if s.settle {
		s.events <- agent.Event{Kind: agent.TurnSettled, Exit: "done", Friction: &f}
	}
	return nil
}
func (s *frictionSession) Interrupt() error { return nil }
func (s *frictionSession) Close() error     { return nil }

func runWithFriction(t *testing.T, a agent.Adapter, config string) (int, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	f := newRunFixture(t)
	if config != "" {
		writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(fxConfig+config))
		gitIn(t, f.repo, "commit", "-q", "-am", "config")
	}
	var stdout, stderr bytes.Buffer
	env := f.env(&stdout, &stderr)
	env.Agents = map[string]agent.Adapter{"fake": a}
	code := Main(env, []string{"fix Add", "--fast", "--agent", "fake", "--unattended"})
	b, err := os.ReadFile(filepath.Join(f.onlyRun(t), "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return code, stdout.String(), string(b)
}

const oneLostTurn = `"policy_friction":{"denied":1,"envelope_refusals":0,"lost_turns":1}`

// Friction the turn showed before a timeout is still recorded, and the
// summary shows it for a Run that ends without a Verdict (#90).
func TestFrictionSurvivesATimeout(t *testing.T) {
	t.Parallel()
	code, out, ledger := runWithFriction(t, frictionAdapter{}, "[pipelines.default.limits]\nstage_timeout = \"1s\"\n")
	if code != ExitInfra || !strings.Contains(out, "Attempt failed: timeout") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "\nFriction      policy friction 1 turn (1 denied)\n") {
		t.Errorf("no friction line:\n%s", out)
	}
	if !strings.Contains(ledger, oneLostTurn) {
		t.Errorf("the Ledger lacks the friction:\n%s", ledger)
	}
}

// A settled turn's friction isn't counted twice with what its Host
// requests already showed.
func TestFrictionOfASettledTurn(t *testing.T) {
	t.Parallel()
	code, out, ledger := runWithFriction(t, frictionAdapter{settle: true}, "")
	if code != 0 || !strings.Contains(out, "\nFriction      policy friction 1 turn (1 denied)\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(ledger, oneLostTurn) {
		t.Errorf("the Ledger lacks the friction:\n%s", ledger)
	}
}

// sendBackFrictionAdapter leaves Add broken on its first Attempt, so the
// Check fails and sends it back, and fixes it on the second. Each Attempt
// has its own friction.
type sendBackFrictionAdapter struct {
	opened   *int
	friction []agent.Friction
}

func (a sendBackFrictionAdapter) Open(_ context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	n := *a.opened
	*a.opened++
	if n > 0 {
		fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
		if err := os.WriteFile(filepath.Join(spec.Workspace, "add.go"), []byte(fixed), 0o644); err != nil {
			return nil, err
		}
	}
	return &settledSession{events: make(chan agent.Event, 8), friction: a.friction[n]}, nil
}

type settledSession struct {
	events   chan agent.Event
	friction agent.Friction
}

func (s *settledSession) Process() agent.Process     { return agent.Process{} }
func (s *settledSession) Events() <-chan agent.Event { return s.events }
func (s *settledSession) Send(agent.Turn) error {
	f := s.friction
	s.events <- agent.Event{Kind: agent.TurnAccepted}
	s.events <- agent.Event{Kind: agent.TurnSettled, Exit: "done", Friction: &f}
	return nil
}
func (s *settledSession) Interrupt() error { return nil }
func (s *settledSession) Close() error     { return nil }

// Each Attempt's friction is in the Ledger, and the summary shows the
// Run's: the sum across the send-back (#90).
func TestFrictionIsSummedAcrossASendBack(t *testing.T) {
	t.Parallel()
	opened := 0
	a := sendBackFrictionAdapter{opened: &opened, friction: []agent.Friction{{Denied: 1, LostTurns: 1}, {Denied: 2, LostTurns: 1, EnvelopeRefusals: 1}}}
	code, out, ledger := runWithFriction(t, a, "")
	if code != 0 || opened != 2 || !strings.Contains(out, "send back  1 of ") {
		t.Fatalf("exit %d, %d Attempts\n%s", code, opened, out)
	}
	if !strings.Contains(out, "\nFriction      policy friction 2 turns (3 denied) · 1 refused before the envelope passed\n") {
		t.Errorf("the summary lacks the Run's friction:\n%s", out)
	}
	for _, want := range []string{oneLostTurn, `"policy_friction":{"denied":2,"envelope_refusals":1,"lost_turns":1}`} {
		if !strings.Contains(ledger, want) {
			t.Errorf("the Ledger lacks %s:\n%s", want, ledger)
		}
	}
}
