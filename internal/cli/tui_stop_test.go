package cli

import (
	"strings"
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
