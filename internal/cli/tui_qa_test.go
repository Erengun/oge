package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// The QA rhythm in the live view (positioning, ADR-0022): QA reviewing,
// "+N held-out", "QA found N issues", "Repairing automatically…", and the
// attention line staying "Nothing needs you" through the repair.

func newStandardHarness(t *testing.T) *tuiHarness {
	f := &pipeline.Frozen{
		Mode: pipeline.Standard,
		Stages: []pipeline.Stage{
			{Name: "implement", Role: "implementer", Agent: "claude"},
			{Name: "verify", Role: "verifier", Agent: "claude"},
		},
		Checks: []pipeline.CheckCommand{{Run: "go test -json ./..."}},
		Limits: pipeline.Limits{SendBacks: 3},
	}
	h := &tuiHarness{t: t, clock: tuiT0, f: f}
	h.m = newModel(task.Parse("fix Add\n"), f, newStyles(false), func() time.Time { return h.clock })
	h.m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	h.res = &run.Result{ID: "20261005T090000-a1b2c3", Source: workspace.SnapshotInfo{Head: "84ca1bd0e1f2", Branch: "main"}}
	h.att = &run.Attempt{ID: "implement#1", Stage: "implement", Role: "implementer", Agent: "claude"}
	return h
}

func (h *tuiHarness) reviewing(n int) *run.Attempt {
	v := &run.Attempt{ID: "verify#" + string(rune('0'+n)), Stage: "verify", Role: "verifier", Agent: "claude"}
	h.send(run.Event{Kind: run.EvAgent, Attempt: v, Agent: agent.Event{Kind: agent.SessionOpened}})
	// What QA says stays off the screen; what it does shows.
	h.send(run.Event{Kind: run.EvAgent, Attempt: v, Agent: agent.Event{Kind: agent.Claim, Text: "I'll check Add(-2, 1) == -1"}})
	h.send(run.Event{Kind: run.EvAgent, Attempt: v, Agent: agent.Event{Kind: agent.Claim, Tool: "Write", Target: "neg_test.go"}})
	return v
}

func (h *tuiHarness) qaDone(v *run.Attempt, added int) {
	v.Exit = "extended"
	v.QA = &run.QA{Exit: run.ExitExtended, HeldOut: added}
	if added == 0 {
		v.QA.Exit = run.ExitNoAdditions
	}
	h.send(run.Event{Kind: run.EvAttempt, Result: h.res, Attempt: v})
}

func TestTUIQAReviewing(t *testing.T) {
	h := newStandardHarness(t)
	h.working()
	h.implemented("")
	h.at(6500 * time.Millisecond)
	h.reviewing(1)
	h.at(8000 * time.Millisecond)
	got := h.m.render()
	golden(t, "qa-reviewing", got)
	if strings.Contains(got, "Add(-2, 1)") {
		t.Errorf("QA's own words reached the frame:\n%s", got)
	}
}

func TestTUIQAFindsIssuesAndRepairs(t *testing.T) {
	h := newStandardHarness(t)
	h.working()
	h.implemented("")
	v := h.reviewing(1)
	h.at(9000 * time.Millisecond)
	h.qaDone(v, 2)
	h.at(10200 * time.Millisecond)
	e := oracle.Execution{Run: "go test -json ./...", DurationMs: 1180, Why: "exit 1",
		Report: &oracle.Report{Ran: 3, Failed: 2, FailedTests: []string{"TestAddNegatives", "TestAddOverflow"}}}
	h.res.Candidate = h.att.Candidate
	h.send(run.Event{Kind: run.EvCheck, Result: h.res, Check: &oracle.Result{Commands: []oracle.Execution{e}},
		Issues: []string{"TestAddNegatives: Add(-2, 1) = 0, want -1", "TestAddOverflow: Add(max, 1) didn't wrap"}})
	h.send(run.Event{Kind: run.EvSendBack, Result: h.res, SendBack: 1, SendBacks: 3})
	got := h.m.render()
	golden(t, "qa-repairing", got)
	if !strings.Contains(got, gate.Attention(nil)) {
		t.Errorf("the repair asks for attention:\n%s", got)
	}

	// The repaired Candidate goes through QA again, then the Check.
	h.att = &run.Attempt{ID: "implement#2", Stage: "implement", Role: "implementer", Agent: "claude", Cause: "send_back"}
	h.at(14000 * time.Millisecond)
	h.att.Exit, h.att.Candidate, h.att.Changed = "done", "77aa31d9f00d", []string{"add.go"}
	h.send(run.Event{Kind: run.EvAttempt, Result: h.res, Attempt: h.att})
	v2 := h.reviewing(2)
	h.at(16000 * time.Millisecond)
	h.qaDone(v2, 0)
	h.at(17000 * time.Millisecond)
	h.res.Candidate, h.res.Oracle = h.att.Candidate, 1
	h.send(run.Event{Kind: run.EvCheck, Result: h.res, Check: &oracle.Result{Pass: true,
		Commands: []oracle.Execution{{Run: "go test -json ./...", DurationMs: 1000, Pass: true, Report: &oracle.Report{Ran: 3}}}}})
	golden(t, "qa-accepted", h.end(run.Accepted))
}

// A held-out test that stops building is not a QA finding.
func TestIssueLinesNameBuildConflictsNeutrally(t *testing.T) {
	got := strings.Join(issueLines(nil, []string{"fx"}), "\n")
	if got != "held-out test no longer builds against the Candidate (package fx)" {
		t.Errorf("got %q", got)
	}
	if got := strings.Join(issueLines([]string{"TestA: boom"}, []string{"fx"}), "\n"); !strings.HasPrefix(got, "QA found 1 issue\n· TestA: boom\nheld-out test no longer builds") {
		t.Errorf("got %q", got)
	}
}

// A command whose report claims a pass while attestation says otherwise
// never shows a bare "pass" (#46 re-review).
func TestCheckLineShowsTheAttestationOverAForgedReport(t *testing.T) {
	c := &oracle.Result{Why: "Oracle tests not attested passing (1): fx.TestAdd never ran; the report claims pass",
		Missing:  []string{"fx.TestAdd never ran; the report claims pass"},
		Commands: []oracle.Execution{{Run: "go test -json ./...", Pass: true, DurationMs: 900, Report: &oracle.Report{Ran: 1}}}}
	got := checkLines(c)
	if !strings.Contains(got[0], "the report claims a pass; attestation: fx.TestAdd never ran") || strings.HasSuffix(strings.TrimSpace(strings.Split(got[0], " · 9")[0]), "· pass") {
		t.Errorf("got %q", got)
	}
}
