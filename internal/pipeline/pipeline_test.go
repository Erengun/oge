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
