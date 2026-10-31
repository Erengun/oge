package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/herdr"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"golang.org/x/term"
)

// tui is the live view of a Run on an interactive terminal (ADR-0022). It
// draws inline, never on the alternate screen, so its last frame and the
// summary stay in the scrollback.
type tui struct {
	in     io.Reader
	out    io.Writer
	stderr io.Writer
	plain  *renderer // prints the summary once the live view has gone
	m      *model
	vim    bool // the composer's vim mode (user config, #156)
	// vimEscape is the INSERT sequence that acts as Esc (user config, #158).
	vimEscape string
	// gone is closed once the live view has stopped; a Gate opened after
	// that is asked in plain lines.
	gone chan struct{}
	// started: a Run was started (the home starts it only on Enter).
	started bool
	// noFallback: a Gate opened after the view has gone is refused rather
	// than asked in plain lines, whose stdin reader would outlive the Run and
	// take the next composer's keys (the home).
	noFallback bool
	// kill stops the running program at once (a forced stop).
	kill func()
	// programStarted, when set, is told each time a program starts (tests).
	programStarted func()
	// programOptions come after the program's own options (tests).
	programOptions []tea.ProgramOption
	// notify is how this view tells the human, nil when it stays silent.
	notify *notifier
	// herdr is released when the program exits (nil: not under herdr).
	herdr herdr.Reporter
}

const (
	tuiFPS       = 15
	tickInterval = 100 * time.Millisecond
	// recentSteps is how much agent activity shows collapsed.
	// TODO(#79-decision): four lines, like a compact tool-output preview.
	recentSteps = 4
	// keptSteps bounds the activity a Run keeps for the expanded view.
	keptSteps = 500
)

func newTUI(env Env, t task.Task, f *pipeline.Frozen, plain *renderer) *tui {
	return &tui{
		in: env.Stdin, out: env.Stdout, stderr: env.Stderr, plain: plain,
		m:              newModel(t, f, newStyles(colorAllowed(env.Getenv)), time.Now),
		gone:           make(chan struct{}),
		programStarted: env.programStarted,
		programOptions: env.programOptions,
	}
}

// colorAllowed honours NO_COLOR (https://no-color.org).
func colorAllowed(getenv func(string) string) bool {
	return getenv("NO_COLOR") == ""
}

func (u *tui) hostRequest(hostPrompt) (string, error) { return "", errNotBuilt }

// show runs the Run on its own goroutine and the live view on this one.
// The Run reports progress into a queue, so a slow terminal never holds it
// up. Whatever ends the Run, the view has restored the terminal before
// show returns, and a panic in the Run is re-raised only after that. A
// forced stop kills the view (which restores the terminal too) and returns
// without waiting for the Run.
func (u *tui) show(ctx context.Context, in *interrupts, start startFunc) (*run.Result, error) {
	done := u.launch(ctx, in, start)
	err := u.program(in, u.m)
	return u.finish(in, err, done)
}

// launch starts the Run on its own goroutine, reporting into u.m's queue.
func (u *tui) launch(ctx context.Context, in *interrupts, start startFunc) <-chan ended {
	u.started = true
	q := &queue{wake: make(chan struct{}, 1)}
	u.m.queue, u.m.interrupt = q, in.interrupt
	u.plain.intr = in
	frozen := u.m.frozen
	return runAsync(ctx, start, func(ev run.Event) { q.push(progressOf(ev, frozen, time.Now())) }, func(e ended) {
		q.push(doneMsg{at: time.Now(), res: e.res, failed: e.err != nil || e.panicked != nil})
	})
}

// program runs root as the inline Bubble Tea program until it quits, and
// closes u.gone once the view has stopped.
func (u *tui) program(in *interrupts, root tea.Model) error {
	if u.gone == nil {
		u.gone = make(chan struct{})
	}
	// Bubble Tea's own signal handler would end the view before the
	// summary; startRun's interrupts take SIGINT and SIGTERM instead.
	out, opts := u.out, []tea.ProgramOption{tea.WithFPS(tuiFPS), tea.WithoutSignalHandler()}
	if !sized(out) {
		// A terminal that reports no size would get an empty frame, so it
		// is drawn as a plain writer at 80x24 instead.
		out = struct{ io.Writer }{out}
		opts = append(opts, tea.WithWindowSize(80, 24))
	}
	opts = append(opts, tea.WithInput(u.in), tea.WithOutput(out))
	p := tea.NewProgram(root, append(opts, u.programOptions...)...)
	u.kill = p.Kill
	if u.programStarted != nil {
		u.programStarted()
	}
	viewDone := make(chan struct{})
	defer close(viewDone)
	go func() {
		select {
		case <-in.forced:
			p.Kill()
		case <-viewDone:
		}
	}()
	_, err := p.Run()
	u.releaseHerdr()
	close(u.gone)
	return err
}

// finish waits for the Run the view showed and prints its summary.
func (u *tui) finish(in *interrupts, err error, done <-chan ended) (*run.Result, error) {
	select {
	case <-in.forced:
		return nil, errForced
	default:
	}
	if err != nil {
		fmt.Fprintf(u.stderr, "oge: the live view stopped (%v); the Run goes on, and its summary follows\n", err)
	}
	var e ended
	select {
	case e = <-done:
	case <-in.forced:
		return nil, errForced
	}
	if e.panicked != nil {
		panic(fmt.Sprintf("%v\n\n%s", e.panicked, e.stack))
	}
	if e.err == nil {
		u.plain.endScreen(e.res)
	}
	return e.res, e.err
}

// sized reports whether w is a terminal that knows its size.
func sized(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	cols, rows, err := term.GetSize(int(f.Fd()))
	return err == nil && cols > 0 && rows > 0
}

// queue carries progress from the Run to the view without blocking the
// Run. The view drains everything queued at once.
type queue struct {
	mu   sync.Mutex
	msgs []tea.Msg
	wake chan struct{}
}

func (q *queue) push(m tea.Msg) {
	q.mu.Lock()
	q.msgs = append(q.msgs, m)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) wait() tea.Msg {
	<-q.wake
	q.mu.Lock()
	defer q.mu.Unlock()
	msgs := q.msgs
	q.msgs = nil
	return batchMsg(msgs)
}

type (
	batchMsg []tea.Msg
	tickMsg  time.Time
	// progressMsg is a run.Event already turned into text, on the Run's
	// goroutine: the view never reads the Run's live Result.
	progressMsg struct {
		at    time.Time
		kind  run.EventKind
		head  string // EvStarted: the Run line
		stage string // the stage the event belongs to
		// unchanged: an implementer Attempt that ended done without
		// changing anything; the Run ends Replied, so no stage follows.
		unchanged bool
		text      string   // a finished stage's line, as plain mode prints it
		sub       []string // further lines under it
		fail      bool
		// timed: the lines carry their own durations (Check commands).
		timed bool
		step  string // EvAgent: one line of activity
		// reply is the implementer's own words (a Claim without a tool),
		// cleaned, whole: transcript text, not activity (ADR-0022).
		reply string
		// todoSet: the agent updated its plan (a todo update); todos is
		// the whole plan, cleaned for the screen.
		todoSet bool
		todos   []todoItem
		next    string // EvResolved: where the walk goes
		// cost and tokens are what the agent reported for its session so
		// far (EvAgent usage); nil when it reported none.
		cost   *float64
		tokens *int
		// ctx is how full the agent's context was (EvAgent usage): an
		// estimate, display only.
		ctx *agent.ContextUse
	}
	doneMsg struct {
		at     time.Time
		res    *run.Result
		failed bool // Öge itself failed; the Result is nil
	}
)

func progressOf(ev run.Event, f *pipeline.Frozen, at time.Time) progressMsg {
	m := progressMsg{at: at, kind: ev.Kind}
	switch ev.Kind {
	case run.EvPreflight:
		m.stage = "preflight"
	case run.EvAgent, run.EvAttempt, run.EvNotice:
		m.stage = ev.Attempt.Stage
	case run.EvCheck:
		m.stage = "check"
	case run.EvSendBack:
		m.stage, m.text = "send back", sendBackText(ev, f)
	case run.EvDecided:
		m.stage, m.text = "decision", decidedText(ev)
	case run.EvResolved:
		m.stage, m.text, m.next = "resolved", resolvedText(ev), ev.Next
	}
	switch ev.Kind {
	case run.EvStarted:
		m.head = startedLine(ev.Result, f)
	case run.EvPreflight:
		m.text = preflightText(f)
		if ev.Why != "" {
			m.text, m.fail = fmt.Sprintf("setup %q fails on the Snapshot (%s)", f.Setup.Run, ev.Why), true
		}
	case run.EvAgent:
		// Only what the agent says it's doing; the Session and Exit
		// bookkeeping stays in -v (ADR-0019).
		if ev.Agent.Tool == "TodoWrite" {
			// The plan is display only: the live list and one activity
			// line, never QA's.
			if ev.Attempt.Role != "verifier" {
				m.todoSet, m.todos = true, todoItems(ev.Agent.Todos)
				m.step = todoSummary(m.todos)
			}
		} else if ev.Attempt.Role != "verifier" || qaStep(ev.Agent) {
			m.step, _ = activityStep(ev.Agent)
		}
		if ev.Attempt.Role == "implementer" && ev.Agent.Kind == agent.Claim && ev.Agent.Tool == "" {
			m.reply = cleanLines(ev.Agent.Text)
		}
		if ev.Agent.Kind == agent.Usage {
			m.cost, m.tokens, m.ctx = ev.Agent.CostUSD, ev.Agent.Tokens, ev.Agent.Context
		}
	case run.EvNotice:
		m.step = "note: " + clean(ev.Notice)
	case run.EvAttempt:
		a := ev.Attempt
		m.unchanged = a.Role == "implementer" && a.Cause != "send_back" && a.Exit == "done" && a.Failure == "" && a.Stop == "" && len(a.Changed) == 0
		m.text = attemptText(ev.Attempt)
		if s := scopeText(ev.Attempt); s != "" {
			m.sub = []string{"scope " + s}
		}
		if ev.Attempt.Role == "verifier" {
			m.text, m.sub = qaText(ev.Attempt), nil
			if s := qaScopeText(ev.Attempt); s != "" {
				m.sub = []string{"scope " + s}
			}
		}
		m.fail = ev.Attempt.Failure != "" || ev.Attempt.Stop != "" || (ev.Attempt.Role != "verifier" && ev.Attempt.Exit != "done")
	case run.EvCheck:
		lines := checkLines(ev.Check)
		lines = append(lines, issueLines(ev.Issues, ev.Conflicts)...)
		if len(lines) > 0 {
			m.text, m.sub = lines[0], lines[1:]
		}
		m.fail, m.timed = !ev.Check.Pass, true
	}
	return m
}

type stageState int

const (
	pending stageState = iota
	running
	passed
	failed
)

type stage struct {
	name       string
	text       string // shown while pending or running, until the line arrives
	sub        []string
	state      stageState
	timed      bool
	mark       string   // a note's mark, in place of ✓
	bg         bool     // keeps running when a later stage begins
	reply      []string // the implementer's replies, printed above its line
	start, end time.Time
}

// model is the TUI's state. It changes only in Update and is drawn only
// by View, so tests drive it with messages and compare frames.
type model struct {
	kb     *keyMap // this session's keys (nil: the defaults)
	title  string
	head   string
	frozen *pipeline.Frozen
	stages []stage
	cur    int
	steps  []string
	marks  []int      // steps indices where a later Attempt begins
	find   *finder    // the open search in the expanded view (#167)
	more   int        // steps dropped beyond keptSteps
	todos  []todoItem // the agent's latest plan, display only

	expanded   bool
	off        int // expanded: steps scrolled back from the newest
	used       int // lines above the activity in the last frame
	started    time.Time
	ctx        *agent.ContextUse   // the latest context use an agent reported
	usage      map[string]reported // what each stage's session reported, latest
	cancelling bool
	still      bool // no spinner ticks: tests draw only on events
	finished   bool
	replied    bool // the Run ended Replied: what never ran isn't shown

	width, height int
	spin          int
	now           func() time.Time
	st            styles
	queue         *queue
	interrupt     func()
	gate          *gateState     // an open Gate, waiting for the human
	notify        *notifier      // nil: no bell or desktop notification (#170)
	herdr         herdr.Reporter // the pane's state for herdr; a no-op outside it
	alerts        []string       // queued, to leave through flushAlerts

	// scroll: finished items are printed above the program (take) and leave
	// the frame, which keeps only what still changes (the home, #190).
	scroll  bool
	echoed  bool // the Task's echo is printed
	headed  bool // the Run line is printed
	began   bool // the Run line is the Run's own, not the stand-in
	printed int  // the stages printed, in order; they stay in stages
}

func newModel(t task.Task, f *pipeline.Frozen, st styles, now func() time.Time) *model {
	m := &model{title: clean(t.Title), frozen: f, st: st, now: now, started: now(), herdr: sessionHerdr}
	m.head = fmt.Sprintf("%s mode", f.Mode)
	pre := stage{name: "preflight"}
	if f.Setup.Run != "" {
		// Setup runs on the Snapshot while the implementer works (#195):
		// preflight stays running behind it until the Run joins them.
		pre.text, pre.bg = fmt.Sprintf("setup %q running on the Snapshot", f.Setup.Run), true
	}
	m.stages = append(m.stages, pre)
	for _, s := range f.Stages {
		if f.Mode == pipeline.Fast && s.Role != "implementer" {
			continue
		}
		m.stages = append(m.stages, m.stageFor(s))
	}
	m.stages = append(m.stages, stage{name: "check", text: m.checkText()})
	m.stages[0].state, m.stages[0].start = running, now()
	return m
}

// stageFor is a frozen Stage as the view lists it before it runs.
func (m *model) stageFor(s pipeline.Stage) stage {
	if s.Role == "verifier" {
		return stage{name: s.Name, text: reviewingText(s.Agent)}
	}
	return stage{name: s.Name, text: s.Agent}
}

// checkText is the Check's line before it runs: its commands.
func (m *model) checkText() string {
	var checks []string
	for _, c := range m.frozen.Checks {
		checks = append(checks, c.Run)
	}
	return strings.Join(checks, " · ")
}

func (m *model) Init() tea.Cmd {
	if m.still {
		return m.waitCmd()
	}
	if m.scroll { // the home asked already
		return tea.Batch(m.waitCmd(), tick())
	}
	return tea.Batch(m.waitCmd(), tick(), askBackground(m.st))
}

// withAlerts puts the queued alerts before next. The home (scroll) sends
// them itself, in order with what it prints.
func (m *model) withAlerts(next tea.Cmd) tea.Cmd {
	if m.scroll {
		return next
	}
	if a := m.flushAlerts(); a != nil {
		return tea.Sequence(a, next)
	}
	return next
}

func (m *model) waitCmd() tea.Cmd {
	if m.queue == nil {
		return nil
	}
	return m.queue.wait
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case batchMsg:
		for _, one := range msg {
			if m.apply(one) {
				return m, m.withAlerts(tea.Quit)
			}
		}
		return m, m.withAlerts(m.waitCmd())
	case tickMsg:
		if m.finished {
			return m, nil
		}
		if m.gate == nil {
			m.spin++
		}
		return m, tick()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.st.setDark(msg.IsDark())
	case tea.KeyPressMsg:
		if m.gate != nil && m.gateKey(msg) {
			return m, nil
		}
		if m.expanded && m.findKey(msg) {
			return m, nil
		}
		switch m.kb.resolve(ctxRun, msg.String()) {
		case actExpand:
			m.expanded, m.off, m.find = !m.expanded, 0, nil
		case "esc":
			if m.expanded && m.find != nil {
				m.find = nil // search closes first, then the view
				break
			}
			m.expanded, m.off = false, 0
		case "/", "n", "N", "{", "}":
			if m.expanded {
				m.searchKey(msg.String())
			}
		case "up", "down", "pgup", "pgdown":
			if m.expanded {
				m.scrollBy(msg.String())
			}
		case "ctrl+c":
			// The first Ctrl-C cancels the Run, which then ends and shows
			// its summary; the view quits only after that. A second one
			// stops Öge without waiting. An open Gate closes with it.
			m.cancelling, m.gate = true, nil
			if m.interrupt != nil {
				m.interrupt()
			}
		}
	}
	return m, nil
}

// apply takes one message from the Run; it reports whether the Run ended.
func (m *model) apply(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case progressMsg:
		m.reportHerdr(herdr.Working, "")
		switch msg.kind {
		case run.EvStarted:
			m.head, m.began = msg.head, true
		case run.EvPreflight, run.EvAttempt, run.EvCheck:
			m.finish(msg)
		case run.EvDecided:
			m.gate = nil // recorded: now it is taken
			m.note(msg)
		case run.EvSendBack:
			m.note(msg)
			m.again()
		case run.EvResolved:
			m.note(msg)
			m.resolved(msg.next)
		case run.EvAgent, run.EvNotice:
			if msg.stage != "" {
				if i := m.index(msg.stage); m.stages[i].state == pending {
					m.begin(i, msg.at)
				}
			}
			if msg.cost != nil || msg.tokens != nil {
				m.record(msg.stage, msg.cost, msg.tokens)
			}
			if msg.ctx != nil {
				m.ctx = msg.ctx
			}
			if msg.todoSet {
				m.todos = msg.todos
			}
			if msg.reply != "" && msg.stage != "" {
				i := m.index(msg.stage)
				m.stages[i].reply = append(m.stages[i].reply, msg.reply)
			}
			if msg.step != "" {
				m.steps = append(m.steps, msg.step)
				if m.off > 0 {
					m.off++ // a scrolled view stays on the same steps
				}
				if len(m.steps) > keptSteps {
					m.steps = m.steps[1:]
					m.more++
					for i := range m.marks {
						m.marks[i]--
					}
					for len(m.marks) > 0 && m.marks[0] <= 0 {
						m.marks = m.marks[1:]
					}
				}
			}
		}
	case gateMsg:
		m.gate = &gateState{req: msg.req, reply: msg.reply}
		m.needsYou()
	case doneMsg:
		m.gate = nil
		m.runEnded(msg.res, msg.failed)
		m.replied = msg.res != nil && msg.res.Outcome == run.Replied
		if m.replied && m.cur < len(m.stages) && m.stages[m.cur].state == running {
			m.stages[m.cur].state = pending // never ran: hidden, never ✗
		}
		if m.cur < len(m.stages) && m.stages[m.cur].state == running {
			s := &m.stages[m.cur]
			s.state, s.end = failed, msg.at
			if msg.res != nil && msg.res.Outcome == run.Refused {
				s.text = "refused"
				if m.stages[0].state == failed && m.cur != 0 {
					// Setup failed on the Snapshot and cancelled the
					// Attempt: the agent is not to blame.
					s.state, s.mark, s.text = passed, "·", "cancelled · setup failed on the Snapshot"
				}
			}
		}
		m.finished = true
		return true
	}
	return false
}

// index is the position of the latest stage named name. A stage the
// frozen graph didn't list goes in before the Check.
func (m *model) index(name string) int {
	for i := len(m.stages) - 1; i >= 0; i-- {
		if m.stages[i].name == name {
			return i
		}
	}
	i := max(len(m.stages)-1, 0, m.printed)
	m.stages = append(m.stages[:i], append([]stage{{name: name}}, m.stages[i:]...)...)
	if m.cur >= i {
		m.cur++
	}
	return i
}

// begin makes the stage at i the running one; one that was running
// without news goes back to pending.
func (m *model) begin(i int, at time.Time) {
	if i == m.cur && m.stages[i].state == running {
		return
	}
	if m.cur < len(m.stages) && m.stages[m.cur].state == running && !m.stages[m.cur].bg {
		m.stages[m.cur].state = pending
	}
	m.stages[i].state, m.stages[i].start = running, at
	m.cur = i
	m.steps, m.marks, m.more, m.off = nil, nil, 0, 0
}

// finish ends the stage the message names with its line. Unless it failed,
// the first stage still pending starts.
func (m *model) finish(msg progressMsg) {
	i := m.index(msg.stage)
	s := &m.stages[i]
	if s.state == running && s.bg && i < m.cur && m.cur < len(m.stages) && m.stages[m.cur].state == running {
		// A background stage ending (setup, behind the implementer): the
		// stage in front of it keeps its clock and its activity.
		s.text, s.sub, s.timed, s.end = msg.text, msg.sub, msg.timed, msg.at
		s.state = passed
		if msg.fail {
			s.state = failed
		}
		return
	}
	if s.state != running {
		// Its start went unseen; it began when the running one did.
		s.start = m.stages[min(m.cur, len(m.stages)-1)].start
		if m.cur < len(m.stages) && m.stages[m.cur].state == running && m.cur != i {
			m.stages[m.cur].state = pending
		}
	}
	s.text, s.sub, s.timed, s.end = msg.text, msg.sub, msg.timed, msg.at
	s.state = passed
	m.cur = i
	if msg.fail {
		s.state = failed
		if msg.kind == run.EvAttempt && len(m.steps) > 0 {
			m.marks = append(m.marks, len(m.steps)) // the next Attempt starts here
		}
		return
	}
	m.steps, m.marks, m.more = nil, nil, 0
	m.cur = len(m.stages)
	if msg.unchanged && m.scroll {
		// The home's turn ends Replied (#254): no Check or QA starts, so
		// none spins. A one-shot Run goes on to its Check as before.
		return
	}
	for j := range m.stages {
		if m.stages[j].state == pending {
			m.stages[j].state, m.stages[j].start = running, msg.at
			m.cur = j
			break
		}
	}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *model) View() tea.View { return tea.NewView(m.render()) }

// render is the screen as text.
func (m *model) render() string {
	st := m.st
	var lines []string
	add := func(s string) { lines = append(lines, s) }

	if !m.scroll {
		add(st.bold(m.title))
		add(st.dim(m.head))
		add("")
	}
	for i, s := range m.stages {
		if m.scroll && i < m.printed {
			continue
		}
		if s.state != running {
			lines = append(lines, m.stageLines(i)...)
			continue
		}
		name := fmt.Sprintf("%-10s", stageLabel(s.name, m.frozen))
		line := st.accent(spinnerFrames[m.spin%len(spinnerFrames)]) + " " + name + " "
		if s.text != "" {
			line += s.text + " · "
		}
		add(line + st.dim(elapsed(m.now().Sub(s.start))))
		if i == m.cur {
			lines = append(lines, m.todoLines()...)
			lines = append(lines, m.activity(len(lines))...)
		}
	}
	lines = append(lines, m.gateLines()...)
	if !m.finished {
		add("")
		add(m.statusLine())
		hint := "ctrl+c to cancel"
		if m.expanded {
			hint = m.kb.expandLabel() + " to collapse · " + hint
		}
		if m.cancelling {
			hint = "cancelling… ctrl+c again to stop now"
		}
		add(st.dim(hint))
	}

	width := m.width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(ansi.Truncate(l, width, "…"))
		b.WriteByte('\n')
	}
	// The renderer erases the line the cursor ends on when it stops, so the
	// frame ends with an empty one.
	return b.String()
}

// stageLines are a stage's lines once it isn't running: pending (not run,
// once the Run has finished) or ended.
func (m *model) stageLines(i int) []string {
	st, s := m.st, m.stages[i]
	name := fmt.Sprintf("%-10s", stageLabel(s.name, m.frozen))
	if s.state == pending {
		if m.replied {
			return nil // a Replied Run's Check and QA never ran
		}
		text := s.text
		if m.finished {
			text = "not run"
		}
		return []string{st.dim("· " + strings.TrimRight(name+" "+text, " "))}
	}
	mark := st.ok("✓")
	switch {
	case s.state == failed:
		mark = st.bad("✗")
	case s.mark != "":
		mark = st.accent(s.mark)
	}
	line := mark + " " + name + " " + s.text
	if !s.timed {
		line += st.dim(" · " + elapsed(s.end.Sub(s.start)))
	}
	lines := []string{line}
	for _, l := range s.sub {
		lines = append(lines, "  "+fmt.Sprintf("%-10s", "")+" "+l)
	}
	return lines
}

// replyLines are what the implementer said in stage i, printed above the
// stage's line: the words come first, then the line that sums the stage
// up, as a chat reads. Only the implementer's text gets here (QA's never
// does), and it is kept whole.
func (m *model) replyLines(i int) []string {
	var out []string
	for j, r := range m.stages[i].reply {
		if j > 0 {
			out = append(out, "") // a blank line between messages
		}
		out = append(out, strings.Split(r, "\n")...)
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	return out
}

// take returns what has finished since it was last called, in order, for
// printing above the program: the Task's echo, the Run line, then each
// ended stage (a Gate's outcome is one). A stage is taken only once all
// before it are, so the transcript keeps the stages' order; once the Run
// has finished, what never ran is taken too. Without scroll it is nothing.
func (m *model) take() []string {
	if !m.scroll {
		return nil
	}
	var out []string
	if !m.echoed {
		m.echoed = true
		out = append(out, m.st.bold("> "+m.title))
	}
	if !m.headed {
		if !m.began && !m.finished {
			return out
		}
		m.headed = true
		out = append(out, m.st.dim(m.head), "")
	}
	for ; m.printed < len(m.stages); m.printed++ {
		if s := m.stages[m.printed].state; s == running || (s == pending && !m.finished) {
			break
		}
		out = append(out, m.replyLines(m.printed)...)
		out = append(out, m.stageLines(m.printed)...)
	}
	return out
}

// activity is the agent's recent steps under the running stage: the last
// few, or with ctrl+o every one that fits on the screen. used is how many
// lines the frame has above it.
func (m *model) activity(used int) []string {
	steps := m.steps
	if len(steps) == 0 {
		return nil
	}
	st := m.st
	m.used = used
	limit := m.window(used)
	hidden, later := m.more, 0
	if m.expanded {
		later = min(m.off, max(len(steps)-limit, 0))
		steps = steps[:len(steps)-later]
	}
	if len(steps) > limit {
		hidden += len(steps) - limit
		steps = steps[len(steps)-limit:]
	}
	var out []string
	cur := m.stepOf()
	first := len(m.steps) - later - len(steps)
	for i, s := range steps {
		prefix := "    "
		if i == 0 {
			prefix = "  ⎿ "
		}
		if m.expanded && cur >= 0 && first+i == cur {
			out = append(out, st.bold(prefix+s))
			continue
		}
		if !m.expanded && i == len(steps)-1 {
			out = append(out, prefix+s)
			continue
		}
		out = append(out, st.dim(prefix+s))
	}
	switch {
	case !m.expanded && hidden > 0:
		out = append(out, st.dim(fmt.Sprintf("    +%d earlier · %s to expand", hidden, m.kb.expandLabel())))
	case m.expanded:
		note := "    "
		if m.gate != nil {
			// A Gate takes the arrows; only ctrl+o and esc are the view's.
			out = append(out, st.dim(note+m.kb.expandLabel()+" to collapse"))
			break
		}
		if m.find != nil {
			out = append(out, st.dim("    "+m.findLine()))
			break
		}
		if hidden > 0 {
			note += fmt.Sprintf("+%d earlier · ", hidden)
		}
		if later > 0 {
			note += fmt.Sprintf("+%d later · ", later)
		}
		out = append(out, st.dim(note+"↑/↓ PgUp/PgDn scroll · / search · esc to collapse"))
	}
	return out
}

// todoItem is one item of the agent's plan, cleaned for the screen.
type todoItem struct {
	text   string
	status string // "pending", "in_progress" or "completed"
}

// maxTodoLines bounds the plan collapsed; ctrl+o shows all of it.
const maxTodoLines = 6

// todoItems cleans an agent's plan: every text is agent-controlled. An
// in-progress item shows its present-tense form when it has one.
func todoItems(in []agent.Todo) []todoItem {
	out := make([]todoItem, 0, len(in))
	for _, t := range in {
		text := t.Text
		if t.Status == "in_progress" && strings.TrimSpace(t.Active) != "" {
			text = t.Active
		}
		first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
		out = append(out, todoItem{text: clean(first), status: t.Status})
	}
	return out
}

// todoSummary is a plan update as one line: "todo: 2/5 done · now: Running
// tests". Empty for an empty plan.
func todoSummary(items []todoItem) string {
	if len(items) == 0 {
		return ""
	}
	done := 0
	now := ""
	for _, t := range items {
		switch t.status {
		case "completed":
			done++
		case "in_progress":
			if now == "" {
				now = t.text
			}
		}
	}
	s := fmt.Sprintf("todo: %d/%d done", done, len(items))
	if now != "" {
		s += " · now: " + now
	}
	return s
}

// todoLines is the agent's plan under the running stage: ☐ pending, ◐ in
// progress, ☑ done. Collapsed it keeps to maxTodoLines; ctrl+o shows all.
func (m *model) todoLines() []string {
	items := m.todos
	if len(items) == 0 {
		return nil
	}
	hidden := 0
	if !m.expanded && len(items) > maxTodoLines {
		hidden = len(items) - (maxTodoLines - 1)
		items = items[:maxTodoLines-1]
	}
	var out []string
	for _, t := range items {
		mark := "☐"
		switch t.status {
		case "in_progress":
			mark = "◐"
		case "completed":
			mark = "☑"
		}
		line := "    " + mark + " " + t.text
		if t.status == "completed" {
			line = m.st.dim(line)
		}
		out = append(out, line)
	}
	if hidden > 0 {
		out = append(out, m.st.dim(fmt.Sprintf("    +%d more · %s to expand", hidden, m.kb.expandLabel())))
	}
	return out
}

// window is how many steps show: a few collapsed, what fits expanded.
func (m *model) window(used int) int {
	if !m.expanded {
		return recentSteps
	}
	limit := len(m.steps)
	if m.height > 0 {
		// Leave room for the stages below, the hint and the footer.
		room := m.height - used - (len(m.stages) - m.cur) - 5
		limit = max(min(limit, room), recentSteps)
	}
	return limit
}

// scrollBy moves the expanded view back (up) or forward (down) in steps.
func (m *model) scrollBy(key string) {
	page := max(m.window(m.used)-1, 1)
	switch key {
	case "up":
		m.off++
	case "down":
		m.off--
	case "pgup":
		m.off += page
	case "pgdown":
		m.off -= page
	}
	m.clampOff()
}

func (m *model) clampOff() {
	m.off = max(min(m.off, max(len(m.steps)-m.window(m.used), 0)), 0)
}

// reported is what an agent's session reported; a nil field was not reported.
type reported struct {
	cost   *float64
	tokens *int
}

// record keeps the latest figures a stage's session reported. They are
// cumulative per session, so the latest replaces the earlier.
func (m *model) record(stage string, cost *float64, tokens *int) {
	if m.usage == nil {
		m.usage = map[string]reported{}
	}
	m.usage[stage] = reported{cost, tokens}
}

// statusLine is one line under the activity: mode, the stage in front,
// the agents, elapsed, what the agents reported spending, and whether the
// Run needs the human. On a narrow terminal the least telling parts go
// first; the attention never does.
func (m *model) statusLine() string {
	attention := gate.Attention(nil)
	if m.gate != nil {
		attention = "Waiting for you"
	}
	label := ""
	if m.cur < len(m.stages) {
		label = stageLabel(m.stages[m.cur].name, m.frozen)
	}
	type part struct {
		text string
		drop int // lower goes first when the line is too wide; 0 stays
	}
	parts := []part{{string(m.frozen.Mode) + " mode", 3}, {label, 6}}
	for _, s := range m.frozen.Stages {
		word, drop := "implement", 2
		if s.Role == "verifier" {
			word, drop = "verify", 1
			if m.frozen.Mode == pipeline.Fast {
				continue
			}
		}
		who := s.Binding()
		if s.Model != "" {
			who += ":" + s.Model
		}
		parts = append(parts, part{word + " " + clean(who), drop})
	}
	parts = append(parts, part{elapsed(m.now().Sub(m.started)), 7})
	var cost float64
	var tokens int
	var haveCost, haveTokens bool
	for _, u := range m.usage {
		if u.cost != nil {
			cost, haveCost = cost+*u.cost, true
		}
		if u.tokens != nil {
			tokens, haveTokens = tokens+*u.tokens, true
		}
	}
	if haveCost {
		parts = append(parts, part{fmt.Sprintf("$%.2f", cost), 4})
	}
	if haveTokens {
		parts = append(parts, part{tokenText(tokens), 5})
	}
	if m.ctx != nil {
		// An estimate: the last message's input against the reported window.
		parts = append(parts, part{fmt.Sprintf("ctx ≈%d%%", m.ctx.Percent()), 5})
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	// The attention is reserved first; the rest fills what is left, the
	// least telling parts going first, and is cut before the attention is.
	if w := ansi.StringWidth(attention); w > width-1 {
		attention = shortAttention(m.gate != nil, width)
	}
	room := width - 1 - ansi.StringWidth(attention) - 3
	join := func() string {
		var texts []string
		for _, p := range parts {
			if p.text != "" {
				texts = append(texts, p.text)
			}
		}
		return strings.Join(texts, " · ")
	}
	for ansi.StringWidth(join()) > room {
		best := -1
		for i, p := range parts {
			if p.text != "" && (best < 0 || p.drop < parts[best].drop) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		parts[best].text = ""
	}
	if pre := join(); pre != "" && room > 0 {
		return m.st.dim(pre + " · " + attention)
	}
	return m.st.dim(attention)
}

// shortAttention is the attention in at most w columns, never claiming
// "Nothing needs you" for a Run that is waiting.
func shortAttention(waiting bool, w int) string {
	forms := []string{"Nothing needs you.", "Nothing needs you", "All clear", "Clear", "OK", "·"}
	if waiting {
		forms = []string{"Waiting for you", "Waiting", "Wait", "!"}
	}
	for _, f := range forms {
		if ansi.StringWidth(f) <= w {
			return f
		}
	}
	return forms[len(forms)-1]
}

// tokenText is 950 tok, then 12.3k tok.
func tokenText(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d tok", n)
	}
	return fmt.Sprintf("%.1fk tok", float64(n)/1000)
}

// elapsed is 0.4s, 12.3s, then 1m05s.
func elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if r := d.Round(100 * time.Millisecond); r < time.Minute {
		return fmt.Sprintf("%.1fs", r.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
}

// cleanLines is clean for text of several lines: each line is cleaned on
// its own, since clean drops the newlines between them.
func cleanLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = clean(l)
	}
	return strings.Join(lines, "\n")
}
