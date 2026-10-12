package pipeline

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderOracleChangesOnlyChecksAndGlobs(t *testing.T) {
	t.Parallel()
	const in = `# my config
schema = 1

[project]
test_globs = [
  "**/*_test.go",   # all tests
  "testdata/**",
]
output_globs = ["*.go"]

[[check.commands]]
run = "go test -json ./..."
report = "go-test-json"

[setup]
run = "make deps"
`
	n := 3
	checks := []CheckCommandConfig{
		{Run: "go test -json ./pkg/...", Report: "go-test-json", ExpectedTests: &n},
		{Run: `echo "a\b"`, Report: "junit"},
	}
	out, err := RenderOracle(in, checks, []string{"pkg/**/*_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, probs := Load([]byte(out))
	if len(probs) > 0 {
		t.Fatalf("not valid: %v\n%s", probs, out)
	}
	if !reflect.DeepEqual(cfg.Check.Commands, checks) || !reflect.DeepEqual(cfg.Project.TestGlobs, []string{"pkg/**/*_test.go"}) {
		t.Errorf("round trip: %+v %v\n%s", cfg.Check.Commands, cfg.Project.TestGlobs, out)
	}
	for _, keep := range []string{"# my config", `output_globs = ["*.go"]`, "[setup]", `run = "make deps"`} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	if strings.Contains(out, "all tests") {
		t.Errorf("old glob lines kept:\n%s", out)
	}
	// Nothing changed: the text is as it was.
	same, _ := RenderOracle(in, mustChecks(t, in), []string{"**/*_test.go", "testdata/**"})
	if same != in {
		t.Errorf("unchanged values rewrote the file:\n%s", same)
	}
}

func mustChecks(t *testing.T, in string) []CheckCommandConfig {
	cfg, _ := Load([]byte(in))
	return cfg.Check.Commands
}

func TestRenderOracleFromNothing(t *testing.T) {
	t.Parallel()
	out, err := RenderOracle("schema = 1\n", []CheckCommandConfig{{Run: "go test -json ./...", Report: "go-test-json"}}, []string{"**/*_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, probs := Load([]byte(out))
	if len(probs) > 0 || len(cfg.Check.Commands) != 1 || len(cfg.Project.TestGlobs) != 1 {
		t.Fatalf("%v %+v\n%s", probs, cfg, out)
	}
}

func TestResolveListsARemovedCheckAsTrustWeakening(t *testing.T) {
	t.Parallel()
	cfg := &Config{Schema: 1}
	cfg.Project.TestGlobs = []string{"**/*_test.go"}
	cfg.Check.Commands = []CheckCommandConfig{
		{Run: "go test -json ./...", Report: "go-test-json"},
		{Run: "go vet ./...", Report: "go-test-json"},
	}
	o := Overrides{Fast: true, Agents: []string{"fake"}, TUIOracle: true, TUITests: cfg.Project.TestGlobs,
		TUIChecks: []CheckCommandConfig{{Run: "go test -json ./...", Report: "go-test-json"}, {Run: "go test -json ./x/..."}}}
	f, probs := Resolve(cfg, o, nil, "fake")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	var got []string
	for _, w := range f.TrustWeakening {
		got = append(got, w.Option+" from "+string(w.Source))
	}
	if !reflect.DeepEqual(got, []string{"Check removed in setup: go vet ./... from tui"}) {
		t.Errorf("trust-weakening: %q", got)
	}
	if len(f.Checks) != 2 || f.Checks[0].Source != FromProject || f.Checks[1].Source != FromTUI || f.Checks[1].Report != "go-test-json" {
		t.Errorf("checks: %+v", f.Checks)
	}
	// Adding a Check is not a weakening.
	o.TUIChecks = append(cfg.Check.Commands, CheckCommandConfig{Run: "go test ./y"})
	if f, _ = Resolve(cfg, o, nil, "fake"); len(f.TrustWeakening) != 0 {
		t.Errorf("an added Check weakened: %+v", f.TrustWeakening)
	}
}

func TestRenderOracleHandlesHeaderSpellingsAndRefusesWhatItCannot(t *testing.T) {
	t.Parallel()
	chk := []CheckCommandConfig{{Run: "go test -json ./...", Report: "go-test-json"}}
	for _, in := range []string{
		"schema = 1\n[project] # main\ntest_globs = [\"a\"]\n",
		"schema = 1\n[ project ]\ntest_globs = [\"a\"]\n",
		"schema = 1\n\n[project]\ntest_globs = [\"a\"]\n\n[[check.commands]]\nrun = \"x\"\nreport = \"junit\"\n# about setup\n[setup]\nrun = \"y\"\n",
	} {
		out, err := RenderOracle(in, chk, []string{"b/**"})
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if strings.Contains(in, "about setup") && !strings.Contains(out, "# about setup") {
			t.Errorf("comment lost:\n%s", out)
		}
		if strings.Contains(out, "\n\n\n") {
			t.Errorf("double blank lines:\n%s", out)
		}
	}
	for _, in := range []string{
		"schema = 1\nproject.test_globs = [\"a\"]\n",
		"schema = 1\nproject = { test_globs = [\"a\"] }\n",
		"schema = 1\n[project]\ntest_globs = [\"a\"]\n[check]\ncommands = [{ run = \"x\", report = \"junit\" }]\n",
	} {
		if out, err := RenderOracle(in, chk, []string{"b/**"}); err == nil {
			t.Errorf("%q: wrote\n%s", in, out)
		}
	}
	// CRLF files stay CRLF.
	out, err := RenderOracle("schema = 1\r\n[project]\r\ntest_globs = [\"a\"]\r\n", chk, []string{"b"})
	if err != nil || strings.Count(out, "\r\n") != strings.Count(out, "\n") {
		t.Errorf("crlf: %v %q", err, out)
	}
}

func TestResolveListsARemovedOrNarrowedGlobAsTrustWeakening(t *testing.T) {
	t.Parallel()
	cfg := &Config{Schema: 1}
	cfg.Project.TestGlobs = []string{"**/*_test.go", "testdata/**"}
	cfg.Check.Commands = []CheckCommandConfig{{Run: "go test -json ./...", Report: "go-test-json"}}
	o := Overrides{Fast: true, Agents: []string{"fake"}, TUIOracle: true, TUIChecks: cfg.Check.Commands,
		TUITests: []string{"*_test.go", "testdata/**"}}
	f, probs := Resolve(cfg, o, nil, "fake")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	if len(f.TrustWeakening) != 1 || f.TrustWeakening[0].Option != "test glob removed in setup: **/*_test.go" || f.TestGlobsSource != FromTUI {
		t.Errorf("%+v %v", f.TrustWeakening, f.TestGlobsSource)
	}
	// Adding a glob only is neither.
	o.TUITests = []string{"**/*_test.go", "testdata/**", "more/**"}
	if f, _ = Resolve(cfg, o, nil, "fake"); len(f.TrustWeakening) != 0 || f.TestGlobsSource != FromTUI {
		t.Errorf("%+v", f.TrustWeakening)
	}
	// The proposal's test_config applies where the file has none.
	o.TUITestConfig = []string{"go.mod"}
	if f, _ = Resolve(cfg, o, nil, "fake"); len(f.Project.TestConfig) != 1 {
		t.Errorf("test_config %v", f.Project.TestConfig)
	}
}
