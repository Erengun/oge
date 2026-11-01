package cli

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/agent/agy"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/pipeline"
)

// Slash commands (#145): one table, so /help and the autocomplete menu
// can't drift from what the composer accepts.

// slashWord is what a command's first word looks like: other text that
// starts with a slash (a path) is a Task.
var slashWord = regexp.MustCompile(`^/[a-z-]+$`)

// slashPrefix is what the menu completes: a slash and the start of a word.
var slashPrefix = regexp.MustCompile(`^/[a-z-]*$`)

// slashCmd is one command. run returns what to print above the program
// and, when the arguments or the state don't allow it, the inline error
// (the text stays in the box).
type slashCmd struct {
	name, usage, desc string
	advanced          bool
	unavailable       bool
	guard             bool // delivers: a completion is armed before Enter runs it
	args              bool // takes arguments it needs: Enter on the bare name completes it
	run               func(h *home, args []string) (tea.Cmd, string)
}

func slashCommands() []slashCmd {
	return []slashCmd{
		{name: "/help", usage: "/help [advanced]", desc: "keys and commands", run: (*home).helpCmd},
		{name: "/setup", advanced: true, usage: "/setup", desc: "open setup (same as tab)", run: (*home).setupCmd},
		{name: "/mode", advanced: true, usage: "/mode fast|standard", desc: "the mode of the next Runs (from setup)", args: true, run: (*home).modeCmd},
		{name: "/agent", advanced: true, usage: "/agent <stage> <agent[@profile]>", desc: "bind a Stage's agent (from setup)", args: true, run: (*home).agentCmd},
		{name: "/model", advanced: true, usage: "/model <stage> <model>", desc: "a Stage's model (from setup)", args: true, run: (*home).modelCmd},
		{name: "/effort", advanced: true, usage: "/effort <stage> <level>", desc: "a Stage's effort, Claude only (from setup)", args: true, run: (*home).effortCmd},
		{name: "/advise", advanced: true, usage: "/advise <question>", desc: "ask a fresh advisor for a second opinion", args: true, run: (*home).adviseCmd},
		{name: "/feedback", advanced: true, usage: "/feedback", desc: "send the failed Check back to the agent", run: (*home).feedbackCmd},
		{name: "/runs", advanced: true, usage: "/runs", desc: "recent Runs in this repo", run: (*home).runsCmd},
		{name: "/receipt", usage: "/receipt [run]", desc: "show the result and checks", run: deliverSlash("r", "receipt")},
		{name: "/diff", usage: "/diff [run]", desc: "review changes", run: (*home).diffCmd},
		{name: "/apply", guard: true, usage: "/apply [run]", desc: "apply changes to your working tree", run: deliverSlash("a", "apply")},
		{name: "/branch", advanced: true, guard: true, usage: "/branch [run] [name]", desc: "a Run's Candidate as a branch", run: deliverSlash("b", "branch")},
		{name: "/label", advanced: true, usage: "/label [run] <text>", desc: "name a Run (yours; not Evidence); no text clears", args: true, run: (*home).labelCmd},
		{name: "/export", advanced: true, usage: "/export", desc: "write this session's transcript to a file (redacted)", run: (*home).exportCmd},
		{name: "/copy", advanced: true, usage: "/copy", desc: "copy the last Receipt to the clipboard (redacted)", run: (*home).copyCmd},
		{name: "/new", usage: "/new [path]", desc: "open another Chat; this one keeps working", run: (*home).newCmd},
		{name: "/projects", usage: "/projects", desc: "open a Chat in another project", run: (*home).projectsCmd},
		{name: "/chats", usage: "/chats", desc: "switch to another Chat", run: (*home).chatsCmd},
		{name: "/inbox", usage: "/inbox", desc: "what the Chats want from you (ctrl+space)", run: (*home).inboxCmd},
		{name: "/close", advanced: true, usage: "/close", desc: "end this Chat", run: (*home).closeCmd},
		{name: "/clear", advanced: true, usage: "/clear", desc: "start this Chat afresh", run: (*home).clearCmd},
		{name: "/notify", advanced: true, usage: "/notify on|off", desc: "alerts for this session", run: (*home).notifyCmd},
		{name: "/terminal", advanced: true, usage: "/terminal", desc: "hints for this terminal (Shift+Enter, Alt keys, tmux)", run: (*home).terminalCmd},
		{name: "/resume", unavailable: true, usage: "/resume", desc: "not built yet (#55)", run: func(*home, []string) (tea.Cmd, string) {
			return nil, "/resume: not built yet (#55)"
		}},
		{name: "/quit", usage: "/quit", desc: "leave", run: (*home).quitCmd},
		{name: "/stop", advanced: true, usage: "/stop", desc: "stop every Chat, keep them, leave", run: (*home).stopCmd},
	}
}

// parseSlash splits a composer line into its command and arguments. ok is
// false when the text isn't a slash word (it is then a Task); cmd is nil
// when the word is no command.
func parseSlash(text string) (word string, cmd *slashCmd, args []string, ok bool) {
	word, rest, _ := strings.Cut(text, " ")
	if !slashWord.MatchString(word) {
		return "", nil, nil, false
	}
	args = strings.Fields(rest)
	for _, c := range slashCommands() {
		if c.name == word {
			return word, &c, args, true
		}
	}
	return word, nil, args, true
}

// slash runs a command typed in the composer; ok is false when text isn't
// one (it is then a Task). Delivery acts only here, on an explicit Enter of
// the whole command, never on a menu highlight (#196).
func (h *home) slash(text string) (cmd tea.Cmd, ok bool) {
	word, c, args, ok := parseSlash(text)
	if !ok {
		return nil, false
	}
	if c == nil {
		h.msg = []string{word + ": unknown command · /help lists commands"}
		return nil, true
	}
	h.dropAdviceEdit() // a command replaced what the box held
	v := h.chatView    // the Chat it was typed in: /new and /chats move the focus
	cmd, problem := c.run(h, args)
	if problem != "" {
		h.msg = []string{problem}
		return nil, true
	}
	v.comp = h.composer()
	h.navigating = false
	return cmd, true
}

func usageOf(name string) string {
	for _, c := range slashCommands() {
		if c.name == name {
			return "usage: " + c.usage
		}
	}
	return ""
}

func (h *home) helpCmd(args []string) (tea.Cmd, string) {
	if len(args) == 1 && args[0] == "advanced" {
		return h.println(advancedHelpText(h.kb)), ""
	}
	if len(args) > 0 {
		return nil, usageOf("/help")
	}
	return h.println(helpText(h.kb)), ""
}

// helpText is /help: the keys (from the key table the ? panel shows too),
// then the commands from the command table.
func helpText(kb *keyMap) string {
	lines := []string{"Write a message to work with your agent. Follow up in the same conversation.", "", "Keys"}
	submit := (keyBinding{act: "composer.submit"}).label(kb)
	keys := (keyBinding{act: "composer.keysPanel"}).label(kb)
	lines = append(lines, fmt.Sprintf("  %-18s send message", submit), "  ctrl+c             stop work, clear the draft, or leave", fmt.Sprintf("  %-18s commands, files, and keyboard shortcuts (on an empty composer)", keys))
	if n := kb.note(); n != "" {
		lines = append(lines, "  "+n)
	}
	lines = append(lines, "Commands")
	for _, c := range slashCommands() {
		if c.advanced || c.unavailable {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %-34s %s", c.usage, c.desc))
	}
	lines = append(lines, "", "More controls: /help advanced")
	return strings.Join(lines, "\n")
}

func advancedHelpText(kb *keyMap) string {
	lines := append([]string{"Keys"}, keyLines(kb)...)
	if n := kb.note(); n != "" {
		lines = append(lines, "  "+n)
	}
	lines = append(lines, "Advanced commands")
	for _, c := range slashCommands() {
		if c.advanced && !c.unavailable {
			lines = append(lines, fmt.Sprintf("  %-34s %s", c.usage, c.desc))
		}
	}
	return strings.Join(lines, "\n")
}

func (h *home) notifyCmd(args []string) (tea.Cmd, string) {
	if h.notify == nil {
		return nil, "/notify: alerts are off in this session"
	}
	if len(args) != 1 {
		return nil, usageOf("/notify")
	}
	line := h.notify.notifyCommand(args[0])
	if !strings.HasPrefix(line, "notifications ") {
		return nil, line
	}
	h.note = line
	return nil, ""
}

func (h *home) quitCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/quit")
	}
	return h.quitOrAsk(), ""
}

// stopCmd is "keep everything, stop now" (docs/ux/away.md, A2): it never
// asks. Every turn in flight is interrupted, Gates park, Workspaces stay.
func (h *home) stopCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/stop")
	}
	h.stopped, h.keep, h.quitAsk, h.quit = true, true, 0, true
	return tea.Quit, ""
}

func (h *home) setupCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/setup")
	}
	if p := h.openSetup(); p != "" {
		return nil, p
	}
	return nil, ""
}

// openSetup opens the setup panel (Tab and /setup); it returns why it
// can't, when it can't.
func (h *home) openSetup() string {
	if p := h.setupReady(); p != "" {
		return p
	}
	h.sel.reopen()
	h.panel = &setupPanel{sel: h.sel, st: h.st, rev0: h.sel.rev}
	return ""
}

// setupReady reads the config again when it didn't resolve as the home
// opened: it may now. It returns why setup can't open, if it can't.
func (h *home) setupReady() string {
	if h.sel != nil && !h.sel.ready {
		if res, problem := h.resolve(h.sel); problem == nil {
			h.sel.init(res)
			h.setSetup(res.frozen)
		}
	}
	if h.sel == nil || !h.sel.ready {
		return "setup opens once the config resolves: fix what is shown above, then press Tab"
	}
	return ""
}

// stageArg names a Stage's choices: "implement" or "verify".
func (s *setupState) stageArg(name string) (*stageSel, bool) {
	switch name {
	case "implement", "implementer":
		return &s.impl, true
	case "verify", "verifier":
		return &s.ver, true
	}
	return nil, false
}

// stageAgent is the agent a Stage will use: the verifier follows the
// implementer's until it is bound.
func (s *setupState) stageAgent(st *stageSel) string {
	if st == &s.ver && st.agent == "" {
		return s.impl.agent
	}
	return st.agent
}

// setupEdit applies a command's choice through the setup state, as the
// panel does: the bar is read again and the first change offers the save.
func (h *home) setupEdit(note string, edit func(s *setupState)) {
	rev0 := h.sel.rev
	edit(h.sel)
	h.afterSetup(rev0)
	h.note = note + " (from setup)"
}

// stageCmd checks the common start of /agent, /model and /effort: the
// setup is ready, there are two arguments, the Stage exists and, for the
// verifier, there is one in this mode.
func (h *home) stageCmd(name string, args []string) (*stageSel, string) {
	if p := h.setupReady(); p != "" {
		return nil, p
	}
	if len(args) != 2 {
		return nil, usageOf(name)
	}
	st, ok := h.sel.stageArg(args[0])
	if !ok {
		return nil, fmt.Sprintf("%s: the stage is implement or verify, not %q", name, clean(args[0]))
	}
	if st == &h.sel.ver && h.sel.mode == pipeline.Fast {
		return nil, name + ": Fast mode has no verifier"
	}
	return st, ""
}

func (h *home) modeCmd(args []string) (tea.Cmd, string) {
	if p := h.setupReady(); p != "" {
		return nil, p
	}
	if len(args) != 1 {
		return nil, usageOf("/mode")
	}
	var m pipeline.Mode
	switch strings.ToLower(args[0]) {
	case "fast":
		m = pipeline.Fast
	case "standard":
		m = pipeline.Standard
	case "blind":
		return nil, "/mode blind: not built yet (#52)"
	default:
		return nil, usageOf("/mode")
	}
	h.setupEdit("mode "+string(m), func(s *setupState) {
		if s.mode != m {
			s.mode, s.tMode = m, true
			s.touch()
		}
	})
	return nil, ""
}

// cycleMode is Shift+Tab: Fast becomes Standard and anything else Fast. The
// setup bar then shows the mode, with Fast marked trust-weakening.
// A toggle key never offers the save: /setup is where the human saves.
func (h *home) cycleMode() string {
	if p := h.setupReady(); p != "" {
		return p
	}
	saving, asked := h.saving, h.sel.asked
	m := pipeline.Fast
	if h.sel.mode == pipeline.Fast {
		m = pipeline.Standard
	}
	h.setupEdit("mode "+string(m), func(s *setupState) {
		s.mode, s.tMode = m, true
		s.touch()
	})
	h.saving, h.sel.asked = saving, asked
	return ""
}

func (h *home) agentCmd(args []string) (tea.Cmd, string) {
	st, problem := h.stageCmd("/agent", args)
	if problem != "" {
		return nil, problem
	}
	name, profile, _ := strings.Cut(args[1], "@")
	if !slices.Contains(h.sel.agents, name) {
		return nil, fmt.Sprintf("/agent: no agent %q here (have %s)", clean(name), strings.Join(h.sel.agents, ", "))
	}
	if profile != "" {
		if name != claude.Name {
			return nil, "/agent: profiles are Claude's"
		}
		if !slices.ContainsFunc(h.sel.profiles, func(p claude.Profile) bool { return p.Name == profile }) {
			return nil, fmt.Sprintf("/agent: no Claude profile %q", clean(profile))
		}
	}
	h.setupEdit(args[0]+" agent "+name+atProfile(profile), func(s *setupState) {
		if st.agent != name {
			*st = stageSel{agent: name, touched: true}
			s.touch()
		}
		if profile != "" && st.profile != profile {
			st.profile, st.touched = profile, true
			s.touch()
		}
	})
	return nil, ""
}

func (h *home) modelCmd(args []string) (tea.Cmd, string) {
	st, problem := h.stageCmd("/model", args)
	if problem != "" {
		return nil, problem
	}
	model := clean(args[1])
	if model == "default" {
		model = ""
	}
	h.setupEdit(args[0]+" model "+args[1], func(s *setupState) {
		if st.model != model {
			st.model, st.touched = model, true
			s.touch()
		}
	})
	return nil, ""
}

func (h *home) effortCmd(args []string) (tea.Cmd, string) {
	st, problem := h.stageCmd("/effort", args)
	if problem != "" {
		return nil, problem
	}
	if a := h.sel.stageAgent(st); a != claude.Name && a != agy.Name {
		return nil, "/effort: effort is Claude's or agy's"
	}
	level := args[1]
	if level == "default" {
		level = ""
	} else if !slices.Contains(claude.Efforts, level) {
		return nil, "/effort: the level is default, " + strings.Join(claude.Efforts, ", ")
	}
	h.setupEdit(args[0]+" effort "+args[1], func(s *setupState) {
		if st == &s.ver && st.agent == "" {
			st.agent, st.profile = s.impl.agent, s.impl.profile // the choice pins the verifier's binding
		}
		if st.effort != level {
			st.effort, st.touched = level, true
			s.touch()
		}
	})
	return nil, ""
}

// runsCmd opens the Run picker (#176) over the repository's recent Runs; on
// a terminal too short for it, it prints them: id, outcome, when (UTC, as the
// id says) and the Task's first line.
func (h *home) runsCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/runs")
	}
	if h.runs == nil {
		return nil, "/runs: no Runs to list here"
	}
	runs, problem := h.runs(10)
	if problem != "" {
		return nil, problem
	}
	if len(runs) == 0 {
		return h.println("no Runs of this repository yet"), ""
	}
	if h.height >= pickerMinHeight {
		h.picker = newRunPicker(runs, h.st, h.height)
		return nil, ""
	}
	lines := make([]string, 0, len(runs))
	for _, r := range runs {
		lines = append(lines, runLine(r))
	}
	return h.println(strings.Join(lines, "\n")), ""
}

// deliverSlash is /receipt, /diff, /apply and /branch: with a Run id they
// are the CLI's own code and refusals; without one they take the Run the
// session ends with (apply and branch never reach for an older Run the
// human hasn't just seen: ADR-0015).
func deliverSlash(key, cli string) func(*home, []string) (tea.Cmd, string) {
	return func(h *home, args []string) (tea.Cmd, string) {
		usage := usageOf("/" + cli)
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				return nil, usage + " · flags are for oge " + cli
			}
		}
		id, name, err := deliverArgs(cli, args)
		if err != nil {
			return nil, usage
		}
		switch {
		case id != "":
			if h.deliver == nil {
				return nil, "/" + cli + ": no Runs to take here"
			}
			if cli == "apply" && h.thread != nil && strings.HasPrefix(h.thread.from.Run, id) {
				h.thread = nil
			}
			return h.delivered(cli, args), ""
		case (cli == "apply" || cli == "branch") && h.verify != nil && h.verify.gate != nil:
			// The newest cut waits at its Gate: delivered only through it.
			return h.openCutGate(key, name), ""
		case h.last != nil:
			return h.act(key, name), ""
		case h.verify != nil:
			return nil, "/" + cli + ": the cut is still verifying"
		case cli == "apply" || cli == "branch":
			return nil, "/" + cli + ": no Accepted Run to take in this session"
		case h.deliver == nil:
			return nil, "/" + cli + ": no Runs to take here"
		}
		return h.delivered(cli, nil), ""
	}
}

// delivered prints what the CLI's command printed, in pieces that fit above
// the frame: a whole Receipt can be taller than the room (#188).
func (h *home) delivered(cmd string, args []string) tea.Cmd {
	if out := h.deliver(cmd, args); out != "" {
		return h.println(out)
	}
	return nil
}

// deliverer is home.deliver: the CLI's receipt, diff, apply and branch for
// a Run, as text to print. Its output is read here and never a terminal: a
// diff is printed, not paged.
func deliverer(env Env) func(cmd string, args []string) string {
	return func(cmd string, args []string) string {
		var b strings.Builder
		q := env
		q.Stdout, q.Stderr = &b, &b
		q.Interactive = func() bool { return false }
		q.StdoutTTY = func() bool { return true }
		if cmd == "receipt" {
			receiptCommand(q, args)
		} else {
			deliverCommand(q, cmd, args)
		}
		return strings.TrimSuffix(b.String(), "\n")
	}
}

// recentRuns is home.runs: the repository's recent Runs.
func recentRuns(env Env, root string) func(n int) ([]*delivery.Run, string) {
	return func(n int) ([]*delivery.Run, string) {
		var b strings.Builder
		q := env
		q.Stderr = &b
		private, code := privateState(q, root)
		if code != ExitOK {
			return nil, strings.TrimSpace(b.String())
		}
		runs, err := delivery.Recent(private, root, n)
		if err != nil {
			return nil, "/runs: " + clean(err.Error())
		}
		return runs, ""
	}
}

// ---- the menu ----

// slashItem is one line of the menu.
type slashItem struct{ cmd slashCmd }

func (i slashItem) FilterValue() string { return i.cmd.name }

type slashDelegate struct{ st styles }

func (slashDelegate) Height() int                         { return 1 }
func (slashDelegate) Spacing() int                        { return 0 }
func (slashDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d slashDelegate) Render(w io.Writer, m list.Model, i int, it list.Item) {
	c := it.(slashItem).cmd
	usage := fmt.Sprintf("%-32s", c.usage)
	if i == m.Index() {
		fmt.Fprint(w, " "+d.st.accent("> ")+d.st.bold(usage)+" "+d.st.dim(c.desc))
		return
	}
	fmt.Fprint(w, "   "+usage+" "+d.st.dim(c.desc))
}

// syncMenu opens, filters or closes the autocomplete menu after every
// update: it is open while the box holds one slash word and something
// completes it, until Esc closes it for that text.
func (h *home) syncMenu() {
	text := h.comp.text()
	if h.run != nil || h.turn != nil || h.panel != nil || h.saving || h.review != nil || h.search != nil || h.actions ||
		!slashPrefix.MatchString(text) || (h.menuOff && text == h.menuOffText) {
		h.menu = nil
		return
	}
	if h.menu != nil && h.menuQuery == text {
		return
	}
	var items []list.Item
	for _, c := range slashCommands() {
		if !c.unavailable && (text != "/" || !c.advanced) && strings.HasPrefix(c.name, text) {
			items = append(items, slashItem{c})
		}
	}
	if len(items) == 0 {
		h.menu = nil
		return
	}
	l := list.New(items, slashDelegate{h.st}, 100, min(len(items), 8)+2)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	h.menu, h.menuQuery = &l, text
}

// menuKey is a key while the menu is open; handled is false when the key
// is the composer's. Tab or Enter complete the highlighted command into
// the box and never run it: Enter runs only a command already typed whole
// (and one that needs arguments is completed, ready for them). Tab on
// /new typed whole opens the project picker instead.
func (h *home) menuKey(msg tea.KeyPressMsg) (handled bool) {
	sel, _ := h.menu.SelectedItem().(slashItem)
	text := h.comp.text()
	complete := func() {
		h.comp.set(sel.cmd.name)
		if sel.cmd.args || sel.cmd.guard {
			h.comp.insert(" ")
		}
		if sel.cmd.guard {
			h.armText, h.armAt = sel.cmd.name, h.clock()
		}
		h.navigating = false
	}
	switch msg.String() {
	case "up", "down", "ctrl+p", "ctrl+n":
		if h.navigating && (msg.String() == "up" || msg.String() == "down") {
			return false // history's, while it is being walked
		}
		l, _ := h.menu.Update(msg)
		h.menu = &l
	case "tab":
		if text == "/new" && sel.cmd.name == "/new" {
			// /new typed whole and Tab: the project picker, as with the space
			// With nothing to pick, keep "/new " ready for a typed path.
			if _, p := h.projectsCmd(nil); h.projectPicker() {
				h.comp.set("")
			} else {
				h.comp.set("/new ")
				if p != "" {
					h.msg = []string{p}
				}
			}
			return true
		}
		complete()
	case "esc":
		h.menuOff, h.menuOffText = true, text
	case "enter":
		if sel.cmd.name == text && !sel.cmd.args {
			return false
		}
		complete()
	default:
		return false
	}
	return true
}

// menuLines draws the open menu.
func (h *home) menuLines() []string {
	var out []string
	for _, l := range strings.Split(h.menu.View(), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, " "))
		}
	}
	if n := len(h.menu.Items()) - len(out); n > 0 {
		out = append(out, " "+h.st.dim(fmt.Sprintf("+%d more · ↓", n)))
	}
	return append(out, " "+h.st.dim("tab complete · enter run a whole command · esc close"))
}
