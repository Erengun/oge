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
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/gate"
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
	// gone is closed once the live view has stopped; a Gate opened after
	// that is asked in plain lines.
	gone chan struct{}
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
		m:    newModel(t, f, newStyles(colorAllowed(env.Getenv)), time.Now),
		gone: make(chan struct{}),
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
	q := &queue{wake: make(chan struct{}, 1)}
	u.m.queue, u.m.interrupt = q, in.interrupt
	u.plain.intr = in
	if u.gone == nil {
		u.gone = make(chan struct{})
	}
	done := runAsync(ctx, start, func(ev run.Event) { q.push(progressOf(ev, u.m.frozen, time.Now())) }, func(e ended) {
		q.push(doneMsg{at: time.Now(), res: e.res, failed: e.err != nil || e.panicked != nil})
	})

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
	p := tea.NewProgram(u.m, opts...)
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
	close(u.gone)
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
		u.plain.summary(e.res)
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
		head  string   // EvStarted: the Run line
		stage string   // the stage the event belongs to
		text  string   // a finished stage's line, as plain mode prints it
		sub   []string // further lines under it
		fail  bool
		// timed: the lines carry their own durations (Check commands).
		timed bool
		step  string // EvAgent: one line of activity
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
	case run.EvAgent, run.EvAttempt:
		m.stage = ev.Attempt.Stage
	case run.EvCheck:
		m.stage = "check"
	case run.EvSendBack:
		m.stage, m.text = "send back", sendBackText(ev)
	case run.EvDecided:
		m.stage, m.text = "decision", decidedText(ev)
	}
	switch ev.Kind {
	case run.EvStarted:
		m.head = startedLine(ev.Result, f)
	case run.EvPreflight:
		m.text = preflightText(f)
	case run.EvAgent:
		// Only what the agent says it's doing; the Session and Exit
		// bookkeeping stays in -v (ADR-0019).
		if ev.Agent.Kind == agent.Claim {
			m.step = clean(ev.Agent.Text)
		}
	case run.EvAttempt:
		m.text = attemptText(ev.Attempt)
		m.fail = ev.Attempt.Failure != "" || ev.Attempt.Exit != "done"
	case run.EvCheck:
		lines := checkLines(ev.Check)
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
	mark       string // a note's mark, in place of ✓
	start, end time.Time
}

// model is the TUI's state. It changes only in Update and is drawn only
// by View, so tests drive it with messages and compare frames.
type model struct {
	title  string
	head   string
	frozen *pipeline.Frozen
	stages []stage
	cur    int
	steps  []string
	more   int // steps dropped beyond keptSteps

	expanded   bool
	cancelling bool
	still      bool // no spinner ticks: tests draw only on events
	finished   bool

	width, height int
	spin          int
	now           func() time.Time
	st            styles
	queue         *queue
	interrupt     func()
	gate          *gateState // an open Gate, waiting for the human
}

func newModel(t task.Task, f *pipeline.Frozen, st styles, now func() time.Time) *model {
	m := &model{title: clean(t.Title), frozen: f, st: st, now: now}
	m.head = fmt.Sprintf("%s mode", f.Mode)
	m.stages = append(m.stages, stage{name: "preflight"})
	for _, s := range f.Stages {
		if f.Mode == pipeline.Fast && s.Role != "implementer" {
			continue
		}
		m.stages = append(m.stages, stage{name: s.Name, text: s.Agent})
	}
	m.stages = append(m.stages, stage{name: "check", text: m.checkText()})
	m.stages[0].state, m.stages[0].start = running, now()
	return m
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
	return tea.Batch(m.waitCmd(), tick())
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
				return m, tea.Quit
			}
		}
		return m, m.waitCmd()
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
	case tea.KeyPressMsg:
		if m.gate != nil && m.gateKey(msg) {
			return m, nil
		}
		switch msg.String() {
		case "ctrl+o":
			m.expanded = !m.expanded
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
		switch msg.kind {
		case run.EvStarted:
			m.head = msg.head
		case run.EvPreflight, run.EvAttempt, run.EvCheck:
			m.finish(msg)
		case run.EvDecided:
			m.gate = nil // recorded: now it is taken
			m.note(msg)
		case run.EvSendBack:
			m.note(msg)
			m.again()
		case run.EvAgent:
			if msg.stage != "" {
				if i := m.index(msg.stage); m.stages[i].state == pending {
					m.begin(i, msg.at)
				}
			}
			if msg.step != "" {
				m.steps = append(m.steps, msg.step)
				if len(m.steps) > keptSteps {
					m.steps = m.steps[1:]
					m.more++
				}
			}
		}
	case gateMsg:
		m.gate = &gateState{req: msg.req, reply: msg.reply}
	case doneMsg:
		m.gate = nil
		if m.cur < len(m.stages) && m.stages[m.cur].state == running {
			s := &m.stages[m.cur]
			s.state, s.end = failed, msg.at
			if msg.res != nil && msg.res.Outcome == run.Refused {
				s.text = "refused"
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
	i := max(len(m.stages)-1, 0)
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
	if m.cur < len(m.stages) && m.stages[m.cur].state == running {
		m.stages[m.cur].state = pending
	}
	m.stages[i].state, m.stages[i].start = running, at
	m.cur = i
	m.steps, m.more = nil, 0
}

// finish ends the stage the message names with its line. Unless it failed,
// the first stage still pending starts.
func (m *model) finish(msg progressMsg) {
	i := m.index(msg.stage)
	s := &m.stages[i]
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
		return
	}
	m.steps, m.more = nil, 0
	m.cur = len(m.stages)
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

	add(st.bold(m.title))
	add(st.dim(m.head))
	add("")
	for i, s := range m.stages {
		name := fmt.Sprintf("%-10s", s.name)
		switch s.state {
		case pending:
			text := s.text
			if m.finished {
				text = "not run"
			}
			add(st.dim("· " + strings.TrimRight(name+" "+text, " ")))
		case running:
			line := st.accent(spinnerFrames[m.spin%len(spinnerFrames)]) + " " + name + " "
			if s.text != "" {
				line += s.text + " · "
			}
			add(line + st.dim(elapsed(m.now().Sub(s.start))))
			if i == m.cur {
				lines = append(lines, m.activity(len(lines))...)
			}
		default:
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
			add(line)
			for _, l := range s.sub {
				add("  " + fmt.Sprintf("%-10s", "") + " " + l)
			}
		}
	}
	lines = append(lines, m.gateLines()...)
	if !m.finished {
		add("")
		if m.gate == nil {
			add(gate.Attention(nil))
		}
		hint := "ctrl+c to cancel"
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

// activity is the agent's recent steps under the running stage: the last
// few, or with ctrl+o every one that fits on the screen. used is how many
// lines the frame has above it.
func (m *model) activity(used int) []string {
	steps := m.steps
	if len(steps) == 0 {
		return nil
	}
	st := m.st
	limit := recentSteps
	if m.expanded {
		limit = len(steps)
		if m.height > 0 {
			// Leave room for the stages below, the hint and the footer.
			room := m.height - used - (len(m.stages) - m.cur) - 4
			limit = max(min(limit, room), recentSteps)
		}
	}
	hidden := m.more
	if len(steps) > limit {
		hidden += len(steps) - limit
		steps = steps[len(steps)-limit:]
	}
	var out []string
	for i, s := range steps {
		prefix := "    "
		if i == 0 {
			prefix = "  ⎿ "
		}
		out = append(out, st.dim(prefix+s))
	}
	switch {
	case hidden > 0 && !m.expanded:
		out = append(out, st.dim(fmt.Sprintf("    +%d earlier · ctrl+o to expand", hidden)))
	case hidden > 0:
		out = append(out, st.dim(fmt.Sprintf("    +%d earlier · ctrl+o to collapse", hidden)))
	case m.expanded:
		out = append(out, st.dim("    ctrl+o to collapse"))
	}
	return out
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

// styles paint text; without colour each is the identity, so a NO_COLOR
// frame holds no escape sequences at all.
type styles struct {
	bold, dim, accent, ok, bad func(string) string
}

func newStyles(color bool) styles {
	if !color {
		id := func(s string) string { return s }
		return styles{id, id, id, id, id}
	}
	// The 16 basic colours: the user's palette decides the exact shades.
	style := func(s lipgloss.Style) func(string) string { return func(t string) string { return s.Render(t) } }
	return styles{
		bold:   style(lipgloss.NewStyle().Bold(true)),
		dim:    style(lipgloss.NewStyle().Faint(true)),
		accent: style(lipgloss.NewStyle().Foreground(lipgloss.Magenta)),
		ok:     style(lipgloss.NewStyle().Foreground(lipgloss.Green)),
		bad:    style(lipgloss.NewStyle().Foreground(lipgloss.Red)),
	}
}
