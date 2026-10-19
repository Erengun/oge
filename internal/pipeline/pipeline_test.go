package pipeline

import (
	"strings"
	"testing"
)

func TestCredentialValuesAreRejectedButNeverEchoed(t *testing.T) {
	secrets := []string{
		"sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA",
		"ghp_abcdefghijklmnopqrstuvwxyz0123",
		"AKIAABCDEFGHIJKLMNOP",
	}
	for _, s := range secrets {
		cfg := "schema = 1\n[setup]\nrun = \"curl -H 'x: " + s + "' example.com\"\n"
		_, probs := Load([]byte(cfg))
		if len(probs) == 0 {
			t.Errorf("%s…: no problem reported", s[:4])
		}
		for _, p := range probs {
			if strings.Contains(p.String(), s) || strings.Contains(p.String(), s[4:]) {
				t.Errorf("problem echoes the credential: %q", p)
			}
		}
	}
}

func TestPassEnvTakesNamesOnly(t *testing.T) {
	_, probs := Load([]byte("schema = 1\n[project]\npass_env = [\"GOFLAGS\", \"FOO=bar\"]\n"))
	if len(probs) != 1 || probs[0].Key != "project.pass_env[1]" {
		t.Fatalf("problems = %v", probs)
	}
	if strings.Contains(probs[0].String(), "bar") {
		t.Fatalf("problem echoes the value: %q", probs[0])
	}
}

func TestEveryMandatoryGateIsAHardError(t *testing.T) {
	for key := range mandatoryGates {
		_, probs := Load([]byte("schema = 1\n[pipelines.default.gates]\n" + key + " = true\n"))
		if len(probs) != 1 || !strings.Contains(probs[0].Msg, "mandatory") {
			t.Errorf("%s: problems = %v", key, probs)
		}
	}
}

func TestModesCompileToDistinctGraphs(t *testing.T) {
	seen := map[string]Mode{}
	for _, m := range []Mode{Fast, Standard, Blind} {
		for _, result := range []bool{false, true} {
			h := Compile(m, result).Hash(DefaultLimits)
			if prev, dup := seen[h]; dup {
				t.Fatalf("%s and %s compile to the same hash", prev, m)
			}
			seen[h] = m
			if h != Compile(m, result).Hash(DefaultLimits) {
				t.Fatalf("%s: hash is not deterministic", m)
			}
		}
	}
	l := DefaultLimits
	l.Retries++
	if Compile(Standard, false).Hash(l) == Compile(Standard, false).Hash(DefaultLimits) {
		t.Fatal("limits don't change the hash")
	}
}

func TestOracleGrowthGateOnlyWithAVerifier(t *testing.T) {
	has := func(g Graph) bool {
		for _, n := range g.Nodes {
			if n.ID == "gate.oracle_growth" {
				return true
			}
		}
		return false
	}
	if has(Compile(Fast, false)) || !has(Compile(Standard, false)) || !has(Compile(Blind, false)) {
		t.Fatal("Oracle-growth Gate must exist exactly when a verifier Stage does")
	}
}

func TestEveryEdgeJoinsExistingNodes(t *testing.T) {
	for _, m := range []Mode{Fast, Standard, Blind} {
		g := Compile(m, true)
		ids := map[string]bool{}
		for _, n := range g.Nodes {
			ids[n.ID] = true
		}
		for _, e := range g.Edges {
			if !ids[e.From] || !ids[e.To] {
				t.Errorf("%s: edge %v joins a missing node", m, e)
			}
		}
	}
}

func TestConfiguredVerifyModelWinsOverInheritance(t *testing.T) {
	cfg, probs := Load([]byte(`schema = 1
[project]
test_globs = ["**/*_test.go"]
[[check.commands]]
run    = "go test -json ./..."
report = "go-test-json"
[pipelines.default.stages.implement]
agent = "claude"
model = "impl-model"
[pipelines.default.stages.verify]
model = "verify-model"
`))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	f, probs := Resolve(cfg, Overrides{}, nil)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	for _, s := range f.Stages {
		if s.Name == "verify" && (s.Agent != "claude" || s.Model != "verify-model") {
			t.Fatalf("verify = %s:%s, want claude:verify-model", s.Agent, s.Model)
		}
	}
}

func TestBlankOracleValuesAreHardErrors(t *testing.T) {
	ok := Overrides{Checks: []string{"go test -json ./..."}, Tests: []string{"*_test.go"}}
	cli := []struct {
		name string
		o    Overrides
		key  string
	}{
		{"check", Overrides{Checks: []string{"  "}, Tests: ok.Tests}, "--check"},
		{"tests", Overrides{Checks: ok.Checks, Tests: []string{" \t"}}, "--tests"},
		{"output", Overrides{Checks: ok.Checks, Tests: ok.Tests, Outputs: []string{""}}, "--output"},
	}
	for _, c := range cli {
		_, probs := Resolve(nil, c.o, []string{"claude"})
		if !hasKey(probs, c.key) {
			t.Errorf("%s: problems = %v, want one for %s", c.name, probs, c.key)
		}
	}

	configs := []struct {
		name, toml, key string
	}{
		{"test_globs", `test_globs = ["  "]`, "project.test_globs[0]"},
		{"output_globs", `test_globs = ["x"]` + "\n" + `output_globs = [""]`, "project.output_globs[0]"},
	}
	for _, c := range configs {
		_, probs := Load([]byte("schema = 1\n[project]\n" + c.toml + "\n[[check.commands]]\nrun = \"go test -json ./...\"\nreport = \"go-test-json\"\n"))
		if !hasKey(probs, c.key) {
			t.Errorf("%s: problems = %v, want one for %s", c.name, probs, c.key)
		}
	}
	_, probs := Load([]byte("schema = 1\n[project]\ntest_globs = [\"x\"]\n[[check.commands]]\nrun = \" \\t \"\nreport = \"go-test-json\"\n"))
	if !hasKey(probs, "check.commands[0].run") {
		t.Errorf("blank run: problems = %v", probs)
	}
}

func TestCLIOracleValuesAreTrimmed(t *testing.T) {
	f, probs := Resolve(nil, Overrides{Checks: []string{"  go test -json ./...  "}, Tests: []string{" *_test.go "}, Outputs: []string{" out/** "}}, []string{"claude"})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	if f.Checks[0].Run != "go test -json ./..." || f.Project.TestGlobs[0] != "*_test.go" || f.Project.OutputGlobs[0] != "out/**" {
		t.Fatalf("not trimmed: %q %q %q", f.Checks[0].Run, f.Project.TestGlobs, f.Project.OutputGlobs)
	}
}

func hasKey(probs []Problem, key string) bool {
	for _, p := range probs {
		if p.Key == key {
			return true
		}
	}
	return false
}

func TestPassEnvCannotOverrideCheckPrivateVariables(t *testing.T) {
	for _, name := range []string{"HOME", "TMPDIR", "GOCACHE", "GOPATH", "GOMODCACHE", "GOTOOLCHAIN", "GOWORK", "XDG_CACHE_HOME"} {
		_, probs := Load([]byte("schema = 1\n[project]\npass_env = [\"GOFLAGS\", \"" + name + "\"]\n"))
		if len(probs) != 1 || probs[0].Key != "project.pass_env[1]" || !strings.Contains(probs[0].Msg, name) {
			t.Errorf("%s: problems = %v", name, probs)
		}
	}
}

// The normal repair loop never trips the growth limit on verifier
// Attempts: one follows every implementer Attempt the send-backs allow.
func TestGrowthAttemptsCoverTheSendBackBudget(t *testing.T) {
	seven, two := 7, 2
	for _, c := range []struct {
		cfg  LimitsConfig
		want int
	}{
		{LimitsConfig{}, DefaultLimits.SendBacks + 1},
		{LimitsConfig{SendBacks: &seven}, 8},
		{LimitsConfig{SendBacks: &seven, OracleGrowthAttempts: &two}, 2}, // configured wins
	} {
		if got := resolveLimits(c.cfg).OracleGrowthAttempts; got != c.want {
			t.Errorf("%+v: %d, want %d", c.cfg, got, c.want)
		}
	}
}

// A Problem names the flag, never the value typed with it, and the
// trust-weakening line never echoes the setup command: either may hold a
// token (#39).
func TestProblemsAndWeakeningsDontEchoValues(t *testing.T) {
	ok := Overrides{Checks: []string{"go test -json ./..."}, Tests: []string{"*_test.go"}}
	for _, o := range []Overrides{
		{Agents: []string{"implement=gemini:sk-secret"}},
		{Agents: []string{"nosuch=claude:sk-secret"}},
		{Implement: "gemini:sk-secret"},
	} {
		o.Checks, o.Tests = ok.Checks, ok.Tests
		_, probs := Resolve(nil, o, []string{"claude"})
		if len(probs) == 0 {
			t.Fatalf("%+v: no problem", o)
		}
		for _, p := range probs {
			if strings.Contains(p.Key, "sk-secret") || strings.Contains(p.Key, "=") {
				t.Errorf("%+v: key %q echoes the value", o, p.Key)
			}
		}
	}
	cfg, probs := Load([]byte("schema = 1\n[project]\ntest_globs = [\"*_test.go\"]\n[[check.commands]]\nrun = \"go test -json ./...\"\nreport = \"go-test-json\"\n[setup]\nrun = \"TOKEN=sk-secret make deps\"\nnetwork = \"on\"\n"))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	f, probs := Resolve(cfg, Overrides{}, []string{"claude"})
	if len(probs) > 0 || len(f.TrustWeakening) != 1 {
		t.Fatalf("%v %v", probs, f)
	}
	if w := f.TrustWeakening[0]; strings.Contains(w.Option+w.Detail, "sk-secret") {
		t.Errorf("the weakening echoes the setup command: %+v", w)
	}
}

func TestParseBinding(t *testing.T) {
	for in, want := range map[string][3]string{
		"claude":          {"claude", "", ""},
		"claude:sonnet":   {"claude", "", "sonnet"},
		"claude@z":        {"claude", "z", ""},
		"claude@z:sonnet": {"claude", "z", "sonnet"},
		"claude@work:a:b": {"claude", "work", "a:b"},
		"codex@x":         {"codex", "x", ""},
	} {
		a, p, m := ParseBinding(in)
		if got := [3]string{a, p, m}; got != want {
			t.Errorf("ParseBinding(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestClaudeProfileBindings(t *testing.T) {
	o := Overrides{
		Checks: []string{"go test -json ./..."}, Tests: []string{"*_test.go"},
		Agents: []string{"implement=claude@z:sonnet"}, Verify: "claude@work",
		ClaudeProfiles: []string{"work", "z"},
	}
	f, probs := Resolve(nil, o, []string{"claude"})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	for _, s := range f.Stages {
		switch s.Name {
		case "implement":
			if s.Agent != "claude" || s.Profile != "z" || s.Model != "sonnet" || s.Binding() != "claude@z" {
				t.Errorf("implement = %+v", s)
			}
		case "verify":
			if s.Agent != "claude" || s.Profile != "work" || s.Model != "" {
				t.Errorf("verify = %+v", s)
			}
		}
	}

	// An unbound verifier inherits the implementer's profile.
	o.Verify = ""
	f, probs = Resolve(nil, o, []string{"claude"})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	if v := f.Stages[1]; v.Profile != "z" {
		t.Errorf("inherited verify = %+v, want profile z", v)
	}
}

func TestUnknownClaudeProfileListsTheKnownOnes(t *testing.T) {
	o := Overrides{Checks: []string{"go test -json ./..."}, Tests: []string{"*_test.go"},
		Implement: "claude@typo", ClaudeProfiles: []string{"work", "z"}}
	_, probs := Resolve(nil, o, []string{"claude"})
	if len(probs) != 1 || probs[0].Key != "--implement" ||
		!strings.Contains(probs[0].Msg, "known: work, z") || strings.Contains(probs[0].Msg, "typo") {
		t.Errorf("problems = %+v, want one listing work and z without echoing the typed name", probs)
	}
	o.ClaudeProfiles = nil
	if _, probs = Resolve(nil, o, []string{"claude"}); len(probs) != 1 || !strings.Contains(probs[0].Msg, "none found") {
		t.Errorf("problems = %+v, want none found", probs)
	}
}

func TestProfilesAreForClaudeOnly(t *testing.T) {
	o := Overrides{Checks: []string{"go test -json ./..."}, Tests: []string{"*_test.go"},
		Implement: "codex@work", ClaudeProfiles: []string{"work"}}
	_, probs := Resolve(nil, o, []string{"claude"})
	if len(probs) != 1 || probs[0].Msg != "profiles are supported for claude only" {
		t.Errorf("problems = %+v", probs)
	}
}

func TestConfigStageBindingWithAProfileIsAProblem(t *testing.T) {
	cfg, probs := Load([]byte(`schema = 1
[project]
test_globs = ["**/*_test.go"]
[[check.commands]]
run    = "go test -json ./..."
report = "go-test-json"
[pipelines.default.stages.implement]
agent = "claude@work"
`))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	_, probs = Resolve(cfg, Overrides{ClaudeProfiles: []string{"work"}}, nil)
	if len(probs) != 1 || probs[0].Key != "pipelines.default.stages.implement.agent" || !strings.Contains(probs[0].Msg, "personal") {
		t.Errorf("problems = %+v, want the profiles-are-personal Problem", probs)
	}
}

func TestEmptyProfileIsAProblem(t *testing.T) {
	for _, spec := range []string{"claude@", "claude@:sonnet"} {
		o := Overrides{Checks: []string{"go test -json ./..."}, Tests: []string{"*_test.go"}, Implement: spec}
		if _, probs := Resolve(nil, o, []string{"claude"}); len(probs) != 1 || probs[0].Msg != emptyProfile {
			t.Errorf("%q: problems = %+v", spec, probs)
		}
	}
}

func TestAgentImplementerLimitations(t *testing.T) {
	cfg, probs := Load([]byte("schema = 1\n[project]\ntest_globs = [\"*_test.go\"]\n[[check.commands]]\nrun = \"go test -json ./...\"\nreport = \"go-test-json\"\n"))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	for agent, want := range map[string]int{"codex": 1, "agy": 1, "claude": 0} {
		f, probs := Resolve(cfg, Overrides{Agents: []string{agent}}, []string{"claude"})
		if len(probs) > 0 {
			t.Fatal(probs)
		}
		if len(f.Limitations) != want {
			t.Errorf("%s: limitations %q, want %d", agent, f.Limitations, want)
		}
		if want > 0 && agent == "codex" && !strings.Contains(f.Limitations[0], "credentials") {
			t.Errorf("%s: %q", agent, f.Limitations[0])
		}
		if want > 0 && agent == "agy" && (!strings.Contains(f.Limitations[0], "dangerously-skip-permissions") || !strings.Contains(f.Limitations[0], "held-out tests")) {
			t.Errorf("%s: %q", agent, f.Limitations[0])
		}
	}
}

// An agent that can't verify yet implements only: --agent binds it to
// the implementer and claude verifies; binding its verifier is refused.
func TestImplementOnlyAgentsLeaveTheVerifierToClaude(t *testing.T) {
	cfg, probs := Load([]byte("schema = 1\n[project]\ntest_globs = [\"*_test.go\"]\n[[check.commands]]\nrun = \"go test -json ./...\"\nreport = \"go-test-json\"\n"))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	for _, agent := range []string{"agy", "codex"} {
		f, probs := Resolve(cfg, Overrides{Standard: true, Agents: []string{agent}}, []string{"claude", agent})
		if len(probs) > 0 {
			t.Fatalf("%s: %v", agent, probs)
		}
		if f.Stages[0].Agent != agent || f.Stages[1].Agent != "claude" {
			t.Errorf("%s: stages %+v", agent, f.Stages)
		}
		for _, o := range []Overrides{{Standard: true, Verify: agent}, {Standard: true, Agents: []string{"verify=" + agent}}} {
			_, probs := Resolve(cfg, o, []string{"claude"})
			if len(probs) == 0 || !strings.Contains(probs[0].Msg, agent+" can't verify yet") {
				t.Errorf("%s: %+v: %v", agent, o, probs)
			}
		}
	}
}

// A project config can't bind a verifier to an agent that can't verify.
func TestProjectVerifierMustVerify(t *testing.T) {
	cfg, probs := Load([]byte("schema = 1\n[project]\ntest_globs = [\"*_test.go\"]\n[[check.commands]]\nrun = \"go test -json ./...\"\nreport = \"go-test-json\"\n[pipelines.default.stages.verify]\nagent = \"agy\"\n"))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	_, probs = Resolve(cfg, Overrides{Standard: true, Agents: []string{"implement=claude"}}, []string{"claude"})
	if len(probs) == 0 || !strings.Contains(probs[0].Msg, "agy can't verify yet") {
		t.Errorf("problems %v", probs)
	}
}
