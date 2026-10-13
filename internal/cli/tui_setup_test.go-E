package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// setupHome is a home whose setup panel can open: a temp HOME with two
// Claude profiles (~/.claude-work, ~/.claude-z) and the agents claude and
// codex registered.
func setupHome(t *testing.T) (*home, string) {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{".claude-work", ".claude-z"} {
		writeFile(t, filepath.Join(dir, p, "settings.json"), []byte("{}"))
	}
	env := Env{
		Agents: map[string]agent.Adapter{"claude": nil, "codex": nil},
		Getenv: func(k string) string {
			switch k {
			case "HOME":
				return dir
			case "XDG_CONFIG_HOME":
				return filepath.Join(dir, ".config")
			}
			return ""
		},
	}
	h := testHome(80)
	h.sel = newSetupState(env, filepath.Join(dir, "repo"))
	h.sel.init(&resolved{frozen: homeFrozen()})
	h.save = h.sel.save
	return h, dir
}

func tab() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyTab} }
func down() tea.KeyPressMsg  { return tea.KeyPressMsg{Code: tea.KeyDown} }
func enter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }

// cursorTo puts the open panel's cursor on the first row of a kind, so a
// test doesn't count rows that new sections move.
func cursorTo(h *home, kind rowKind) {
	for i, r := range h.panel.rows() {
		if r.kind == kind {
			h.panel.cursor = i
			return
		}
	}
	panic("no such row")
}

func TestSetupPanelGolden(t *testing.T) {
	h, _ := setupHome(t)
	press(h, tab())
	golden(t, "setup-panel", frame(h))
}

func TestSetupProfilePickerListsDiscoveredProfiles(t *testing.T) {
	h, _ := setupHome(t)
	press(h, tab(), down(), enter()) // implement profile
	got := frame(h)
	for _, want := range []string{"claude", "claude@work", "claude@z"} {
		if !strings.Contains(got, want) {
			t.Errorf("picker lacks %s:\n%s", want, got)
		}
	}
	golden(t, "setup-profile-picker", got)
}

func TestSetupSavePromptAfterTheFirstChange(t *testing.T) {
	h, dir := setupHome(t)
	// Tab, the "unattended" row, Enter flips it, Tab closes.
	press(h, tab())
	cursorTo(h, rowUnattended)
	press(h, enter(), tab())
	if !h.saving {
		t.Fatalf("no save prompt after a change:\n%s", frame(h))
	}
	golden(t, "setup-save-prompt", frame(h))
	// Other keys don't answer it, and nothing is written without a y.
	press(h, enter())
	if !h.saving {
		t.Error("Enter answered the prompt")
	}
	if _, err := os.Stat(filepath.Join(dir, ".config")); err == nil {
		t.Error("the user config was written before a yes")
	}
	press(h, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if h.saving {
		t.Error("n left the prompt open")
	}
	// A second change in the session doesn't ask again.
	press(h, tab(), enter(), tab())
	if h.saving {
		t.Error("asked twice in one session")
	}
}

func TestSetupNoPromptWithoutAChange(t *testing.T) {
	h, _ := setupHome(t)
	press(h, tab(), tab())
	if h.saving || h.panel != nil {
		t.Errorf("saving=%v panel=%v", h.saving, h.panel != nil)
	}
}

// Only what was changed becomes the panel's binding (source "setup"); what
// the command line bound stays. Fast and unattended picked in the panel are
// the weak kind, so they are shown.
func TestSetupChoicesBecomeFlags(t *testing.T) {
	h, _ := setupHome(t)
	var f runFlags
	f.o.Agents = []string{"implement=codex"}
	press(h, tab(), down(), enter(), down(), down(), enter(), tab()) // profile -> z
	got := h.sel.flags(f)
	if len(got.o.Agents) != 1 || got.o.Agents[0] != "implement=codex" {
		t.Errorf("the typed binding changed: %q", got.o.Agents)
	}
	if len(got.o.TUIAgents) != 1 || got.o.TUIAgents[0] != "implement=claude@z:sonnet" {
		t.Errorf("setup bindings %q", got.o.TUIAgents)
	}
	h.sel.mode, h.sel.tMode, h.sel.unattended, h.sel.tUnatt = pipeline.Fast, true, true, true
	got = h.sel.flags(f)
	if !got.o.Fast || !got.unattended || got.o.UnattendedFrom != pipeline.FromTUI {
		t.Errorf("flags %+v", got)
	}
}

func TestSetupSaveWritesTheUserConfigOnly(t *testing.T) {
	h, dir := setupHome(t)
	press(h, tab(), down(), enter(), down(), enter(), tab()) // implement profile -> claude@work
	press(h, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if h.saving || h.msg != nil {
		t.Fatalf("saving=%v msg=%v", h.saving, h.msg)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".config", "oge", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `agent = "claude@work"`) || strings.Contains(string(b), "unattended") {
		t.Errorf("user config:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "repo", ".oge")); err == nil {
		t.Error("something was written into the repo")
	}
}

func TestSetupWeakerModeIsShownLikeTheUsers(t *testing.T) {
	f := homeFrozen()
	f.Mode, f.ModeSource = pipeline.Fast, pipeline.FromTUI
	f.TrustWeakening = []pipeline.Weakening{{Option: "mode = fast", Source: pipeline.FromTUI, Detail: "no independent verifier"}}
	if got := strings.Join(setupLines(f), "\n"); !strings.Contains(got, "! trust-weakening: mode = fast (from setup)") {
		t.Errorf("setup bar:\n%s", got)
	}
}

// --- end to end ---

// Tab, pick the profile claude@z for implement, save, Run: the Ledger names
// the profile, the user config holds it, and the next session shows it.
func TestSetupPicksAProfileRunsAndSaves(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	home := f.vars["HOME"]
	writeFile(t, filepath.Join(home, ".claude-z", "settings.json"), []byte("{}"))
	withClaude := func(env *Env) {
		env.Agents["claude"] = env.Agents["fake"]
		env.LookPath = func(name string) (string, error) {
			if name == "claude" {
				return name, nil
			}
			return "", os.ErrNotExist
		}
	}
	s := startHomeSessionWith(t, f, withClaude, "--fast")
	s.composer()
	s.type_("\t")
	s.expect("Setup")   // the header stays on a 24-row screen (#212)
	s.type_("\x1b[B\r") // implement profile
	s.expect("claude@z")
	s.type_("\x1b[B\r") // claude@z
	s.type_("\t")
	s.expect("Save as default for this repo?")
	s.type_("y")
	s.expect("default in your user config")
	s.type_("fix Add\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	recs, err := ledger.Replay(f.onlyRun(t))
	if err != nil {
		t.Fatal(err)
	}
	var named bool
	for _, r := range recs {
		if r.Type == run.RecAttemptStarting && bytes.Contains(r.Data, []byte(`"profile":"z"`)) {
			named = true
		}
	}
	if !named {
		t.Error("AttemptStarting doesn't name the profile z")
	}
	b, err := os.ReadFile(filepath.Join(home, ".config", "oge", "config.toml"))
	if err != nil || !strings.Contains(string(b), `agent = "claude@z"`) {
		t.Fatalf("user config (%v):\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".oge", "oge.toml")); err != nil {
		t.Fatal(err)
	}
	if proj, _ := os.ReadFile(filepath.Join(f.repo, ".oge", "oge.toml")); bytes.Contains(proj, []byte("claude@z")) {
		t.Error("the profile reached the project config")
	}

	// A new session: the setup bar shows the saved profile.
	s2 := startHomeSessionWith(t, f, withClaude, "--fast")
	s2.expect("implement claude@z")
	s2.type_("\x03")
	if code := s2.wait(); code != ExitOK {
		t.Fatalf("second session exit %d\n%s", code, s2.errOut.String())
	}
}

// --- the user config's unattended and apply ---

func saveUserDefaults(t *testing.T, f *runFixture, d pipeline.RepoDefaults) {
	t.Helper()
	path := filepath.Join(f.vars["HOME"], ".config", "oge", "config.toml")
	if err := pipeline.SaveRepoDefaults(path, f.repo, d); err != nil {
		t.Fatal(err)
	}
}

func yes() *bool { v := true; return &v }

// unattended = true in the user config is what --unattended is, and it is
// shown, in the dry run and the Receipt, as a trust-weakening option from the
// user config, because it removes Gates (ADR-0008). apply = true is --apply.
func TestUserConfigUnattendedAndApplyAreUsed(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	saveUserDefaults(t, f, pipeline.RepoDefaults{Unattended: yes(), Apply: yes()})

	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--dry-run")
	if code != ExitOK {
		t.Fatalf("dry run exit %d\n%s", code, errOut)
	}
	if !strings.Contains(out, "Trust-weakening options in effect") || !strings.Contains(out, "unattended") || !strings.Contains(out, "user config") {
		t.Errorf("the dry run doesn't show unattended as a user-config weakening:\n%s", out)
	}
	if strings.Contains(out+errOut, "isn't used yet") {
		t.Errorf("the not-used notice is still there:\n%s%s", out, errOut)
	}

	// No terminal and no --unattended: the user config lets it run.
	code, out, errOut = f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "trust-weakening option in effect: unattended (from user config)") {
		t.Errorf("the Receipt doesn't show it:\n%s", out)
	}
	b, err := os.ReadFile(filepath.Join(f.repo, "add.go"))
	if err != nil || !strings.Contains(string(b), "a + b") {
		t.Errorf("apply = true didn't apply the Candidate (%v):\n%s", err, b)
	}
}

// A typed --unattended is the human's explicit flag, not a weakening to flag.
func TestTypedUnattendedIsNotATrustWeakening(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	_, out, _ := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended", "--dry-run")
	if strings.Contains(out, "Trust-weakening") {
		t.Errorf("a typed --unattended was shown as a weakening:\n%s", out)
	}
}

// Blind isn't built: the picker shows it but never takes it, and nothing
// saves it.
func TestSetupBlindCannotBePickedOrSaved(t *testing.T) {
	h, _ := setupHome(t)
	press(h, tab())
	cursorTo(h, rowMode)
	press(h, enter(), down(), down(), enter())
	if h.sel.mode == pipeline.Blind || h.sel.tMode {
		t.Fatalf("Blind was picked: %v", h.sel.mode)
	}
	if got := frame(h); !strings.Contains(got, "not built yet (#52)") {
		t.Errorf("the picker doesn't say why:\n%s", got)
	}
	h.sel.mode, h.sel.tMode = pipeline.Blind, true
	if d := h.sel.defaults(pipeline.RepoDefaults{}); d.Mode != "" {
		t.Errorf("Blind would be saved: %q", d.Mode)
	}
}

// A close the Run would refuse doesn't leave a bar that says otherwise.
func TestSetupRefusedCloseClearsTheBar(t *testing.T) {
	h, _ := setupHome(t)
	h.resolve = func(*setupState) (*resolved, []string) { return nil, []string{"this Run isn't valid"} }
	press(h, tab())
	cursorTo(h, rowUnattended)
	press(h, enter(), tab())
	if len(h.setup) != 0 || len(h.msg) == 0 || h.saving {
		t.Errorf("setup=%v msg=%v saving=%v", h.setup, h.msg, h.saving)
	}
}

// [s] saves after a "no", and Tab works when the config only resolves later.
func TestSetupSaveKeyAndLateTab(t *testing.T) {
	h, dir := setupHome(t)
	press(h, tab())
	cursorTo(h, rowUnattended)
	press(h, enter(), tab(), tea.KeyPressMsg{Code: 'n', Text: "n"})
	press(h, tab(), tea.KeyPressMsg{Code: 's', Text: "s"})
	if h.note == "" {
		t.Fatalf("no save: %v", h.msg)
	}
	if _, err := os.Stat(filepath.Join(dir, ".config", "oge", "config.toml")); err != nil {
		t.Error(err)
	}
	late := testHome(80)
	late.sel = &setupState{env: h.sel.env, root: h.sel.root}
	press(late, tab())
	if late.panel == nil {
		t.Errorf("Tab stayed dead: %v", late.msg)
	}
}

// With no agent registered the picker is empty and nothing breaks.
func TestSetupPickerWithNoAgents(t *testing.T) {
	h, _ := setupHome(t)
	h.sel.env.Agents = nil
	h.sel.agents, _ = h.sel.choices()
	press(h, tab(), enter(), enter(), tab())
	_ = frame(h)
	if h.sel.impl.touched {
		t.Error("a pick came from nowhere")
	}
}

// A user-config unattended removes Gates; it doesn't stop the first-run
// proposal, which needs a terminal.
func TestUserUnattendedKeepsTheFirstRunProposal(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.interactive = true
	f.setenv("TERM", "xterm-256color")
	saveUserDefaults(t, f, pipeline.RepoDefaults{Unattended: yes()})
	if err := os.Remove(filepath.Join(f.repo, ".oge", "oge.toml")); err != nil {
		t.Skip(err)
	}
	_, _, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--dry-run")
	if strings.Contains(errOut, "no terminal to review") {
		t.Errorf("a user-config unattended counted as no terminal:\n%s", errOut)
	}
}

// The picker learns in the background which profiles are logged in, and
// reuses what the session already found (#186).
func TestSetupChoicesRemembersLogins(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".claude-work", ".claude-z"} {
		writeFile(t, filepath.Join(home, d, "settings.json"), []byte("{}"))
	}
	stub := &countingLogins{loginStub: loginStub{home: home, in: []string{".claude-z"}}}
	env := Env{
		Getenv: func(k string) string {
			if k == "HOME" {
				return home
			}
			return ""
		},
		Agents: map[string]agent.Adapter{claude.Name: stub},
		logins: &loginCache{},
	}
	s := newSetupState(env, t.TempDir())
	_, profiles := s.choices()
	<-s.refreshLogins(profiles)
	notes := map[string]string{}
	for _, p := range profiles {
		notes[p.Label()] = s.loginNote(p.Dir)
	}
	want := map[string]string{"claude": "not logged in", "claude@work": "not logged in", "claude@z": ""}
	if !reflect.DeepEqual(notes, want) {
		t.Errorf("notes = %v, want %v", notes, want)
	}
	// Only the logged-in profile is remembered; the others are asked again.
	stub.asked = nil
	<-s.refreshLogins(profiles)
	if len(stub.asked) != 2 {
		t.Errorf("asked %v, want the two logged-out profiles", stub.asked)
	}
}

type countingLogins struct {
	loginStub
	asked []string
}

func (c *countingLogins) LoginStatuses(ctx context.Context, ps []claude.Profile) []claude.Login {
	for _, p := range ps {
		c.asked = append(c.asked, p.Label())
	}
	return c.loginStub.LoginStatuses(ctx, ps)
}

// loginRunner is the scripted fake agent that also answers login questions.
type loginRunner struct {
	agent.Adapter
	loginStub
}

// A bare oge whose default profile is logged out still opens the composer:
// Enter shows the refusal, and Tab picks a profile that is logged in (#186).
func TestHomeLoggedOutDefaultOpensComposerAndPicksAnother(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	home := f.vars["HOME"]
	writeFile(t, filepath.Join(home, ".claude-z", "settings.json"), []byte("{}"))
	withClaude := func(env *Env) {
		env.Agents["claude"] = loginRunner{Adapter: env.Agents["fake"], loginStub: loginStub{home: home, in: []string{".claude-z"}}}
		env.LookPath = func(name string) (string, error) {
			if name == "claude" {
				return name, nil
			}
			return "", os.ErrNotExist
		}
	}
	s := startHomeSessionWith(t, f, withClaude, "--fast", "--agent", "claude")
	s.composer()
	s.type_("fix Add\r")
	s.expect("claude isn't logged in. Logged in: claude@z. Pick one with Tab,")
	s.type_("\t")
	s.expect("Setup")   // the header stays on a 24-row screen (#212)
	s.type_("\x1b[B\r") // implement profile
	s.expect("claude@z")
	s.type_("\x1b[B\r") // claude@z
	s.type_("\t")
	s.type_("n")
	s.type_("\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}

func TestSetupPanelShowsAgentLaunchProfile(t *testing.T) {
	h, _ := setupHome(t)
	press(h, tab())
	h.height = 60 // tall enough to show the read-only launch section whole
	got := frame(h)
	info, err := claude.LaunchInfoFor("implementer", "")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Launch profile v%d %s", info.Version, info.Hash)
	if !strings.Contains(got, want) {
		t.Errorf("the panel lacks %q:\n%s", want, got)
	}
	if strings.Contains(got, ".ssh") || strings.Contains(got, ".aws") {
		t.Errorf("the panel names credential paths:\n%s", got)
	}
	golden(t, "setup-agent-launch", got)
}

func TestSetupEffortPickerChangesTheLaunchProfile(t *testing.T) {
	h, _ := setupHome(t)
	press(h, tab())
	cursorTo(h, rowEffort)
	before := frame(h)
	press(h, enter(), down(), down(), down()) // default, low, medium, high
	press(h, enter())
	got := frame(h)
	if before == got || !strings.Contains(got, "implement effort") || !strings.Contains(got, "high") {
		t.Fatalf("effort not shown as high:\n%s", got)
	}
	h.height = 60 // tall enough to show the read-only launch section whole
	got = frame(h)
	info, err := claude.LaunchInfoFor("implementer", "high")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("Launch profile v%d %s", info.Version, info.Hash); !strings.Contains(got, want) {
		t.Errorf("the launch view lacks %q:\n%s", want, got)
	}
	if !strings.Contains(got, "--effort high") {
		t.Errorf("the launch view lacks the effort:\n%s", got)
	}
	f := h.panel.sel.flags(runFlags{})
	if f.o.TUIEfforts["implement"] != "high" {
		t.Errorf("the next Run is not sent the effort: %+v", f.o)
	}
	golden(t, "setup-effort", got)
}

func TestSetupThemeRowGolden(t *testing.T) {
	h, _ := setupHome(t)
	h.st = newStyles(true)
	h.st.setTheme("auto")
	press(h, tab())
	cursorTo(h, rowTheme)
	h.panel.st = h.st
	golden(t, "setup-theme-row", ansi.Strip(frame(h)))
	press(h, enter())
	got := ansi.Strip(frame(h))
	for _, n := range pipeline.KnownThemes {
		if !strings.Contains(got, n) {
			t.Errorf("picker lacks %s:\n%s", n, got)
		}
	}
	golden(t, "setup-theme-picker", got)
}

func TestSetupThemeAppliesAtOnceAndIsSaved(t *testing.T) {
	h, dir := setupHome(t)
	h.st = newStyles(true)
	h.st.setTheme("auto")
	press(h, tab())
	cursorTo(h, rowTheme)
	h.panel.st = h.st
	before := h.st.ok("x")
	press(h, enter(), down(), enter()) // auto -> dark
	if h.st.themeName() != "dark" || h.st.ok("x") == before {
		t.Fatalf("theme = %q, ok paints %q", h.st.themeName(), h.st.ok("x"))
	}
	if !h.sel.tTheme || h.sel.theme != "dark" {
		t.Fatalf("not recorded: %+v", h.sel)
	}
	press(h, tab(), tea.KeyPressMsg{Code: 'y', Text: "y"})
	data, err := os.ReadFile(filepath.Join(dir, ".config", "oge", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `theme = "dark"`) {
		t.Errorf("not saved:\n%s", data)
	}
}

func TestSetupThemeRowWithoutColour(t *testing.T) {
	h, _ := setupHome(t) // newStyles(false): NO_COLOR
	press(h, tab())
	cursorTo(h, rowTheme)
	press(h, enter())
	if h.panel.pick != nil || !strings.Contains(frame(h), "NO_COLOR is set") {
		t.Errorf("NO_COLOR theme row:\n%s", frame(h))
	}
}

// The panel is windowed to the terminal (#212): 80x24 with the cursor at the
// top, middle and bottom of the panel.
func TestSetupPanelWindowGoldens(t *testing.T) {
	for _, c := range []struct {
		name string
		at   func(n int) int
	}{
		{"top", func(int) int { return 0 }},
		{"middle", func(n int) int { return n / 2 }},
		{"bottom", func(n int) int { return n - 1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _ := setupHome(t)
			h.width, h.height = 80, 24
			press(h, tab())
			h.panel.cursor = c.at(len(h.panel.rows()))
			f := frame(h)
			if n := strings.Count(f, "\n"); n > 24 {
				t.Errorf("frame is %d lines on a 24-row terminal", n)
			}
			if !strings.Contains(f, "Setup") || !strings.Contains(f, "> ") {
				t.Errorf("header or focused row scrolled off:\n%s", f)
			}
			golden(t, "setup-panel-"+c.name, f)
		})
	}
}

func TestSetupPanelTooSmall(t *testing.T) {
	h, _ := setupHome(t)
	h.width, h.height = 80, 8
	press(h, tab())
	if f := frame(h); !strings.Contains(f, "terminal too small") {
		t.Errorf("no too-small line:\n%s", f)
	}
}

// Down past the last row scrolls through the read-only sections, so the whole
// launch section can be read at 80x24 (#212).
func TestSetupPanelScrollsToReadOnlySections(t *testing.T) {
	h, _ := setupHome(t)
	h.width, h.height = 80, 24
	press(h, tab())
	cursorTo(h, rowAddGlob)
	if f := frame(h); strings.Contains(f, "no reads of") {
		t.Fatalf("launch section visible before scrolling:\n%s", f)
	}
	for i := 0; i < 40; i++ {
		press(h, down())
	}
	f := frame(h)
	for _, w := range []string{"Agent launched with", "MCP servers", "no reads of", "verify     codex"} {
		if !strings.Contains(f, w) {
			t.Errorf("%q not visible at the end:\n%s", w, f)
		}
	}
	golden(t, "setup-panel-scrolled-end", f)
	press(h, tea.KeyPressMsg{Code: tea.KeyUp})
	if h.panel.scroll == 0 || h.panel.cursor != len(h.panel.rows())-1 {
		t.Errorf("up should scroll back first: scroll=%d", h.panel.scroll)
	}
}
