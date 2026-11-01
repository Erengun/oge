package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/herdr"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
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
	kb       *keyMap // the session's keys (nil: the defaults)
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

// composer is a new, empty box on this session's keys.
func (h *home) composer() *composer {
	c := newComposer()
	c.kb = h.kb
	return c
}

// setKeys swaps in a reloaded keyMap whole; the boxes in use follow it.
func (h *home) setKeys(kb *keyMap) {
	h.kb = kb
	for _, v := range h.views {
		v.comp.kb = kb
	}
	h.comp.kb = kb
	if h.next != nil {
		h.next.comp.kb = kb
	}
}

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
	switch c.kb.resolve(ctxComposer, k.String()) {
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
	case actNewline:
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
	kb *keyMap // this session's keys (nil: the defaults); swapped whole on reload
	// settling holds the printing back after a Gate closed (#233): held
	// is what finished meanwhile, already out of the frame, and heldEnd
	// the Run's end that arrived meanwhile.
	settling bool
	held     []string
	heldEnd  *runEndedMsg
	// next is the Task typed during a Run (tui_queue.go); queuedText is what
	// moved into the composer when the Run ended, while it is untouched.
	next       *queued
	queuedText string
	gateArm    time.Time // when a Gate opened while a next Task was being typed
	// getenv reads the environment for /terminal; nil reads nothing.
	getenv func(string) string
	// vim is the composer's vim mode (#156); normal is its NORMAL state.
	vim, normal bool
	vimState    // the pending operator, count and last change (tui_vim_ops.go)
	// nav is the history entry ↑ and ↓ show (the repo's, repoCtx.hist);
	// draft is what was in the box before the first ↑. search is Ctrl+R's
	// search while it is open.
	nav        int // the entry shown; meaningful only while navigating
	navigating bool
	draft      string
	// cleared is the Task Ctrl+C last cleared: ↑ on the then-empty box
	// brings it back, before any history entry.
	cleared string
	search  *histSearch
	// shell runs a ! command (#160) in the focused Chat's root; job is
	// the command in flight.
	shell func(ctx context.Context, root, line string) shellResult
	job   *shellJob
	// stash is Ctrl+S's one slot: a draft put aside for this session.
	stash string
	// hidden is the confirmation of a Task that held hidden characters.
	hidden *hiddenPrompt
	st     styles
	// start begins the Run for a Task in this program: it returns the Run's
	// model and its first command.
	start func(task.Task, *resolved, int, int) (*model, tea.Cmd)
	// reloadTheme re-reads the custom theme file (#175) and the keybindings
	// file (#163); it returns a notice when one is bad. Called when the composer opens and on Ctrl+L,
	// never during a Run.
	reloadTheme func() string
	// The setup panel edits the focused Chat's repo's sel (repoCtx).
	panel  *setupPanel    // non-nil while Tab's setup is open
	saving bool           // the save prompt waits for y or n
	review *projectReview // the reviewed save into .oge/oge.toml waits for y or n
	note   string         // what happened, shown under the box (a save)
	ctx    context.Context
	// exiting is closed once Öge is exiting: a hangup or SIGTERM came, or
	// the view ended. A Gate still waiting then parks.
	exiting <-chan struct{}
	// msg is what stops or went wrong, shown under the box.
	msg  []string
	edit func(text string) tea.Cmd // Ctrl+E: $EDITOR on the Task
	// menu is the slash menu while it is open: it lists the commands that
	// complete menuQuery. Esc closes it for the text it was closed on.
	menu   *list.Model
	picker *runPicker  // /runs: the Run picker while it is open (#176)
	chats  *list.Model // /chats: the Chat picker while it is open
	// diff is the /diff viewer while it is open (#169).
	diff        *diffViewer
	menuQuery   string
	menuOff     bool
	menuOffText string
	// at is the @file picker (#146).
	at atState
	// armText and armAt: a delivery command the menu completed runs only on
	// an Enter that comes actionsArm later, so a double tap never delivers.
	armText string
	armAt   time.Time

	width, height int
	// full: the home draws on the alternate screen (tui_fullscreen.go).
	// width and height are then the main column's; termW and termH are the
	// terminal's. toast is the notice passing over the transcript.
	full         bool
	termW, termH int
	sidebarOff   bool
	// railHits are where the rail's last frame drew each Chat, for a
	// click (tui_desk.go).
	railHits []railHit
	// deskMax and deskQuiet are [desk] max_active and quiet (0: the
	// defaults); capNoted is /new having said once that the working
	// Chats are past deskMax.
	deskMax   int
	deskQuiet time.Duration
	capNoted  bool
	// redraw schedules a redraw (tea.Tick); nil schedules none, as in
	// tests that drive the home by hand.
	redraw   func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	toast    *toast
	toastSeq int
	run      *model    // nil while composing
	notify   *notifier // the session's alerts; nil when silent (#170)
	// exportDir is where /export goes ("" is the working directory); tmux
	// wraps /copy's OSC 52.
	exportDir string
	tmux      bool
	// shown is the rows of the last frame View handed the renderer: the
	// frame on screen when the next print is made (#188).
	shown int
	keys  *keyPanel // the ? panel, when open (#155)
	quit  bool
	// quitArmed: Ctrl+C would have quit with another Chat working, and
	// said so; a second Ctrl+C, the very next key, quits (leave).
	quitArmed bool

	// done delivers the Run's end once its view has finished; nil in tests.
	done <-chan ended
	// after takes the Run's end: it returns the summary to print and, after
	// an Accepted Run, what can take it. stop ends the session (Öge failed).
	after func(ended) (text string, last *lastRun, stop bool)
	// hold, while true, swallows interrupts: a pager or $EDITOR has the
	// terminal, and its Ctrl+C is its own (nil in tests). now is the
	// clock; nil is time.Now.
	hold func(bool)
	now  func() time.Time

	// *chatView is the focused Chat's state (tui_chatview.go): its fields
	// read as the home's own, its repo's too (repoCtx). views are the
	// session's open Chats, in the order they opened; closed are the Chats
	// /close closed, which quitting waits for. repoFor opens the repo at a
	// root for /new <path>: the same repoCtx for the same repo.
	*chatView
	views   []*chatView
	closed  []*homeChat
	repoFor func(root string) (*repoCtx, []string)
	// background: the message being handled is for a Chat without the
	// focus (route).
	background bool
	// dropped counts messages for a Chat no longer in views.
	dropped int
	// parked is what other Chats brought while the human typed, released
	// as one toast when the box is sent or cleared (attention.go).
	// deskStale is [desk] stale_gate (0: the default). herdr is told the
	// Desk's state, herdrState, when no Run reports its own.
	parked     []parked
	deskStale  time.Duration
	herdr      herdr.Reporter
	herdrState herdr.State
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
	branch  func(name string) string                // "" is the default name
	diff    func(emit func(string) tea.Cmd) tea.Cmd // printed, or paged with $PAGER
	receipt func() string
	// copy is the Receipt for /copy: unwrapped, so the clipboard has no hard
	// breaks. Nil means receipt.
	copy func() string
	// full is /receipt's: the Receipt with its dismissed Consultations. Nil
	// means receipt.
	full func() string
	// patch is the Candidate's diff from the Snapshot, for the Chat's
	// inline diff.
	patch func() ([]byte, error)
	// delivered, when set, is what apply or branch (how) shows, given
	// what the command printed and its exit code: a Chat's cut marks its
	// delivery there.
	delivered func(how, out string, code int) string
}

// act runs one of the Run's actions by its key; arg is a branch's name.
// emit puts text above the program.
func (l *lastRun) act(key, arg string, emit func(string) tea.Cmd) tea.Cmd {
	out := func(s string) tea.Cmd { return emit(strings.TrimSuffix(s, "\n")) }
	switch key {
	case "a":
		return out(l.apply())
	case "b":
		return out(l.branch(arg))
	case "d":
		return l.diff(emit)
	case "r":
		if l.full != nil {
			return out(l.full())
		}
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
	case res.Outcome == run.Refused, res.Outcome == run.Replied:
		// A Replied Run changed nothing: it never becomes the thread's tip.
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
// summary if it ended meanwhile. What it prints left the frame when it was
// held, so the frame on screen doesn't change with the print: a print that
// shrinks the frame in the same update can leave a row of the old frame
// above it (#233).
func (h *home) settled() (tea.Model, tea.Cmd) {
	h.settling = false
	var print tea.Cmd
	if lines := append(h.held, h.run.take()...); len(lines) > 0 {
		print = h.println(strings.Join(lines, "\n"))
	}
	h.held = nil
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
	if _, ok := msg.(staleTickMsg); ok {
		return h, h.attend()
	}
	m, cmd := h.routed(msg)
	return m, withTick(cmd, h.attend())
}

func (h *home) routed(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A Chat's message is handled with that Chat focused, the whole update
	// through; one for a Chat the home no longer has is dropped.
	restore, ok := h.route(msg)
	if !ok {
		return h, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() != "ctrl+c" {
		h.quitArmed = false // only the very next key quits
	}
	if h.full {
		if cmd, ok := h.fullUpdate(msg); ok {
			cmd = withTick(cmd, h.deskTick())
			restore()
			return h, cmd
		}
	}
	m, cmd := h.update(msg)
	cmd = withTick(cmd, h.deskTick())
	if h.adviceEdit != nil && h.comp != nil && h.comp.empty() {
		h.dropAdviceEdit() // emptied: what is typed next isn't the advice
	}
	if flush := h.flushAdvice(); flush != nil {
		cmd = tea.Sequence(cmd, flush) // after whatever this update prints
	}
	// The menu and the @ picker are the focused composer's.
	restore()
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
		if h.full {
			h.termW, h.termH = ws.Width, ws.Height
			h.layout()
			msg = tea.WindowSizeMsg{Width: h.width, Height: h.height}
		}
		if h.diff != nil {
			h.diff.resize(h.width)
		}
	}
	switch m := msg.(type) {
	case chatCutMsg:
		return h, h.cutEnded(m) // in the background: whatever else is on
	case chatStepMsg:
		return h, h.cutStepped(m)
	case deskTickMsg:
		if h.tickAt.Equal(m.at) {
			h.tickAt = time.Time{} // drawn again now; Update asks for the next
		}
		return h, nil
	case verifyTickMsg:
		if h.verify != nil && h.verify.seq == m.seq {
			return h, verifyTick(h.chatView, m.seq)
		}
		return h, nil
	case chatGateMsg:
		return h, h.cutGated(m)
	case sideChangesMsg:
		if h.last != nil && h.last.id == m.id {
			h.side.changes = m.files
		}
		return h, nil
	case diffEntryMsg:
		return h, h.diffEntry(m.text)
	case chatDiffMsg:
		lines := m.lines
		for i, l := range lines {
			lines[i] = h.st.diffLine(l)
		}
		return h, h.println(strings.Join(lines, "\n"))
	case chatClosedMsg:
		h.resetting = false
		return h, nil
	case adviceMsg:
		return h, h.adviceLanded(m)
	case askAdviceTickMsg:
		if t := h.turn; t != nil && t.askAdv != nil && t.askAdv.running && t.askAdv.ask == m.ask && t.askAdv.q == m.q && t.askAdv.seq == m.seq {
			return h, askAdviceTick(h.chatView, m.ask, m.q, m.seq)
		}
		return h, nil
	case askAdviceMsg:
		return h, h.askAdviceLanded(m)
	case adviceTickMsg:
		if h.adv != nil && h.adv.running && h.adv.seq == m.seq {
			return h, adviceTick(h.chatView, m.seq)
		}
		return h, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && h.chats != nil {
		// Over whatever the Chat shows: switching decides nothing.
		h.chatsKey(k)
		return h, nil
	}
	if h.turn != nil {
		return h.chatUpdate(msg)
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && h.stale != nil {
		return h, h.staleKey(k)
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && h.cutGate != nil {
		return h, h.cutGateKey(k)
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
		if h.queueKey(msg) {
			return h, nil
		}
		was := h.run.finished
		gated := h.run.gate != nil
		_, cmd := h.run.Update(msg)
		alert := h.run.flushAlerts()
		if !gated && h.run.gate != nil && h.next != nil && !h.next.comp.empty() {
			h.gateArm = h.clock() // a Gate opened under the human's typing: see queueKey
		}
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
		// While settling it leaves the frame now and is printed once the
		// frame has been drawn without it.
		var print tea.Cmd
		switch _, batch := msg.(batchMsg); {
		case h.settling:
			h.held = append(h.held, h.run.take()...)
		case batch:
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
		case h.saving, h.review != nil, h.search != nil, h.picker != nil, h.chats != nil, h.diff != nil, h.hidden != nil:
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
		h.seen = true // a key in this Chat: its ✓ has been seen
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
		if cmd, ok := h.adviceKey(msg); ok {
			return h, cmd
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
		switch h.kb.resolve(ctxComposer, msg.String()) {
		case actHistorySearch:
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
			// Ctrl+C cancels a cut verifying, then clears a Task (↑ brings
			// it back) and, on an empty box, leaves (as Claude Code's
			// prompt does). h.verify is the focused Chat's: a Chat in the
			// background goes on verifying.
			if v := h.verify; v != nil && !v.stopped && v.gate == nil {
				// One waiting at its Gate isn't verifying: it goes with
				// the Chat.
				v.stopped = true
				v.cancel()
				return h, nil
			}
			if h.comp.empty() {
				if !h.leave() {
					return h, nil
				}
				h.quit = true
				return h, tea.Quit
			}
			h.cleared = h.comp.expanded()
			h.comp.set("")
			h.dropAdviceEdit()
			h.navigating = false
			h.visual = 0
		case actSubmit:
			if h.comp.backslashNewline() {
				h.endNavigation()
				return h, nil
			}
			if a := strings.TrimSpace(task.StripComments(h.comp.expanded())); h.armText != "" && a == h.armText && h.clock().Sub(h.armAt) < actionsArm {
				return h, nil // typed ahead of the completion: no path (shell, slash, Task) takes it
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
		case actStash:
			h.stashDraft()
		case actRedraw:
			if n := h.themeNotice(); n != "" {
				h.msg = []string{n}
			}
			return h, tea.ClearScreen
		case actEditor:
			if h.edit != nil {
				return h, h.edit(h.comp.expanded())
			}
		case actKeysPanel:
			if !h.openKeys() {
				h.comp.key(msg)
				h.endNavigation()
			}
		case "tab":
			if p := h.openSetup(); p != "" {
				h.msg = []string{p}
			}
		case actCycleMode:
			// Standard <-> Fast through the setup state, as /mode does; Blind
			// is not in the cycle. A menu owns its keys; a Run never gets here.
			if h.menu == nil && h.at.list == nil {
				h.vimState.cancel() // a pending operator or count ends here
				if p := h.cycleMode(); p != "" {
					h.msg = []string{p}
				}
			}
		case actModel:
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
			if (h.last != nil || h.pendingFB != nil) && h.composerFree() {
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
	if h.full {
		h.tr.latest()
	}
	h.cleared, h.queuedText = "", ""
	if e := h.adviceEdit; e != nil && h.chat != nil && h.chat.get() == e.chat && strings.TrimSpace(text) == strings.TrimSpace(e.text) {
		// Sent as it was: still the advice turn, outside the Task.
		h.adviceEdit = nil
		if cmd := h.sendAdviceTurn(e.chat, &run.Advice{ID: e.id, Text: e.text}, false, true); cmd != nil {
			return h, cmd
		}
		return h, nil
	}
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
	h.setSetup(res.frozen)
	h.sel.syncFrom(res)
	h.at.files, h.at.filesReady = nil, false // listed again for the next composer
	h.hist.add(text)                         // what the human typed, never the agent's output
	if h.name == "" {
		h.name = task.Parse(text).Title
	}
	h.comp.clearUndo()
	h.navigating = false
	h.visual = 0
	if h.chat.fits(res) {
		return h.chatSubmit(text, res, nil)
	}
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
	h.dropAdviceEdit()
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
	h.dropAdviceEdit() // history replaces the box
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
	if h.kb.actionFor(ctxComposer, msg.String()) == actHistorySearch { // again: older
		if s.hit > 0 {
			if i := h.hist.find(s.query, s.hit-1); i >= 0 {
				s.hit = i
			}
		}
		return
	}
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+g":
		h.search = nil
	case "enter":
		if s.hit >= 0 {
			h.comp.set(h.hist.entries[s.hit])
			h.navigating = false
		}
		h.search = nil
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
	case "s":
		if h.pendingFB == nil {
			break // nothing to send back: an ordinary key
		}
		if h.clock().Sub(h.actionsAt) < actionsArm || !h.composerFree() {
			return h, nil // typed ahead, before the row could be seen
		}
		h.actions = false
		cmd, _ := h.sendBack()
		return h, cmd
	case "a", "b", "d", "r":
		if h.clock().Sub(h.actionsAt) < actionsArm || !h.composerFree() || h.last == nil {
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
//
// It is the newest cut whose Verdict is in, never one still verifying,
// and says so when a newer one is.
// TODO(#261): ask apply verified / wait.
func (h *home) act(key, arg string) tea.Cmd {
	if key == "a" || key == "b" {
		if v := h.verify; v != nil && v.gate != nil {
			// The newest cut waits at its Gate: delivered only through it.
			return h.openCutGate(key, arg)
		}
		if h.last != nil && (h.verify != nil || h.unchecked) {
			// Newer edits than the verified cut: delivering it is a choice.
			h.stale = &staleAsk{key: key, arg: arg, last: h.last}
			return nil
		}
	}
	return h.actNow(key, arg)
}

// actNow is act once any stale-edit choice is made.
func (h *home) actNow(key, arg string) tea.Cmd {
	var notice tea.Cmd
	if h.verify != nil && h.last != nil {
		notice = h.println(h.ogeLine(h.st.dim("a newer cut is still verifying; this is the last verified one")))
	}
	if key == "a" {
		h.thread = nil
	}
	if key == "d" && h.last != nil && h.openDiff(h.last.id) {
		return notice // the viewer; the pager is its fallback
	}
	cmd := h.last.act(key, arg, h.fit)
	if h.recut {
		// Refused for a late Tamper event: its turns are cut again.
		h.recut = false
		if h.chat != nil && h.cutFrozen != nil && h.turn == nil {
			cmd = tea.Sequence(cmd, h.cutWith(h.cutFrozen, 0))
		}
	}
	return tea.Sequence(notice, cmd)
}

// onEnded prints the Run's summary above the program (its stages are
// printed already) and opens the composer again.
func (h *home) onEnded(e ended) (tea.Model, tea.Cmd) {
	text, last, stop := h.after(e)
	if stop {
		h.quit = true
		return h, tea.Quit
	}
	if last != nil || e.res == nil || e.res.Outcome != run.Replied {
		// A Replied turn leaves the earlier Run's actions alone: its
		// verified Candidate can still be applied (#254).
		h.last = last
	}
	if h.ctx != nil && h.ctx.Err() != nil {
		// A signal ended the session during the Run (its ctxDoneMsg went to
		// the Run's view): the session ends once the Run has.
		h.quit = true
		return h, tea.Sequence(h.println(text), tea.Quit)
	}
	h.reopen()
	h.takeQueue()
	return h, h.println(text)
}

// reopen opens the composer again after a Run: empty, with what the next
// Run would use read afresh (the files may have changed during the Run).
func (h *home) reopen() {
	if h.background {
		// The Chat's own box only: the lines under the box, the setup bar
		// and the history walk are the focused Chat's.
		h.actions = false
		h.comp = h.composer()
		return
	}
	h.run, h.done, h.actions = nil, nil, false
	h.comp, h.queuedText = h.composer(), ""
	h.navigating = false
	res, problem := h.resolve(h.sel)
	h.msg, h.setup = problem, nil
	if n := h.themeNotice(); n != "" {
		h.msg = append(h.msg, n)
	}
	if res != nil {
		h.setSetup(res.frozen)
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
	if h.full && h.termW > 0 && h.width != h.mainWidth() {
		h.layout() // a Chat opened or closed: the rail came or went
	}
	v := h.view()
	if h.full {
		return h.compose(v)
	}
	h.shown = rows(v.Content)
	return v
}

func (h *home) view() tea.View {
	if h.chats != nil {
		return tea.NewView(strings.Join(append([]string{"", " " + h.st.bold("Chats")}, h.chatsLines()...), "\n"))
	}
	if h.turn != nil {
		return tea.NewView(h.turnView())
	}
	if h.stale != nil {
		return tea.NewView(h.ogeLine("newer edits, unchecked: " + h.st.bold("[a] "+h.stale.verb()+" verified  [w] wait")))
	}
	if h.cutGate != nil {
		return tea.NewView(strings.Join(gateView(h.st, h.cutGate), "\n"))
	}
	if h.run != nil {
		return h.queueView()
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

// boxLines is the composer's box, drawn at width, with the cursor's row in
// it and its column; blank is shown in an empty box. The idle composer and
// a turn's live area share it.
func (h *home) boxLines(width int, blank string) (lines []string, curRow, curCol int) {
	if h.full {
		return h.fullBox(width, blank)
	}
	st := h.st
	inner := max(width-4, 1)
	h.comp.sel, h.comp.selPaint = h.selection(), st.selected
	rows, cr, cc := h.comp.layout(inner)
	empty := len(h.comp.lines) == 1 && len(h.comp.lines[0]) == 0
	title := " " + h.title + " "
	if _, shell := shellLine(h.comp.expanded()); shell || h.job != nil {
		title = " shell · " + h.title + " " // not a Task: it never reaches an agent
	}
	// Keep both corners intact when the repository or branch is long.
	// The composer, rather than its metadata, owns the terminal width.
	title = ansi.Truncate(title, max(width-4, 0), "…")
	lines = append(lines, st.border("╭─")+st.bold(title)+st.border(strings.Repeat("─", max(width-3-ansi.StringWidth(title), 0))+"╮"))
	for i, r := range rows {
		body := r
		if empty && i == 0 {
			body = blank
			if h.job != nil {
				body = st.dim("running… (ctrl+c to stop)")
			}
		}
		body = ansi.Truncate(body, inner, "…")
		pad := strings.Repeat(" ", max(inner-ansi.StringWidth(ansi.Strip(body)), 0))
		lines = append(lines, st.border("│")+" "+body+pad+" "+st.border("│"))
	}
	lines = append(lines, st.border("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
	return lines, cr, cc
}

// render is the composer frame, and where the cursor is in it.
func (h *home) render() (string, int, int) {
	st := h.st
	width := h.width
	if width <= 0 {
		width = 80
	}
	var lines []string
	top := 0 // lines above the box
	if h.full {
		lines, top = append(lines, ""), 1 // a row of air between the transcript and the box
	}
	switch {
	case h.actions:
		keys := "[a] apply  [d] diff  [r] receipt  [b] branch"
		if h.pendingFB != nil {
			keys += "  [s] send back"
		}
		lines = append(lines, " "+st.bold(keys)+"   "+st.dim("esc back"))
		top++
	case h.pendingFB != nil && h.turn == nil && h.verify == nil:
		lines = append(lines, " "+st.dim("✗ Check failed · ↓ then s send back · /feedback · /advise"))
		top++
	case h.resetting:
		lines = append(lines, " "+st.dim("starting a new Chat…"))
		top++
	case h.full && h.verify != nil:
		// the status inside the box's border says it
	case h.verify != nil && h.verify.stopped:
		lines = append(lines, " "+st.dim("cancelling verification…"))
		top++
	case h.verify != nil && h.verify.gate != nil:
		lines = append(lines, " "+st.dim(files(h.verify.changed)+" · waiting for you at its Gate · /apply"))
		top++
	case h.verify != nil:
		step := h.verify.step
		if step == "" {
			step = "verifying…"
		}
		lines = append(lines, " "+st.dim(files(h.verify.changed)+" · "+step+" · "+elapsed(h.clock().Sub(h.verify.since))+" · ctrl+c to cancel"))
		top++
	case h.last != nil:
		lines = append(lines, " "+st.dim("↓ actions · /apply /diff /receipt /branch · /new"))
		top++
	}
	if s := h.adviseStatus(); s != "" {
		lines = append(lines, s)
		top++
	}
	if s := h.adviceKeys(); s != "" {
		lines = append(lines, s)
		top++
	}
	placeholder := "Describe the task"
	if h.chat != nil {
		placeholder = "Ask a question or describe a change"
	}
	if h.full && h.menu != nil {
		pop := h.menuLinesFull()
		lines = append(lines, pop...)
		top += len(pop) // the popup sits above the box
	}
	if h.full && h.at.list != nil {
		pop := h.atLines()
		lines = append(lines, pop...)
		top += len(pop)
	}
	box, cr, cc := h.boxLines(width, st.dim(placeholder))
	lines = append(lines, box...)
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
	if h.menu != nil && !h.full {
		lines = append(lines, h.menuLines()...)
	}
	if h.at.list != nil && !h.full {
		lines = append(lines, h.atLines()...)
	}
	if h.panel == nil && h.keys == nil {
		_, shell := shellLine(h.comp.expanded())
		for i, l := range h.setup {
			if h.full && (i == 0 || i == len(h.setup)-1 && strings.HasPrefix(l, "[tab]")) {
				continue // the status is in the box's border
			}
			if i == 0 && h.chat != nil {
				l = strings.Replace(l, "implement ", "", 1)
			}
			// Commands belong in setup; keep deliberate edits visible until
			// the next task so changing a Check never becomes silent.
			if strings.HasPrefix(l, "Checks:") && !strings.Contains(l, "(from setup)") {
				continue
			}
			if i == len(h.setup)-1 && strings.HasPrefix(l, "[tab]") {
				submit := keyBinding{act: "composer.submit"}.label(h.kb)
				action := "run"
				if h.chat != nil {
					action = "send"
				}
				l = "[" + submit + "] " + action + "  [tab] setup  /help"
				if width < 40 {
					l = submit + " " + action + " · tab setup"
				}
				switch {
				case h.job != nil:
					l = "running… ctrl+c stop"
				case shell:
					l = "[" + submit + "] run shell  [ctrl+j] newline"
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
	h.setSetup(res.frozen)
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
			h.setSetup(res.frozen)
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
//
// first is the repo oge started in, the first Chat's; res is its config
// (nil: it didn't resolve, for problem). repoFor opens another repo for
// /new <path>.
func (u *tui) home(env Env, histDir string, first *repoCtx, res *resolved, problem []string,
	repoFor func(root string) (*repoCtx, []string)) (code int, err error) {
	// A signal while composing ends the session; during a Run it goes to
	// the Run (forwardTo), as Ctrl+C does.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := newInterrupts(cancel)
	u.exiting = make(chan struct{})
	in.exit = u.exit
	if histDir != "" && env.Stdout == io.Writer(os.Stdout) {
		// After a hangup there is no terminal: what the cleanup prints
		// goes to the state root's runner.log instead.
		private := filepath.Dir(histDir)
		in.hangup = func() { _ = redirectStdio(filepath.Join(private, "runner.log")) }
	}
	defer in.watch(os.Interrupt, syscall.SIGTERM, hangupSignal)()

	h := &home{chatView: &chatView{comp: newComposer(), tr: &transcript{}, repoCtx: first}, st: u.m.st, ctx: ctx, msg: problem, notify: u.notify, tmux: env.Getenv("TMUX") != "", vim: u.vim, vimState: vimState{vimEscape: u.vimEscape}, deskMax: u.deskMax, deskQuiet: u.deskQuiet, deskStale: u.deskStale, herdr: u.herdr, redraw: tea.Tick}
	h.chatView.num = 1
	h.chatView.focusAt = h.clock() // the first Chat has the focus from the start
	h.views = []*chatView{h.chatView}
	if histDir != "" {
		h.exportDir = filepath.Join(filepath.Dir(histDir), "exports")
	}
	h.getenv = env.Getenv
	h.shell = runShell
	h.reloadTheme = func() string {
		theme := h.st.reloadCustomTheme(env.Getenv)
		kb, keys := loadKeybindings(env.Getenv)
		h.setKeys(kb)
		return strings.Join(slices.DeleteFunc([]string{theme, keys}, func(s string) bool { return s == "" }), "; ")
	}
	if n := h.themeNotice(); n != "" {
		h.msg = append(h.msg, n)
	}
	if res != nil {
		h.setSetup(res.frozen)
	}
	u.noFallback = true
	h.exiting = u.exiting
	h.full = true
	h.st = h.st.warm()
	var (
		runIn     *interrupts // the running (or last) Run's
		runCancel context.CancelFunc
		running   chan struct{} // closed once the Run has ended
		used      *resolved     // the Run's config
		failed    *ended        // Öge itself failed or panicked
		cur       task.Task     // the Run's Task, with its follow-ups
		runRoot   = first.root  // the Run's repo: the focused Chat's when it began
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
		u.m.kb = h.kb
		u.m.notify = u.notify
		if u.herdr != nil {
			u.m.herdr = u.herdr
		}
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
		u.plain.width = func() int { return h.width }
		runRoot = h.root
		h.done = u.launch(runCtx, runIn, h.startFor(t, r, fu))
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
		text, l, last = u.summary(envAt(env, runRoot), used.flags, runRoot, e.res, h.hold)
		h.thread = nextThread(h.thread, cur, e.res, used.flags)
		return text, l, false
	}
	// verdictFor is the cut's Verdict line and what takes its Run, for the
	// Chats of the repo at root: they deliver there and nowhere else.
	verdictFor := func(root string) func(res *run.Result, changed int) (string, *lastRun) {
		return func(res *run.Result, changed int) (string, *lastRun) {
			line := verdictLine(res, changed)
			if res.Dir == "" {
				return line, nil
			}
			receiptText := func(full, fit bool) string {
				var b strings.Builder
				p := *u.plain
				p.w, p.full = &b, full
				if !fit {
					p.width = nil
				}
				p.endScreen(res)
				return b.String()
			}
			l, err := takeRun(envAt(env, root), root, res, h.hold, receiptText)
			if err != nil {
				return line + "\noge: reading the Run for delivery: " + clean(err.Error()), nil
			}
			c := h.chat.get() // the Chat the cut is of: route focused it
			l.apply = func() string { return h.chatApply(envAt(env, root), root, c, res) }
			branch := l.branch
			l.branch = func(name string) string {
				// A branch moves the Task's window too: never past a late
				// Tamper event (as apply).
				if msg, ok := h.chatDeliverable(c, res); !ok {
					return msg
				}
				return branch(name)
			}
			l.delivered = func(how, out string, code int) string {
				return h.chatDelivered(c, res, how, out, code)
			}
			return line, l
		}
	}
	hc := first.hc
	if hc != nil {
		h.chat = hc
		hc.verdict = verdictFor(first.root)
	}
	h.repoFor = func(root string) (*repoCtx, []string) {
		rc, problem := repoFor(root)
		if rc != nil && rc.hc != nil && rc.hc.verdict == nil {
			rc.hc.verdict = verdictFor(rc.root)
		}
		return rc, problem
	}
	if env.Edit != nil {
		h.edit = func(text string) tea.Cmd { return editInEditor(env, text, h.hold) }
	}
	err = u.program(in, h)
	u.exit() // the home's one program: its end is Öge's
	if ctx.Err() == nil && errors.Is(err, syscall.EIO) {
		// The terminal read EIO: it hung up, and its SIGHUP may not have
		// arrived yet. The session ends as on a hangup.
		in.hangUp()
		err = nil
	}
	if ctx.Err() != nil {
		// A signal ended the session (a hangup, SIGTERM): every Chat
		// stops as an interrupt does, and keeps its Workspace.
		why := in.why()
		for _, v := range h.views {
			v.chat.stop(why)
		}
		for _, c := range h.closed {
			c.stop(why)
		}
	}
	// hc is the caller's to close; the other Chats are the home's.
	for _, v := range h.views {
		if v.chat != hc {
			v.chat.close()
		}
	}
	for _, c := range h.closed {
		if c != hc {
			c.close()
		}
	}
	if h.full {
		// The alternate screen took the transcript with it: leave it in the
		// terminal's scrollback, as an inline session would have.
		h.leaveTranscript(u.out)
	}
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
			if code := afterRun(envAt(env, runRoot), used.flags, u.plain, runRoot, out); code != ExitOK {
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
	receiptText := func(full, fit bool) string {
		var b strings.Builder
		p := *u.plain
		p.w, p.full = &b, full
		if !fit {
			p.width = nil
		}
		p.endScreen(res)
		return b.String()
	}
	buf.WriteString(receiptText(false, true))
	if res.Outcome == run.Replied {
		return buf.String(), nil, ExitOK
	}
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
	s, err := takeRun(env, root, res, hold, receiptText)
	if err != nil {
		fmt.Fprintf(&buf, "oge: reading the Run for delivery: %s\n", clean(err.Error()))
		return buf.String(), nil, ExitInternal
	}
	return buf.String(), s, ExitOK
}

// takeRun is what can take the Run res from the home: /apply, /branch,
// /diff and /receipt. receiptText is its Receipt as the home prints it.
func takeRun(env Env, root string, res *run.Result, hold func(bool), receiptText func(full, fit bool) string) (*lastRun, error) {
	r, err := delivery.Load(res.Dir)
	if err != nil {
		return nil, err
	}
	// Once applied the Receipt lists the Delivery, so it says nothing about
	// what was written itself.
	var s *lastRun
	deliver := func(how string, do func(Env) int) string {
		var b strings.Builder
		q := env
		q.Stdout, q.Stderr = &b, &b
		code := do(q)
		if s.delivered != nil {
			return s.delivered(how, b.String(), code)
		}
		return b.String()
	}
	s = &lastRun{
		id: r.ID,
		apply: func() string {
			return deliver("apply", func(q Env) int { return applyCommand(q, r, root, delivery.Choice{}) })
		},
		branch: func(name string) string {
			return deliver("branch", func(q Env) int { return branchCommand(q, r, root, name, delivery.Choice{}) })
		},
		receipt: func() string { return receiptText(false, true) },
		copy:    func() string { return receiptText(false, false) },
		full:    func() string { return receiptText(true, true) },
		patch:   func() ([]byte, error) { return delivery.Diff(r) },
	}
	s.diff = func(emit func(string) tea.Cmd) tea.Cmd {
		patch, err := delivery.Diff(r)
		switch {
		case err != nil:
			return emit("oge: " + clean(err.Error()))
		case len(patch) == 0:
			return emit(fmt.Sprintf("Candidate %s changes nothing since the Snapshot.", delivery.Short(r.Candidate)))
		case env.Page == nil:
			return emit(strings.TrimSuffix(diffText(env, r, patch), "\n"))
		}
		// The pager has the terminal (released), and its Ctrl+C (hold).
		return tea.Exec(editorCmd{hold: hold, run: func() error { return env.Page(diffText(env, r, patch)) }}, func(err error) tea.Msg {
			if err != nil {
				return pageFailedMsg{err}
			}
			return nil
		})
	}
	return s, nil
}

// pageFailedMsg says the pager failed to show the diff.
type pageFailedMsg struct{ err error }

// copyText is the Receipt as /copy puts it on the clipboard.
func (l *lastRun) copyText() string {
	if l.copy != nil {
		return l.copy()
	}
	return l.receipt()
}
