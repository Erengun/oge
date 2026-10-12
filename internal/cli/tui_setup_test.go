package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/agent"
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
	// Tab, down to "unattended", Enter flips it, Tab closes.
	press(h, tab(), down(), down(), down(), down(), down(), down(), down(), down(), enter(), tab())
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

// The choices become flags with the source "tui", replacing what the
// command line bound; Fast and unattended picked in the panel are the weak
// kind, so they are shown.
func TestSetupChoicesBecomeFlags(t *testing.T) {
	h, _ := setupHome(t)
	var f runFlags
	f.o.Agents = []string{"implement=codex"}
	press(h, tab(), down(), enter(), down(), down(), enter(), tab()) // profile -> z
	got := h.sel.flags(f)
	if got.o.BindingsFrom != pipeline.FromTUI {
		t.Errorf("source %q", got.o.BindingsFrom)
	}
	if len(got.o.Agents) != 1 || got.o.Agents[0] != "implement=claude@z:sonnet" {
		t.Errorf("agents %q", got.o.Agents)
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

// With a typed --agent and a TUI pick for the same Stage, the pick wins and
// the Run is valid (a flag can't bind a Stage twice).
func TestSetupWeakerModeIsShownLikeTheUsers(t *testing.T) {
	f := homeFrozen()
	f.Mode, f.ModeSource = pipeline.Fast, pipeline.FromTUI
	f.TrustWeakening = []pipeline.Weakening{{Option: "mode = fast", Source: pipeline.FromTUI, Detail: "no independent verifier"}}
	if got := strings.Join(setupLines(f), "\n"); !strings.Contains(got, "! trust-weakening: mode = fast (from tui)") {
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
	s.expect("applies to the next Run")
	s.type_("\x1b[B\r") // implement profile
	s.expect("claude@z")
	s.type_("\x1b[B\r") // claude@z
	s.type_("\t")
	s.expect("Save as default for this repo?")
	s.type_("y")
	s.expect("default in your user config")
	s.type_("fix Add\r")
	s.expect("[a] apply")
	s.type_("q")
	s.composer()
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
