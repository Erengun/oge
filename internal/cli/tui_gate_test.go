package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/run"
)

// typeLine types s and presses Enter.
func (h *tuiHarness) typeLine(s string) {
	for _, r := range s {
		h.m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// openGate opens r in the view and returns where its decision arrives.
func (h *tuiHarness) openGate(r gate.Request) chan gate.Decision {
	reply := make(chan gate.Decision, 1)
	h.m.Update(batchMsg{gateMsg{req: r, reply: reply}})
	return reply
}

func (h *tuiHarness) decided(r gate.Request, d gate.Decision) {
	h.send(run.Event{Kind: run.EvDecided, Gate: &r, Decision: &d})
}

func noDecision(t *testing.T, reply chan gate.Decision) {
	t.Helper()
	select {
	case d := <-reply:
		t.Fatalf("decided %+v", d)
	default:
	}
}

func boundExhaustionAt(h *tuiHarness) gate.Request {
	r := boundExhaustionRequest()
	r.Check = &oracle.Result{Commands: []oracle.Execution{{Run: "go test -json ./...", DurationMs: 1180, Why: "exit 1",
		Report: &oracle.Report{Ran: 1, Failed: 1, FailedTests: []string{"TestAdd"}}}}}
	r.Choices[0].Says = "one more send-back past the limit, with the failure output (type it in full, with a reason)"
	r.Choices[1].Says = "end the Run Rejected (type the word and a reason)"
	r.Choices[2].Says = "end the Run Cancelled"
	return r
}

// The bound-exhaustion Gate in the live view: no default, Enter alone
// does nothing, reject is typed in full with a reason, and Ctrl-C at the
// reason returns to the Gate without cancelling the Run.
func TestTUIBoundExhaustionGate(t *testing.T) {
	h := newTUIHarness(t, false, 100)
	cancelled := 0
	h.m.interrupt = func() { cancelled++ }
	h.working()
	h.implemented("")
	h.checked(false)
	r := boundExhaustionAt(h)
	reply := h.openGate(r)
	frame := h.m.render()
	golden(t, "gate-bound-exhaustion", frame)

	h.typeLine("")
	if got := h.m.render(); got != frame {
		t.Errorf("Enter alone changed the screen:\n%s", got)
	}
	h.typeLine("r")
	if got := h.m.render(); !strings.Contains(got, `type "reject" in full`) {
		t.Errorf("r chose something:\n%s", got)
	}
	h.typeLine("reject")
	golden(t, "gate-reason", h.m.render())
	h.typeLine("")
	if got := h.m.render(); !strings.Contains(got, "a reason is required for reject") {
		t.Errorf("an empty reason went through:\n%s", got)
	}
	h.m.Update(ctrlKey('c'))
	if cancelled != 0 {
		t.Error("Ctrl-C at the reason prompt cancelled the Run")
	}
	if got := h.m.render(); strings.Contains(got, "reason:") {
		t.Errorf("Ctrl-C didn't return to the Gate:\n%s", got)
	}
	noDecision(t, reply)

	h.typeLine("reject")
	h.typeLine("e")
	if got := h.m.render(); !strings.Contains(got, "the editor isn't available in the live view yet") {
		t.Errorf("e was taken as a reason:\n%s", got)
	}
	noDecision(t, reply)
	h.typeLine("the test can't pass as written")
	select {
	case d := <-reply:
		if d != (gate.Decision{Choice: "reject", Reason: "the test can't pass as written"}) {
			t.Fatalf("decided %+v", d)
		}
	default:
		t.Fatal("no decision")
	}
	// The Gate stays until the Run has recorded the decision; only then
	// is it echoed and closed.
	got := h.m.render()
	if strings.Contains(got, "decision") || !strings.Contains(got, "bound-exhaustion Gate") || !strings.Contains(got, "recording…") {
		t.Errorf("before GateDecided:\n%s", got)
	}
	h.typeLine("quit")
	noDecision(t, reply)
	h.at(9 * time.Second)
	h.decided(r, gate.Decision{Choice: "reject", Reason: "the test can't pass as written"})
	if got := h.m.render(); strings.Contains(got, "recording…") || strings.Contains(got, "  bound-exhaustion Gate") {
		t.Errorf("after GateDecided the Gate is still open:\n%s", got)
	}
	golden(t, "gate-rejected", h.end(run.Rejected))
}

func TestTUIResultGate(t *testing.T) {
	h := newTUIHarness(t, false, 100)
	h.working()
	h.implemented("")
	h.checked(true)
	r := gate.Request{
		Name: "Result gate", What: "Öge's Check passed on the Candidate.", Need: "Decide whether to take it.",
		Check: &oracle.Result{Pass: true, Commands: []oracle.Execution{{Run: "go test -json ./...", DurationMs: 1180, Pass: true, Report: &oracle.Report{Ran: 1}}}},
		Pins:  gate.Pins{Gate: "gate.result", Attempt: "implement#1", Candidate: "6d1231d9f00d", Verdicts: []int{1}},
	}
	for _, ws := range [][2]string{{"take", "end the Run Accepted"}, {"send back", "return it to the implementer, with an optional note"},
		{"reject", "end the Run Rejected (type the word and a reason)"}, {"quit", "end the Run Cancelled"}} {
		c := gate.Choices[ws[0]]
		c.Says = ws[1]
		r.Choices = append(r.Choices, c)
	}
	reply := h.openGate(r)
	golden(t, "gate-result", h.m.render())
	h.typeLine("t")
	if d := <-reply; d.Choice != "take" {
		t.Fatalf("decided %+v", d)
	}
}

// A send-back starts the implementer and the Check again, under a line
// that says why.
func TestTUISendBack(t *testing.T) {
	h := newTUIHarness(t, false, 100)
	h.working()
	h.implemented("")
	h.checked(false)
	h.send(run.Event{Kind: run.EvSendBack, Result: h.res, SendBack: 1, SendBacks: 3})
	h.att = &run.Attempt{ID: "implement#2", Stage: "implement", Agent: "fake", Cause: "send_back"}
	h.at(7500 * time.Millisecond)
	h.agent(agent.Event{Kind: agent.Claim, Text: "fixing Add for real"})
	golden(t, "send-back-running", h.m.render())
	h.at(9 * time.Second)
	h.att.Exit, h.att.Candidate, h.att.Changed = "done", "77aa31d9f00d", []string{"add.go"}
	h.send(run.Event{Kind: run.EvAttempt, Result: h.res, Attempt: h.att})
	h.at(10 * time.Second)
	h.res.Candidate = h.att.Candidate
	h.send(run.Event{Kind: run.EvCheck, Result: h.res, Check: &oracle.Result{Pass: true, Commands: []oracle.Execution{
		{Run: "go test -json ./...", DurationMs: 1020, Pass: true, Report: &oracle.Report{Ran: 1}}}}})
	golden(t, "send-back-accepted", h.end(run.Accepted))
}

// Once the live view has stopped, a Gate is asked in plain lines rather
// than waiting on a view nobody draws.
func TestTUIGateAfterTheViewStoppedFallsBackToPlain(t *testing.T) {
	var out syncBuf
	u := &tui{plain: &renderer{w: &out, input: &lines{in: strings.NewReader("q\n")}}, m: &model{queue: &queue{wake: make(chan struct{}, 1)}},
		gone: make(chan struct{})}
	close(u.gone)
	d, err := u.gate(context.Background(), boundExhaustionRequest())
	if err != nil || d.Choice != "quit" || !strings.Contains(out.b.String(), "bound-exhaustion Gate") {
		t.Fatalf("%+v %v\n%s", d, err, out.b.String())
	}
}

func TestTUICtrlCAtTheChoiceCancelsAndClosesTheGate(t *testing.T) {
	h := newTUIHarness(t, false, 100)
	cancelled := 0
	h.m.interrupt = func() { cancelled++ }
	h.working()
	h.implemented("")
	h.checked(false)
	h.openGate(boundExhaustionAt(h))
	h.m.Update(ctrlKey('c'))
	if cancelled != 1 || strings.Contains(h.m.render(), "bound-exhaustion Gate") {
		t.Errorf("cancelled %d\n%s", cancelled, h.m.render())
	}
}

// The attention line says whether the Run needs the human (ADR-0022).
func TestTUIAttentionLine(t *testing.T) {
	h := newTUIHarness(t, false, 100)
	h.working()
	if got := h.m.render(); !strings.Contains(got, "\nNothing needs you.\n") || strings.Contains(got, "ATTENTION") {
		t.Errorf("happy path:\n%s", got)
	}
	h.implemented("")
	h.checked(false)
	h.openGate(boundExhaustionAt(h))
	got := h.m.render()
	if !strings.Contains(got, "\nATTENTION NEEDED: Decide what happens to the Candidate.\n    send back") || strings.Contains(got, "Nothing needs you") {
		t.Errorf("at a Gate:\n%s", got)
	}
}
