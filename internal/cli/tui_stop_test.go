package cli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
)

// An Infrastructure stop in Standard mode (the implementer's agent can't
// authenticate): the stages that never ran show as not run, never as
// reviewing; "Nothing needs you" is gone; and the stop's reason shows.
func TestTUIStandardInfrastructureStop(t *testing.T) {
	h := newStandardHarness(t)
	h.working()
	h.at(6100 * time.Millisecond)
	h.att.Stop = "claude isn't logged in"
	h.agent(agent.Event{Kind: agent.TurnSettled, Failure: h.att.Stop})
	h.send(run.Event{Kind: run.EvAttempt, Result: h.res, Attempt: h.att})
	got := h.end(run.InfrastructureStop, h.att.Stop)
	for _, bad := range []string{"reviewing", "Nothing needs you"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q after the stop:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, h.att.Stop) {
		t.Errorf("the stop's reason is missing:\n%s", got)
	}
	golden(t, "standard-infrastructure-stop", got)
}

// stoppedAgent is an adapter whose implementer can't authenticate, as an
// unlogged-in claude: its turn settles with an Infrastructure stop.
type stoppedAgent struct {
	agent.Adapter
	reason string
}

func (s stoppedAgent) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	if spec.Role != "implementer" {
		return s.Adapter.Open(ctx, spec)
	}
	return &stoppedSession{reason: s.reason, events: make(chan agent.Event, 1)}, nil
}

type stoppedSession struct {
	reason string
	events chan agent.Event
	once   sync.Once
}

func (s *stoppedSession) Process() agent.Process { return agent.Process{} }
func (s *stoppedSession) Send(agent.Turn) error {
	s.events <- agent.Event{Kind: agent.TurnSettled, Stop: s.reason}
	return nil
}
func (s *stoppedSession) Events() <-chan agent.Event { return s.events }
func (s *stoppedSession) Interrupt() error           { return nil }
func (s *stoppedSession) Close() error               { s.once.Do(func() { close(s.events) }); return nil }

// The same stop, live in a bare oge: the view's last frame is the finished
// one, and the Receipt with the stop's reason follows it (#188).
func TestBareOgeShowsAnInfrastructureStop(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	const reason = "Claude's authentication failed: not logged in"
	f.wrap = func(a agent.Adapter) agent.Adapter { return stoppedAgent{a, reason} }
	s := startHomeSession(t, f, "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.expect("Infrastructure stop")
	// The live frame names the stop first; the Receipt is printed with the
	// final frame once the Run has returned.
	s.expect("ÖGE RECEIPT")
	s.composer()
	var screen vt
	screen.write([]byte(s.out.String()))
	got := screen.String()
	s.type_("\x03")
	s.wait()
	for _, bad := range []string{"reviewing", "Nothing needs you"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q after the stop:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, reason) || !strings.Contains(got, "Result") {
		t.Errorf("no Receipt with the stop's reason:\nscreen:\n%s\nstderr:\n%s", got, s.errOut.String())
	}
}
