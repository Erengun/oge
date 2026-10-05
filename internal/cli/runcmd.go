package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/proc"
	"github.com/erengun/oge/internal/redact"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

// startRun refuses what this build can't run yet, then runs the Task and
// maps the outcome to its exit code (ADR-0015).
func startRun(env Env, f runFlags, root string, t task.Task, frozen *pipeline.Frozen, cfgData []byte) int {
	impl := frozen.Stages[0]
	for _, s := range frozen.Stages {
		if s.Role == "implementer" {
			impl = s
		}
	}
	if env.Agents[impl.Agent] == nil {
		fmt.Fprintf(env.Stderr, "oge: running a Task with %s isn't implemented yet; use --dry-run to see what it would do\n", impl.Agent)
		return ExitRefused
	}
	if frozen.Mode != pipeline.Fast {
		fmt.Fprintf(env.Stderr, "oge: %s mode needs a verifier, which isn't built yet. Pass --fast to run the implementer, then Öge's Check\n", frozen.Mode)
		return ExitRefused
	}
	for _, c := range frozen.Checks {
		if c.Report != "" && c.Report != oracle.ReportGoTestJSON {
			fmt.Fprintf(env.Stderr, "oge: Check %q declares a %s report, which Öge can't read yet; use go-test-json\n", c.Run, c.Report)
			return ExitRefused
		}
	}
	if !f.unattended && !env.Interactive() {
		fmt.Fprintln(env.Stderr, "oge: an attended Run needs a terminal; pass --unattended to run without one")
		return ExitRefused
	}

	dir, err := ledger.DefaultStateDir(env.Getenv)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: %v\n", err)
		return ExitRefused
	}
	state, err := ledger.OpenStateRoot(dir, root)
	var refused *ledger.RefusedError
	switch {
	case errors.As(err, &refused):
		fmt.Fprintf(env.Stderr, "oge: %v\n", err)
		return ExitRefused
	case err != nil:
		fmt.Fprintf(env.Stderr, "oge: opening the state root: %v\n", err)
		return ExitInternal
	}

	// TODO(#40-decision): Ctrl-C or SIGTERM cancels the context, which
	// interrupts the Attempt; ADR-0012's Checkpoint and interrupted status
	// come later. A second one stops Öge without waiting.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := newInterrupts(cancel)
	defer in.watch(os.Interrupt, syscall.SIGTERM)()
	res, err := selectView(env, f, t, frozen).show(ctx, in, func(ctx context.Context, observe func(run.Event)) (*run.Result, error) {
		return run.Start(ctx, run.Params{
			Repo: root, Task: t, Frozen: frozen, Config: cfgData, Agents: env.Agents,
			State: state, Version: env.Version, Getenv: env.Getenv, CheckGoCache: env.CheckGoCache, Observe: observe,
		})
	})
	if errors.Is(err, errForced) {
		proc.KillAll()
		fmt.Fprintln(env.Stderr, "oge: stopped without waiting for the Run to end; its agent and Check processes were killed")
		return ExitInterrupted
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: internal error: %v\n", err)
		return ExitInternal
	}
	switch res.Outcome {
	case run.Accepted:
		return ExitOK
	case run.Rejected:
		return ExitRejected
	case run.Refused:
		fmt.Fprintln(env.Stderr, "oge: Preflight refused this Run")
		for _, w := range res.Why {
			fmt.Fprintf(env.Stderr, "  %s\n", w)
		}
		return ExitRefused
	default:
		return ExitInfra
	}
}

// view shows a Run as it happens. Both views read only Öge's normalised
// events and the Result, so the TUI reaches nothing plain mode can't
// (ADR-0022).
type view interface {
	// show runs start with an observe callback for its progress, shows the
	// Result's summary, and returns what start returned. It returns
	// errForced without waiting once in is forced. A view that takes
	// Ctrl-C as a key passes it to in.
	show(ctx context.Context, in *interrupts, start startFunc) (*run.Result, error)
	// gate and hostRequest are where a Gate (#43) and an agent's Host
	// request (#45) will be answered. Neither is built yet; whatever answers
	// them never offers a default choice (ADR-0015).
	gate(g gatePrompt) (string, error)
	hostRequest(h hostPrompt) (string, error)
}

type startFunc func(ctx context.Context, observe func(run.Event)) (*run.Result, error)

// gatePrompt is a Gate waiting for a human decision.
type gatePrompt struct {
	Name    string
	Choices []string // full words; there is no default
}

// hostPrompt is an agent's Host request, such as a permission to run a tool.
type hostPrompt struct {
	Attempt string
	What    string
	Choices []string // full words; there is no default
}

var errNotBuilt = errors.New("not built yet")

// selectView picks the live TUI on an interactive terminal, and plain lines
// otherwise: without a TTY, on a terminal that can't move the cursor
// (TERM empty or dumb), with --plain, or with -v/-vv (ADR-0022).
func selectView(env Env, f runFlags, t task.Task, frozen *pipeline.Frozen) view {
	plain := &renderer{w: env.Stdout, verbose: f.verbose || f.veryVerbose, frozen: frozen}
	// TODO(#79-decision): --unattended on a terminal still draws the live
	// view; unattended means "never prompt", and the view doesn't.
	// TODO(#79-decision): an empty TERM gets plain lines too. Windows
	// consoles set none, but Runs are refused on Windows for now.
	if term := env.Getenv("TERM"); term == "" || term == "dumb" {
		return plain
	}
	if f.plain || plain.verbose || !env.Interactive() {
		return plain
	}
	return newTUI(env, t, frozen, plain)
}

// renderer is the plain view. It prints one line per stage, then the
// summary; -v adds the event stream (ADR-0019).
type renderer struct {
	w       io.Writer
	verbose bool
	frozen  *pipeline.Frozen
}

func (r *renderer) show(ctx context.Context, in *interrupts, start startFunc) (*run.Result, error) {
	e := runAsync(ctx, start, r.observe, nil)
	select {
	case e := <-e:
		if e.panicked != nil {
			panic(fmt.Sprintf("%v\n\n%s", e.panicked, e.stack))
		}
		if e.err == nil {
			r.summary(e.res)
		}
		return e.res, e.err
	case <-in.forced:
		return nil, errForced
	}
}

// ended is how start returned, or the panic it raised.
type ended struct {
	res      *run.Result
	err      error
	panicked any
	stack    []byte
}

// runAsync runs start on its own goroutine, so a view can stop waiting for
// it. after, when set, runs once start has returned or panicked.
func runAsync(ctx context.Context, start startFunc, observe func(run.Event), after func(ended)) <-chan ended {
	done := make(chan ended, 1)
	go func() {
		var e ended
		defer func() {
			if p := recover(); p != nil {
				e.panicked, e.stack = p, debug.Stack()
			}
			if after != nil {
				after(e)
			}
			done <- e
		}()
		e.res, e.err = start(ctx, observe)
	}()
	return done
}

func (r *renderer) gate(gatePrompt) (string, error)        { return "", errNotBuilt }
func (r *renderer) hostRequest(hostPrompt) (string, error) { return "", errNotBuilt }

func (r *renderer) p(format string, a ...any) { fmt.Fprintf(r.w, format+"\n", a...) }

func (r *renderer) observe(ev run.Event) {
	switch ev.Kind {
	case run.EvStarted:
		r.p("%s", startedLine(ev.Result, r.frozen))
	case run.EvPreflight:
		r.p("%-10s %s", "preflight", preflightText(r.frozen))
	case run.EvAgent:
		if !r.verbose {
			return
		}
		if step, ok := agentStep(ev.Agent); ok {
			r.p("[%s %s] %s", strings.Replace(ev.Attempt.ID, "#", " #", 1), ev.Attempt.Agent, step)
		}
	case run.EvAttempt:
		r.p("%-10s %s", ev.Attempt.Stage, attemptText(ev.Attempt))
	case run.EvCheck:
		res := ev.Result
		if r.verbose {
			r.p("[check #1] Candidate %s · Oracle v%d · fresh Check directory", res.Candidate[:7], res.Oracle)
		}
		for _, l := range checkLines(ev.Check) {
			r.p("%-10s %s", "check", l)
		}
		if r.verbose {
			verdict := "FAIL"
			if ev.Check.Pass {
				verdict = "PASS"
			}
			r.p("[verdict] %s   (Candidate %s, Oracle v%d)", verdict, res.Candidate[:7], res.Oracle)
		}
	}
}

// The line texts below are shared by both views.

func startedLine(res *run.Result, f *pipeline.Frozen) string {
	return fmt.Sprintf("Run %s · %s mode · %s", res.ID, f.Mode, res.Source)
}

func preflightText(f *pipeline.Frozen) string {
	if f.Setup.Run != "" {
		return fmt.Sprintf("ok · setup %q passed on the Snapshot", f.Setup.Run)
	}
	return "ok"
}

// agentStep is one agent event as a line of activity; ok is false for
// events that show nothing.
func agentStep(e agent.Event) (string, bool) {
	switch e.Kind {
	case agent.SessionOpened:
		return "started (cause: first) · fresh Session · Workspace from Snapshot", true
	case agent.Claim:
		return fmt.Sprintf("%q", clean(e.Text)), true
	case agent.Warning:
		return "warning: " + clean(e.Text), true
	case agent.TurnSettled:
		if e.Failure != "" {
			return "Attempt failure: " + clean(e.Failure), true
		}
		return "Exit: " + clean(e.Exit) + "   (Claim)", true
	}
	return "", false
}

func attemptText(a *run.Attempt) string {
	switch {
	case a.Failure != "":
		return fmt.Sprintf("%s · Attempt failed: %s", a.Agent, clean(a.Failure))
	case a.Candidate == "":
		return fmt.Sprintf("%s · Exit %s", a.Agent, clean(a.Exit))
	default:
		return fmt.Sprintf("%s · Exit %s · Candidate %s · %s", a.Agent, clean(a.Exit), a.Candidate[:7], files(len(a.Changed)))
	}
}

// checkLines are a Check's lines: a failed setup, each command, and why
// the Check failed when no command shows it.
func checkLines(c *oracle.Result) []string {
	var lines []string
	if c.Setup != nil && !c.Setup.Pass {
		lines = append(lines, clean(fmt.Sprintf("setup %q failed on the Candidate (%s)", c.Setup.Run, c.Setup.Why)))
	}
	for _, e := range c.Commands {
		lines = append(lines, commandLine(e))
	}
	if c.Why != "" {
		lines = append(lines, clean(fmt.Sprintf("fail (%s)", c.Why)))
	}
	return lines
}

func commandLine(e oracle.Execution) string {
	parts := []string{e.Run}
	if rep := e.Report; rep != nil && rep.Error == "" {
		s := fmt.Sprintf("%d ran · %d failed", rep.Ran, rep.Failed)
		if len(rep.FailedTests) > 0 {
			names := rep.FailedTests
			if len(names) > 3 {
				names = append(names[:3:3], "…")
			}
			s += ": " + strings.Join(names, ", ")
		}
		parts = append(parts, s)
	}
	switch {
	case e.Pass:
		parts = append(parts, "pass")
	case e.Why != "":
		parts = append(parts, "fail ("+e.Why+")")
	}
	parts = append(parts, (time.Duration(e.DurationMs) * time.Millisecond).Round(100*time.Millisecond).String())
	// Test names come from the Candidate's own test output.
	return clean(strings.Join(parts, " · "))
}

func (r *renderer) summary(res *run.Result) {
	switch res.Outcome {
	case run.Accepted, run.Rejected:
		head := strings.ToUpper(string(res.Outcome))
		r.p("")
		r.p("%-10s Candidate %s · Oracle v%d · %s", head, res.Candidate[:7], res.Oracle, res.Duration.Round(100*time.Millisecond))
		// TODO(#63): the Receipt replaces these lines.
		r.p("%-10s an independent verifier and held-out tests (Fast mode) · Checks run Candidate code uncontained; a hostile Candidate can forge test results; they run with your privileges", "Not covered")
		r.p("Nothing was written to your repository.")
	case run.InfrastructureStop:
		r.p("")
		r.p("INFRASTRUCTURE STOP   no Verdict")
		for _, w := range res.Why {
			r.p("  %s", clean(w))
		}
	}
}

func files(n int) string {
	if n == 1 {
		return "1 file changed"
	}
	return fmt.Sprintf("%d files changed", n)
}

// clean redacts agent- or Candidate-provided text and drops control
// characters before it reaches the terminal: C0, DEL and the C1 range,
// where U+009B is a CSI and U+009D an OSC to some terminals. Invalid UTF-8
// becomes U+FFFD.
func clean(s string) string {
	s = string(redact.Redact([]byte(s)))
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
