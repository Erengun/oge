package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
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

	// TODO(#40-decision): Ctrl-C cancels the context, which interrupts the
	// Attempt; ADR-0012's Checkpoint and interrupted status come later.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	r := &renderer{w: env.Stdout, verbose: f.verbose, frozen: frozen}
	res, err := run.Start(ctx, run.Params{
		Repo: root, Task: t, Frozen: frozen, Config: cfgData, Agents: env.Agents,
		State: state, Version: env.Version, Getenv: env.Getenv, CheckGoCache: env.CheckGoCache, Observe: r.observe,
	})
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: internal error: %v\n", err)
		return ExitInternal
	}
	r.summary(res)
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

// renderer prints one line per stage, then the summary; -v adds the event
// stream (ADR-0019).
type renderer struct {
	w       io.Writer
	verbose bool
	frozen  *pipeline.Frozen
}

func (r *renderer) p(format string, a ...any) { fmt.Fprintf(r.w, format+"\n", a...) }

func (r *renderer) observe(ev run.Event) {
	switch ev.Kind {
	case run.EvStarted:
		res := ev.Result
		r.p("Run %s · %s mode · %s", res.ID, r.frozen.Mode, res.Source)
	case run.EvPreflight:
		line := "ok"
		if r.frozen.Setup.Run != "" {
			line = fmt.Sprintf("ok · setup %q passed on the Snapshot", r.frozen.Setup.Run)
		}
		r.p("%-10s %s", "preflight", line)
	case run.EvAgent:
		if !r.verbose {
			return
		}
		tag := fmt.Sprintf("[%s %s]", strings.Replace(ev.Attempt.ID, "#", " #", 1), ev.Attempt.Agent)
		switch e := ev.Agent; e.Kind {
		case agent.SessionOpened:
			r.p("%s started (cause: first) · fresh Session · Workspace from Snapshot", tag)
		case agent.Claim:
			r.p("%s %q", tag, clean(e.Text))
		case agent.Warning:
			r.p("%s warning: %s", tag, clean(e.Text))
		case agent.TurnSettled:
			if e.Failure != "" {
				r.p("%s Attempt failure: %s", tag, clean(e.Failure))
			} else {
				r.p("%s Exit: %s   (Claim)", tag, clean(e.Exit))
			}
		}
	case run.EvAttempt:
		a := ev.Attempt
		switch {
		case a.Failure != "":
			r.p("%-10s %s · Attempt failed: %s", a.Stage, a.Agent, clean(a.Failure))
		case a.Candidate == "":
			r.p("%-10s %s · Exit %s", a.Stage, a.Agent, clean(a.Exit))
		default:
			r.p("%-10s %s · Exit %s · Candidate %s · %s", a.Stage, a.Agent, clean(a.Exit), a.Candidate[:7], files(len(a.Changed)))
		}
	case run.EvCheck:
		res := ev.Result
		if r.verbose {
			r.p("[check #1] Candidate %s · Oracle v%d · fresh Check directory", res.Candidate[:7], res.Oracle)
		}
		c := ev.Check
		if c.Setup != nil && !c.Setup.Pass {
			r.p("%-10s setup %q failed on the Candidate (%s)", "check", c.Setup.Run, c.Setup.Why)
		}
		if c.Why != "" {
			r.p("%-10s fail (%s)", "check", c.Why)
		}
		for _, e := range c.Commands {
			r.p("%-10s %s", "check", commandLine(e))
		}
		if r.verbose {
			verdict := "FAIL"
			if c.Pass {
				verdict = "PASS"
			}
			r.p("[verdict] %s   (Candidate %s, Oracle v%d)", verdict, res.Candidate[:7], res.Oracle)
		}
	}
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
	return strings.Join(parts, " · ")
}

func (r *renderer) summary(res *run.Result) {
	switch res.Outcome {
	case run.Accepted, run.Rejected:
		head := strings.ToUpper(string(res.Outcome))
		r.p("")
		r.p("%-10s Candidate %s · Oracle v%d · %s", head, res.Candidate[:7], res.Oracle, res.Duration.Round(100*time.Millisecond))
		// TODO(#63): the Receipt replaces these lines.
		r.p("%-10s an independent verifier and held-out tests (Fast mode) · Checks run Candidate code uncontained; a hostile Candidate can forge test results", "Not covered")
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

// clean redacts agent-provided text and drops control characters before
// it reaches the terminal.
func clean(s string) string {
	s = string(redact.Redact([]byte(s)))
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
