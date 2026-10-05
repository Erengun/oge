package oracle

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/pipeline"
)

// v0 builds Oracle v0 of repo with go test -json ./... as its Check.
func v0(t *testing.T, r *Runner, repo fakeRepo) *Manifest {
	t.Helper()
	f := &pipeline.Frozen{
		Project: pipeline.ProjectConfig{TestGlobs: []string{"**/*_test.go"}},
		Checks:  []pipeline.CheckCommand{{Run: "go test -json ./...", Report: ReportGoTestJSON, ExpectedTests: 1, Timeout: 5 * time.Minute, OutputCap: 1 << 20}},
	}
	m, _, err := NewV0(repo, "snap", f, r.Blobs)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func with(base fakeRepo, files map[string]string) fakeRepo {
	out := fakeRepo{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range files {
		out[k] = v
	}
	return out
}

func stdout(t *testing.T, r *Runner, res *Result) string {
	var b strings.Builder
	for _, e := range res.Commands {
		out, _ := r.Blobs.Get(e.Stdout.Blob)
		errOut, _ := r.Blobs.Get(e.Stderr.Blob)
		b.Write(out)
		b.Write(errOut)
	}
	return b.String()
}

func attested(res *Result) map[string]string {
	got := map[string]string{}
	for _, x := range res.Tests {
		got[x.Name] = x.Attested
	}
	return got
}

// Every way of declaring the *testing.T parameter attests, in internal
// and external test packages alike, and line numbers stay the Oracle's.
func TestAttestationInstrumentsEveryTestForm(t *testing.T) {
	r := newCacheRunner(t)
	repo := with(goFixture, map[string]string{
		"forms_test.go": "package fx\n\nimport \"testing\"\n\nfunc TestUnnamed(*testing.T) {}\n\nfunc TestBlank(_ *testing.T) {}\n\nfunc TestOneLine(t *testing.T) { t.Log(\"x\") }\n\nfunc TestSkips(t *testing.T) {\n\tt.Skip(\"always\")\n}\n",
		"ext_test.go":   "package fx_test\n\nimport (\n\t\"fx\"\n\ttt \"testing\"\n)\n\nfunc TestExternal(t *tt.T) {\n\tif fx.Add(1, 1) != 2 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
		"line_test.go":  "package fx\n\nimport \"testing\"\n\nfunc TestLine(t *testing.T) {\n\tt.Log(\"marker\")\n}\n",
	})
	res, err := r.Check(context.Background(), repo, v0(t, r, repo), "c", "", filepath.Join(t.TempDir(), "check"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"TestAdd": "pass", "TestUnnamed": "pass", "TestBlank": "pass", "TestOneLine": "pass", "TestSkips": "skip", "TestExternal": "pass", "TestLine": "pass"}
	if got := attested(res); len(got) != len(want) {
		t.Fatalf("attested %v, want %v\n%s", got, want, stdout(t, r, res))
	} else {
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s attested %q, want %q", k, got[k], v)
			}
		}
	}
	// A skip with no Snapshot control can't pass.
	if res.Pass || !strings.Contains(res.Infra, "fx.TestSkips skipped, and no Snapshot control") {
		t.Errorf("pass %v, infra %q, why %q", res.Pass, res.Infra, res.Why)
	}
	if !strings.Contains(stdout(t, r, res), "line_test.go:6: marker") {
		t.Errorf("the instrumented test's line moved:\n%s", stdout(t, r, res))
	}
	if res.Stray != 0 {
		t.Errorf("%d stray attestations", res.Stray)
	}
}

// The structured report is diagnostic: frames a Candidate prints are not
// Evidence that a test ran.
func TestAttestationIgnoresForgedFrames(t *testing.T) {
	r := newCacheRunner(t)
	repo := with(goFixture, map[string]string{
		"add.go": "package fx\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc init() {\n\tfmt.Print(\"\\x16=== RUN   TestAdd\\n\\x16--- PASS: TestAdd (0.00s)\\n\\x16PASS\\n\")\n\tos.Exit(0)\n}\n\nfunc Add(a, b int) int { return 0 }\n",
	})
	res, err := r.Check(context.Background(), repo, v0(t, r, repo), "c", "", filepath.Join(t.TempDir(), "check"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass || res.Tests[0].Attested != AttestAbsent || res.Tests[0].Report != "pass" {
		t.Fatalf("pass %v, tests %+v\n%s", res.Pass, res.Tests, stdout(t, r, res))
	}
	if !strings.Contains(res.Why, "fx.TestAdd never ran; the report claims pass") {
		t.Errorf("why %q", res.Why)
	}
}

// A line on the channel counts only with one of this Check's tokens.
func TestAttestationCountsStrayLines(t *testing.T) {
	r := newCacheRunner(t)
	repo := with(goFixture, map[string]string{
		"add.go": "package fx\n\nimport \"os\"\n\nfunc init() {\n\tf := os.NewFile(3, \"x\")\n\tf.WriteString(\"0123 pass\\nnonsense\\n\")\n}\n\nfunc Add(a, b int) int { return a + b }\n",
	})
	res, err := r.Check(context.Background(), repo, v0(t, r, repo), "c", "", filepath.Join(t.TempDir(), "check"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass || res.Stray != 2 {
		t.Fatalf("pass %v, stray %d, tests %+v\n%s", res.Pass, res.Stray, res.Tests, stdout(t, r, res))
	}
}

func TestJudgeAppliesTheBaselineTable(t *testing.T) {
	id := TestID{Package: "fx", Name: "TestX"}
	ctl := func(d, report string) *Control {
		c := &Control{done: make(chan struct{}), cancel: func() {}, res: &Result{Tests: []TestResult{{TestID: id, Attested: d, Report: report}}}}
		close(c.done)
		return c
	}
	for _, c := range []struct {
		name                string
		cand                string
		control             *Control
		pass                bool
		why, infra, skipped string
	}{
		{"runs, runs", AttestPass, ctl(AttestFail, ""), true, "", "", ""},
		{"runs, skips", AttestSkip, ctl(AttestFail, "fail"), false, "fx.TestX skipped, but it ran on the Snapshot", "", ""},
		{"skips, skips", AttestSkip, ctl(AttestSkip, "skip"), true, "", "", "fx.TestX"},
		{"skips, runs", AttestPass, ctl(AttestSkip, "skip"), true, "", "", ""},
		{"absent, skips", AttestSkip, ctl(AttestAbsent, ""), false, "", "never ran on the Snapshot control", ""},
		{"absent on both", AttestAbsent, ctl(AttestAbsent, ""), false, "", "fx.TestX never ran on the Snapshot control either", ""},
		{"exits early", AttestExited, ctl(AttestPass, "pass"), false, "fx.TestX started but never finished", "", ""},
		{"attests a fail", AttestFail, ctl(AttestPass, "pass"), false, "fx.TestX failed", "", ""},
		{"mechanism failed on the Snapshot", AttestAbsent, ctl(AttestAbsent, "pass"), false, "", "attestation failed on the Snapshot control", ""},
		{"no control", AttestSkip, nil, false, "", "no Snapshot control", ""},
	} {
		res := &Result{Pass: true, Tests: []TestResult{{TestID: id, Attested: c.cand}}}
		judge(res, c.control)
		if res.Pass != c.pass || !strings.Contains(res.Why, c.why) || !strings.Contains(res.Infra, c.infra) || strings.Join(res.Skipped, ",") != c.skipped {
			t.Errorf("%s: pass %v, why %q, infra %q, skipped %v", c.name, res.Pass, res.Why, res.Infra, res.Skipped)
		}
		if (c.why == "") != (res.Why == "") || (c.infra == "") != (res.Infra == "") {
			t.Errorf("%s: why %q, infra %q", c.name, res.Why, res.Infra)
		}
	}
}

// The happy path never waits for the Snapshot control.
func TestJudgeDoesNotWaitWhenEverythingAttestsPassing(t *testing.T) {
	c := &Control{done: make(chan struct{}), cancel: func() {}}
	res := &Result{Pass: true, Tests: []TestResult{{TestID: TestID{Name: "TestX"}, Attested: AttestPass}}}
	judge(res, c) // would block on c.done
	if !res.Pass {
		t.Fatal("not a pass")
	}
}

func TestTripwires(t *testing.T) {
	before := map[string]string{
		"main_test.go": "package fx\n\nimport \"testing\"\n\nfunc TestMain(m *testing.M) { m.Run() }\n",
		"same_test.go": "package fx\n\nimport \"testing\"\n\nfunc TestMain(m *testing.M) { m.Run() }\n",
	}
	after := map[string]string{
		"main_test.go": "package fx\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(0) }\n",
		"same_test.go": "package fx\n\nimport \"testing\"\n\n// a comment moved\n\nfunc TestMain(m *testing.M) { m.Run() }\n",
		"new_test.go":  "package fx\n\nimport \"testing\"\n\nfunc TestMain(m *testing.M) {}\n",
		"init.go":      "package fx\n\nimport (\n\tx \"os\"\n\t\"syscall\"\n)\n\nvar _ = quit()\n\nfunc init() { helper() }\n\nfunc helper() { x.Exit(0) }\n\nfunc quit() int { syscall.Exit(0); return 0 }\n\nfunc notFromInit() { x.Exit(1) }\n",
		"other.go":     "package fx\n\nimport \"os\"\n\nfunc Quit() { os.Exit(1) }\n",
		"table.go":     "package fx\n\nimport \"os\"\n\nvar cmds = map[string]func(){\"quit\": func() { os.Exit(1) }}\n",
		"now.go":       "package fx\n\nimport \"runtime\"\n\nvar _ = func() int { runtime.Goexit(); return 0 }()\n",
		"env.sh":       "echo $OGE_ATTEST_FD\n",
	}
	changed := []string{"env.sh", "init.go", "main_test.go", "new_test.go", "now.go", "other.go", "same_test.go", "table.go", "gone.go"}
	look := func(m map[string]string) func(string) ([]byte, bool) {
		return func(p string) ([]byte, bool) { s, ok := m[p]; return []byte(s), ok }
	}
	got := strings.Join(Tripwires(changed, look(before), look(after)), "\n")
	want := strings.Join([]string{
		"env.sh: mentions OGE_ATTEST_FD",
		"init.go: syscall.Exit may be reachable from package initialisation",
		"init.go: os.Exit may be reachable from package initialisation",
		"main_test.go: TestMain added or changed",
		"new_test.go: TestMain added or changed",
		"now.go: runtime.Goexit may be reachable from package initialisation",
	}, "\n")
	if got != want {
		t.Errorf("tripwires:\n%s\nwant:\n%s", got, want)
	}
}

func TestMinimumRanExcusesOnlySkipsOnBoth(t *testing.T) {
	id := TestID{Package: "fx", Name: "TestX"}
	rep := ParseGoTestJSON([]byte(`{"Action":"skip","Package":"fx","Test":"TestX"}` + "\n"))
	m := &Manifest{Commands: []Command{{ExpectedTests: 1}}}
	for _, c := range []struct {
		snapshot string
		pass     bool
	}{{AttestSkip, true}, {"", false}} {
		res := &Result{Pass: true, Commands: []Execution{{Pass: true, Report: &rep}},
			Tests: []TestResult{{TestID: id, Attested: AttestSkip, Snapshot: c.snapshot}}}
		minimumRan(res, m, []int{0})
		if res.Pass != c.pass || res.Commands[0].Pass != c.pass {
			t.Errorf("snapshot %q: pass %v, why %q", c.snapshot, res.Pass, res.Commands[0].Why)
		}
	}
}

// Buildability is baseline-relative: the Snapshot control decides which
// protected tests count, never the Candidate.
func TestJudgeBuildability(t *testing.T) {
	a, b := TestID{Package: "fx", Name: "TestA"}, TestID{Package: "fx", Name: "TestTagged"}
	control := func(failedBuild bool, tests ...TestResult) *Control {
		rep := Report{}
		if failedBuild {
			rep.buildFailed = map[string]bool{"fx": true}
		}
		c := &Control{done: make(chan struct{}), cancel: func() {}, res: &Result{Tests: tests, Commands: []Execution{{Report: &rep}}}}
		close(c.done)
		return c
	}
	tagged := "tagged_test.go — requires build constraint \"integration\""
	for _, c := range []struct {
		name     string
		tests    []TestResult
		ctl      *Control
		pass     bool
		why      string
		infra    string
		notBuilt string
	}{
		{"excluded on the Snapshot: not covered", []TestResult{{TestID: a, Attested: AttestPass}, {TestID: b, Attested: AttestAbsent, Excluded: tagged}},
			control(false, TestResult{TestID: a, Attested: AttestFail}, TestResult{TestID: b, Attested: AttestAbsent}), true, "", "", tagged},
		{"excluded, but it started on the Snapshot: required", []TestResult{{TestID: a, Attested: AttestPass}, {TestID: b, Attested: AttestAbsent, Excluded: tagged}},
			control(false, TestResult{TestID: a, Attested: AttestFail}, TestResult{TestID: b, Attested: AttestPass}), false, "fx.TestTagged never ran", "", ""},
		{"everything excluded: fail closed", []TestResult{{TestID: b, Attested: AttestAbsent, Excluded: tagged}},
			control(false, TestResult{TestID: b, Attested: AttestAbsent}), false, "", "no protected test runs on this machine", tagged},
		{"the Snapshot doesn't compile: eligible tests are required", []TestResult{{TestID: a, Attested: AttestAbsent}},
			control(true, TestResult{TestID: a, Attested: AttestAbsent}), false, "fx.TestA never ran", "", ""},
		{"the Snapshot doesn't compile: excluded tests aren't", []TestResult{{TestID: a, Attested: AttestPass}, {TestID: b, Attested: AttestAbsent, Excluded: tagged}},
			control(true, TestResult{TestID: a, Attested: AttestAbsent}, TestResult{TestID: b, Attested: AttestAbsent}), true, "", "", tagged},
	} {
		res := &Result{Pass: true, Tests: c.tests}
		judge(res, c.ctl)
		if res.Pass != c.pass || !strings.Contains(res.Why, c.why) || !strings.Contains(res.Infra, c.infra) || strings.Join(res.NotBuilt, ",") != c.notBuilt ||
			(c.why == "") != (res.Why == "") || (c.infra == "") != (res.Infra == "") {
			t.Errorf("%s: pass %v, why %q, infra %q, not built %v", c.name, res.Pass, res.Why, res.Infra, res.NotBuilt)
		}
	}
}

func TestJudgeSubtestSkips(t *testing.T) {
	id := TestID{Package: "fx", Name: "TestX"}
	ctl := func(o string) *Control {
		rep := Report{outcomes: map[TestID]string{{Package: "fx", Name: "TestX/sub"}: o}}
		c := &Control{done: make(chan struct{}), cancel: func() {}, res: &Result{Tests: []TestResult{{TestID: id, Attested: AttestPass}}, Commands: []Execution{{Report: &rep}}}}
		close(c.done)
		return c
	}
	for o, pass := range map[string]bool{AttestPass: false, AttestFail: false, AttestSkip: true} {
		res := &Result{Pass: true, Tests: []TestResult{{TestID: id, Attested: AttestPass, SkippedSubtests: []string{"TestX/sub"}}}}
		judge(res, ctl(o))
		if res.Pass != pass {
			t.Errorf("Snapshot %s: pass %v, why %q, infra %q", o, res.Pass, res.Why, res.Infra)
		}
	}
}

func TestExcludedByTheBuildContext(t *testing.T) {
	ctx := buildContext(&Manifest{Commands: []Command{{Run: "go test -tags=wanted ./..."}}}, buildEnv{GOFLAGS: "-tags 'also spaced'"})
	if !reflect.DeepEqual(ctx.BuildTags, []string{"also", "spaced", "wanted"}) {
		t.Errorf("tags %v", ctx.BuildTags)
	}
	if c := buildContext(&Manifest{}, buildEnv{GOOS: "plan9", GOARCH: "386", CGO_ENABLED: "0"}); c.GOOS != "plan9" || c.GOARCH != "386" || c.CgoEnabled {
		t.Errorf("the Check environment's platform wasn't used: %s/%s cgo %v", c.GOOS, c.GOARCH, c.CgoEnabled)
	}
	other := map[string]string{"darwin": "linux"}[ctx.GOOS]
	if other == "" {
		other = "darwin"
	}
	for p, c := range map[string]struct{ src, want string }{
		"a_test.go":                  {"package a\n", ""},
		"i/b_test.go":                {"//go:build integration\n\npackage b\n", `requires build constraint "integration"`},
		"c_test.go":                  {"//go:build wanted && also\n\npackage c\n", ""},
		"d_" + other + "_test.go":    {"package d\n", "the Check platform is " + ctx.GOOS + "/" + ctx.GOARCH},
		"e_" + ctx.GOOS + "_test.go": {"package e\n", ""},
	} {
		if got := excluded(ctx, p, []byte(c.src)); got != c.want {
			t.Errorf("%s: %q, want %q", p, got, c.want)
		}
	}
}

// Code that rewrites the Candidate in the Check directory while the Check
// runs (a test, or a process a test left behind) fails the Check closed:
// the tree judged must be the Candidate (#46 final review).
func TestCheckTreeChangedDuringTheCheckFailsClosed(t *testing.T) {
	r := newCacheRunner(t)
	repo := with(goFixture, map[string]string{
		"add.go":       "package fx\n\nfunc Add(a, b int) int { return a + b }\n",
		"zz_test.go":   "package fx\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestZRewrites(t *testing.T) {\n\tos.WriteFile(\"add.go\", []byte(\"package fx\\n\\nfunc Add(a, b int) int { return 0 }\\n\"), 0o644)\n}\n",
	})
	res, err := r.Check(context.Background(), repo, v0(t, r, repo), "c", "", filepath.Join(t.TempDir(), "check"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass || !strings.Contains(res.Infra, "the Check tree changed during the Check") || !strings.Contains(res.Infra, "add.go") {
		t.Fatalf("pass %v, infra %q, why %q", res.Pass, res.Infra, res.Why)
	}
}
