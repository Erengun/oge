package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// The TUI model is driven with the messages a fake-agent Run sends, on a
// fake clock, and each frame is compared with a golden file under
// testdata/tui (go test ./internal/cli -run TUI -update rewrites them).

var tuiT0 = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

type tuiHarness struct {
	t     *testing.T
	m     *model
	clock time.Time
	f     *pipeline.Frozen
	res   *run.Result
	att   *run.Attempt
}

func newTUIHarness(t *testing.T, color bool, width int) *tuiHarness {
	f := &pipeline.Frozen{
		Mode:   pipeline.Fast,
		Stages: []pipeline.Stage{{Name: "implement", Role: "implementer", Agent: "fake"}},
		Checks: []pipeline.CheckCommand{{Run: "go test -json ./..."}},
	}
	h := &tuiHarness{t: t, clock: tuiT0, f: f}
	tk := task.Parse("fix Add\n\nAdd returns 0 for every input.\n")
	h.m = newModel(tk, f, newStyles(color), func() time.Time { return h.clock })
	h.m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	h.res = &run.Result{ID: "20261005T090000-a1b2c3", Source: workspace.SnapshotInfo{Head: "84ca1bd0e1f2", Branch: "main", Untracked: 1}}
	h.att = &run.Attempt{ID: "implement#1", Stage: "implement", Agent: "fake"}
	return h
}

// at moves the clock to d after the start.
func (h *tuiHarness) at(d time.Duration) { h.clock = tuiT0.Add(d) }

func (h *tuiHarness) send(ev run.Event) {
	h.m.Update(batchMsg{progressOf(ev, h.f, h.clock)})
}

func (h *tuiHarness) agent(e agent.Event) {
	h.send(run.Event{Kind: run.EvAgent, Agent: e, Attempt: h.att})
}

// working takes the Run to the middle of the implementer's turn.
func (h *tuiHarness) working() {
	h.at(200 * time.Millisecond)
	h.send(run.Event{Kind: run.EvStarted, Result: h.res})
	h.at(400 * time.Millisecond)
	h.send(run.Event{Kind: run.EvPreflight, Result: h.res})
	h.agent(agent.Event{Kind: agent.SessionOpened})
	h.agent(agent.Event{Kind: agent.TurnAccepted})
	for _, c := range []string{
		"reading add.go", "Add ignores its arguments and returns 0",
		"running go test ./...", "TestAdd fails: Add(2, 3) != 5",
		"fixing Add in add.go\x1b[2J",
	} {
		h.agent(agent.Event{Kind: agent.Claim, Text: c})
	}
	h.at(4200 * time.Millisecond)
	h.m.spin = 3
}

func (h *tuiHarness) implemented(failure string) {
	h.at(6100 * time.Millisecond)
	if failure != "" {
		h.att.Failure = failure
		h.agent(agent.Event{Kind: agent.TurnSettled, Failure: failure})
	} else {
		h.att.Exit, h.att.Candidate, h.att.Changed = "done", "6d1231d9f00d", []string{"add.go"}
		h.agent(agent.Event{Kind: agent.TurnSettled, Exit: "done"})
	}
	h.send(run.Event{Kind: run.EvAttempt, Result: h.res, Attempt: h.att})
}

func (h *tuiHarness) checked(pass bool) {
	h.at(7300 * time.Millisecond)
	e := oracle.Execution{Run: "go test -json ./...", DurationMs: 1180, Pass: pass,
		Report: &oracle.Report{Ran: 1}}
	if !pass {
		e.Why, e.Report.Failed, e.Report.FailedTests = "exit 1", 1, []string{"TestAdd"}
	}
	h.res.Candidate, h.res.Oracle = h.att.Candidate, 0
	h.send(run.Event{Kind: run.EvCheck, Result: h.res, Check: &oracle.Result{Pass: pass, Commands: []oracle.Execution{e}}})
}

// end finishes the Run and returns the screen left in the scrollback: the
// last frame, then the summary.
func (h *tuiHarness) end(o run.Outcome, why ...string) string {
	h.res.Outcome, h.res.Why, h.res.Duration = o, why, 7300*time.Millisecond
	_, cmd := h.m.Update(batchMsg{doneMsg{at: h.clock, res: h.res}})
	if cmd == nil {
		h.t.Fatal("the view didn't quit when the Run ended")
	}
	var b bytes.Buffer
	(&renderer{w: &b, frozen: h.f}).summary(h.res)
	return h.m.render() + b.String()
}

func TestTUIRunning(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	got := h.m.render()
	golden(t, "running", got)
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("a claim's escape sequence reached the frame:\n%q", got)
	}
}

func TestTUIExpandedActivity(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.m.Update(ctrlKey('o'))
	golden(t, "running-expanded", h.m.render())
	h.m.Update(ctrlKey('o'))
	if got, want := h.m.render(), readGolden(t, "running"); got != want {
		t.Errorf("ctrl+o twice didn't collapse the activity:\n%s", got)
	}
}

func TestTUIChecking(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.implemented("")
	h.at(6900 * time.Millisecond)
	golden(t, "checking", h.m.render())
}

func TestTUIAccepted(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.implemented("")
	h.checked(true)
	golden(t, "accepted", h.end(run.Accepted))
}

func TestTUIRejected(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.implemented("")
	h.checked(false)
	golden(t, "rejected", h.end(run.Rejected))
}

func TestTUIInfrastructureStop(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.implemented("interrupted")
	golden(t, "infrastructure-stop", h.end(run.InfrastructureStop, "the implementer Attempt failed: interrupted"))
}

func TestTUINarrowTerminalTruncates(t *testing.T) {
	h := newTUIHarness(t, false, 40)
	h.working()
	got := h.m.render()
	golden(t, "narrow", got)
	for _, l := range strings.Split(got, "\n") {
		if w := len([]rune(l)); w > 40 {
			t.Errorf("line is %d wide: %q", w, l)
		}
	}
}

func TestTUIColor(t *testing.T) {
	h := newTUIHarness(t, true, 80)
	h.working()
	got := h.m.render()
	if !strings.ContainsRune(got, 0x1b) {
		t.Fatal("no colour in a colour frame")
	}
	golden(t, "running-color", got)
}

func TestTUINoColor(t *testing.T) {
	env := map[string]string{"NO_COLOR": "1", "TERM": "xterm-256color"}
	if colorAllowed(func(k string) string { return env[k] }) {
		t.Error("NO_COLOR=1 allows colour")
	}
	h := newTUIHarness(t, false, 80)
	h.working()
	h.implemented("")
	h.checked(true)
	if got := h.end(run.Accepted); strings.ContainsRune(got, 0x1b) {
		t.Errorf("a NO_COLOR screen holds escape sequences:\n%q", got)
	}
}

func TestTUICtrlCCancelsTheRunAndWaitsForItsEnd(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	cancelled := 0
	in := newInterrupts(func() { cancelled++ })
	h.m.interrupt = in.interrupt
	h.working()
	if _, cmd := h.m.Update(ctrlKey('c')); cmd != nil {
		t.Fatal("ctrl+c quit the view before the Run ended")
	}
	if cancelled != 1 {
		t.Errorf("cancel called %d times", cancelled)
	}
	if got := h.m.render(); !strings.Contains(got, "cancelling… ctrl+c again to stop now") {
		t.Errorf("no cancelling hint:\n%s", got)
	}
	h.m.Update(ctrlKey('c'))
	select {
	case <-in.forced:
	default:
		t.Error("a second ctrl+c didn't force the stop")
	}
}

func TestTUIQueueNeverBlocksTheRun(t *testing.T) {
	q := &queue{wake: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		for range 10000 {
			q.push(progressMsg{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pushing progress blocked with nobody drawing")
	}
	if got := len(q.wait().(batchMsg)); got != 10000 {
		t.Errorf("drained %d messages", got)
	}
}

func TestSelectView(t *testing.T) {
	f := &pipeline.Frozen{Mode: pipeline.Fast}
	for _, c := range []struct {
		name, term string
		tty        bool
		fl         runFlags
		tui        bool
	}{
		{"terminal", "xterm-256color", true, runFlags{}, true},
		{"no terminal", "xterm-256color", false, runFlags{}, false},
		{"--plain", "xterm-256color", true, runFlags{plain: true}, false},
		{"-v", "xterm-256color", true, runFlags{verbose: true}, false},
		{"-vv", "xterm-256color", true, runFlags{veryVerbose: true}, false},
		{"TERM=dumb", "dumb", true, runFlags{}, false},
		{"TERM empty", "", true, runFlags{}, false},
	} {
		getenv := func(k string) string {
			if k == "TERM" {
				return c.term
			}
			return ""
		}
		env := Env{Stdout: &bytes.Buffer{}, Interactive: func() bool { return c.tty }, Getenv: getenv}
		_, isTUI := selectView(env, c.fl, task.Task{}, f).(*tui)
		if isTUI != c.tui {
			t.Errorf("%s: TUI = %v", c.name, isTUI)
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "tui", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want := readGolden(t, name); got != want {
		t.Errorf("frame differs from %s (run with -update to accept)\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

func readGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "tui", name+".golden"))
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	return string(b)
}

// --plain on a terminal prints exactly what a Run without one prints.
func TestPlainFlagOnATerminalPrintsPlainLines(t *testing.T) {
	f := newRunFixture(t)
	_, want, _ := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	f.interactive = true
	code, got, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--plain")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, got, errOut)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("--plain wrote escape sequences:\n%q", got)
	}
	norm := regexp.MustCompile(`\d{8}T\d{6}-[0-9a-f]{6}|[0-9a-f]{7}\b|\d+(\.\d+)?m?s\b`)
	if g, w := norm.ReplaceAllString(got, "X"), norm.ReplaceAllString(want, "X"); g != w {
		t.Errorf("--plain on a terminal differs from a Run without one\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func ctrlKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

func TestElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0.0s", 449 * time.Millisecond: "0.4s", 12340 * time.Millisecond: "12.3s",
		59960 * time.Millisecond: "1m00s", 65 * time.Second: "1m05s", 61 * time.Minute: "61m00s", -time.Second: "0.0s",
	} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

// The default view's activity is what the agent says it's doing. Session,
// cause and Exit bookkeeping is -v vocabulary (ADR-0019 #8).
func TestTUIActivityShowsOnlyClaims(t *testing.T) {
	h := newTUIHarness(t, false, 120)
	h.working()
	h.agent(agent.Event{Kind: agent.Warning, Text: "slow disk"})
	h.agent(agent.Event{Kind: agent.TurnSettled, Exit: "done"})
	h.m.expanded = true
	got := h.m.render()
	for _, leak := range []string{"started (cause", "fresh Session", "Workspace from Snapshot", "Exit: done", "(Claim)", "warning:"} {
		if strings.Contains(got, leak) {
			t.Errorf("the activity shows %q:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, "reading add.go") {
		t.Errorf("the activity lacks the Claims:\n%s", got)
	}
}

// A stage's line lands on the stage the event names, whatever order the
// stages are listed in (Blind mode runs the verifier first).
func TestTUIMatchesStagesByName(t *testing.T) {
	f := &pipeline.Frozen{
		Mode: pipeline.Blind,
		Stages: []pipeline.Stage{
			{Name: "implement", Role: "implementer", Agent: "claude"},
			{Name: "verify", Role: "verifier", Agent: "codex"},
		},
		Checks: []pipeline.CheckCommand{{Run: "go test ./..."}},
	}
	now := tuiT0
	m := newModel(task.Parse("fix Add\n"), f, newStyles(false), func() time.Time { return now })
	m.Update(batchMsg{progressOf(run.Event{Kind: run.EvPreflight}, f, now)})
	v := &run.Attempt{ID: "verify#1", Stage: "verify", Agent: "codex", Exit: "done", Candidate: "abcdef0123", Changed: []string{"x_test.go"}}
	m.Update(batchMsg{progressOf(run.Event{Kind: run.EvAttempt, Attempt: v}, f, now)})
	got := m.render()
	for _, want := range []string{
		"✓ verify     codex · Exit done · Candidate abcdef0 · 1 file changed",
		"⠋ implement  claude",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frame lacks %q:\n%s", want, got)
		}
	}

	// A stage the graph didn't list shows up before the Check.
	r := &run.Attempt{ID: "review#1", Stage: "review", Agent: "claude", Failure: "timeout"}
	m.Update(batchMsg{progressOf(run.Event{Kind: run.EvAttempt, Attempt: r}, f, now)})
	got = m.render()
	if i, j := strings.Index(got, "✗ review"), strings.Index(got, "· check"); i < 0 || j < i {
		t.Errorf("the unlisted stage isn't before the Check:\n%s", got)
	}
}
