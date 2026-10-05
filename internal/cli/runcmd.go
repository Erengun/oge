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
	"github.com/erengun/oge/internal/gate"
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
	for _, s := range frozen.Stages {
		if env.Agents[s.Agent] == nil {
			fmt.Fprintf(env.Stderr, "oge: running a Task with %s isn't implemented yet; use --dry-run to see what it would do\n", s.Agent)
			return ExitRefused
		}
	}
	if frozen.Mode == pipeline.Blind {
		// TODO(#52): verify = "before".
		fmt.Fprintln(env.Stderr, "oge: Blind mode isn't built yet. Leave out --blind (and verify = \"before\") for Standard mode, or pass --fast")
		return ExitRefused
	}
	// TODO(#46-decision): no report_path key yet: held-out collection is
	// go-test-json on stdout only, until a non-Go held-out runner needs junit.
	for _, c := range frozen.Checks {
		if c.Report != "" && c.Report != oracle.ReportGoTestJSON {
			fmt.Fprintf(env.Stderr, "oge: Check %q declares a %s report, which Öge can't read yet; use go-test-json\n", c.Run, c.Report)
			return ExitRefused
		}
	}
	for _, n := range frozen.Notices {
		fmt.Fprintf(env.Stderr, "oge: %s\n", n)
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
	v := selectView(env, f, t, frozen)
	plainOf(v).applying = f.apply
	withWarning(v, testConfigWarning(root, frozen))
	res, err := v.show(ctx, in, func(ctx context.Context, observe func(run.Event)) (*run.Result, error) {
		p := run.Params{
			Repo: root, Task: t, Frozen: frozen, Config: cfgData, Agents: env.Agents,
			State: state, Version: env.Version, Getenv: env.Getenv, CacheSeedTemplate: env.CacheSeedTemplate, Observe: observe,
		}
		if !f.unattended {
			p.Gates = viewPort{v}
		}
		return run.Start(ctx, p)
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
	if code := afterRun(env, f, v, root, res); code != ExitOK {
		return code
	}
	switch res.Outcome {
	case run.Accepted:
		return ExitOK
	case run.Rejected:
		return ExitRejected
	case run.Infeasible:
		return ExitInfeasible
	case run.Overridden:
		return ExitOverridden
	case run.Cancelled:
		return ExitCancelled
	case run.Parked:
		return ExitParked
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
	// gate is where a Gate waits for the human's decision (ADR-0015), and
	// hostRequest where an agent's Host request (#45) will be answered;
	// they stay separate ports. Neither ever offers a default choice.
	gate(ctx context.Context, r gate.Request) (gate.Decision, error)
	hostRequest(h hostPrompt) (string, error)
}

type startFunc func(ctx context.Context, observe func(run.Event)) (*run.Result, error)

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
	plain := &renderer{w: env.Stdout, verbose: f.verbose || f.veryVerbose, frozen: frozen,
		input: &lines{in: env.Stdin}, edit: env.Edit}
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
	input   *lines             // what the human types at a Gate
	edit    func(string) error // $EDITOR, for a Gate reason
	intr    *interrupts
	warn    string // shown with the summary
	// applying: --apply is about to take an Accepted Candidate into the
	// working tree, so the summary doesn't say nothing was written.
	applying bool
}

func (r *renderer) show(ctx context.Context, in *interrupts, start startFunc) (*run.Result, error) {
	r.intr = in
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

func (r *renderer) hostRequest(hostPrompt) (string, error) { return "", errNotBuilt }

func (r *renderer) p(format string, a ...any) { fmt.Fprintf(r.w, format+"\n", a...) }

func (r *renderer) observe(ev run.Event) {
	switch ev.Kind {
	case run.EvStarted:
		r.p("%s", startedLine(ev.Result, r.frozen))
	case run.EvPreflight:
		r.p("%-10s %s", "preflight", preflightText(r.frozen))
	case run.EvAgent:
		if !r.verbose || (ev.Attempt.Role == "verifier" && !qaStep(ev.Agent)) {
			return
		}
		if step, ok := agentStep(ev.Agent, ev.Attempt.Cause); ok {
			if ev.Attempt.Role == "verifier" {
				step = strings.Replace(step, "Workspace from Snapshot", "Workspace: the Promoted view of the Candidate", 1)
				step = strings.Replace(step, "Workspace from the Candidate sent back", "Workspace: the Promoted view of the Candidate", 1)
			}
			r.p("[%s %s] %s", strings.Replace(ev.Attempt.ID, "#", " #", 1), ev.Attempt.Agent, step)
		}
		if f := ev.Agent.Friction; f != nil && ev.Agent.Kind == agent.TurnSettled {
			r.p("[%s %s] %s", strings.Replace(ev.Attempt.ID, "#", " #", 1), ev.Attempt.Agent, frictionText(*f))
		}
	case run.EvNotice:
		r.p("%-10s %s", "note", clean(ev.Notice))
	case run.EvAttempt:
		if ev.Attempt.Role == "verifier" {
			r.p("%-10s %s", qaLabel, qaText(ev.Attempt))
			if s := qaScopeText(ev.Attempt); s != "" {
				r.p("%-10s %s", "scope", s)
			}
			return
		}
		r.p("%-10s %s", ev.Attempt.Stage, attemptText(ev.Attempt))
		if s := scopeText(ev.Attempt); s != "" {
			r.p("%-10s %s", "scope", s)
		}
	case run.EvSendBack:
		r.p("%-10s %s", "send back", sendBackText(ev, r.frozen))
	case run.EvDecided:
		r.p("%-10s %s", "decision", decidedText(ev))
	case run.EvCheck:
		res := ev.Result
		if r.verbose {
			r.p("[check #%d] Candidate %s · Oracle v%d · fresh Check directory", res.Checks, res.Candidate[:7], res.Oracle)
		}
		for _, l := range checkLines(ev.Check) {
			r.p("%-10s %s", "check", l)
		}
		for _, l := range issueLines(ev.Issues, ev.Conflicts) {
			r.p("%-10s %s", "", l)
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
func agentStep(e agent.Event, cause string) (string, bool) {
	if step, ok := activityStep(e); ok {
		if e.Kind == agent.Claim && e.Tool == "" {
			step = fmt.Sprintf("%q", step) // what the agent says, as said
		}
		if h := e.Host; e.Kind == agent.HostRequest && h.Decision == "deny" {
			if why := denialWhy(h); why != "" {
				step += clean(" (" + why + ")")
			}
		}
		return step, true
	}
	switch e.Kind {
	case agent.SessionOpened:
		line := "started (cause: first) · fresh Session · Workspace from Snapshot"
		if cause == "send_back" {
			line = "started (cause: send_back) · fresh Session · Workspace from the Candidate sent back"
		}
		if s := e.Session; s != nil {
			line += clean(fmt.Sprintf(" · %s · envelope %s · Launch profile %s", s.AgentVersion, s.Envelope, s.Profile))
		}
		return line, true
	case agent.HostRequest:
		if h := e.Host; h != nil {
			return clean(fmt.Sprintf("%s %s %s · %s", h.Decision, h.Tool, h.Target, h.Reason)), true
		}
	case agent.Warning:
		return "warning: " + clean(e.Text), true
	case agent.TurnSettled:
		switch {
		case e.Stop != "":
			return "Infrastructure stop: " + clean(e.Stop), true
		case e.Failure != "":
			return "Attempt failure: " + clean(e.Failure), true
		}
		return "Exit: " + clean(e.Exit) + "   (Claim)", true
	}
	return "", false
}

// activityStep is what the agent is doing, as both views show it: a tool
// it uses, the first line of what it says, or a request Öge denied. It
// never shows a raw frame.
func activityStep(e agent.Event) (string, bool) {
	switch e.Kind {
	case agent.Claim:
		if e.Tool != "" {
			return clean(strings.TrimSpace(e.Tool + " " + e.Target)), true
		}
		first, _, _ := strings.Cut(strings.TrimSpace(e.Text), "\n")
		return clean(first), true
	case agent.HostRequest:
		if h := e.Host; h != nil && h.Decision != "allow" {
			what := "denied"
			if h.Family == agent.Question {
				what = "question cancelled"
			}
			return clean(strings.TrimSpace(fmt.Sprintf("%s: %s %s", what, h.Tool, h.Target))), true
		}
	}
	return "", false
}

func attemptText(a *run.Attempt) string {
	switch {
	case a.Stop != "":
		return fmt.Sprintf("%s · Infrastructure stop", a.Agent)
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
	// A report Candidate code can forge claimed a pass the attestation
	// channel didn't confirm (ADR-0020): the line says so, not "pass".
	forged := ""
	if !c.Pass && len(c.Missing) > 0 && strings.HasPrefix(c.Why, "Oracle tests not attested") {
		first, _, _ := strings.Cut(c.Missing[0], "; the report claims")
		forged = "the report claims a pass; attestation: " + first
	}
	for _, e := range c.Commands {
		l := commandLine(e)
		switch e.Part {
		case oracle.PartVisible:
			l = "visible Oracle · " + l
		case oracle.PartHeldOut:
			l = "held-out · " + l
		}
		if forged != "" && e.Pass {
			l = strings.Replace(l, " · pass · ", " · "+clean(forged)+" · ", 1)
		}
		lines = append(lines, l)
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
	case run.Parked:
		r.parkedSummary(res)
	case run.Accepted, run.Rejected, run.Cancelled, run.Overridden, run.Infeasible:
		head := strings.ToUpper(string(res.Outcome))
		r.p("")
		r.p("%-10s Candidate %s · Oracle v%d · %s", head, res.Candidate[:7], res.Oracle, res.Duration.Round(100*time.Millisecond))
		if res.Outcome == run.Parked {
			for _, w := range res.Why {
				r.p("%-10s the Check passed, but %s", "", clean(w))
			}
		}
		r.friction(res)
		// TODO(#63): the Receipt replaces these lines.
		label := "Not covered"
		if c := res.Check; c != nil {
			// Coverage gaps the Snapshot already had: shown first.
			for _, gap := range []struct {
				what  string
				items []string
			}{{"Oracle tests skipped on the Snapshot and the Candidate", c.Skipped}, {"Oracle test files this machine doesn't build", c.NotBuilt}} {
				if len(gap.items) > 0 {
					r.p("%-10s %s", label, clean(fmt.Sprintf("%s (%d): %s", gap.what, len(gap.items), strings.Join(gap.items, "; "))))
					label = strings.Repeat(" ", len("Not covered"))
				}
			}
		}
		r.p("%-10s %s", label, notCovered(r.frozen, res))
		r.observed(res)
		if r.warn != "" {
			r.p("! %s", r.warn)
		}
		if !r.applying || res.Outcome != run.Accepted {
			r.p("Nothing was written to your repository.")
		}
	case run.InfrastructureStop:
		r.p("")
		if res.Gate != "" {
			r.p("INFRASTRUCTURE STOP   no decision at the %s", gateTitle(gateLabel(res)))
		} else {
			r.p("INFRASTRUCTURE STOP   no Verdict")
		}
		for _, w := range res.Why {
			r.p("  %s", clean(w))
		}
		r.friction(res)
	}
}

// observed prints the tripwires a Run set off, if any: the static ones
// on the Candidate's changes, and attestation lines that named no test.
func (r *renderer) observed(res *run.Result) {
	obs := append([]string(nil), res.Tripwires...)
	if c := res.Check; c != nil && c.Stray > 0 {
		obs = append(obs, fmt.Sprintf("%d stray lines on the attestation channel", c.Stray))
	}
	if len(obs) > 0 {
		r.p("%-10s %s (tripwires: signals, not proof)", "Observed", clean(strings.Join(obs, " · ")))
	}
}

// friction is the Run's policy friction line, for every outcome.
// TODO(#90-decision): shown only when there was friction, so the happy
// path stays quiet; -v always shows each Attempt's.
func (r *renderer) friction(res *run.Result) {
	if f := res.Friction; f != nil && (f.Denied > 0 || f.LostTurns > 0) {
		r.p("%-10s %s", "friction", frictionText(*f))
	}
}

// denialWhy is why a request was denied, in short: its recovery hint,
// or the reason's first sentence (#90).
func denialWhy(h *agent.HostDecision) string {
	if h.Hint != "" {
		return h.Hint
	}
	why := strings.TrimPrefix(h.Reason, "Öge denied this: ")
	if first, _, ok := strings.Cut(why, ". "); ok {
		why = first
	}
	return strings.TrimSuffix(why, ".")
}

// frictionText is an Attempt's policy friction (ADR-0019).
func frictionText(f agent.Friction) string {
	s := fmt.Sprintf("policy friction %s (%d denied)", plural(f.LostTurns, "turn"), f.Denied)
	if f.EnvelopeRefusals > 0 {
		s += fmt.Sprintf(" · %d refused before the envelope passed", f.EnvelopeRefusals)
	}
	return s
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
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
