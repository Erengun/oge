package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const userCfg = `schema = 1

[profiles]
side = "/opt/claude-side"

[repos."/r/one"]
mode = "blind"
unattended = true

[repos."/r/one".stages.implement]
agent = "claude@z"
model = "sonnet"

[repos."/r/one".stages.verify]
agent = "claude@work"
`

func TestLoadUserConfigParses(t *testing.T) {
	c, probs := LoadUserConfig([]byte(userCfg))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	d := c.ForRepo("/r/one")
	if d == nil || d.Mode != "blind" || d.Unattended == nil || !*d.Unattended ||
		d.Stages.Implement != (UserStage{"claude@z", "sonnet"}) || d.Stages.Verify.Agent != "claude@work" {
		t.Fatalf("defaults = %+v", d)
	}
	if c.Profiles["side"] != "/opt/claude-side" || c.ForRepo("/r/other") != nil {
		t.Fatalf("profiles/other = %v", c.Profiles)
	}
}

func TestLoadUserConfigIsStrict(t *testing.T) {
	for name, tc := range map[string]struct{ in, key string }{
		"no schema":     {"[profiles]\n", "schema"},
		"newer schema":  {"schema = 2\n", "schema"},
		"unknown key":   {"schema = 1\nbogus = 1\n", "bogus"},
		"unknown stage": {"schema = 1\n[repos.\"/r\".stages.plan]\nagent = \"claude\"\n", `repos."/r".stages.plan`},
		"bad mode":      {"schema = 1\n[repos.\"/r\"]\nmode = \"turbo\"\n", `repos."/r".mode`},
		"relative repo": {"schema = 1\n[repos.\"r\"]\nmode = \"fast\"\n", `repos."r"`},
		"relative prof": {"schema = 1\n[profiles]\nx = \"rel\"\n", "profiles.x"},
		"wrong type":    {"schema = 1\n[repos.\"/r\"]\napply = \"yes\"\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			c, probs := LoadUserConfig([]byte(tc.in))
			if c != nil || len(probs) == 0 {
				t.Fatalf("accepted: %v", probs)
			}
			if tc.key != "" && probs[0].Key != tc.key {
				t.Errorf("problems = %v", probs)
			}
		})
	}
	if _, probs := LoadUserConfig([]byte("schema = [")); len(probs) == 0 {
		t.Error("invalid TOML accepted")
	}
}

func userRes(t *testing.T, cfg *Config, o Overrides) *Frozen {
	t.Helper()
	if cfg.Check.Commands == nil {
		cfg.Check.Commands = []CheckCommandConfig{{Run: "go test -json ./...", Report: "go-test-json"}}
		cfg.Project.TestGlobs = []string{"**/*_test.go"}
	}
	f, probs := Resolve(cfg, o, []string{"claude"})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	return f
}

func stage(f *Frozen, name string) Stage {
	for _, s := range f.Stages {
		if s.Name == name {
			return s
		}
	}
	return Stage{}
}

func TestPrecedenceFlagsOverUserOverRepoOverInstalled(t *testing.T) {
	repo := &Config{Schema: 1}
	repo.Pipelines.Default.Stages.Implement = StageConfig{Agent: "codex", Model: "gpt"}
	user := &RepoDefaults{Mode: "blind"}
	user.Stages.Implement = UserStage{Agent: "claude@z", Model: "sonnet"}

	// Installed default when nothing else binds.
	f := userRes(t, &Config{Schema: 1}, Overrides{})
	if s := stage(f, "implement"); s.Agent != "claude" || s.Source != FromInstalled {
		t.Fatalf("installed: %+v", s)
	}
	// Repo config binds.
	f = userRes(t, repo, Overrides{})
	if s := stage(f, "implement"); s.Agent != "codex" || s.Source != FromProject || f.Mode != Standard {
		t.Fatalf("repo: %+v mode %s", s, f.Mode)
	}
	// User beats repo, including mode.
	f = userRes(t, repo, Overrides{User: user})
	if s := stage(f, "implement"); s.Agent != "claude@z" || s.Model != "sonnet" || s.Source != FromUser || f.Mode != Blind {
		t.Fatalf("user: %+v mode %s", s, f.Mode)
	}
	// Flags beat user.
	f = userRes(t, repo, Overrides{User: user, Fast: true, Agents: []string{"implement=claude:opus"}})
	if s := stage(f, "implement"); s.Agent != "claude" || s.Model != "opus" || s.Source != FromCLI || f.Mode != Fast {
		t.Fatalf("flags: %+v mode %s", s, f.Mode)
	}
}

func TestUserModelAloneKeepsRepoAgent(t *testing.T) {
	repo := &Config{Schema: 1}
	repo.Pipelines.Default.Stages.Implement.Agent = "claude"
	u := &RepoDefaults{}
	u.Stages.Implement.Model = "haiku"
	s := stage(userRes(t, repo, Overrides{User: u}), "implement")
	if s.Agent != "claude" || s.Model != "haiku" || s.Source != FromUser {
		t.Fatalf("%+v", s)
	}
}

func TestUserConfigUnknownAgentIsAProblem(t *testing.T) {
	u := &RepoDefaults{}
	u.Stages.Implement.Agent = "nope@x"
	cfg := &Config{Schema: 1, Project: ProjectConfig{TestGlobs: []string{"a_test.go"}},
		Check: CheckConfig{Commands: []CheckCommandConfig{{Run: "go test -json", Report: "go-test-json"}}}}
	if _, probs := Resolve(cfg, Overrides{User: u}, []string{"claude"}); len(probs) == 0 ||
		!strings.Contains(probs[0].Key, "user config") {
		t.Fatalf("problems = %v", probs)
	}
}

func TestSaveRepoDefaultsRoundTripsOtherSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "oge", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(userCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	apply := true
	d := RepoDefaults{Mode: "fast", Apply: &apply}
	d.Stages.Implement = UserStage{Agent: "codex"}
	if err := SaveRepoDefaults(path, "/r/two", d); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	c, probs := LoadUserConfig(data)
	if len(probs) > 0 {
		t.Fatalf("saved file invalid: %v\n%s", probs, data)
	}
	if c.Profiles["side"] != "/opt/claude-side" {
		t.Errorf("profiles lost: %v", c.Profiles)
	}
	one, two := c.ForRepo("/r/one"), c.ForRepo("/r/two")
	if one == nil || one.Stages.Implement.Agent != "claude@z" || one.Stages.Verify.Agent != "claude@work" || *one.Unattended != true {
		t.Errorf("other repo changed: %+v", one)
	}
	if two == nil || two.Mode != "fast" || two.Apply == nil || !*two.Apply || two.Stages.Implement.Agent != "codex" {
		t.Errorf("new repo: %+v", two)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.toml.*")); len(m) != 0 {
		t.Errorf("temp file left: %v", m)
	}
}

func TestSaveCreatesDirPrivatelyAndRefusesToClobberInvalidFile(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "new", "oge", "config.toml")
	if err := SaveRepoDefaults(path, "/r/one", RepoDefaults{Mode: "blind"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v %v", fi, err)
	}
	bad := filepath.Join(base, "bad.toml")
	os.WriteFile(bad, []byte("schema = 1\nbogus = 1\n"), 0o600)
	if err := SaveRepoDefaults(bad, "/r/one", RepoDefaults{}); err == nil {
		t.Fatal("overwrote an invalid file")
	}
	if b, _ := os.ReadFile(bad); !strings.Contains(string(b), "bogus") {
		t.Error("invalid file was changed")
	}
}

func TestUserConfigPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if p := UserConfigPath(env(map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"})); p != "/x/oge/config.toml" {
		t.Error(p)
	}
	if p := UserConfigPath(env(map[string]string{"HOME": "/h"})); p != "/h/.config/oge/config.toml" {
		t.Error(p)
	}
	if UserConfigPath(nil) != "" {
		t.Error("nil getenv")
	}
}
