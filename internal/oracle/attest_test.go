package oracle

import (
	"context"
	"path/filepath"
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
		{"absent, skips", AttestSkip, ctl(AttestAbsent, ""), false, "never ran on the Snapshot", "", ""},
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
		"env.sh":       "echo $OGE_ATTEST_FD\n",
	}
	changed := []string{"env.sh", "init.go", "main_test.go", "new_test.go", "other.go", "same_test.go", "gone.go"}
	look := func(m map[string]string) func(string) ([]byte, bool) {
		return func(p string) ([]byte, bool) { s, ok := m[p]; return []byte(s), ok }
	}
	got := strings.Join(Tripwires(changed, look(before), look(after)), "\n")
	want := strings.Join([]string{
		"env.sh: mentions OGE_ATTEST_FD",
		"init.go: syscall.Exit called from init",
		"init.go: os.Exit called from init",
		"main_test.go: TestMain added or changed",
		"new_test.go: TestMain added or changed",
	}, "\n")
	if got != want {
		t.Errorf("tripwires:\n%s\nwant:\n%s", got, want)
	}
}
