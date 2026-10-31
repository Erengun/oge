package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func keysEnv(t *testing.T, content string) func(string) string {
	t.Helper()
	dir := t.TempDir()
	env := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return dir
		}
		return ""
	}
	if content != "" {
		p := keybindingsPath(env)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

// withKeys loads content as the keybindings file.
func withKeys(t *testing.T, content string) (*keyMap, string) {
	t.Helper()
	return loadKeybindings(keysEnv(t, content))
}

const validKeys = "[composer]\neditor = \"ctrl+g\"\nnewline = [\"ctrl+j\", \"alt+enter\"]\n[run]\nexpand = \"ctrl+t\"\n"

func TestKeybindingsOverrideResolve(t *testing.T) {
	kb, n := withKeys(t, validKeys)
	if n != "" {
		t.Fatalf("notice %q for a good file", n)
	}
	for _, c := range []struct{ ctx, key, want string }{
		{"composer", "ctrl+g", "editor"},
		{"composer", "ctrl+e", "ctrl+e"}, // the old key is no longer the editor's
		{"composer", "shift+enter", "shift+enter"},
		{"composer", "alt+enter", "newline"},
		{"run", "ctrl+t", "expand"},
		{"run", "ctrl+o", "ctrl+o"},
		{"composer", "ctrl+s", "stash"}, // not listed: default kept
	} {
		if got := kb.resolve(c.ctx, c.key); got != c.want {
			t.Errorf("resolve(%s, %s) = %q, want %q", c.ctx, c.key, got, c.want)
		}
	}
}

func TestEverydayHelpShowsRemappedKeys(t *testing.T) {
	kb, _ := withKeys(t, "[composer]\nsubmit = \"ctrl+q\"\nkeysPanel = \"f1\"\n")
	got := helpText(kb)
	for _, want := range []string{"ctrl+q", "f1"} {
		if !strings.Contains(got, want) {
			t.Errorf("everyday help lacks %s", want)
		}
	}
}

func TestKeybindingsShowInHelpAndPanel(t *testing.T) {
	kb, _ := withKeys(t, validKeys)
	help := advancedHelpText(kb)
	for _, want := range []string{"ctrl+g", `ctrl+j · \+enter`, "ctrl+t"} {
		if !strings.Contains(help, want) {
			t.Errorf("/help lacks %q", want)
		}
	}
	kb.src = "~/.config/oge/keybindings.toml" // the temp dir isn't stable
	h := testHome(80)
	h.setKeys(kb)
	typeText(h, "?")
	golden(t, "keys-panel-override", frame(h))
}

func TestKeybindingsMissingFileKeepsDefaults(t *testing.T) {
	kb, n := withKeys(t, "")
	if n != "" {
		t.Errorf("notice %q without a file", n)
	}
	if !slices.Equal(kb.actionKeys("composer", "editor"), []string{"ctrl+e", "ctrl+g"}) {
		t.Error("defaults changed")
	}
}

func TestKeybindingsProblems(t *testing.T) {
	for _, c := range []struct{ name, file, want string }{
		{"unknown context", "[compose]\neditor = \"ctrl+g\"\n", "compose"},
		{"unknown action", "[composer]\nedtor = \"ctrl+g\"\n", "composer.edtor"},
		{"bad key name", "[composer]\neditor = \"Ctrl+G\"\n", "composer.editor"},
		{"bad key name order", "[composer]\neditor = \"alt+ctrl+g\"\n", "composer.editor"},
		{"locked ctrl+c", "[composer]\neditor = \"ctrl+c\"\n", "ctrl+c"},
		{"locked esc", "[run]\nexpand = \"esc\"\n", "esc"},
		{"locked enter", "[composer]\nstash = \"enter\"\n", "locked"},
		{"typed text", "[run]\ncancelQueue = \"y\"\n", "types text"},
		{"shift+space", "[run]\nexpand = \"shift+space\"\n", "types text"},
		{"space", "[composer]\nstash = \"space\"\n", "locked"},
		{"shift+letter", "[composer]\nstash = \"shift+a\"\n", "types text"},
		{"uppercase", "[composer]\neditor = \"ctrl+G\"\n", "write it as ctrl+g"},
		{"alias", "[composer]\neditor = \"escape\"\n", "write it as esc"},
		{"out of table", "[composer]\nstash = \"tab\"\n", "composer.stash"},
		{"emacs key", "[run]\nexpand = \"ctrl+k\"\n", "run.expand"},
		{"duplicate in a context", "[composer]\nstash = \"ctrl+r\"\n", "also bound to composer.historySearch"},
		{"duplicate across contexts", "[run]\nexpand = \"ctrl+s\"\n", "also bound to composer.stash"},
		{"duplicate in one list", "[composer]\neditor = [\"ctrl+g\", \"ctrl+g\"]\n", "also bound"},
		{"empty list", "[composer]\nnewline = []\n", "can't be empty"},
		{"empty submit", "[composer]\nsubmit = \"\"\n", "can't be empty"},
		{"not a key", "[composer]\neditor = 3\n", "composer.editor"},
		{"not TOML", "[composer\n", "not valid TOML"},
	} {
		t.Run(c.name, func(t *testing.T) {
			kb, n := withKeys(t, c.file)
			if !strings.Contains(n, keybindingsFile) || !strings.Contains(n, c.want) {
				t.Errorf("notice %q lacks the file or %q", n, c.want)
			}
			if !strings.Contains(n, "default keys") {
				t.Errorf("notice %q doesn't say the defaults are used", n)
			}
			if !slices.EqualFunc(kb.binds, defaultKeyMap.binds, func(a, b actionBinding) bool {
				return a.ctx == b.ctx && a.name == b.name && slices.Equal(a.keys, b.keys)
			}) || len(kb.custom) > 0 {
				t.Error("a bad file was partly applied")
			}
			if !strings.Contains(kb.note(), "keybindings.toml ignored: ") {
				t.Errorf("the panel note is %q", kb.note())
			}
		})
	}
}

// The whole file is ignored when one entry is bad, not just the entry.
func TestKeybindingsBadFileAppliesNothing(t *testing.T) {
	kb, _ := withKeys(t, "[composer]\nredraw = \"ctrl+t\"\nstash = \"ctrl+c\"\n")
	if got := kb.resolve("composer", "ctrl+t"); got == "redraw" {
		t.Error("ctrl+t became the redraw key despite the bad stash entry")
	}
}

func TestKeybindingsDefaultsKeepTheirOwnKeys(t *testing.T) {
	// ? and enter are locked for other actions but fine for their own.
	kb, n := withKeys(t, "[composer]\nkeysPanel = [\"?\", \"f1\"]\nsubmit = [\"enter\", \"ctrl+q\"]\n")
	if n != "" {
		t.Errorf("notice %q", n)
	}
	if kb.resolve("composer", "f1") != "keysPanel" || kb.resolve("composer", "ctrl+q") != "submit" {
		t.Error("extra keys not bound")
	}
}

func TestKeyNames(t *testing.T) {
	for _, ok := range []string{"ctrl+g", "alt+enter", "shift+tab", "f2", "f12", "ctrl+alt+x", "ctrl+/", "alt+backspace", "ctrl+left", "?"} {
		if !validKeyName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "ctrl+", "Ctrl+g", "ctrl+G", "alt+X", "ctrl+gg", "f0", "f64", "fx", "alt+ctrl+g", "ctrl+ctrl+g", "return", "ctrl-g"} {
		if validKeyName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

// Every key the Bubble Tea table can print for the defaults is a valid name.
func TestDefaultKeysAreValidNames(t *testing.T) {
	for _, a := range defaultActions {
		for _, k := range a.keys {
			if !validKeyName(k) {
				t.Errorf("%s.%s default %q is not a valid name", a.ctx, a.name, k)
			}
		}
	}
}

// After a remap of other actions, the Gate still gets its keys.
func TestKeybindingsLeaveGateKeysAlone(t *testing.T) {
	kb, _ := withKeys(t, validKeys)
	m := &model{gate: &gateState{}, kb: kb}
	press := func(k tea.KeyPressMsg) bool { return m.gateKey(k) }
	if press(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}) {
		t.Error("the remapped expand key no longer reaches the view")
	}
	if !press(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}) {
		t.Error("the old expand key should be the Gate's now")
	}
	if press(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) {
		t.Error("ctrl+c no longer reaches the view")
	}
	if !press(tea.KeyPressMsg{Code: 'a', Text: "a"}) || m.gate.input != "a" {
		t.Errorf("a choice letter did not reach the Gate: %q", m.gate.input)
	}
	if !press(tea.KeyPressMsg{Code: tea.KeyEscape}) {
		t.Error("esc did not reach the Gate")
	}
}

func TestKeybindingsReloadWithTheComposer(t *testing.T) {
	env := keysEnv(t, validKeys)
	h := &home{comp: newComposer(), resolve: func(*setupState) (*resolved, []string) { return nil, nil }, chatView: &chatView{}}
	h.reloadTheme = func() string {
		kb, n := loadKeybindings(env)
		h.setKeys(kb)
		return n
	}
	h.reopen()
	if h.kb.resolve("run", "ctrl+t") != "expand" || h.comp.kb != h.kb {
		t.Fatal("first open did not load the file")
	}
	first := h.kb
	if err := os.WriteFile(keybindingsPath(env), []byte("[run]\nexpand = \"nope\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if len(h.msg) != 1 || !strings.Contains(h.msg[0], "run.expand") {
		t.Errorf("no notice for a bad file: %q", h.msg)
	}
	if h.kb.resolve("run", "ctrl+o") != "expand" {
		t.Error("a bad file did not restore the defaults")
	}
	if first.resolve("run", "ctrl+t") != "expand" {
		t.Error("the earlier keyMap was mutated")
	}
}

// Two sessions never see each other's remaps.
func TestKeybindingsArePerSession(t *testing.T) {
	kb, _ := withKeys(t, validKeys)
	if defaultKeyMap.resolve("run", "ctrl+o") != "expand" || kb.resolve("run", "ctrl+o") == "expand" {
		t.Error("a remap leaked into the defaults")
	}
}

func TestKeybindingsFileMustBeSmallAndRegular(t *testing.T) {
	env := keysEnv(t, "")
	p := keybindingsPath(env)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	big := "[composer]\n# " + strings.Repeat("x", maxConfigFile) + "\n"
	if err := os.WriteFile(p, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, n := loadKeybindings(env); !strings.Contains(n, "larger than") {
		t.Errorf("notice %q for an oversized file", n)
	}
	// A directory is not a regular file either, on every platform.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, n := loadKeybindings(env); !strings.Contains(n, "not a regular file") {
		t.Errorf("notice %q for a directory", n)
	}
}

// With submit moved off Enter, the queue box follows the submit keys.
func TestQueueBoxFollowsSubmitKeys(t *testing.T) {
	kb, n := withKeys(t, "[composer]\nsubmit = \"ctrl+q\"\n")
	if n != "" {
		t.Fatal(n)
	}
	if kb.actionFor("composer", "ctrl+q") != "submit" || kb.actionFor("composer", "enter") == "submit" {
		t.Error("submit not remapped")
	}
}

func TestKeybindingsPanelMarksOverrides(t *testing.T) {
	kb, _ := withKeys(t, "[composer]\neditor = \"ctrl+g\"\n")
	kb.src = "~/.config/oge/keybindings.toml" // the temp dir isn't stable
	h := testHome(80)
	h.setKeys(kb)
	typeText(h, "?")
	got := frame(h)
	for _, want := range []string{"ctrl+g *", "* keys from ~/.config/oge/keybindings.toml"} {
		if !strings.Contains(got, want) {
			t.Errorf("panel lacks %q", want)
		}
	}
	if strings.Contains(frame(func() *home { h := testHome(80); typeText(h, "?"); return h }()), " *") {
		t.Error("the default panel marks a key")
	}
	if !strings.Contains(advancedHelpText(kb), "* keys from ~/.config/oge/keybindings.toml") {
		t.Error("/help lacks the note")
	}
	checkPanelWidth(t, got)
	golden(t, "keys-panel-custom", got)
}

func TestKeybindingsPanelShowsProblem(t *testing.T) {
	kb, _ := withKeys(t, "[composer]\nstash = \"ctrl+c\"\n")
	h := testHome(80)
	h.setKeys(kb)
	typeText(h, "?")
	got := frame(h)
	if !strings.Contains(got, "keybindings.toml ignored: composer.stash") {
		t.Errorf("panel lacks the problem:\n%s", got)
	}
	checkPanelWidth(t, got)
	golden(t, "keys-panel-problem", got)
}

func checkPanelWidth(t *testing.T, frame string) {
	t.Helper()
	for _, l := range strings.Split(frame, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("line is %d columns: %q", w, l)
		}
	}
}

func TestShortPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "me")
	env := func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	if got := shortPath(filepath.Join(home, ".config", "oge", "keybindings.toml"), env); got != "~/.config/oge/keybindings.toml" {
		t.Errorf("got %q", got)
	}
	other := filepath.Join(t.TempDir(), "k.toml")
	if got := shortPath(other, env); got != other {
		t.Errorf("got %q", got)
	}
}

// docs/user-config.md lists every action with its default keys; this keeps
// the list and the table from drifting.
func TestKeybindingsDocsMatchTable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "user-config.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, a := range actionTable {
		var keys []string
		for _, k := range a.keys {
			keys = append(keys, "`"+k+"`")
		}
		row := "| `" + a.name + "` | " + strings.Join(keys, ", ") + " |"
		i := strings.Index(doc, "### `["+a.ctx+"]`")
		if i < 0 || !strings.Contains(doc[i:], row) {
			t.Errorf("docs/user-config.md lacks %s in [%s]", row, a.ctx)
		}
	}
	if n := strings.Count(doc, "| `"); n < len(actionTable) {
		t.Errorf("docs list %d rows for %d actions", n, len(actionTable))
	}
	for k := range lockedKeys {
		if !strings.Contains(doc, "`"+k+"`") {
			t.Errorf("docs don't mention the locked key %s", k)
		}
	}
}
