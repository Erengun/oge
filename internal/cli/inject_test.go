package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/receipt/receipttest"
	"github.com/erengun/oge/internal/run"
)

// hostile holds an ESC CSI, a C1 CSI (U+009B), a C1 OSC (U+009D) with BEL,
// and a raw 0x9b byte, which is invalid UTF-8.
const hostile = "x\x1b[2J\u009b31m\u009d0;pwned\u0007\x9by"

// assertInert fails if out holds anything a terminal would act on.
func assertInert(t *testing.T, where, out string) {
	t.Helper()
	for _, r := range out {
		if r != '\n' && (r < 0x20 || (r >= 0x7f && r <= 0x9f)) {
			t.Errorf("%s: control U+%04X reached the terminal:\n%q", where, r, out)
			return
		}
	}
}

// Every agent- or Candidate-provided string is cleaned on its way to the
// terminal, in both views.
func TestControlCharactersNeverReachTheTerminal(t *testing.T) {
	agentEvents := []agent.Event{
		{Kind: agent.Claim, Text: hostile},
		{Kind: agent.Warning, Text: hostile},
		{Kind: agent.TurnSettled, Exit: hostile},
		{Kind: agent.TurnSettled, Failure: hostile},
	}
	attempts := []*run.Attempt{
		{ID: "implement#1", Stage: "implement", Agent: "fake", Failure: hostile},
		{ID: "implement#1", Stage: "implement", Agent: "fake", Exit: hostile},
		{ID: "implement#1", Stage: "implement", Agent: "fake", Exit: hostile, Candidate: "6d1231d9f00d", Changed: []string{"a"}},
	}
	check := &oracle.Result{
		Why:   hostile,
		Setup: &oracle.Execution{Run: "make setup", Why: hostile},
		Commands: []oracle.Execution{{Run: "go test -json ./...", Why: hostile,
			Report: &oracle.Report{Ran: 2, Failed: 2, FailedTests: []string{hostile, "TestB" + hostile}}}},
	}

	// Plain, with -v so the agent events show.
	var out bytes.Buffer
	h := newTUIHarness(t, false, 200)
	r := &renderer{w: &out, verbose: true, frozen: h.f}
	res := &run.Result{ID: "id", Candidate: "6d1231d9f00d"}
	for _, e := range agentEvents {
		r.observe(run.Event{Kind: run.EvAgent, Agent: e, Attempt: attempts[0]})
	}
	for _, a := range attempts {
		r.observe(run.Event{Kind: run.EvAttempt, Attempt: a, Result: res})
	}
	r.observe(run.Event{Kind: run.EvCheck, Check: check, Result: res})
	lb := receipttest.New(pipeline.Fast)
	lb.Attempt(receipttest.Attempt{ID: "implement#1", From: time.Second, To: 2 * time.Second, Candidate: receipttest.C1, Exit: "done", Claims: []string{hostile}})
	lb.Check(1, receipttest.C1, 0, 3*time.Second, 4*time.Second, []receipttest.Test{{Name: "TestB" + hostile, Attested: "fail"}}, nil)
	lb.End(5*time.Second, run.InfrastructureStop, receipttest.C1, hostile)
	r.receipt(lb.Receipt(), &run.Result{Outcome: run.InfrastructureStop})
	assertInert(t, "plain", out.String())
	if !strings.Contains(out.String(), "TestB") {
		t.Errorf("the failing test names are gone:\n%s", out.String())
	}

	// The TUI: activity under the running stage, then each finished line.
	for i, a := range attempts {
		h := newTUIHarness(t, false, 200)
		h.working()
		for _, e := range agentEvents {
			h.agent(e)
		}
		h.m.expanded = true
		assertInert(t, "tui activity", h.m.render())
		h.send(run.Event{Kind: run.EvAttempt, Attempt: a, Result: res})
		if i == 2 {
			h.send(run.Event{Kind: run.EvCheck, Check: check, Result: res})
		}
		assertInert(t, "tui stages", h.m.render())
	}
}

// clean escapes every character that makes text read other than it is,
// and drops invalid UTF-8, in every line the terminal shows.
func TestCleanEscapesBidiAndInvisibleRunes(t *testing.T) {
	for _, r := range []rune{0x202a, 0x202e, 0x2066, 0x2069, 0x200e, 0x200f, 0x061c, 0x200b, 0x200c, 0x200d, 0x2028, 0x2029, 0xfeff} {
		got := clean("a" + string(r) + "b")
		if want := fmt.Sprintf("a<U+%04X>b", r); got != want {
			t.Errorf("clean(%U) = %q, want %q", r, got, want)
		}
	}
	if got := clean("a\xffb"); got != "a�b" {
		t.Errorf("invalid UTF-8: %q", got)
	}
}
