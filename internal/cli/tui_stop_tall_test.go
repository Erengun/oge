package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

// An Infrastructure stop under a tall frame: QA's agent works a while, its
// activity expanded with ctrl+o, then can't authenticate. The frame on
// screen at the stop is the tall running one (23 of the 24 rows), so what
// is printed above it, the stopped stages, the summary and the Receipt,
// must still land whole, with no row of that frame left behind (#188).
func TestBareOgeShowsAnInfrastructureStopUnderATallFrame(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	const reason = "Claude's authentication failed: not logged in"
	release := make(chan struct{})
	f.wrap = func(a agent.Adapter) agent.Adapter { return busyStoppedAgent{a, reason, release} }
	s := startHomeSession(t, f, "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.expectScreen(busySteps[len(busySteps)-1])
	s.type_("\x0f")
	s.expectScreen("ctrl+o to collapse")
	close(release)
	s.expect("ÖGE RECEIPT")
	s.composer()
	got := normHome(s.settledScreen())
	s.type_("\x03")
	s.wait()
	for _, bad := range []string{"reviewing", "Nothing needs you", "ctrl+o to collapse"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q after the stop:\n%s", bad, got)
		}
	}
	// The Receipt is whole and in order, once.
	at := 0
	for _, want := range []string{"ÖGE RECEIPT", "Result", reason, "Task", "Checks", "Protected", "Scope", "Handled", "Time", "Not covered", "Ledger"} {
		i := strings.Index(got[at:], want)
		if i < 0 {
			t.Fatalf("the Receipt lost %q:\n%s\nstderr:\n%s", want, got, s.errOut.String())
		}
		at += i + len(want)
	}
	if n := strings.Count(got, "ÖGE RECEIPT"); n != 1 {
		t.Errorf("%d Receipts:\n%s", n, got)
	}
}

// busySteps is what QA's agent does before its stop: enough to make the
// running frame tall.
var busySteps = func() []string {
	var out []string
	for i := 1; i <= 30; i++ {
		out = append(out, fmt.Sprintf("probe_%02d.go", i))
	}
	return out
}()

// busyStoppedAgent is an adapter whose QA agent reads busySteps, waits for
// release, then can't authenticate.
type busyStoppedAgent struct {
	agent.Adapter
	reason  string
	release <-chan struct{}
}

func (b busyStoppedAgent) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	if spec.Role != "verifier" {
		return b.Adapter.Open(ctx, spec)
	}
	return &busyStoppedSession{b: b, events: make(chan agent.Event, len(busySteps)+1), quit: make(chan struct{})}, nil
}

type busyStoppedSession struct {
	b      busyStoppedAgent
	events chan agent.Event
	quit   chan struct{}
	once   sync.Once
}

func (s *busyStoppedSession) Process() agent.Process { return agent.Process{} }
func (s *busyStoppedSession) Send(agent.Turn) error {
	for _, p := range busySteps {
		s.events <- agent.Event{Kind: agent.Claim, Tool: "Read", Target: p}
	}
	go func() {
		defer close(s.events)
		select {
		case <-s.b.release:
			s.events <- agent.Event{Kind: agent.TurnSettled, Stop: s.b.reason}
			<-s.quit
		case <-s.quit:
		}
	}()
	return nil
}
func (s *busyStoppedSession) Events() <-chan agent.Event { return s.events }
func (s *busyStoppedSession) Interrupt() error           { return nil }
func (s *busyStoppedSession) Close() error               { s.once.Do(func() { close(s.quit) }); return nil }
