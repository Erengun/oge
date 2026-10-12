package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

func mk(t *testing.T, path string, dir bool) {
	t.Helper()
	if dir {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func noEnv(string) string { return "" }

func TestProfilesDiscovery(t *testing.T) {
	home := t.TempDir()
	mk(t, filepath.Join(home, ".claude-work", ".claude.json"), false)
	mk(t, filepath.Join(home, ".claude-z", "settings.json"), false)
	mk(t, filepath.Join(home, ".claude-personal", "projects"), true)
	mk(t, filepath.Join(home, ".claude-empty"), true)                      // no config
	mk(t, filepath.Join(home, ".claude-notes", "README.md"), false)        // no config
	mk(t, filepath.Join(home, ".claude-bad@name", "settings.json"), false) // not bindable
	mk(t, filepath.Join(home, ".claude-file"), false)                      // a file, not a dir
	mk(t, filepath.Join(home, "claude-nodot", "settings.json"), false)     // wrong prefix

	got := Profiles(home, noEnv, nil)
	want := []Profile{
		{"", filepath.Join(home, ".claude")},
		{"personal", filepath.Join(home, ".claude-personal")},
		{"work", filepath.Join(home, ".claude-work")},
		{"z", filepath.Join(home, ".claude-z")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Profiles\n got %v\nwant %v", got, want)
	}
	for i, l := range []string{"claude", "claude@personal", "claude@work", "claude@z"} {
		if got[i].Label() != l {
			t.Errorf("Label %d = %q, want %q", i, got[i].Label(), l)
		}
	}
}

func TestProfilesParentConfigDirIsTheDefault(t *testing.T) {
	home := t.TempDir()
	mk(t, filepath.Join(home, ".claude-work", "settings.json"), false)
	getenv := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(home, ".claude-work")
		}
		return ""
	}
	got := Profiles(home, getenv, nil)
	if got[0].Name != "" || got[0].Dir != filepath.Join(home, ".claude-work") {
		t.Errorf("default = %v, want the parent's CLAUDE_CONFIG_DIR", got[0])
	}
}

func TestProfilesExtraAddsAndSorts(t *testing.T) {
	home := t.TempDir()
	mk(t, filepath.Join(home, ".claude-work", "settings.json"), false)
	got := Profiles(home, noEnv, map[string]string{"corp": "/srv/corp-claude", "bad name": "/x", "rel": "relative"})
	var names []string
	for _, p := range got {
		names = append(names, p.Label())
	}
	if want := []string{"claude", "claude@corp", "claude@work"}; !reflect.DeepEqual(names, want) {
		t.Errorf("profiles %v, want %v", names, want)
	}
}

func TestProfilesNeverReadFileContents(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".claude-work", ".claude.json")
	mk(t, cfg, false)
	if err := os.Chmod(cfg, 0); err != nil { // unreadable: only a Stat may touch it
		t.Fatal(err)
	}
	if got := Profiles(home, noEnv, nil); len(got) != 2 {
		t.Errorf("an unreadable config file still marks a profile; got %v", got)
	}
}

func TestStagesOnDifferentProfilesGetTheirOwnDirAndDenyTheOthers(t *testing.T) {
	home := t.TempDir()
	work, z := filepath.Join(home, ".claude-work"), filepath.Join(home, ".claude-z")
	mk(t, filepath.Join(work, "settings.json"), false)
	mk(t, filepath.Join(z, "settings.json"), false)
	parent := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "PATH=/usr/bin"}

	launch := func(role, prof string) ([]string, []string) {
		p, err := profileFor(role)
		if err != nil {
			t.Fatal(err)
		}
		spec := agent.LaunchSpec{Role: role, Profile: prof, RunID: "r"}
		env, err := p.env(parent, spec)
		if err != nil {
			t.Fatal(err)
		}
		return env, p.denyRead(spec, parent)
	}
	configDir := func(env []string) (dirs []string) {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
				dirs = append(dirs, v)
			}
		}
		return dirs
	}
	implEnv, implDeny := launch("implementer", "work")
	verEnv, verDeny := launch("verifier", "z")
	if got := configDir(implEnv); !reflect.DeepEqual(got, []string{work}) {
		t.Errorf("implementer CLAUDE_CONFIG_DIR = %v, want exactly %s", got, work)
	}
	if got := configDir(verEnv); !reflect.DeepEqual(got, []string{z}) {
		t.Errorf("verifier CLAUDE_CONFIG_DIR = %v, want exactly %s", got, z)
	}
	for name, c := range map[string]struct {
		deny []string
		dirs []string
	}{
		"implementer": {implDeny, []string{z, filepath.Join(home, ".claude")}},
		"verifier":    {verDeny, []string{work, filepath.Join(home, ".claude")}},
	} {
		for _, d := range c.dirs {
			if !oneOf(d, c.deny) {
				t.Errorf("%s doesn't deny %s: %v", name, d, c.deny)
			}
		}
	}
}

func TestDefaultProfileKeepsTheParentConfigDir(t *testing.T) {
	parent := []string{"HOME=/synthetic/home", "CLAUDE_CONFIG_DIR=/synthetic/claude-config"}
	p, _ := profileFor("implementer")
	env, err := p.env(parent, agent.LaunchSpec{Role: "implementer"})
	if err != nil {
		t.Fatal(err)
	}
	if !oneOf("CLAUDE_CONFIG_DIR=/synthetic/claude-config", env) {
		t.Errorf("the default profile changed CLAUDE_CONFIG_DIR: %v", env)
	}
}

func TestUnknownProfileFailsTheLaunchNamingTheKnownOnes(t *testing.T) {
	home := t.TempDir()
	mk(t, filepath.Join(home, ".claude-work", "settings.json"), false)
	p, _ := profileFor("implementer")
	_, err := p.env([]string{"HOME=" + home}, agent.LaunchSpec{Role: "implementer", Profile: "nope"})
	if err == nil || !strings.Contains(err.Error(), "known: work") {
		t.Errorf("err = %v, want one listing the known profiles", err)
	}
}
