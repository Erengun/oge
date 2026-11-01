package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func themeEnv(t *testing.T, content string) func(string) string {
	t.Helper()
	dir := t.TempDir()
	if content != "" {
		p := filepath.Join(dir, "oge", customThemeFile)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return dir
		}
		return ""
	}
}

func writeTheme(t *testing.T, getenv func(string) string, content string) {
	t.Helper()
	p := customThemePath(getenv)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCustomThemeOverridesBase(t *testing.T) {
	env := themeEnv(t, "base = \"dark\"\naccent = \"#112233\"\nok = 2\n")
	st := newStyles(true)
	st.setTheme("light")
	if n := st.reloadCustomTheme(env); n != "" {
		t.Fatalf("notice %q", n)
	}
	p := st.th.palette()
	if p.accent != rgb(0x112233) || p.ok != lipgloss.ANSIColor(2) {
		t.Errorf("overrides not applied: %+v", p)
	}
	if p.bad != paletteOf("dark", false, false).bad {
		t.Error("bad should keep the base preset's colour (base = dark, not the light preset)")
	}
	if got := st.ok("x"); !strings.Contains(got, "x") {
		t.Errorf("paints %q", got)
	}
}

func TestCustomThemeWithoutBaseKeepsPreset(t *testing.T) {
	env := themeEnv(t, "bad = \"#010203\"\n")
	st := newStyles(true)
	st.setTheme("light")
	st.reloadCustomTheme(env)
	p := st.th.palette()
	if p.bad != rgb(0x010203) || p.ok != paletteOf("light", false, false).ok {
		t.Errorf("palette %+v", p)
	}
}

func TestCustomThemeIsStrictAndFallsBack(t *testing.T) {
	for name, tc := range map[string]struct{ file, want string }{
		"unknown key": {"acent = \"#112233\"\n", "acent: unknown key"},
		"bad hex":     {"accent = \"#12\"\n", "accent: must be #rrggbb"},
		"ansi range":  {"ok = 16\n", "ok: must be #rrggbb"},
		"bad base":    {"base = \"neon\"\n", "base: must be one of"},
		"not toml":    {"accent = = 1\n", "not valid TOML"},
	} {
		t.Run(name, func(t *testing.T) {
			env := themeEnv(t, tc.file)
			st := newStyles(true)
			st.setTheme("dark")
			note := st.reloadCustomTheme(env)
			if !strings.Contains(note, tc.want) || !strings.Contains(note, customThemeFile) || strings.Contains(note, "\n") {
				t.Errorf("notice %q, want one line with %q", note, tc.want)
			}
			if st.th.custom != nil || st.th.palette() != paletteOf("dark", false, false) {
				t.Error("did not fall back to the base preset")
			}
		})
	}
}

func TestCustomThemeNoColorWins(t *testing.T) {
	env := themeEnv(t, "accent = \"#112233\"\n")
	st := newStyles(false)
	if n := st.reloadCustomTheme(env); n != "" {
		t.Errorf("notice %q without colour", n)
	}
	if got := st.accent("x"); got != "x" {
		t.Errorf("NO_COLOR painted %q", got)
	}
}

func TestCustomThemeReloadsBetweenRuns(t *testing.T) {
	env := themeEnv(t, "accent = \"#112233\"\n")
	st := newStyles(true)
	h := &home{st: st, resolve: func(*setupState) (*resolved, []string) { return nil, nil }, chatView: &chatView{comp: newComposer()}}
	h.reloadTheme = func() string { return st.reloadCustomTheme(env) }
	h.reopen()
	if st.th.palette().accent != rgb(0x112233) {
		t.Fatal("first open did not load the file")
	}
	writeTheme(t, env, "accent = \"#445566\"\n")
	h.reopen() // the composer opens after a Run
	if st.th.palette().accent != rgb(0x445566) {
		t.Error("the edit was not picked up when the composer reopened")
	}
	writeTheme(t, env, "accent = \"oops\"\n")
	h.reopen()
	if len(h.msg) != 1 || !strings.Contains(h.msg[0], "accent") {
		t.Errorf("no notice for a bad file: %q", h.msg)
	}
	if st.th.custom != nil {
		t.Error("bad file still applied")
	}
	if err := os.Remove(customThemePath(env)); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if st.th.custom != nil || len(h.msg) != 0 {
		t.Error("a removed file should clear the custom theme silently")
	}
}
