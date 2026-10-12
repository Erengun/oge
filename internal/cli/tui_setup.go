package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/pipeline"
)

// The setup panel (#131): what Tab opens on the home. It edits a
// setupState that lives for the whole session, so every choice applies to
// the next Run and the ones after it, as flags would (source "tui"). Nothing
// is written anywhere until the human answers the save prompt with y.
//
// The pickers are bubbles/list and the model box is bubbles/textinput. Their
// keys follow the composer's: Enter accepts, Esc backs out, Ctrl+C leaves.

// stageSel is one Stage's binding as the panel shows it.
type stageSel struct {
	agent, profile, model string
	touched               bool // the human changed it: it is sent as a flag
	inherits              bool // the verifier follows the implementer's agent
}

// setupState is the panel's choices for the session.
type setupState struct {
	env   Env
	root  string
	ready bool
	impl  stageSel
	ver   stageSel
	mode  pipeline.Mode
	// unattended and apply are what the next Run uses; the t* flags say the
	// human set them, so they override the user config.
	unattended, apply     bool
	tMode, tUnatt, tApply bool
	changed, asked        bool // any change; the save prompt was shown
	rev                   int  // counts changes
	agents                []string
	profiles              []claude.Profile
	logins                map[string]claude.Login // by profile dir; checked once per session (#186)
	lastFrozen            *pipeline.Frozen
	oracleState           // the Checks section (#132)
}

func newSetupState(env Env, root string) *setupState { return &setupState{env: env, root: root} }

// init reads the panel's starting values from a resolved Run, once.
func (s *setupState) init(res *resolved) {
	if s == nil || s.ready || res == nil {
		return
	}
	s.ready = true
	s.syncFrom(res)
}

// syncFrom takes what the Run would use as the shown values, keeping what
// the human changed.
func (s *setupState) syncFrom(res *resolved) {
	if s == nil {
		return
	}
	f := res.frozen
	s.lastFrozen = f
	take := func(dst *stageSel, role string) {
		if dst.touched {
			return
		}
		*dst = stageSel{}
		for _, st := range f.Stages {
			if st.Role == role {
				*dst = stageSel{agent: st.Agent, profile: st.Profile, model: st.Model,
					inherits: st.Source == pipeline.FromInherited}
			}
		}
	}
	take(&s.impl, "implementer")
	take(&s.ver, "verifier")
	if !s.tMode {
		s.mode = f.Mode
	}
	if !s.tUnatt {
		s.unattended = res.flags.unattended
	}
	if !s.tApply {
		s.apply = res.flags.apply
	}
	s.syncOracle(res)
	s.agents, s.profiles = s.choices()
}

// choices are the agents that can run and the Claude profiles to bind.
func (s *setupState) choices() ([]string, []claude.Profile) {
	var agents []string
	for name := range s.env.Agents {
		if slices.Contains(pipeline.KnownAgents, name) && s.env.LookPath != nil {
			if _, err := s.env.LookPath(name); err != nil {
				continue
			}
		}
		agents = append(agents, name)
	}
	sort.Strings(agents)
	getenv := s.env.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	var extra map[string]string
	if user, code := loadUserConfig(Env{Getenv: s.env.Getenv, Stderr: io.Discard}); code == ExitOK && user != nil {
		extra = user.Profiles
	}
	profiles := claude.Profiles(getenv("HOME"), getenv, extra)
	if s.logins == nil {
		s.logins = map[string]claude.Login{}
		if l := claudeLogins(s.env); l != nil {
			for i, st := range l.LoginStatuses(context.Background(), profiles) {
				s.logins[profiles[i].Dir] = st
			}
		}
	}
	return agents, profiles
}

func (s *setupState) touch() { s.changed = true; s.rev++ }

func (st stageSel) binding() string {
	b := st.agent
	if st.profile != "" {
		b += "@" + st.profile
	}
	if st.model != "" {
		b += ":" + st.model
	}
	return b
}

// flags adds the human's choices to the flags for the next Run, as the
// command line would: they replace what the command line bound for the same
// Stage, and a weaker mode or unattended is shown as trust-weakening.
func (s *setupState) flags(f runFlags) runFlags {
	if s == nil {
		return f
	}
	if !s.ready { // a first run's proposal applies before the panel can open
		return s.oracleFlags(f)
	}
	if s.tMode {
		f.o.ModeFrom = pipeline.FromTUI
		f.o.Fast, f.o.Blind, f.o.Standard = false, false, false
		switch s.mode {
		case pipeline.Fast:
			f.o.Fast = true
		case pipeline.Blind:
			f.o.Blind = true
		default:
			f.o.Standard = true
		}
	}
	if s.tUnatt {
		f.unattended, f.o.Unattended, f.o.UnattendedFrom = s.unattended, s.unattended, pipeline.FromTUI
	}
	if s.tApply {
		f.apply = s.apply
	}
	f = s.oracleFlags(f)
	// Only what the human changed is sent as the panel's; a Stage left alone
	// keeps the binding (and the source) it already had.
	f.o.TUIAgents = nil
	for _, st := range []struct {
		name string
		sel  stageSel
	}{{"implement", s.impl}, {"verify", s.ver}} {
		if !st.sel.touched || (st.name == "verify" && s.mode == pipeline.Fast) {
			continue
		}
		sel := st.sel
		if sel.agent == "" { // the verifier follows the implementer
			sel.agent, sel.profile = s.impl.agent, s.impl.profile
		}
		f.o.TUIAgents = append(f.o.TUIAgents, st.name+"="+sel.binding())
	}
	return f
}

// defaults are the choices to save: what is already saved for this repo,
// with what the human changed in this session on top.
func (s *setupState) defaults(d pipeline.RepoDefaults) pipeline.RepoDefaults {
	if s.tMode && s.mode != pipeline.Blind { // Blind isn't built: a saved one would refuse every Run
		d.Mode = strings.ToLower(string(s.mode))
	}
	if s.tUnatt {
		v := s.unattended
		d.Unattended = &v
	}
	if s.tApply {
		v := s.apply
		d.Apply = &v
	}
	if s.impl.touched {
		d.Stages.Implement = pipeline.UserStage{Agent: s.impl.agent + atProfile(s.impl.profile), Model: s.impl.model}
	}
	if s.ver.touched && s.mode != pipeline.Fast {
		agent, profile := s.ver.agent, s.ver.profile
		if agent == "" {
			agent, profile = s.impl.agent, s.impl.profile
		}
		d.Stages.Verify = pipeline.UserStage{Agent: agent + atProfile(profile), Model: s.ver.model}
	}
	return d
}

func atProfile(p string) string {
	if p == "" {
		return ""
	}
	return "@" + p
}

// save writes the session's choices to the user config, for this repo.
func (s *setupState) save() (string, error) {
	path := pipeline.UserConfigPath(s.env.Getenv)
	if path == "" {
		return "", fmt.Errorf("no home directory to keep the user config in")
	}
	var cur pipeline.RepoDefaults
	switch data, err := os.ReadFile(path); {
	case err == nil:
		cfg, probs := pipeline.LoadUserConfig(data)
		if len(probs) > 0 {
			return "", fmt.Errorf("%s isn't valid, so it was not changed", path)
		}
		if d := cfg.ForRepo(s.root); d != nil {
			cur = *d
		}
	case !os.IsNotExist(err):
		return "", err
	}
	return path, pipeline.SaveRepoDefaults(path, s.root, s.defaults(cur))
}

// modelSuggestions are the aliases an agent's CLI takes for --model.
func modelSuggestions(agent string) []string {
	if agent == claude.Name {
		return []string{"sonnet", "opus", "haiku"}
	}
	return nil
}

// ---- the panel ----

type rowKind int

const (
	rowAgent rowKind = iota
	rowProfile
	rowModel
	rowMode
	rowUnattended
	rowApply
	rowCheck    // a Check command: idx
	rowAddCheck // add one
	rowGlob     // a test glob: idx
	rowAddGlob
)

type setupRow struct {
	kind  rowKind
	stage int // 0 implement, 1 verify
	idx   int // the Check or glob
}

// choice is one line of a picker.
type choice struct {
	value, label, note string
	disabled           bool // shown, but can't be picked
}

func (c choice) FilterValue() string { return c.label }

type choiceDelegate struct{ st styles }

func (choiceDelegate) Height() int                         { return 1 }
func (choiceDelegate) Spacing() int                        { return 0 }
func (choiceDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d choiceDelegate) Render(w io.Writer, m list.Model, i int, it list.Item) {
	c := it.(choice)
	line := "  " + c.label
	if i == m.Index() {
		line = d.st.accent("> ") + d.st.bold(c.label)
	}
	switch {
	case c.disabled:
		line = d.st.dim(strings.TrimSpace(line)) + "   " + d.st.dim(c.note)
		line = "  " + line
	case c.note != "":
		line += "   " + d.st.dim(c.note)
	}
	fmt.Fprint(w, "      "+line)
}

// setupPanel is the open panel: a row cursor, and at most one sub-editor.
type setupPanel struct {
	sel         *setupState
	st          styles
	cursor      int
	rev0        int              // the state's rev when the panel opened
	save        bool             // [s] was pressed
	saveProject bool             // [w] was pressed
	width       int              // the terminal's, for wrapping
	pick        *list.Model      // a picker is open for the cursor's row
	input       *textinput.Model // the model box is open for the cursor's row
}

func (p *setupPanel) rows() []setupRow {
	rows := []setupRow{{kind: rowAgent}}
	if p.sel.impl.agent == claude.Name {
		rows = append(rows, setupRow{kind: rowProfile, stage: 0})
	}
	rows = append(rows, setupRow{kind: rowModel, stage: 0})
	if p.sel.mode != pipeline.Fast {
		rows = append(rows, setupRow{kind: rowAgent, stage: 1})
		if p.sel.ver.agent == claude.Name || (p.sel.ver.agent == "" && p.sel.impl.agent == claude.Name) {
			rows = append(rows, setupRow{kind: rowProfile, stage: 1})
		}
		rows = append(rows, setupRow{kind: rowModel, stage: 1})
	}
	rows = append(rows, setupRow{kind: rowMode}, setupRow{kind: rowUnattended}, setupRow{kind: rowApply})
	for i := range p.sel.checks {
		rows = append(rows, setupRow{kind: rowCheck, idx: i})
	}
	rows = append(rows, setupRow{kind: rowAddCheck})
	for i := range p.sel.globs {
		rows = append(rows, setupRow{kind: rowGlob, idx: i})
	}
	return append(rows, setupRow{kind: rowAddGlob})
}

func (p *setupPanel) stage(i int) *stageSel {
	if i == 1 {
		return &p.sel.ver
	}
	return &p.sel.impl
}

func (p *setupPanel) label(r setupRow) string {
	name := [...]string{"implement", "verify"}[r.stage]
	switch r.kind {
	case rowAgent:
		return name + " agent"
	case rowProfile:
		return name + " profile"
	case rowModel:
		return name + " model"
	case rowMode:
		return "mode"
	case rowUnattended:
		return "unattended"
	case rowCheck:
		return fmt.Sprintf("check %d", r.idx+1)
	case rowAddCheck:
		return "+ Check command"
	case rowGlob:
		return fmt.Sprintf("test glob %d", r.idx+1)
	case rowAddGlob:
		return "+ test glob"
	}
	return "apply on accept"
}

func (p *setupPanel) value(r setupRow) string {
	st := p.stage(r.stage)
	switch r.kind {
	case rowCheck:
		return clean(p.sel.checks[r.idx].Run)
	case rowGlob:
		return clean(p.sel.globs[r.idx])
	case rowAddCheck, rowAddGlob:
		return ""
	case rowAgent:
		if st.agent == "" || (st.inherits && !st.touched) {
			return "same as implement (" + clean(p.sel.impl.agent) + ")"
		}
		return clean(st.agent)
	case rowProfile:
		// rows shows this row only for Claude; an unset verifier agent
		// inherits implement's profile.
		profile := st.profile
		if st.agent == "" {
			profile = p.sel.impl.profile
		}
		return clean(claude.Profile{Name: profile}.Label())
	case rowModel:
		if st.model == "" {
			return "agent's default"
		}
		return clean(st.model)
	case rowMode:
		return string(p.sel.mode) + "   " + modeNotes[p.sel.mode]
	case rowUnattended:
		return onOff(p.sel.unattended) + "   never stops at an optional Gate (ADR-0008)"
	}
	return onOff(p.sel.apply) + "   applies an Accepted Candidate to your working tree"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// open starts the editor a row needs.
func (p *setupPanel) open(r setupRow) {
	st := p.stage(r.stage)
	var items []list.Item
	cur := 0
	add := func(c choice, current bool) {
		if current {
			cur = len(items)
		}
		items = append(items, c)
	}
	switch r.kind {
	case rowAgent:
		if r.stage == 1 {
			add(choice{value: "", label: "same as implement"}, st.agent == "")
		}
		for _, a := range p.sel.agents {
			add(choice{value: a, label: clean(a)}, a == st.agent)
		}
	case rowProfile:
		current := st.profile
		if r.stage == 1 && st.agent == "" {
			current = p.sel.impl.profile
		}
		for _, pr := range p.sel.profiles {
			c := choice{value: pr.Name, label: pr.Label()}
			if p.sel.logins[pr.Dir] == claude.LoggedOut {
				c.note = "not logged in"
			}
			add(c, pr.Name == current)
		}
	case rowMode:
		for _, m := range []pipeline.Mode{pipeline.Fast, pipeline.Standard, pipeline.Blind} {
			c := choice{value: string(m), label: string(m), note: modeNotes[m]}
			if m == pipeline.Blind {
				c.disabled, c.note = true, "not built yet (#52)"
			}
			add(c, m == p.sel.mode)
		}
	case rowModel:
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = "agent's default"
		in.ShowSuggestions = true
		agent := st.agent
		if agent == "" {
			agent = p.sel.impl.agent
		}
		in.SetSuggestions(modelSuggestions(agent))
		in.SetValue(st.model)
		in.CursorEnd()
		in.Focus()
		p.input = &in
		return
	case rowCheck, rowAddCheck, rowGlob, rowAddGlob:
		in := textinput.New()
		in.Prompt = ""
		switch r.kind {
		case rowCheck:
			in.SetValue(p.sel.checks[r.idx].Run)
		case rowGlob:
			in.SetValue(p.sel.globs[r.idx])
		case rowAddCheck:
			in.Placeholder = "command that runs the tests, e.g. go test -json ./..."
		default:
			in.Placeholder = "glob of the test files, e.g. **/*_test.go"
		}
		in.CursorEnd()
		in.Focus()
		p.input = &in
		return
	case rowUnattended:
		p.sel.unattended, p.sel.tUnatt = !p.sel.unattended, true
		p.sel.touch()
		return
	case rowApply:
		p.sel.apply, p.sel.tApply = !p.sel.apply, true
		p.sel.touch()
		return
	}
	l := list.New(items, choiceDelegate{p.st}, 100, min(len(items), 8)+2) // the list keeps a line or two for itself
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	l.Select(cur)
	p.pick = &l
}

// commitPick applies the picker's choice to the row.
func (p *setupPanel) commitPick(r setupRow) {
	c, ok := p.pick.SelectedItem().(choice)
	if ok && c.disabled {
		return // stays open: Blind can't be picked
	}
	p.pick = nil
	if !ok {
		return
	}
	st := p.stage(r.stage)
	switch r.kind {
	case rowAgent:
		if st.agent != c.value {
			st.agent, st.profile, st.model, st.touched, st.inherits = c.value, "", "", true, false
			p.sel.touch()
		}
	case rowProfile:
		if st.agent == "" && r.stage == 1 {
			st.agent = p.sel.impl.agent // the choice pins the verifier's account
		}
		if st.profile != c.value {
			st.profile, st.touched = c.value, true
			p.sel.touch()
		}
	case rowMode:
		if string(p.sel.mode) != c.value {
			p.sel.mode, p.sel.tMode = pipeline.Mode(c.value), true
			p.sel.touch()
			p.cursor = min(p.cursor, len(p.rows())-1)
		}
	}
}

// key handles a key while the panel is open; closed reports that the human
// left it.
func (p *setupPanel) key(k tea.KeyPressMsg) (closed bool) {
	rows := p.rows()
	p.cursor = min(p.cursor, len(rows)-1)
	r := rows[p.cursor]
	switch {
	case p.input != nil:
		switch k.String() {
		case "enter":
			v := strings.TrimSpace(p.input.Value())
			if r.kind == rowModel {
				if st := p.stage(r.stage); v != st.model {
					st.model, st.touched = v, true
					p.sel.touch()
				}
			} else {
				p.commitOracle(r, v)
			}
			p.input = nil
		case "esc", "ctrl+c":
			p.input = nil
		default:
			in, _ := p.input.Update(k)
			p.input = &in
		}
	case p.pick != nil:
		switch k.String() {
		case "enter":
			p.commitPick(r)
		case "esc", "ctrl+c":
			p.pick = nil
		case "up", "down", "ctrl+p", "ctrl+n":
			l, _ := p.pick.Update(k)
			p.pick = &l
		}
	default:
		switch k.String() {
		case "tab", "esc", "ctrl+c":
			return true
		case "s":
			p.save = true
		case "w":
			p.saveProject = true
		case "d", "x":
			p.remove(r)
			p.cursor = min(p.cursor, len(p.rows())-1)
		case "up", "ctrl+p":
			p.cursor = max(p.cursor-1, 0)
		case "down", "ctrl+n":
			p.cursor = min(p.cursor+1, len(rows)-1)
		case "enter", "space", " ":
			p.open(r)
		}
	}
	return false
}

// paste puts pasted text in the model box, when it is open.
func (p *setupPanel) paste(s string) {
	if p.input != nil {
		in, _ := p.input.Update(tea.PasteMsg{Content: strings.ReplaceAll(s, "\n", " ")})
		p.input = &in
	}
}

// lines draws the panel.
func (p *setupPanel) lines() []string {
	st := p.st
	rows := p.rows()
	out := []string{st.bold("Setup") + st.dim("   next Run · [enter] change · [s] save as default · [tab] done")}
	for i, r := range rows {
		if (r.kind == rowCheck || r.kind == rowAddCheck) && rows[i-1].kind != rowCheck {
			out = append(out, p.checksHeader())
		}
		mark, label := "  ", fmt.Sprintf("%-18s", p.label(r))
		if i == p.cursor {
			mark, label = st.accent("> "), st.bold(label)
		}
		out = append(out, " "+mark+label+" "+p.value(r))
		if i == p.cursor {
			switch {
			case p.pick != nil:
				for _, l := range strings.Split(p.pick.View(), "\n") {
					if strings.TrimSpace(l) != "" {
						out = append(out, strings.TrimRight(l, " "))
					}
				}
			case p.input != nil:
				if r.kind == rowModel {
					out = append(out, "      model: "+p.input.View()+st.dim("   [enter] set · [tab] complete · [esc] cancel"))
				} else {
					out = append(out, "      > "+p.input.View()+st.dim("   [enter] set · [esc] cancel"))
				}
			}
		}
	}
	return append(out, p.validation(rows)...)
}
