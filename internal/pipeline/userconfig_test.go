package pipeline

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// abs returns a clean absolute path for this OS.
func abs(t *testing.T, name string) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(d, name)
}

// q quotes p as a TOML literal string, so Windows backslashes survive.
func q(p string) string { return "'" + p + "'" }

func userCfg(side, one string) string {
	return `schema = 1

[profiles]
side = ` + q(side) + `

[repos.` + q(one) + `]
mode = "blind"
unattended = true

[repos.` + q(one) + `.stages.implement]
agent = "claude@z"
model = "sonnet"

[repos.` + q(one) + `.stages.verify]
agent = "claude@work"
`
}

func TestLoadUserConfigParses(t *testing.T) {
	side, one := abs(t, "side"), abs(t, "one")
	c, probs := LoadUserConfig([]byte(userCfg(side, one)))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	d := c.ForRepo(one)
	if d == nil || d.Mode != "blind" || d.Unattended == nil || !*d.Unattended ||
		d.Stages.Implement != (UserStage{Agent: "claude@z", Model: "sonnet"}) || d.Stages.Verify.Agent != "claude@work" {
		t.Fatalf("defaults = %+v", d)
	}
	if c.Profiles["side"] != side || c.ForRepo(abs(t, "other")) != nil {
		t.Fatalf("profiles/other = %v", c.Profiles)
	}
}

// fakeKey is key-shaped but assembled at run time, so the repo's secret
// scan (internal/fixturescan) sees no key literal.
var fakeKey = "sk-" + "ant-" + "api03-" + strings.Repeat("A", 20)

func TestLoadUserConfigIsStrict(t *testing.T) {
	r := abs(t, "r")
	rk := func(suffix string) string { return `repos."` + r + `"` + suffix }
	// The TOML decoder's unknown-key messages quote a key with its
	// backslashes escaped (Windows paths); our own checks print it raw.
	rkDecoded := func(suffix string) string { return `repos."` + strings.ReplaceAll(r, `\`, `\\`) + `"` + suffix }
	for name, tc := range map[string]struct{ in, key string }{
		"no schema":     {"[profiles]\n", "schema"},
		"newer schema":  {"schema = 2\n", "schema"},
		"unknown key":   {"schema = 1\nbogus = 1\n", "bogus"},
		"unknown stage": {"schema = 1\n[repos." + q(r) + ".stages.plan]\nagent = \"claude\"\n", rkDecoded(".stages.plan")},
		"bad mode":      {"schema = 1\n[repos." + q(r) + "]\nmode = \"turbo\"\n", rk(".mode")},
		"relative repo": {"schema = 1\n[repos.\"r\"]\nmode = \"fast\"\n", `repos."r"`},
		"unclean repo":  {"schema = 1\n[repos." + q(r+string(filepath.Separator)+"..") + "]\nmode = \"fast\"\n", ""},
		"relative prof": {"schema = 1\n[profiles]\nx = \"rel\"\n", "profiles.x"},
		"wrong type":    {"schema = 1\n[repos." + q(r) + "]\napply = \"yes\"\n", ""},
		"key as model":  {"schema = 1\n[repos." + q(r) + ".stages.implement]\nmodel = \"" + fakeKey + "\"\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			c, probs := LoadUserConfig([]byte(tc.in))
			if c != nil || len(probs) == 0 {
				t.Fatalf("accepted: %v", probs)
			}
			if tc.key != "" && probs[0].Key != tc.key {
				t.Errorf("problems = %v", probs)
			}
			for _, p := range probs {
				if strings.Contains(p.String(), "AAAAAAAA") {
					t.Errorf("problem echoes a value: %v", p)
				}
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
	profiles := []string{"z"}

	f := userRes(t, &Config{Schema: 1}, Overrides{})
	if s := stage(f, "implement"); s.Agent != "claude" || s.Source != FromInstalled {
		t.Fatalf("installed: %+v", s)
	}
	f = userRes(t, repo, Overrides{})
	if s := stage(f, "implement"); s.Agent != "codex" || s.Source != FromProject || f.Mode != Standard {
		t.Fatalf("repo: %+v mode %s", s, f.Mode)
	}
	f = userRes(t, repo, Overrides{User: user, ClaudeProfiles: profiles})
	if s := stage(f, "implement"); s.Agent != "claude" || s.Profile != "z" || s.Model != "sonnet" || s.Source != FromUser || f.Mode != Blind || f.ModeSource != FromUser {
		t.Fatalf("user: %+v mode %s", s, f.Mode)
	}
	f = userRes(t, repo, Overrides{User: user, ClaudeProfiles: profiles, Fast: true, Agents: []string{"implement=claude:opus"}})
	if s := stage(f, "implement"); s.Agent != "claude" || s.Profile != "" || s.Model != "opus" || s.Source != FromCLI || f.Mode != Fast {
		t.Fatalf("flags: %+v mode %s", s, f.Mode)
	}
	// --standard beats a user mode of fast.
	f = userRes(t, repo, Overrides{User: &RepoDefaults{Mode: "fast"}, Standard: true})
	if f.Mode != Standard || len(f.TrustWeakening) != 0 {
		t.Fatalf("--standard: %s %v", f.Mode, f.TrustWeakening)
	}
}

func TestUserAgentBringsItsOwnModel(t *testing.T) {
	repo := &Config{Schema: 1}
	repo.Pipelines.Default.Stages.Implement = StageConfig{Agent: "codex", Model: "gpt-5"}
	u := &RepoDefaults{}
	u.Stages.Implement.Agent = "claude"
	if s := stage(userRes(t, repo, Overrides{User: u}), "implement"); s.Agent != "claude" || s.Model != "" {
		t.Fatalf("%+v", s)
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

func resolveProbs(cfg *Config, o Overrides) []Problem {
	cfg.Project.TestGlobs = []string{"a_test.go"}
	cfg.Check.Commands = []CheckCommandConfig{{Run: "go test -json", Report: "go-test-json"}}
	_, probs := Resolve(cfg, o, []string{"claude"})
	return probs
}

func TestUserBindingProblems(t *testing.T) {
	for name, agent := range map[string]string{"unknown agent": "nope@x", "codex profile": "codex@foo", "unknown profile": "claude@zz", "bare at": "claude@"} {
		u := &RepoDefaults{}
		u.Stages.Implement.Agent = agent
		probs := resolveProbs(&Config{Schema: 1}, Overrides{User: u, ClaudeProfiles: []string{"z"}})
		if len(probs) == 0 || !strings.Contains(probs[0].Key, "user config") {
			t.Errorf("%s: problems = %v", name, probs)
		}
	}
}

func TestProjectProfileStillRefusedButUserProfileAccepted(t *testing.T) {
	repo := &Config{Schema: 1}
	repo.Pipelines.Default.Stages.Implement.Agent = "claude@z"
	if probs := resolveProbs(repo, Overrides{ClaudeProfiles: []string{"z"}}); len(probs) == 0 {
		t.Error("project profile accepted")
	}
	u := &RepoDefaults{}
	u.Stages.Implement.Agent = "claude@z"
	if probs := resolveProbs(&Config{Schema: 1}, Overrides{User: u, ClaudeProfiles: []string{"z"}}); len(probs) != 0 {
		t.Errorf("user profile refused: %v", probs)
	}
	// A user model must not launder a project-file profile.
	u = &RepoDefaults{}
	u.Stages.Implement.Model = "haiku"
	if probs := resolveProbs(repo, Overrides{User: u, ClaudeProfiles: []string{"z"}}); len(probs) == 0 {
		t.Error("project profile accepted after a user model")
	}
}

func TestUserFastIsATrustWeakeningOption(t *testing.T) {
	f := userRes(t, &Config{Schema: 1}, Overrides{User: &RepoDefaults{Mode: "fast"}})
	if len(f.TrustWeakening) != 1 || f.TrustWeakening[0].Option != "mode = fast" || f.TrustWeakening[0].Source != FromUser {
		t.Fatalf("%v", f.TrustWeakening)
	}
	f = userRes(t, &Config{Schema: 1}, Overrides{Fast: true})
	if len(f.TrustWeakening) != 0 {
		t.Fatalf("an explicit --fast is not a weakening: %v", f.TrustWeakening)
	}
	probs := resolveProbs(&Config{Schema: 1}, Overrides{User: &RepoDefaults{Mode: "fast"}, UserConfigPath: "/u/config.toml", Require: []string{"held-out"}})
	if len(probs) != 1 || !strings.Contains(probs[0].Msg, "/u/config.toml") || !strings.Contains(probs[0].Msg, "--standard") {
		t.Fatalf("%v", probs)
	}
	if probs := resolveProbs(&Config{Schema: 1}, Overrides{Fast: true, Standard: true}); len(probs) != 1 {
		t.Fatalf("%v", probs)
	}
}

func TestSaveRepoDefaultsRoundTripsOtherSections(t *testing.T) {
	side, one, two := abs(t, "side"), abs(t, "one"), abs(t, "two")
	path := filepath.Join(t.TempDir(), "cfg", "oge", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(userCfg(side, one)), 0o644); err != nil {
		t.Fatal(err)
	}
	apply := true
	d := RepoDefaults{Mode: "fast", Apply: &apply}
	d.Stages.Implement = UserStage{Agent: "codex"}
	if err := SaveRepoDefaults(path, two, d); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	c, probs := LoadUserConfig(data)
	if len(probs) > 0 {
		t.Fatalf("saved file invalid: %v\n%s", probs, data)
	}
	if c.Profiles["side"] != side {
		t.Errorf("profiles lost: %v", c.Profiles)
	}
	o, n := c.ForRepo(one), c.ForRepo(two)
	if o == nil || o.Stages.Implement.Agent != "claude@z" || o.Stages.Verify.Agent != "claude@work" || *o.Unattended != true {
		t.Errorf("other repo changed: %+v", o)
	}
	if n == nil || n.Mode != "fast" || n.Apply == nil || !*n.Apply || n.Stages.Implement.Agent != "codex" {
		t.Errorf("new repo: %+v", n)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.toml.*")); len(m) != 0 {
		t.Errorf("temp file left: %v", m)
	}
}

func TestSaveCreatesDirPrivatelyAndRefusesToClobberInvalidFile(t *testing.T) {
	base := t.TempDir()
	repo := abs(t, "one")
	path := filepath.Join(base, "new", "oge", "config.toml")
	if err := SaveRepoDefaults(path, repo, RepoDefaults{Mode: "blind"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) {
		t.Errorf("dir: %v %v", fi, err)
	}
	bad := filepath.Join(base, "bad.toml")
	os.WriteFile(bad, []byte("schema = 1\nbogus = 1\n"), 0o600)
	if err := SaveRepoDefaults(bad, repo, RepoDefaults{}); err == nil {
		t.Fatal("overwrote an invalid file")
	}
	if b, _ := os.ReadFile(bad); !strings.Contains(string(b), "bogus") {
		t.Error("invalid file was changed")
	}
}

func TestSaveKeepsASymlinkedConfigAndCanonicalisesTheKey(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "dotfiles", "config.toml")
	link := filepath.Join(base, "xdg", "oge", "config.toml")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.WriteFile(real, []byte("schema = 1\n"), 0o600)
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	repo := abs(t, "r")
	os.MkdirAll(repo, 0o755)
	if err := SaveRepoDefaults(link, repo+string(filepath.Separator)+".", RepoDefaults{Mode: "fast"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced")
	}
	data, _ := os.ReadFile(real)
	c, probs := LoadUserConfig(data)
	if len(probs) > 0 || c.ForRepo(repo) == nil {
		t.Fatalf("%v\n%s", probs, data)
	}
}

func TestUserConfigPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	x, h := abs(t, "x"), abs(t, "h")
	if p := UserConfigPath(env(map[string]string{"XDG_CONFIG_HOME": x, "HOME": h})); p != filepath.Join(x, "oge", "config.toml") {
		t.Error(p)
	}
	if p := UserConfigPath(env(map[string]string{"HOME": h})); p != filepath.Join(h, ".config", "oge", "config.toml") {
		t.Error(p)
	}
	if UserConfigPath(nil) != "" {
		t.Error("nil getenv")
	}
}

func TestLoadUserConfigNotify(t *testing.T) {
	c, probs := LoadUserConfig([]byte("schema = 1\n[notify]\nattention = false\nbell = false\n"))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	n := c.NotifyConfig()
	if n == nil || n.Attention == nil || *n.Attention || n.Bell == nil || *n.Bell || n.RunEnd != nil {
		t.Fatalf("notify = %+v", n)
	}
	if (*UserConfig)(nil).NotifyConfig() != nil {
		t.Fatal("a nil config has settings")
	}
	if _, probs := LoadUserConfig([]byte("schema = 1\n[notify]\nsound = true\n")); len(probs) == 0 {
		t.Fatal("an unknown notify key was accepted")
	}
}

func TestEffortIsClaudeOnlyAndInherited(t *testing.T) {
	stages := func(agent, effort string) []Stage {
		return []Stage{
			{Name: "implement", Role: "implementer", Agent: agent, Effort: effort},
			{Name: "verify", Role: "verifier"},
		}
	}
	got := stages("claude", "high")
	if probs := defaultBindings(got, nil); len(probs) != 0 || got[1].Effort != "high" || got[1].Source != FromInherited {
		t.Errorf("verifier = %+v, problems %v", got[1], probs)
	}
	got = stages("claude", "high")
	got[1].Agent, got[1].Effort = "claude", "low"
	if probs := defaultBindings(got, nil); len(probs) != 0 || got[1].Effort != "low" {
		t.Errorf("own effort lost: %+v %v", got[1], probs)
	}
	for _, agent := range []string{"codex", "fake"} {
		probs := defaultBindings(stages(agent, "high"), nil)
		if len(probs) == 0 || !strings.Contains(probs[0].Msg, "claude only") {
			t.Errorf("%s: effort accepted: %v", agent, probs)
		}
	}
}

func TestLoadUserConfigTheme(t *testing.T) {
	for _, name := range KnownThemes {
		c, probs := LoadUserConfig([]byte("schema = 1\ntheme = \"" + name + "\"\n"))
		if len(probs) > 0 || c.ThemeName() != name {
			t.Errorf("theme %q: %v, %q", name, probs, c.ThemeName())
		}
	}
	if c, probs := LoadUserConfig([]byte("schema = 1\n")); len(probs) > 0 || c.ThemeName() != "" {
		t.Errorf("no theme = %q, %v", c.ThemeName(), probs)
	}
	_, probs := LoadUserConfig([]byte("schema = 1\ntheme = \"neon\"\n"))
	if len(probs) != 1 || probs[0].Key != "theme" {
		t.Errorf("a bad theme gave %v", probs)
	}
	if _, probs := LoadUserConfig([]byte("schema = 1\ntheme = 3\n")); len(probs) == 0 {
		t.Error("a non-string theme was accepted")
	}
	if (*UserConfig)(nil).ThemeName() != "" {
		t.Error("a nil config has a theme")
	}
}

// [advisor] takes an agent, a model and an effort (D-A1), each optional.
func TestLoadUserConfigAdvisor(t *testing.T) {
	c, probs := LoadUserConfig([]byte("schema = 1\n[advisor]\nagent = \"claude\"\nmodel = \"opus\"\neffort = \"max\"\n"))
	if len(probs) > 0 || c.AdvisorConfig() != (AdvisorConfig{Agent: "claude", Model: "opus", Effort: "max"}) {
		t.Errorf("advisor %+v, %v", c.AdvisorConfig(), probs)
	}
	if c, probs := LoadUserConfig([]byte("schema = 1\n")); len(probs) > 0 || c.AdvisorConfig() != (AdvisorConfig{}) {
		t.Errorf("no [advisor] gave %+v, %v", c.AdvisorConfig(), probs)
	}
	if _, probs := LoadUserConfig([]byte("schema = 1\n[advisor]\neffort = \"huge\"\n")); len(probs) != 1 || probs[0].Key != "advisor.effort" {
		t.Errorf("a bad effort gave %v", probs)
	}
	if _, probs := LoadUserConfig([]byte("schema = 1\n[advisor]\nmodel = \"sk-" + "abc\"\n")); len(probs) != 1 || probs[0].Key != "advisor.model" {
		t.Errorf("a credential-like model gave %v", probs)
	}
	if (*UserConfig)(nil).AdvisorConfig() != (AdvisorConfig{}) {
		t.Error("a nil config has an advisor")
	}
}

func TestSaveUserDefaultsKeepsAndSetsTheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oge", "config.toml")
	root := t.TempDir()
	if err := SaveUserDefaults(path, root, RepoDefaults{Mode: "fast"}, "daltonized"); err != nil {
		t.Fatal(err)
	}
	if err := SaveRepoDefaults(path, root, RepoDefaults{Mode: "standard"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	c, probs := LoadUserConfig(data)
	if len(probs) > 0 || c.ThemeName() != "daltonized" || c.ForRepo(root).Mode != "standard" {
		t.Fatalf("saved %s: %v", data, probs)
	}
}

func TestVimEscapeKey(t *testing.T) {
	c, probs := LoadUserConfig([]byte("schema = 1\nvim_escape = \"jj\"\n"))
	if len(probs) > 0 || c.VimEscapeKeys() != "jj" {
		t.Fatalf("vim_escape = jj: %v, %q", probs, c.VimEscapeKeys())
	}
	for _, bad := range []string{"j", "jjj", "j ", "\\u0007j"} {
		if _, probs := LoadUserConfig([]byte("schema = 1\nvim_escape = \"" + bad + "\"\n")); len(probs) == 0 {
			t.Errorf("vim_escape %q should be refused", bad)
		}
	}
	var none *UserConfig
	if none.VimEscapeKeys() != "" {
		t.Error("a nil config has no escape sequence")
	}
}
