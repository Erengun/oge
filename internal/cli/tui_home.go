package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"github.com/rivo/uniseg"
)

// The home is what a bare oge opens on a terminal: a composer for the Task
// and, under it, a read-only line of what the Run will use. Enter starts the
// Run in the same Bubble Tea program, so the live view takes over the frame
// the composer was in. When the Run ends the program ends too; the summary
// and the action bar follow as for any Run, and startHome opens the next
// composer. (Moving the summary and the action bar into the program would
// make one program span a whole session, but they read the terminal in raw
// bytes and page the diff, so that is a rewrite for no felt gain: ADR-0022.)

// maxComposerRows is how many lines of the Task the box shows before it
// scrolls.
const maxComposerRows = 8

// composer is a minimal multiline text box. It keeps the Task as lines of
// runes, so the cursor is a (row, col) in them.
type composer struct {
	lines    [][]rune
	row, col int
	top      int // first visible row of the wrapped text
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

func (c *composer) set(text string) {
	*c = composer{lines: [][]rune{nil}}
	c.insert(text)
}

// insert types s at the cursor. Newlines split the line; tabs become spaces
// and other control characters are dropped, so pasted bytes can't steer the
// terminal.
func (c *composer) insert(s string) {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", "    ").Replace(s)
	for _, r := range s {
		switch {
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
	switch k.String() {
	case "backspace", "ctrl+h":
		c.backspace()
	case "delete", "ctrl+d":
		c.delete()
	case "left", "ctrl+b":
		c.move(0, -1)
	case "right", "ctrl+f":
		c.move(0, 1)
	case "up", "ctrl+p":
		c.move(-1, 0)
	case "down", "ctrl+n":
		c.move(1, 0)
	case "home", "ctrl+a":
		c.col = 0
	case "end":
		c.col = len(c.lines[c.row])
	case "ctrl+u":
		c.lines[c.row] = append([]rune(nil), c.lines[c.row][c.col:]...)
		c.col = 0
	case "ctrl+j", "shift+enter", "alt+enter":
		c.newline()
	default:
		if k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) == 0 && k.Text != "" {
			c.insert(k.Text)
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
		var row strings.Builder
		w, n := 0, 0 // columns in the row; runes before the cluster
		rest := string(l)
		for rest != "" {
			cl, tail, cw, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
			k := len([]rune(cl))
			if w+cw > width && w > 0 {
				all = append(all, row.String())
				row.Reset()
				w = 0
			}
			if i == c.row && c.col >= n && c.col < n+k {
				curRow, curCol = len(all), w
			}
			row.WriteString(cl)
			w += cw
			n += k
			rest = tail
		}
		if i == c.row && c.col >= n {
			if w >= width { // the cursor wraps onto a fresh row
				all = append(all, row.String())
				row.Reset()
				w = 0
			}
			curRow, curCol = len(all), w
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

// home is the program's model while the Task is composed, and the Run's
// own model once Enter starts it.
type home struct {
	comp  *composer
	title string   // the box's title: repo and branch
	setup []string // the setup bar, as lines
	st    styles
	// start begins the Run for a Task in this program: it returns the Run's
	// model and its first command.
	start func(task.Task, *resolved, int, int) (*model, tea.Cmd)
	// resolve reads the config again, for the Task about to start and with
	// the setup panel's choices (sel) added.
	resolve func(*setupState) (*resolved, []string)
	// sel is what the setup panel edits; it lives for the session.
	sel    *setupState
	panel  *setupPanel // non-nil while Tab's setup is open
	saving bool        // the save prompt waits for y or n
	save   func() (string, error)
	note   string // what happened, shown under the box (a save)
	ctx    context.Context
	// msg is what stops or went wrong, shown under the box.
	msg         []string
	interrupted bool                      // the context ended while composing
	edit        func(text string) tea.Cmd // Ctrl+E: $EDITOR on the Task

	width, height int
	run           *model // nil while composing
	quit          bool
}

// editedMsg is the Task back from $EDITOR.
type editedMsg struct {
	text string
	err  error
}

// ctxDoneMsg tells the composer that Öge was interrupted.
type ctxDoneMsg struct{}

func (h *home) Init() tea.Cmd {
	if h.ctx == nil {
		return nil
	}
	return func() tea.Msg {
		<-h.ctx.Done()
		return ctxDoneMsg{}
	}
}

func (h *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		h.width, h.height = ws.Width, ws.Height
	}
	if h.run != nil {
		_, cmd := h.run.Update(msg)
		return h, cmd
	}
	switch msg := msg.(type) {
	case ctxDoneMsg:
		h.interrupted, h.quit = true, true
		return h, tea.Quit
	case tea.PasteMsg:
		switch {
		case h.panel != nil:
			h.panel.paste(msg.Content)
		case h.saving:
		default:
			h.comp.insert(msg.Content)
		}
	case editedMsg:
		if msg.err != nil {
			h.msg = []string{"the editor failed: " + clean(msg.err.Error())}
		} else {
			h.comp.set(msg.text)
		}
	case tea.KeyPressMsg:
		h.msg, h.note = nil, ""
		switch {
		case h.saving:
			h.answerSave(msg.String())
			return h, nil
		case h.panel != nil:
			if h.panel.key(msg) {
				h.closeSetup()
			}
			return h, nil
		}
		switch msg.String() {
		case "esc", "ctrl+c":
			// On an empty box both leave. With a Task in the box Esc does
			// nothing, so it never wipes one; Ctrl+C clears it first (as
			// Claude Code's prompt does).
			if h.comp.empty() {
				h.quit = true
				return h, tea.Quit
			}
			if msg.String() == "ctrl+c" {
				h.comp.set("")
			}
		case "enter":
			text := task.StripComments(h.comp.text())
			if task.IsEmpty(text) {
				return h, nil
			}
			if h.ctx != nil && h.ctx.Err() != nil {
				h.interrupted, h.quit = true, true
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
			m, cmd := h.start(task.Parse(text), res, h.width, h.height)
			h.run = m
			return h, cmd
		case "ctrl+e":
			if h.edit != nil {
				return h, h.edit(h.comp.text())
			}
		case "tab":
			if h.sel == nil || !h.sel.ready {
				h.msg = []string{"setup opens once the config resolves: fix what is shown above, then press Tab"}
				return h, nil
			}
			h.panel = &setupPanel{sel: h.sel, st: h.st}
		default:
			h.comp.key(msg)
		}
	}
	return h, nil
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
	return v
}

// render is the composer frame, and where the cursor is in it.
func (h *home) render() (string, int, int) {
	st := h.st
	width := h.width
	if width <= 0 {
		width = 80
	}
	inner := max(width-4, 1)
	rows, cr, cc := h.comp.layout(inner)
	blank := len(h.comp.lines) == 1 && len(h.comp.lines[0]) == 0

	var lines []string
	title := " " + h.title + " "
	lines = append(lines, st.dim("╭─")+st.bold(title)+st.dim(strings.Repeat("─", max(width-3-ansi.StringWidth(title), 0))+"╮"))
	for i, r := range rows {
		body := r
		if blank && i == 0 {
			body = st.dim("Describe the task")
		}
		pad := strings.Repeat(" ", max(inner-ansi.StringWidth(ansi.Strip(body)), 0))
		lines = append(lines, st.dim("│")+" "+body+pad+" "+st.dim("│"))
	}
	lines = append(lines, st.dim("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
	for _, l := range h.msg {
		lines = append(lines, " "+st.bad(l))
	}
	if h.note != "" {
		lines = append(lines, " "+st.ok(h.note))
	}
	switch {
	case h.saving:
		lines = append(lines, " "+st.bold("Save as default for this repo?")+" [y]es [n]o"+st.dim("   (your user config only)"))
	case h.panel != nil:
		lines = append(lines, h.panel.lines()...)
	}
	if h.panel == nil {
		for _, l := range h.setup {
			lines = append(lines, " "+st.dim(l))
		}
	}

	var b strings.Builder
	for _, l := range lines {
		b.WriteString(ansi.Truncate(l, width, "…"))
		b.WriteByte('\n')
	}
	return b.String(), cc + 2, cr + 1
}

// closeSetup leaves the panel. The setup bar shows what the next Run will
// use, read again with the choices; the first time something changed, the
// save prompt follows (and never a second time in the session).
func (h *home) closeSetup() {
	h.panel = nil
	res, problem := h.resolve(h.sel)
	if problem != nil {
		h.msg = problem
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
		_, err := h.save()
		h.saving = false
		if err != nil {
			h.msg = []string{"not saved: " + clean(err.Error())}
			return
		}
		h.note = "Saved as this repo's default in your user config"
	case "n", "N", "esc", "ctrl+c":
		h.saving = false
	}
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
	for _, c := range f.Checks {
		checks = append(checks, clean(c.Run))
	}
	lines := []string{strings.Join(parts, " · ")}
	for _, w := range f.TrustWeakening {
		lines = append(lines, "! trust-weakening: "+clean(w.Option)+" (from "+sourceName(w.Source)+")")
	}
	return append(lines,
		"Checks: "+strings.Join(checks, " · "),
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
type editorCmd struct{ run func() error }

func (e editorCmd) Run() error          { return e.run() }
func (e editorCmd) SetStdin(io.Reader)  {}
func (e editorCmd) SetStdout(io.Writer) {}
func (e editorCmd) SetStderr(io.Writer) {}

// editInEditor opens text in env.Edit, with the template's comments when the
// box is empty, and returns what the human left, comments stripped.
func editInEditor(env Env, text string) tea.Cmd {
	var out string
	return tea.Exec(editorCmd{func() error {
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

// home shows the composer and, on Enter, the Run in the same program. quit
// is true when the human left the composer without running anything, or
// Öge was interrupted while composing. res is the config as of now (nil
// when problem says why it can't be resolved); resolve reads it again when
// a Task is sent.
func (u *tui) home(ctx context.Context, in *interrupts, env Env, root string, startFor func(task.Task, *resolved) startFunc,
	res *resolved, problem []string, resolve func(*setupState) (*resolved, []string), sel *setupState) (out *run.Result, quit bool, err error) {
	h := &home{comp: newComposer(), title: homeTitle(env, root), st: u.m.st, ctx: ctx, resolve: resolve, msg: problem, sel: sel}
	if res != nil {
		h.setup = setupLines(res.frozen)
		sel.init(res)
	}
	h.save = sel.save
	u.noFallback = true
	var done <-chan ended
	h.start = func(t task.Task, r *resolved, w, ht int) (*model, tea.Cmd) {
		u.m = newModel(t, r.frozen, u.m.st, time.Now)
		u.m.width, u.m.height = w, ht
		u.plain.frozen, u.plain.warn = r.frozen, r.warn
		u.plain.applying = r.flags.apply
		done = u.launch(ctx, in, startFor(t, r))
		return u.m, u.m.Init()
	}
	if env.Edit != nil {
		h.edit = func(text string) tea.Cmd { return editInEditor(env, text) }
	}
	err = u.program(in, h)
	if h.run == nil {
		select {
		case <-in.forced:
			return nil, false, errForced
		default:
		}
		return nil, err == nil, err
	}
	out, err = u.finish(in, err, done)
	return out, false, err
}
