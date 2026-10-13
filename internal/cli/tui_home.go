package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
	"github.com/rivo/uniseg"
)

// The home is what a bare oge opens on a terminal: a composer for the Task
// and, under it, a read-only line of what the Run will use. One Bubble Tea
// program owns the whole session (#182): Enter starts the Run in it, so the
// live view takes over the frame the composer was in. The session reads as
// one transcript: the Task's echo, each stage as it ends, a Gate's outcome
// and the summary are printed above the program (#190), and the live area
// keeps only what still changes. When the Run ends the composer comes back
// at once. An Accepted Run is taken only by an explicit act (#184): the
// /apply /diff /receipt /branch commands, or ↓ on an empty composer and a
// key on the action row; never a bare key the composer would take as text.

// maxComposerRows is how many lines of the Task the box shows before it
// scrolls.
const maxComposerRows = 8

// composer is a minimal multiline text box. It keeps the Task as lines of
// runes, so the cursor is a (row, col) in them.
type composer struct {
	lines    [][]rune
	row, col int
	top      int // first visible row of the wrapped text

	kills    string   // the kill ring's one entry (tui_edit.go)
	killing  bool     // the last edit was a kill
	killLine bool     // the ring's text was taken as whole lines (vim's dd, yy)
	undo     []snap   // what undo brings back, newest last
	lastOp   string   // the last edit's kind, to join typing into one undo step
	pastes   []string // the text behind each large-paste placeholder (tui_paste.go)

	sel      *vimRange           // the VISUAL selection to paint, set by the home's render (#158)
	selPaint func(string) string // how it is painted
}

// inSel reports whether the cluster at rune n of line i is selected.
func (c *composer) inSel(i, n int) bool {
	r := c.sel
	switch {
	case r == nil || i < r.r0 || i > r.r1:
		return false
	case r.line:
		return true
	}
	return (i > r.r0 || n >= r.c0) && (i < r.r1 || n < r.c1)
}

func newComposer() *composer { return &composer{lines: [][]rune{nil}} }

func (c *composer) text() string {
	parts := make([]string, len(c.lines))
	for i, l := range c.lines {
		parts[i] = string(l)
	}
	return strings.Join(parts, "\n")
}

func (c *composer) empty() bool { return strings.TrimSpace(c.text()) == "" }

// set replaces the text and puts the cursor at its end; undo can bring the
// old text back, and the kill ring is kept.
func (c *composer) set(text string) {
	c.change("set", func() {
		c.lines, c.row, c.col, c.top = [][]rune{nil}, 0, 0, 0
		c.insert(text)
	})
}

// insert types s at the cursor. Newlines split the line; tabs become spaces
// and other control characters are dropped, so pasted bytes can't steer the
// terminal.
func (c *composer) insert(s string) {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", "    ").Replace(s)
	for _, r := range s {
		switch {
		case isPasteRune(r): // only a placeholder of ours may be one
		case r == '\n':
			c.newline()
		case r < 0x20 || (r >= 0x7f && r < 0xa0) || r == 0xfffd:
		default:
			l := c.lines[c.row]
			c.lines[c.row] = append(l[:c.col:c.col], append([]rune{r}, l[c.col:]...)...)
			c.col++
		}
	}
}

func (c *composer) newline() {
	l := c.lines[c.row]
	rest := append([]rune(nil), l[c.col:]...)
	c.lines[c.row] = l[:c.col:c.col]
	c.lines = append(c.lines[:c.row+1], append([][]rune{rest}, c.lines[c.row+1:]...)...)
	c.row, c.col = c.row+1, 0
}

// backslashNewline is "\\" then Enter: the backslash before the cursor
// becomes a newline, so a newline can be typed where Ctrl+J and Shift+Enter
// don't reach. It reports whether it did.
func (c *composer) backslashNewline() bool {
	if c.col == 0 || c.lines[c.row][c.col-1] != '\\' {
		return false
	}
	c.change("newline", func() {
		c.backspace()
		c.newline()
	})
	return true
}

func (c *composer) backspace() {
	switch {
	case c.col > 0:
		l := c.lines[c.row]
		c.lines[c.row] = append(l[:c.col-1:c.col-1], l[c.col:]...)
		c.col--
	case c.row > 0:
		prev := c.lines[c.row-1]
		c.col = len(prev)
		c.lines[c.row-1] = append(prev[:len(prev):len(prev)], c.lines[c.row]...)
		c.lines = append(c.lines[:c.row], c.lines[c.row+1:]...)
		c.row--
	}
}

func (c *composer) delete() {
	l := c.lines[c.row]
	switch {
	case c.col < len(l):
		c.lines[c.row] = append(l[:c.col:c.col], l[c.col+1:]...)
	case c.row+1 < len(c.lines):
		c.lines[c.row] = append(l[:len(l):len(l)], c.lines[c.row+1]...)
		c.lines = append(c.lines[:c.row+1], c.lines[c.row+2:]...)
	}
}

func (c *composer) move(dr, dc int) {
	switch {
	case dc < 0 && c.col > 0:
		c.col--
	case dc < 0 && c.row > 0:
		c.row--
		c.col = len(c.lines[c.row])
	case dc > 0 && c.col < len(c.lines[c.row]):
		c.col++
	case dc > 0 && c.row+1 < len(c.lines):
		c.row, c.col = c.row+1, 0
	}
	if dr != 0 {
		c.row = min(max(c.row+dr, 0), len(c.lines)-1)
		c.col = min(c.col, len(c.lines[c.row]))
	}
}

// key applies an editing key; it reports whether the key was one.
func (c *composer) key(k tea.KeyPressMsg) bool {
	if c.editKey(k) {
		return true
	}
	switch k.String() {
	case "backspace", "ctrl+h":
		c.change("del", c.backspace)
	case "delete", "ctrl+d":
		c.change("del", c.delete)
	case "left", "ctrl+b":
		c.change("move", func() { c.move(0, -1) })
	case "right", "ctrl+f":
		c.change("move", func() { c.move(0, 1) })
	case "up", "ctrl+p":
		c.change("move", func() { c.move(-1, 0) })
	case "down", "ctrl+n":
		c.change("move", func() { c.move(1, 0) })
	case "home", "ctrl+a":
		c.change("move", func() { c.col = 0 })
	case "end":
		c.change("move", func() { c.col = len(c.lines[c.row]) })
	case "ctrl+j", "shift+enter", "alt+enter":
		c.change("newline", c.newline)
	default:
		if k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) == 0 && k.Text != "" {
			c.change("type", func() { c.insert(k.Text) })
			return true
		}
		return false
	}
	return true
}

// layout wraps the text to width columns, by grapheme cluster, so wide and
// combined characters take the columns the terminal gives them. It returns
// the visible rows and where the cursor is among them, and scrolls to keep
// the cursor in view.
func (c *composer) layout(width int) (rows []string, curRow, curCol int) {
	width = max(width, 1)
	var all []string
	for i, l := range c.lines {
		var row, seg strings.Builder // seg is the run of selected clusters not yet in row
		flush := func() {
			if seg.Len() > 0 {
				row.WriteString(c.selPaint(seg.String()))
				seg.Reset()
			}
		}
		w, n := 0, 0 // columns in the row; runes before the cluster
		rest := string(l)
		for rest != "" {
			cl, tail, cw, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
			k := len([]rune(cl))
			if r := []rune(cl); len(r) == 1 && isPasteRune(r[0]) {
				cl = c.pasteLabel(r[0])
				cw = uniseg.StringWidth(cl)
			}
			if w+cw > width && w > 0 {
				flush()
				all = append(all, row.String())
				row.Reset()
				w = 0
			}
			if i == c.row && c.col >= n && c.col < n+k {
				curRow, curCol = len(all), w
			}
			if c.inSel(i, n) {
				seg.WriteString(cl)
			} else {
				flush()
				row.WriteString(cl)
			}
			w += cw
			n += k
			rest = tail
		}
		flush()
		if i == c.row && c.col >= n {
			if w >= width { // the cursor wraps onto a fresh row
				all = append(all, row.String())
				row.Reset()
				w = 0
			}
			curRow, curCol = len(all), w
		}
		if len(l) == 0 && c.inSel(i, 0) {
			row.WriteString(c.selPaint(" ")) // a selected empty line shows as a cell
		}
		all = append(all, row.String())
	}
	if curRow < c.top {
		c.top = curRow
	}
	if curRow >= c.top+maxComposerRows {
		c.top = curRow - maxComposerRows + 1
	}
	c.top = min(c.top, max(len(all)-maxComposerRows, 0))
	end := min(c.top+maxComposerRows, len(all))
	return all[c.top:end], curRow - c.top, curCol
}

// home is the session program's root model: the composer, the Run's own
// model once Enter starts it, then the composer again.
type home struct {
	// settling holds the printing back after a Gate closed (#233); heldEnd
	// is the Run's end that arrived meanwhile.
	settling bool
	heldEnd  *runEndedMsg
	// getenv reads the environment for /terminal; nil reads nothing.
	getenv func(string) string
	comp   *composer
	// vim is the composer's vim mode (#156); normal is its NORMAL state.
	vim, normal bool
	vimState    // the pending operator, count and last change (tui_vim_ops.go)
	// hist is the repo's prompt history. nav is the entry ↑ and ↓ show; draft is what was in the box before the
	// first ↑. search is Ctrl+R's search while it is open.
	hist       *history
	nav        int // the entry shown; meaningful only while navigating
	navigating bool
	draft      string
	// cleared is the Task Ctrl+C last cleared: ↑ on the then-empty box
	// brings it back, before any history entry.
	cleared string
	search  *histSearch
	// shell runs a ! command (#160); root is its cwd; shellHist is the shell
	// history, apart from hist; job is the command in flight.
	shell     func(ctx context.Context, root, line string) shellResult
	root      string
	shellHist *history
	job       *shellJob
	// stash is Ctrl+S's one slot: a draft put aside for this session.
	stash string
	// hidden is the confirmation of a Task that held hidden characters.
	hidden *hiddenPrompt
	title  string   // the box's title: repo and branch
	setup  []string // the setup bar, as lines
	st     styles
	// start begins the Run for a Task in this program: it returns the Run's
	// model and its first command.
	start func(task.Task, *resolved, int, int) (*model, tea.Cmd)
	// resolve reads the config again, for the Task about to start and with
	// the setup panel's choices (sel) added.
	resolve func(*setupState) (*resolved, []string)
	// reloadTheme re-reads the custom theme file (#175); it returns a notice
	// when the file is bad. Called when the composer opens and on Ctrl+L,
	// never during a Run.
	reloadTheme func() string
	// sel is what the setup panel edits; it lives for the session.
	sel    *setupState
	panel  *setupPanel    // non-nil while Tab's setup is open
	saving bool           // the save prompt waits for y or n
	review *projectReview // the reviewed save into .oge/oge.toml waits for y or n
	save   func() (string, error)
	note   string // what happened, shown under the box (a save)
	ctx    context.Context
	// msg is what stops or went wrong, shown under the box.
	msg  []string
	edit func(text string) tea.Cmd // Ctrl+E: $EDITOR on the Task
	// deliver is /receipt, /diff, /apply and /branch of a named Run, and
	// runs lists the repository's recent Runs for /runs (#145).
	deliver func(cmd string, args []string) tea.Cmd
	runs    func(n int) ([]*delivery.Run, string)
	// label is /label and the picker's l: it names a Run beside its
	// Ledger (#177).
	label func(id, text string) (string, string)
	// menu is the slash menu while it is open: it lists the commands that
	// complete menuQuery. Esc closes it for the text it was closed on.
	menu   *list.Model
	picker *runPicker // /runs: the Run picker while it is open (#176)
	// diff is the /diff viewer while it is open (#169); diffOf reads a
	// Run's Candidate as files.
	diff        *diffViewer
	diffOf      func(id string) (string, []diffFile, bool)
	menuQuery   string
	menuOff     bool
	menuOffText string
	// at is the @file picker (#146); listFiles reads the tracked files.
	at        atState
	listFiles func() ([]string, error)
	// armText and armAt: a delivery command the menu completed runs only on
	// an Enter that comes actionsArm later, so a double tap never delivers.
	armText string
	armAt   time.Time

	width, height int
	run           *model    // nil while composing
	notify        *notifier // the session's alerts; nil when silent (#170)
	// scroll is what was printed above the program, for /export (#168);
	// exportDir is where it goes ("" is the working directory); tmux wraps
	// /copy's OSC 52.
	scroll    []string
	exportDir string
	tmux      bool
	keys      *keyPanel // the ? panel, when open (#155)
	quit      bool

	// done delivers the Run's end once its view has finished; nil in tests.
	done <-chan ended
	// after takes the Run's end: it returns the summary to print and, after
	// an Accepted Run, what can take it. stop ends the session (Öge failed).
	after func(ended) (text string, last *lastRun, stop bool)
	// thread is the Run the next message continues (#185): set when a Run
	// ends with a Candidate that wasn't applied, until /new, /clear or
	// /apply. nil: the next message is a fresh Task.
	thread *thread
	// last is the latest ended Run the human can take: /apply, /diff,
	// /receipt, /branch, or ↓ and a key. nil when there is none.
	last *lastRun
	// actions: ↓ moved the focus to the action row, whose keys act once
	// actionsArm has passed since actionsAt. Keys before were typed ahead,
	// never meant for it (ADR-0015: applying is an explicit act). now is
	// the clock; nil is time.Now.
	actions   bool
	actionsAt time.Time
	// hold, while true, swallows interrupts: a pager or $EDITOR has the
	// terminal, and its Ctrl+C is its own (nil in tests).
	hold func(bool)
	now  func() time.Time
}

// actionsArm is how long the action row ignores its keys once focused.
const actionsArm = 300 * time.Millisecond

func (h *home) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// lastRun is what can take an Accepted Run from the home (#184): each
// returns what to print, the delivery code's own lines, as oge apply, oge
// branch, oge diff and oge receipt print them.
type lastRun struct {
	id      string // the Run's id, for /label
	apply   func() string
	branch  func(name string) string // "" is the default name
	diff    func() tea.Cmd           // printed, or paged with $PAGER
	receipt func() string
}

// act runs one of the Run's actions by its key; arg is a branch's name.
func (l *lastRun) act(key, arg string) tea.Cmd {
	out := func(s string) tea.Cmd { return tea.Println(strings.TrimSuffix(s, "\n")) }
	switch key {
	case "a":
		return out(l.apply())
	case "b":
		return out(l.branch(arg))
	case "d":
		return l.diff()
	case "r":
		return out(l.receipt())
	}
	return nil
}

// thread is what a follow-up continues: the Task with the follow-ups so far,
// and the Run whose Candidate the next one starts from.
type thread struct {
	task task.Task
	from run.FollowUp
}

// nextThread is what the next message continues once a Run ends (#185):
// the Run's Candidate, unless it was applied, a human rejected it at a
// Gate, or the session never prompts (TUI sessions only). A Cancelled or
// Infrastructure-stopped Run with a Candidate is continued too. A refused
// follow-up keeps the thread it was to continue (prev): once the user
// undoes the change that conflicts, the next message continues it.
func nextThread(prev *thread, cur task.Task, res *run.Result, f runFlags) *thread {
	switch {
	case res.Outcome == run.Refused:
		return prev
	case res.Candidate == "" || res.Dir == "" || res.Outcome == run.Rejected || f.unattended || f.apply:
		return nil
	}
	return &thread{task: cur, from: run.FollowUp{Run: res.ID, Dir: res.Dir, Candidate: res.Candidate, Snapshot: res.Snapshot}}
}

// runEndedMsg is the Run's end, after its view has finished.
type runEndedMsg ended

// editedMsg is the Task back from $EDITOR.
type editedMsg struct {
	text string
	err  error
}

// gateSettledMsg says the view without a resolved Gate has had time to be
// drawn: what waited for it may be printed.
type gateSettledMsg struct{}

// gateSettle waits out two frames, so the renderer has drawn the view
// without the resolved Gate before anything is printed above it.
func gateSettle() tea.Cmd {
	return tea.Tick(2*time.Second/tuiFPS, func(time.Time) tea.Msg { return gateSettledMsg{} })
}

// settled prints what waited for the Gate's frame to go, then the Run's
// summary if it ended meanwhile.
func (h *home) settled() (tea.Model, tea.Cmd) {
	h.settling = false
	var print tea.Cmd
	if lines := h.run.take(); len(lines) > 0 {
		print = tea.Println(strings.Join(lines, "\n"))
	}
	if e := h.heldEnd; e != nil {
		h.heldEnd = nil
		m, cmd := h.onEnded(ended(*e))
		return m, tea.Sequence(print, cmd)
	}
	return h, print
}

// ctxDoneMsg tells the composer that Öge was interrupted.
type ctxDoneMsg struct{}

func (h *home) Init() tea.Cmd {
	ask := askBackground(h.st)
	if h.ctx == nil {
		return ask
	}
	return tea.Batch(ask, func() tea.Msg {
		<-h.ctx.Done()
		return ctxDoneMsg{}
	})
}

func (h *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := h.update(msg)
	h.syncMenu()
	h.syncAt()
	h.dropRecordingInOverlay()
	return m, cmd
}

func (h *home) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if bg, ok := msg.(tea.BackgroundColorMsg); ok {
		h.st.setDark(bg.IsDark())
	}
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		h.width, h.height = ws.Width, ws.Height
		if h.diff != nil {
			h.diff.resize(ws.Width)
		}
	}
	if h.run != nil {
		if _, ok := msg.(gateSettledMsg); ok {
			return h.settled()
		}
		if e, ok := msg.(runEndedMsg); ok {
			if h.settling {
				h.heldEnd = &e
				return h, nil
			}
			return h.onEnded(ended(e))
		}
		was := h.run.finished
		gated := h.run.gate != nil
		_, cmd := h.run.Update(msg)
		alert := h.run.flushAlerts()
		// The open Gate frame is still what the terminal shows when it
		// closes (by its answer, Ctrl+C, or the Run's end): a print now
		// would scroll it into the transcript (#233). Nothing is printed
		// until a frame without it has been drawn.
		var settle tea.Cmd
		if gated && h.run.gate == nil && !h.settling {
			h.settling, settle = true, gateSettle()
		}
		// Only progress finishes stages. What finished is printed before the
		// next progress is waited for, so the transcript keeps its order.
		var print tea.Cmd
		if _, ok := msg.(batchMsg); ok && !h.settling {
			if lines := h.run.take(); len(lines) > 0 {
				print = h.println(strings.Join(lines, "\n"))
			}
		}
		if !was && h.run.finished {
			// The view has finished; its tea.Quit is for oge "task". The
			// session goes on once the Run itself has returned.
			if h.done == nil {
				return h, tea.Sequence(print, alert, settle)
			}
			done := h.done
			return h, tea.Sequence(print, alert, tea.Batch(settle, func() tea.Msg { return runEndedMsg(<-done) }))
		}
		return h, tea.Sequence(print, alert, tea.Batch(settle, cmd))
	}
	switch msg := msg.(type) {
	case shellDoneMsg:
		return h, h.shellDone(msg)
	case ctxDoneMsg:
		if h.job != nil {
			// Stop the command's group before the program ends.
			h.job.cancel()
			select {
			case <-h.job.finished:
			case <-time.After(shellGrace + 500*time.Millisecond):
			}
		}
		h.quit = true
		return h, tea.Quit
	case pageFailedMsg:
		h.msg = []string{"oge: showing the diff: " + clean(msg.err.Error())}
		return h, nil
	case tea.MouseWheelMsg:
		return h, h.wheel(msg)
	case tea.PasteMsg:
		h.actions, h.escArmed = false, false
		switch {
		case h.job != nil:
		case h.panel != nil:
			h.panel.paste(msg.Content)
		case h.saving, h.review != nil, h.search != nil, h.picker != nil, h.diff != nil, h.hidden != nil:
		default:
			h.comp.change("paste", func() { h.comp.insertPaste(msg.Content) })
			h.endNavigation()
		}
	case editedMsg:
		h.actions = false
		if msg.err != nil {
			h.msg = []string{"the editor failed: " + clean(msg.err.Error())}
		} else {
			h.comp.set(msg.text)
		}
	case tea.KeyPressMsg:
		h.msg, h.note = nil, ""
		switch {
		case h.job != nil:
			// A shell command runs: Ctrl+C stops it (never Öge); other keys are dropped.
			if msg.String() == "ctrl+c" {
				h.job.cancel()
			}
			return h, nil
		case h.keys != nil:
			h.keysKey(msg)
			return h, nil
		case h.hidden != nil:
			return h.hiddenKey(msg)
		case h.picker != nil:
			return h, h.pickerKey(msg)
		case h.diff != nil:
			return h, h.diffKey(msg)
		case h.actions:
			return h.actionKey(msg)
		case h.review != nil:
			h.answerReview(msg.String())
			return h, nil
		case h.saving:
			h.answerSave(msg.String())
			return h, nil
		case h.panel != nil:
			closed := h.panel.key(msg)
			if h.panel.save {
				h.panel.save = false
				h.saveNow()
			}
			if h.panel.saveProject {
				h.panel.saveProject = false
				h.reviewProject()
			}
			if closed {
				h.closeSetup()
			}
			return h, nil
		}
		if h.search != nil {
			h.searchKey(msg)
			return h, nil
		}
		if h.menu != nil && h.menuKey(msg) {
			return h, nil
		}
		if h.at.list != nil && h.atKey(msg) {
			return h, nil
		}
		if h.vim && !h.normal && !h.replay && h.escapeSeq(msg) {
			return h, nil
		}
		if h.vim && !h.normal && h.insRec != nil && !h.replay {
			h.recordInsert(msg)
		}
		if h.vim && h.normal && h.normalKey(msg) {
			return h, nil
		}
		switch msg.String() {
		case "ctrl+r":
			if h.hist.len() > 0 {
				h.search = &histSearch{hit: -1}
			}
		case "up":
			if !h.recall(-1) {
				h.comp.key(msg)
			}
		case "esc":
			// Esc never quits, and never wipes a Task: it closes overlays
			// (above) and otherwise does nothing. With vim on, it leaves
			// INSERT for NORMAL.
			if h.vim && !h.normal {
				h.enterNormal()
			}
			h.visual = 0
			h.vimState.cancel()
		case "ctrl+c":
			// Ctrl+C clears a Task (↑ brings it back) and, on an empty
			// box, leaves (as Claude Code's prompt does).
			if h.comp.empty() {
				h.quit = true
				return h, tea.Quit
			}
			h.cleared = h.comp.expanded()
			h.comp.set("")
			h.navigating = false
			h.visual = 0
		case "enter":
			if h.comp.backslashNewline() {
				h.endNavigation()
				return h, nil
			}
			if esc, escaped := unescapeBang(h.comp.expanded()); escaped {
				h.comp.set(esc) // a Task that starts with !
			} else if line, ok := shellLine(esc); ok {
				return h, h.shellSubmit(line)
			}
			text := task.StripComments(h.comp.expanded())
			if task.IsEmpty(text) {
				return h, nil
			}
			if h.armText != "" && strings.TrimSpace(text) == h.armText && h.clock().Sub(h.armAt) < actionsArm {
				return h, nil // typed ahead of the completion
			}
			if cmd, ok := h.slash(strings.TrimSpace(text)); ok {
				return h, cmd
			}
			if n := hiddenCount(text); n > 0 {
				// Hidden characters can carry instructions the human can't
				// see: say so, and submit only the cleaned text (#153).
				h.hidden = &hiddenPrompt{text: text, n: n}
				return h, nil
			}
			return h.submit(text)
		case "ctrl+s":
			h.stashDraft()
		case "ctrl+l":
			if n := h.themeNotice(); n != "" {
				h.msg = []string{n}
			}
			return h, tea.ClearScreen
		case "ctrl+e", "ctrl+g":
			if h.edit != nil {
				return h, h.edit(h.comp.expanded())
			}
		case "?":
			if !h.openKeys() {
				h.comp.key(msg)
				h.endNavigation()
			}
		case "tab":
			if p := h.openSetup(); p != "" {
				h.msg = []string{p}
			}
		case "shift+tab":
			// Standard <-> Fast through the setup state, as /mode does; Blind
			// is not in the cycle. A menu owns its keys; a Run never gets here.
			if h.menu == nil && h.at.list == nil {
				h.vimState.cancel() // a pending operator or count ends here
				if p := h.cycleMode(); p != "" {
					h.msg = []string{p}
				}
			}
		case "alt+m":
			// Alt+M: setup with the implement model box open; the draft
			// stays. During a Run the keys go to the Run, so no mid-Run change.
			if p := h.openSetup(); p != "" {
				h.msg = []string{p}
			} else {
				h.panel.focusModel()
			}
		case "down":
			// ↓ is history only once ↑ has started navigating; on an empty
			// composer otherwise it focuses the action row (#184).
			if h.navigating {
				if !h.recall(1) {
					h.comp.key(msg)
					h.endNavigation()
				}
				return h, nil
			}
			if h.last != nil && h.comp.empty() {
				h.actions, h.actionsAt = true, h.clock()
				return h, nil
			}
			h.comp.key(msg)
			h.endNavigation()
		default:
			h.comp.key(msg)
			h.endNavigation()
		}
	}
	return h, nil
}

// submit starts the Run (or the follow-up) for text, which is final: slash
// commands and hidden characters are dealt with already.
func (h *home) submit(text string) (tea.Model, tea.Cmd) {
	h.cleared = ""
	if h.ctx != nil && h.ctx.Err() != nil {
		h.quit = true
		return h, tea.Quit
	}
	res, problem := h.resolve(h.sel)
	if problem != nil {
		// The Run can't start: say why, and keep the Task.
		h.msg, h.setup = problem, nil
		return h, nil
	}
	h.setup = setupLines(res.frozen)
	h.sel.syncFrom(res)
	h.at.files, h.at.filesReady = nil, false // listed again for the next composer
	h.hist.add(text)                         // what the human typed, never the agent's output
	h.comp.clearUndo()
	h.navigating = false
	h.visual = 0
	t := task.Parse(text)
	if h.thread != nil {
		// A follow-up: the whole thread, this message last.
		t = h.thread.task.FollowUp(text)
	}
	m, cmd := h.start(t, res, h.width, h.height)
	h.run = m
	if lines := m.take(); len(lines) > 0 { // the Task's echo
		cmd = tea.Sequence(h.println(strings.Join(lines, "\n")), cmd)
	}
	return h, cmd
}

// stashDraft is Ctrl+S: it puts the draft aside and clears the box; on an
// empty box it brings the stash back. With both a draft and a stash it
// swaps them, so neither is lost.
func (h *home) stashDraft() {
	h.navigating, h.visual = false, 0
	draft := h.comp.expanded()
	if draft == "" && h.stash == "" {
		return
	}
	h.comp.set(h.stash)
	h.stash = draft
}

// endNavigation stops history navigation once the box no longer holds the
// entry it showed (an edit or a paste), so ↓ never replaces the edit and
// an emptied box is an empty composer again (↓ focuses the actions, #184).
func (h *home) endNavigation() {
	if h.navigating && h.nav < h.hist.len() && h.comp.text() != h.hist.entries[h.nav] {
		h.navigating = false
	}
}

// recall moves through the history: dir -1 is older (↑), 1 is newer (↓).
// ↑ acts on the first line of the box and ↓ on the last, so multi-line text
// is still walked line by line; it reports false when the key isn't history's
// and the box should take it. Entries are copied into the box, so editing
// one never changes the history. Past the newest entry the box gets back
// what was in it before the first ↑.
func (h *home) recall(dir int) bool {
	n := h.hist.len()
	if dir < 0 {
		if h.cleared != "" && h.comp.empty() && !h.navigating {
			h.comp.set(h.cleared)
			h.cleared = ""
			return true
		}
		if n == 0 || h.comp.row != 0 {
			return false
		}
		if !h.navigating {
			h.draft, h.nav, h.navigating = h.comp.expanded(), n, true
		}
		if h.nav == 0 {
			return true
		}
		h.nav--
		h.comp.set(h.hist.entries[h.nav])
		h.comp.row, h.comp.col = 0, len(h.comp.lines[0])
		return true
	}
	if !h.navigating || h.comp.row != len(h.comp.lines)-1 {
		return false
	}
	h.nav++
	if h.nav >= n {
		h.navigating = false
		h.comp.set(h.draft)
		return true
	}
	h.comp.set(h.hist.entries[h.nav])
	return true
}

// searchKey is a key while Ctrl+R's search is open: text narrows the
// query, Ctrl+R steps to an older match, Enter puts the match in the box
// (it never submits) and Esc leaves the box as it was.
func (h *home) searchKey(msg tea.KeyPressMsg) {
	s := h.search
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+g":
		h.search = nil
	case "enter":
		if s.hit >= 0 {
			h.comp.set(h.hist.entries[s.hit])
			h.navigating = false
		}
		h.search = nil
	case "ctrl+r":
		if s.hit > 0 {
			if i := h.hist.find(s.query, s.hit-1); i >= 0 {
				s.hit = i
			}
		}
	case "backspace":
		if r := []rune(s.query); len(r) > 0 {
			s.query = string(r[:len(r)-1])
			s.hit = h.hist.find(s.query, h.hist.len()-1)
		}
	default:
		if msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) == 0 && msg.Text != "" {
			s.query += msg.Text
			s.hit = h.hist.find(s.query, h.hist.len()-1)
		}
	}
}

// actionKey is a key while the action row has the focus. Its keys act only
// once actionsArm has passed and on an empty composer, and each ↓ allows one
// action: the focus then goes back. Esc or ↑ go back to the composer, and
// any other key goes back and is typed there.
func (h *home) actionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc", "up", "ctrl+c":
		h.actions = false
		return h, nil
	case "a", "b", "d", "r":
		if h.clock().Sub(h.actionsAt) < actionsArm || !h.comp.empty() || h.last == nil {
			return h, nil // typed ahead, before the row could be seen
		}
		h.actions = false
		return h, h.act(key, "")
	}
	h.actions = false
	return h.Update(msg)
}

// act runs one of the last Run's actions. Applying it ends the thread: the
// next message is a fresh Task.
func (h *home) act(key, arg string) tea.Cmd {
	if key == "a" {
		h.thread = nil
	}
	if key == "d" && h.last != nil && h.openDiff(h.last.id) {
		return nil // the viewer; the pager is its fallback
	}
	return h.last.act(key, arg)
}

// onEnded prints the Run's summary above the program (its stages are
// printed already) and opens the composer again.
func (h *home) onEnded(e ended) (tea.Model, tea.Cmd) {
	text, last, stop := h.after(e)
	if stop {
		h.quit = true
		return h, tea.Quit
	}
	h.last = last
	h.reopen()
	return h, h.println(text)
}

// reopen opens the composer again after a Run: empty, with what the next
// Run would use read afresh (the files may have changed during the Run).
func (h *home) reopen() {
	h.run, h.done, h.actions = nil, nil, false
	h.comp = newComposer()
	h.navigating = false
	res, problem := h.resolve(h.sel)
	h.msg, h.setup = problem, nil
	if n := h.themeNotice(); n != "" {
		h.msg = append(h.msg, n)
	}
	if res != nil {
		h.setup = setupLines(res.frozen)
		h.sel.init(res)
	}
}

// themeNotice reloads the custom theme and returns its notice, if any.
func (h *home) themeNotice() string {
	if h.reloadTheme == nil {
		return ""
	}
	return h.reloadTheme()
}

func (h *home) View() tea.View {
	if h.run != nil {
		return h.run.View()
	}
	if h.quit {
		return tea.NewView("")
	}
	frame, x, y := h.render()
	v := tea.NewView(frame)
	c := tea.NewCursor(x, y)
	c.Blink = false
	v.Cursor = c
	if h.wantsMouse() {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

// wantsMouse is true only while the slash menu, the Run picker or the diff
// viewer is open.
// Mouse reporting takes the wheel and selection from the terminal, so it
// is never on while the transcript or the composer is what is on screen.
func (h *home) wantsMouse() bool {
	return h.menu != nil || h.picker != nil || h.diff != nil
}

// wheel turns a wheel tick over an open list into Up or Down. Clicks are
// not handled: an inline frame has no known screen row to map them to.
func (h *home) wheel(msg tea.MouseWheelMsg) tea.Cmd {
	var code rune
	switch msg.Button {
	case tea.MouseWheelUp:
		code = tea.KeyUp
	case tea.MouseWheelDown:
		code = tea.KeyDown
	default:
		return nil
	}
	k := tea.KeyPressMsg{Code: code}
	switch {
	case h.picker != nil:
		return h.pickerKey(k)
	case h.diff != nil:
		return h.diffKey(k)
	case h.menu != nil:
		h.menuKey(k)
	}
	return nil
}

// render is the composer frame, and where the cursor is in it.
func (h *home) render() (string, int, int) {
	st := h.st
	width := h.width
	if width <= 0 {
		width = 80
	}
	inner := max(width-4, 1)
	h.comp.sel, h.comp.selPaint = h.selection(), st.selected
	rows, cr, cc := h.comp.layout(inner)
	blank := len(h.comp.lines) == 1 && len(h.comp.lines[0]) == 0

	var lines []string
	top := 0 // lines above the box
	switch {
	case h.actions:
		lines = append(lines, " "+st.bold("[a] apply  [d] diff  [r] receipt  [b] branch")+"   "+st.dim("esc back"))
		top++
	case h.last != nil:
		lines = append(lines, " "+st.dim("↓ actions · /apply /diff /receipt /branch · /new"))
		top++
	}
	title := " " + h.title + " "
	if _, shell := shellLine(h.comp.expanded()); shell || h.job != nil {
		title = " shell · " + h.title + " " // not a Task: it never reaches an agent
	}
	lines = append(lines, st.dim("╭─")+st.bold(title)+st.dim(strings.Repeat("─", max(width-3-ansi.StringWidth(title), 0))+"╮"))
	for i, r := range rows {
		body := r
		if blank && i == 0 {
			body = st.dim("Describe the task")
			if h.job != nil {
				body = st.dim("running… (ctrl+c to stop)")
			}
		}
		pad := strings.Repeat(" ", max(inner-ansi.StringWidth(ansi.Strip(body)), 0))
		lines = append(lines, st.dim("│")+" "+body+pad+" "+st.dim("│"))
	}
	lines = append(lines, st.dim("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
	if h.vim {
		mode := "-- INSERT --"
		switch {
		case h.normal && h.visual == 'v':
			mode = "-- VISUAL --"
		case h.normal && h.visual == 'V':
			mode = "-- VISUAL LINE --"
		case h.normal:
			mode = "-- NORMAL --"
		}
		lines = append(lines, " "+st.dim(mode))
	}
	for _, l := range h.msg {
		lines = append(lines, " "+st.bad(l))
	}
	if s := h.search; s != nil {
		label, match := "(reverse-i-search)", ""
		if s.hit >= 0 {
			match, _, _ = strings.Cut(h.hist.entries[s.hit], "\n")
		} else if s.query != "" {
			label = "(failed reverse-i-search)"
		}
		lines = append(lines, " "+st.dim(label+"'"+clean(s.query)+"': ")+clean(match)+st.dim("   enter use · esc cancel"))
	}
	if h.note != "" {
		lines = append(lines, " "+st.ok(h.note))
	}
	if h.stash != "" {
		lines = append(lines, " "+st.dim("stashed · ctrl+s restores it on an empty box"))
	}
	switch {
	case h.keys != nil:
		lines = append(lines, h.keysFrame(max(h.height-len(lines), 4))...)
	case h.hidden != nil:
		lines = append(lines, h.hidden.lines(st)...)
	case h.saving:
		lines = append(lines, " "+st.bold("Save as default for this repo?")+" [y]es [n]o"+st.dim("   (your user config only)"))
	case h.review != nil:
		lines = append(lines, " "+st.bold("Save these Checks to "+pipeline.ConfigPath+"?")+st.dim("   the file is written, never committed"))
		for _, l := range h.review.diff {
			switch l[0] {
			case '+':
				l = st.ok(l)
			case '-':
				l = st.bad(l)
			default:
				l = st.dim(l)
			}
			lines = append(lines, "   "+l)
		}
		lines = append(lines, " [y]es write it [n]o")
	case h.panel != nil:
		h.panel.width = width
		h.panel.termHeight = h.height
		if h.height > 0 { // window the panel to what the composer leaves (#212)
			h.panel.height = max(h.height-len(lines), 4)
		}
		lines = append(lines, h.panel.lines()...)
	case h.picker != nil:
		lines = append(lines, h.picker.lines()...)
	case h.diff != nil:
		lines = append(lines, h.diff.lines()...)
	}
	if h.menu != nil {
		lines = append(lines, h.menuLines()...)
	}
	if h.at.list != nil {
		lines = append(lines, h.atLines()...)
	}
	if h.panel == nil && h.keys == nil {
		_, shell := shellLine(h.comp.expanded())
		for i, l := range h.setup {
			if i == len(h.setup)-1 && strings.HasPrefix(l, "[tab]") {
				switch {
				case h.job != nil:
					l = "running… ctrl+c stop"
				case shell:
					l = "[enter] run shell  [ctrl+j] newline"
				}
			}
			lines = append(lines, " "+st.dim(l))
		}
		if h.thread != nil {
			lines = append(lines, " "+st.dim("continuing Run "+h.thread.from.Run+" (/new for a fresh task)"))
		}
		if h.sel != nil && h.sel.noFile {
			lines = append(lines, " "+st.dim("no "+pipeline.ConfigPath+" yet: [tab] shows the proposed Checks; [w] there saves them"))
		}
	}

	var b strings.Builder
	for _, l := range lines {
		b.WriteString(ansi.Truncate(l, width, "…"))
		b.WriteByte('\n')
	}
	return b.String(), cc + 2, cr + 1 + top
}

// closeSetup leaves the panel. The setup bar shows what the next Run will
// use, read again with the choices; the first time something changed, the
// save prompt follows (and never a second time in the session).
func (h *home) closeSetup() {
	rev0 := h.panel.rev0
	h.panel = nil
	h.afterSetup(rev0)
}

// afterSetup reads what the next Run would use again once setup changed
// the choices (the panel closing, or a command).
func (h *home) afterSetup(rev0 int) {
	if h.sel.rev == rev0 {
		return // nothing changed: the bar is as it was
	}
	res, problem := h.resolve(h.sel)
	if problem != nil {
		// What the bar shows must be what the next Run uses: say why it
		// can't start, as Enter would.
		h.msg, h.setup = problem, nil
		return
	}
	h.setup = setupLines(res.frozen)
	h.sel.syncFrom(res)
	if h.sel.changed && !h.sel.asked && h.save != nil {
		h.saving, h.sel.asked = true, true
	}
}

// answerSave takes the save prompt's answer. Only y and n answer it; Esc and
// Ctrl+C mean no. Nothing is written without a y.
func (h *home) answerSave(key string) {
	switch key {
	case "y", "Y":
		h.saving = false
		h.saveNow()
	case "n", "N", "esc", "ctrl+c":
		h.saving = false
	}
}

// reviewProject opens the reviewed save of the Checks into .oge/oge.toml.
func (h *home) reviewProject() {
	r, err := h.sel.reviewProject()
	if err != nil {
		h.msg = []string{clean(err.Error())}
		return
	}
	h.review = r
}

// answerReview takes the review's answer. Only y writes; Esc and Ctrl+C
// mean no.
func (h *home) answerReview(key string) {
	switch key {
	case "y", "Y":
		r := h.review
		h.review = nil
		if err := h.sel.writeProject(r); errors.Is(err, errChanged) {
			h.msg = []string{clean(err.Error()) + ": review it again"}
			h.reviewProject()
			return
		} else if err != nil {
			h.msg = []string{"not saved: " + clean(err.Error())}
			return
		}
		h.note = "Saved " + pipeline.ConfigPath + ". Commit it so every clone uses the same Checks."
		if res, problem := h.resolve(h.sel); problem == nil {
			h.sel.syncFrom(res)
			h.setup = setupLines(res.frozen)
			if h.panel != nil {
				h.panel.rev0 = h.sel.rev
			}
		} else {
			h.msg = problem
		}
	case "n", "N", "esc", "ctrl+c":
		h.review = nil
	}
}

// saveNow writes the session's choices to the user config.
func (h *home) saveNow() {
	if h.save == nil {
		return
	}
	if _, err := h.save(); err != nil {
		h.msg = []string{"not saved: " + clean(err.Error())}
		return
	}
	h.note = "Saved as this repo's default in your user config"
}

// setupLines is the setup bar for a resolved config: what this Run will use
// (read-only until the setup tickets land), the Checks, and the keys.
func setupLines(f *pipeline.Frozen) []string {
	binding := func(role string) string {
		for _, s := range f.Stages {
			if s.Role == role {
				if s.Model != "" {
					return s.Binding() + ":" + s.Model
				}
				return s.Binding()
			}
		}
		return ""
	}
	parts := []string{fmt.Sprint(f.Mode), "implement " + clean(binding("implementer"))}
	if v := binding("verifier"); v != "" {
		parts = append(parts, "verify "+clean(v))
	} else {
		parts = append(parts, "no verifier")
	}
	var checks []string
	edited := false
	fromSetup := ""
	for _, c := range f.Checks {
		checks = append(checks, clean(c.Run))
		edited = edited || c.Source == pipeline.FromTUI
	}
	if edited {
		fromSetup = " (from setup)"
	}
	lines := []string{strings.Join(parts, " · ")}
	for _, w := range f.TrustWeakening {
		lines = append(lines, "! trust-weakening: "+clean(w.Option)+" (from "+sourceName(w.Source)+")")
	}
	if f.TestGlobsSource == pipeline.FromTUI {
		lines = append(lines, "Test globs: "+clean(strings.Join(f.Project.TestGlobs, " · "))+" (from setup)")
	}
	return append(lines,
		"Checks: "+strings.Join(checks, " · ")+fromSetup,
		"[tab] setup  [enter] run  [ctrl+j] newline  [ctrl+e] editor",
	)
}

// homeTitle is the composer's title: the repository and its branch.
func homeTitle(env Env, root string) string {
	title := "oge · " + clean(filepath.Base(root))
	cmd := exec.Command("git", "-C", root, "symbolic-ref", "--short", "-q", "HEAD")
	if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		title += " · " + clean(strings.TrimSpace(string(out)))
	}
	return title
}

// editorCmd runs $EDITOR on the Task inside the program: Bubble Tea hands
// over the terminal and takes it back.
type editorCmd struct {
	run  func() error
	hold func(bool) // see home.hold
}

func (e editorCmd) Run() error {
	if e.hold != nil {
		e.hold(true)
		defer e.hold(false)
	}
	// Bubble Tea (v2.0.10 and v2.1.0) stops its frame ticker from the old
	// renderer goroutine after releasing the terminal, and restarts that same
	// ticker when it takes the terminal back. If the command returns before
	// that goroutine has run (an editor or a pager that exits at once, a busy
	// machine), the stop lands after the restart and the screen never
	// repaints again. Sleeping here lets that goroutine stop the ticker first.
	time.Sleep(rendererSettle)
	return e.run()
}

// rendererSettle is how long a terminal hand-over waits for Bubble Tea's
// old renderer to stop: about a frame, unnoticed before an editor or pager.
const rendererSettle = 20 * time.Millisecond

func (e editorCmd) SetStdin(io.Reader)  {}
func (e editorCmd) SetStdout(io.Writer) {}
func (e editorCmd) SetStderr(io.Writer) {}

// editInEditor opens text in env.Edit, with the template's comments when the
// box is empty, and returns what the human left, comments stripped.
func editInEditor(env Env, text string, hold func(bool)) tea.Cmd {
	var out string
	return tea.Exec(editorCmd{hold: hold, run: func() error {
		tmp, err := os.CreateTemp("", "oge-task-*.md")
		if err != nil {
			return err
		}
		path := tmp.Name()
		defer os.Remove(path)
		if strings.TrimSpace(text) == "" {
			text = task.EditorTemplate
		}
		_, werr := tmp.WriteString(text)
		if cerr := tmp.Close(); werr != nil || cerr != nil {
			return fmt.Errorf("writing the Task: %v %v", werr, cerr)
		}
		if err := env.Edit(path); err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		out = strings.TrimRight(task.StripComments(string(b)), "\n ")
		return err
	}}, func(err error) tea.Msg { return editedMsg{text: out, err: err} })
}

// home runs the session: the composer and, on Enter, the Run in the same
// program, then the summary and the composer again, until the human leaves
// it or Öge is interrupted while composing. code is the last Run's exit
// code. res is the config as of now (nil when problem says why it can't be
// resolved); resolve reads it again when a Task is sent.
func (u *tui) home(env Env, root, histDir string, startFor func(task.Task, *resolved, *run.FollowUp) startFunc,
	res *resolved, problem []string, resolve func(*setupState) (*resolved, []string), sel *setupState) (code int, err error) {
	// A signal while composing ends the session; during a Run it goes to
	// the Run (forwardTo), as Ctrl+C does.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := newInterrupts(cancel)
	defer in.watch(os.Interrupt, syscall.SIGTERM)()

	h := &home{comp: newComposer(), hist: loadHistory(historyPath(histDir, root)), title: homeTitle(env, root), st: u.m.st, ctx: ctx, resolve: resolve, msg: problem, sel: sel, notify: u.notify, tmux: env.Getenv("TMUX") != "", vim: u.vim, vimState: vimState{vimEscape: u.vimEscape}}
	if histDir != "" {
		h.exportDir = filepath.Join(filepath.Dir(histDir), "exports")
	}
	h.getenv = env.Getenv
	h.shell, h.root = runShell, root
	if histDir != "" {
		h.shellHist = loadHistory(shellHistoryFile(histDir, root))
	}
	h.reloadTheme = func() string { return h.st.reloadCustomTheme(env.Getenv) }
	if n := h.themeNotice(); n != "" {
		h.msg = append(h.msg, n)
	}
	if res != nil {
		h.setup = setupLines(res.frozen)
		sel.init(res)
	}
	h.save = sel.save
	u.noFallback = true
	var (
		runIn     *interrupts // the running (or last) Run's
		runCancel context.CancelFunc
		running   chan struct{} // closed once the Run has ended
		used      *resolved     // the Run's config
		failed    *ended        // Öge itself failed or panicked
		cur       task.Task     // the Run's Task, with its follow-ups
	)
	last := ExitOK
	h.hold = func(on bool) {
		if on {
			in.forwardTo(newInterrupts(func() {}))
		} else {
			in.forwardTo(nil)
		}
	}
	h.start = func(t task.Task, r *resolved, w, ht int) (*model, tea.Cmd) {
		var runCtx context.Context
		runCtx, runCancel = context.WithCancel(context.Background())
		runIn, used, running = newInterrupts(runCancel), r, make(chan struct{})
		in.forwardTo(runIn)
		u.m = newModel(t, r.frozen, u.m.st, time.Now)
		u.m.notify = u.notify
		u.m.width, u.m.height = w, ht
		u.m.scroll = true
		var fu *run.FollowUp
		if h.thread != nil && !r.flags.unattended {
			from := h.thread.from
			fu = &from
			u.m.title = task.Parse(t.FollowUps[len(t.FollowUps)-1]).Title // the echo is the message
		}
		cur = t
		u.plain.frozen, u.plain.warn = r.frozen, r.warn
		u.plain.applying = r.flags.apply
		h.done = u.launch(runCtx, runIn, startFor(t, r, fu))
		go func(forced, ended <-chan struct{}) {
			select {
			case <-forced:
				u.kill()
			case <-ended:
			}
		}(runIn.forced, running)
		return u.m, u.m.Init()
	}
	h.after = func(e ended) (string, *lastRun, bool) {
		close(running)
		in.forwardTo(nil)
		runCancel()
		u.started = false // no Run is live: a forced stop has nothing to kill
		if e.panicked != nil || e.err != nil {
			failed = &e
			return "", nil, true
		}
		var text string
		var l *lastRun
		text, l, last = u.summary(env, used.flags, root, e.res, h.hold)
		h.thread = nextThread(h.thread, cur, e.res, used.flags)
		return text, l, false
	}
	h.deliver, h.runs, h.label = deliverer(env), recentRuns(env, root), labeller(env, root)
	h.diffOf = diffLoader(env)
	h.listFiles = func() ([]string, error) { return workspace.TrackedFiles(root) }
	if env.Edit != nil {
		h.edit = func(text string) tea.Cmd { return editInEditor(env, text, h.hold) }
	}
	err = u.program(in, h)
	select {
	case <-in.forced:
		return 0, errForced
	default:
	}
	if runIn != nil {
		select {
		case <-runIn.forced:
			return 0, errForced
		default:
		}
	}
	if failed != nil {
		if failed.panicked != nil {
			panic(fmt.Sprintf("%v\n\n%s", failed.panicked, failed.stack))
		}
		return 0, failed.err
	}
	if h.run != nil && running != nil {
		select {
		case <-running:
		default:
			// The view stopped during a Run: the Run goes on, and its
			// summary follows in plain lines.
			out, err := u.finish(runIn, err, h.done)
			close(running)
			if err != nil {
				return 0, err
			}
			if code := afterRun(env, used.flags, u.plain, root, out); code != ExitOK {
				return code, nil
			}
			return outcomeCode(env, out), nil
		}
	}
	return last, err
}

// summary is what follows a Run in the home: its Receipt, then what afterRun
// does, as text to print above the program. After an Accepted Run that the
// human takes, what can take it comes too. code is the Run's exit code.
func (u *tui) summary(env Env, f runFlags, root string, res *run.Result, hold func(bool)) (string, *lastRun, int) {
	var buf strings.Builder
	receiptText := func() string {
		var b strings.Builder
		p := *u.plain
		p.w = &b
		p.endScreen(res)
		return b.String()
	}
	buf.WriteString(receiptText())
	quiet := env
	quiet.Stdout, quiet.Stderr = &buf, &buf
	if res.Outcome != run.Accepted || f.apply || f.unattended {
		// u.plain as the view: an unattended Run prints the commands, as
		// plain lines do.
		code := afterRun(quiet, f, u.plain, root, res)
		if code == ExitOK {
			code = outcomeCode(quiet, res)
		}
		return buf.String(), nil, code
	}
	r, err := delivery.Load(res.Dir)
	if err != nil {
		fmt.Fprintf(&buf, "oge: reading the Run for delivery: %s\n", clean(err.Error()))
		return buf.String(), nil, ExitInternal
	}
	// Once applied the Receipt lists the Delivery, so it says nothing about
	// what was written itself.
	deliver := func(do func(Env) int) func() string {
		return func() string {
			var b strings.Builder
			q := env
			q.Stdout, q.Stderr = &b, &b
			do(q)
			return b.String()
		}
	}
	s := &lastRun{
		id:    r.ID,
		apply: deliver(func(q Env) int { return applyCommand(q, r, root, delivery.Choice{}) }),
		branch: func(name string) string {
			return deliver(func(q Env) int { return branchCommand(q, r, root, name, delivery.Choice{}) })()
		},
		receipt: receiptText,
	}
	s.diff = func() tea.Cmd {
		patch, err := delivery.Diff(r)
		switch {
		case err != nil:
			return tea.Println("oge: " + clean(err.Error()))
		case len(patch) == 0:
			return tea.Println(fmt.Sprintf("Candidate %s changes nothing since the Snapshot.", delivery.Short(r.Candidate)))
		case env.Page == nil:
			return tea.Println(strings.TrimSuffix(diffText(env, r, patch), "\n"))
		}
		// The pager has the terminal (released), and its Ctrl+C (hold).
		return tea.Exec(editorCmd{hold: hold, run: func() error { return env.Page(diffText(env, r, patch)) }}, func(err error) tea.Msg {
			if err != nil {
				return pageFailedMsg{err}
			}
			return nil
		})
	}
	return buf.String(), s, ExitOK
}

// pageFailedMsg says the pager failed to show the diff.
type pageFailedMsg struct{ err error }
