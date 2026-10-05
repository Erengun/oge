package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// The fixture is a Go package whose test fails until Add is fixed. The
// test file is untracked, so reaching it in the Check proves the Snapshot
// includes untracked work.
const (
	fxConfig = `schema = 1
[project]
test_globs = ["**/*_test.go"]
[[check.commands]]
run    = "go test -json ./..."
report = "go-test-json"
`
	fxBroken = "package fx\n\nfunc Add(a, b int) int { return 0 }\n"
	fxTest   = "package fx\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2, 3) != 5\")\n\t}\n}\n"

	// fixScript fixes Add.
	fixScript = `echo "fixing Add in add.go"
printf 'package fx\n\nfunc Add(a, b int) int { return a + b }\n' > add.go
echo "exit: done"
`
	// cheatScript leaves Add broken and rewrites the test to pass; the
	// Oracle overlay must put the real test back.
	cheatScript = `echo "making the test pass"
printf 'package fx\n\nimport "testing"\n\nfunc TestAdd(t *testing.T) {}\n' > add_test.go
`
)

type runFixture struct {
	repo, state, hookMarker string
	statusBefore            string
	// interactive mocks a terminal at stdin and stdout.
	interactive bool
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	base := t.TempDir()
	// A private temp dir keeps the state root (under HOME) outside the
	// system temp directory without disabling that refusal.
	for _, d := range []string{"home", "tmp", "repo"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv(ledger.StateDirEnv, "")
	t.Setenv("TMPDIR", filepath.Join(base, "tmp"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	f := &runFixture{repo: filepath.Join(base, "repo"), state: filepath.Join(home, ".local", "state", "oge")}
	initRepo(t, f.repo)
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(fxConfig))
	writeFile(t, filepath.Join(f.repo, "go.mod"), []byte("module fx\n\ngo 1.22\n"))
	writeFile(t, filepath.Join(f.repo, "add.go"), []byte(fxBroken))
	gitIn(t, f.repo, "add", "-A")
	gitIn(t, f.repo, "commit", "-q", "-m", "fixture")
	writeFile(t, filepath.Join(f.repo, "add_test.go"), []byte(fxTest)) // untracked

	f.hookMarker = filepath.Join(base, "hook-ran")
	hook := "#!/bin/sh\ntouch '" + f.hookMarker + "'\n"
	for _, h := range []string{"pre-commit", "post-commit", "post-checkout", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(f.repo, ".git", "hooks", h), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")
	return f
}

func (f *runFixture) run(t *testing.T, script string, args ...string) (int, string, string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(f.repo), "agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	env := Env{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Dir:         f.repo,
		Interactive: func() bool { return f.interactive },
		LookPath:    exec.LookPath,
		Edit:        func(string) error { t.Fatal("editor opened"); return nil },
		GOOS:        runtime.GOOS,
		Version:     "test",
		Getenv:      os.Getenv,
		Agents:      map[string]agent.Adapter{fake.Name: &fake.Adapter{Script: path, Env: []string{"OGE_TEST_STATE=" + f.state}}},
		// A shared GOCACHE keeps these tests fast; real Runs never share.
		CheckGoCache: hostGoCache,
	}
	code := Main(env, args)
	return code, stdout.String(), stderr.String()
}

// hostGoCache is the developer's GOCACHE, read before any test swaps HOME.
var hostGoCache = func() string {
	out, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}()

// assertUntouched checks the user's checkout was never written.
func (f *runFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if got := gitOut(t, f.repo, "status", "--porcelain"); got != f.statusBefore {
		t.Errorf("the user's checkout changed:\n%s\nwas:\n%s", got, f.statusBefore)
	}
	for path, want := range map[string]string{"add.go": fxBroken, "add_test.go": fxTest} {
		if b, _ := os.ReadFile(filepath.Join(f.repo, path)); string(b) != want {
			t.Errorf("%s in the user's checkout changed: %q", path, b)
		}
	}
	if _, err := os.Stat(f.hookMarker); err == nil {
		t.Error("a hook in the user's repository ran")
	}
}

// onlyRun returns the one Run's private directory.
func (f *runFixture) onlyRun(t *testing.T) string {
	t.Helper()
	runs, _ := filepath.Glob(filepath.Join(f.state, "private", "runs", "*"))
	if len(runs) != 1 {
		t.Fatalf("want one Run, got %v", runs)
	}
	return runs[0]
}

func recordTypes(t *testing.T, runDir string) []string {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatalf("replaying the Ledger: %v", err)
	}
	var types []string
	for _, r := range recs {
		types = append(types, r.Type)
	}
	return types
}

var wantOrder = []string{
	run.RecRunStarted, run.RecSnapshotTaken, run.RecOracleVersion, run.RecPreflightObserved,
	run.RecAttemptStarting, run.RecProcessStarted, run.RecAttemptEnded,
	run.RecCheckStarted, run.RecCheckEnded, run.RecVerdict, run.RecRunEnded,
}

// writeAheadScript fails the Attempt unless the Ledger already records it
// as starting while the agent runs (ADR-0012).
const writeAheadScript = `grep '"type":"AttemptStarting"' "$OGE_TEST_STATE"/private/runs/*/ledger.jsonl | grep -q '"attempt":"implement#1"' || exit 9
`

func TestRunAcceptsWhenTheCheckPasses(t *testing.T) {
	f := newRunFixture(t)
	code, out, errOut := f.run(t, writeAheadScript+fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	t.Logf("stdout:\n%s", out)
	for _, want := range []string{
		"· Fast mode · HEAD ", "+ 1 untracked",
		"implement  fake · Exit done · Candidate ", "· 1 file changed",
		"check      go test -json ./... · 1 ran · 0 failed · pass",
		"ACCEPTED   Candidate ", "Oracle v0",
		"Not covered", "Checks run Candidate code uncontained; a hostile Candidate can forge test results",
		"Nothing was written to your repository.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[implement") {
		t.Errorf("the event stream showed without -v:\n%s", out)
	}
	f.assertUntouched(t)

	dir := f.onlyRun(t)
	if got := strings.Join(recordTypes(t, dir), ","); got != strings.Join(wantOrder, ",") {
		t.Errorf("Ledger order:\n got %s\nwant %s", got, strings.Join(wantOrder, ","))
	}
	repo := filepath.Join(dir, "repo.git")
	if out := gitOut(t, repo, "remote"); out != "" {
		t.Errorf("the Run repository has remotes: %q", out)
	}
	if out := strings.TrimSpace(gitOut(t, repo, "config", "core.hooksPath")); out != os.DevNull {
		t.Errorf("core.hooksPath = %q", out)
	}
	if fi, err := os.Stat(filepath.Join(f.state, "private")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("private/: %v %v", fi, err)
	}
	if left, _ := filepath.Glob(filepath.Join(f.state, "work", "*")); len(left) != 0 {
		t.Errorf("Workspaces left behind: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "checks", "*")); len(left) != 0 {
		t.Errorf("Check directories left behind: %v", left)
	}
}

// sendBackLimit sets the fixture's send-back limit.
func (f *runFixture) sendBackLimit(t *testing.T, n int) {
	t.Helper()
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(fxConfig+fmt.Sprintf("[pipelines.default.limits]\nsend_backs = %d\n", n)))
	gitIn(t, f.repo, "-c", "core.hooksPath="+os.DevNull, "commit", "-q", "-am", "limit send-backs")
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")
}

// fixOnSendBack cheats on the first turn and fixes Add once a send-back
// turn hands it the Check's failure output.
const fixOnSendBack = `case "$OGE_FAKE_TURN" in
*"Add(2, 3) != 5"*) ;;
*) echo "making the test pass"; printf 'package fx\n\nimport "testing"\n\nfunc TestAdd(t *testing.T) {}\n' > add_test.go; exit 0 ;;
esac
` + fixScript

func TestRunSendsAFailingCandidateBackWithTheFailureOutput(t *testing.T) {
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixOnSendBack, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"check      go test -json ./... · 1 ran · 1 failed: TestAdd · fail (exit 1)",
		"send back  1 of 3 · the Candidate goes back to the implementer with the failure output",
		"check      go test -json ./... · 1 ran · 0 failed · pass",
		"ACCEPTED   Candidate ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	f.assertUntouched(t)
	recs, err := ledger.Replay(f.onlyRun(t))
	if err != nil {
		t.Fatal(err)
	}
	var causes []string
	for _, r := range recs {
		if r.Type == run.RecAttemptStarting {
			var d struct{ Attempt, Cause string }
			if err := json.Unmarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
			causes = append(causes, d.Attempt+" "+d.Cause)
		}
	}
	if got := strings.Join(causes, ", "); got != "implement#1 first, implement#2 send_back" {
		t.Errorf("Attempts: %s", got)
	}
}

// Unattended, a fail Verdict with the send-back limit used up opens the
// mandatory bound-exhaustion Gate, and the Run parks there: a park is
// never turned into Rejected (ADR-0008).
func TestRunUnattendedParksAtBoundExhaustion(t *testing.T) {
	f := newRunFixture(t)
	f.sendBackLimit(t, 1)
	code, out, errOut := f.run(t, cheatScript, "fix Add", "--fast", "--agent", "fake", "--unattended", "-v")
	if code != ExitParked {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		`[implement #1 fake] "making the test pass"`,
		"check      go test -json ./... · 1 ran · 1 failed: TestAdd · fail (exit 1)",
		"[verdict] FAIL",
		"send back  1 of 1 · ",
		"[implement #2 fake] started (cause: send_back)",
		"PARKED     at the bound-exhaustion Gate · Candidate ",
		"The Check failed and the send-back limit (1) is used up.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "REJECTED") {
		t.Errorf("a park was shown as Rejected:\n%s", out)
	}
	f.assertUntouched(t)
	want := append(append([]string{}, wantOrder[:len(wantOrder)-1]...),
		run.RecAttemptStarting, run.RecProcessStarted, run.RecAttemptEnded,
		run.RecCheckStarted, run.RecCheckEnded, run.RecVerdict, run.RecGateOpened, run.RecRunParked)
	if got := strings.Join(recordTypes(t, f.onlyRun(t)), ","); got != strings.Join(want, ",") {
		t.Errorf("Ledger order:\n got %s\nwant %s", got, strings.Join(want, ","))
	}
}

func TestRunRefusals(t *testing.T) {
	f := newRunFixture(t)
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"standard needs a verifier": {[]string{"fix Add", "--agent", "fake", "--unattended"}, "Standard mode needs a verifier"},
		"blind needs a verifier":    {[]string{"fix Add", "--blind", "--agent", "fake", "--unattended"}, "Blind mode needs a verifier"},
		"no adapter for claude":     {[]string{"fix Add", "--fast", "--agent", "claude", "--unattended"}, "running a Task with claude isn't implemented yet"},
		"attended needs a terminal": {[]string{"fix Add", "--fast", "--agent", "fake"}, "pass --unattended"},
	} {
		code, _, errOut := f.run(t, fixScript, c.args...)
		if code != ExitRefused || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: exit %d, stderr %q", name, code, errOut)
		}
	}
	if runs, _ := filepath.Glob(filepath.Join(f.state, "private", "runs", "*")); len(runs) != 0 {
		t.Errorf("a refused Run left state: %v", runs)
	}
}

func TestRunPreflightRefusesAMergeInProgress(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, ".git", "MERGE_HEAD"), []byte(strings.Repeat("0", 40)+"\n"))
	code, _, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitRefused || !strings.Contains(errOut, "a merge is in progress") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestRunStateRootInsideTheRepositoryRefuses(t *testing.T) {
	f := newRunFixture(t)
	t.Setenv(ledger.StateDirEnv, filepath.Join(f.repo, "state"))
	code, _, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitRefused || !strings.Contains(errOut, "inside the repository") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if len(args) > 0 && (args[0] == "config" || args[0] == "remote") {
			return string(out)
		}
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func TestRunPreflightRunsSetupOnTheSnapshot(t *testing.T) {
	f := newRunFixture(t)
	// The setup command sees the Snapshot (untracked test included) and
	// fails, so Preflight refuses before any agent starts.
	cfg := fxConfig + "[setup]\nrun = \"test -f add_test.go && exit 7\"\n"
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	marker := filepath.Join(t.TempDir(), "agent-ran")
	code, _, errOut := f.run(t, "touch '"+marker+"'\n", "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitRefused || !strings.Contains(errOut, "fails on the Snapshot (exit 7)") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the agent ran after setup failed on the Snapshot")
	}
}

func TestRunSetupCannotRewriteTheOracle(t *testing.T) {
	f := newRunFixture(t)
	// Setup is Candidate-controlled; it runs before the overlay, so a
	// setup that replaces the Oracle's test changes nothing.
	cfg := fxConfig + "[setup]\nrun = \"sh setup.sh\"\n[pipelines.default.limits]\nsend_backs = 0\n"
	writeFile(t, filepath.Join(f.repo, "setup.sh"), []byte("printf 'package fx\\n\\nimport \"testing\"\\n\\nfunc TestAdd(t *testing.T) {}\\n' > add_test.go\n"))
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	code, out, errOut := f.run(t, "echo 'nothing to do'\n", "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked || !strings.Contains(out, "1 failed: TestAdd") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func TestRunRedactsAgentText(t *testing.T) {
	const secret = "sk-ant-abcdefghijklmnopqrstuvwxyz0123"
	for name, script := range map[string]string{
		"claim": "echo 'my key is " + secret + "'\n" + fixScript,
		"exit":  "echo 'exit: " + secret + "'\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newRunFixture(t)
			code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
			if code != ExitOK && code != ExitInfra {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			err := filepath.WalkDir(f.state, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(secret)) {
					t.Errorf("%s holds the secret", p)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunOverlayNeverWritesThroughASymlink(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "sub", "x.go"), []byte("package sub\n"))
	writeFile(t, filepath.Join(f.repo, "sub", "x_test.go"), []byte("package sub\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n"))
	outside := filepath.Join(filepath.Dir(f.repo), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "rm -rf sub && ln -s '" + outside + "' sub\n" + fixScript
	f.sendBackLimit(t, 0)
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if _, err := os.Lstat(filepath.Join(outside, "x_test.go")); err == nil {
		t.Error("the overlay wrote an Oracle test outside the Check directory")
	}
	if code != ExitParked || !strings.Contains(out, "Oracle path sub/x_test.go is blocked by a symlink in the Candidate") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func TestRunPreflightRefusesASymlinkOutOfTheRepository(t *testing.T) {
	f := newRunFixture(t)
	if err := os.Symlink(filepath.Dir(f.repo), filepath.Join(f.repo, "up")); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitRefused || !strings.Contains(errOut, "up is a symlink that points outside the repository") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestRunEveryOracleTestMustPass(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "sub", "x.go"), []byte("package sub\n\nfunc X() int { return 0 }\n"))
	writeFile(t, filepath.Join(f.repo, "sub", "x_test.go"), []byte("package sub\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tif X() != 1 {\n\t\tt.Fatal(\"X() != 1\")\n\t}\n}\n"))
	// A nested module drops sub out of ./..., so TestX never runs.
	script := fixScript + "printf 'module sub\\n\\ngo 1.22\\n' > sub/go.mod\n"
	f.sendBackLimit(t, 0)
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked || !strings.Contains(out, "Oracle tests that never passed (1): fx/sub.TestX") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// A Run cancelled during its Check stops with no Verdict, in both views:
// the Check killed by the cancel is not a fail. SIGTERM cancels like
// Ctrl-C, rather than killing Öge and orphaning its children.
func TestRunCancelledDuringTheCheckHasNoVerdict(t *testing.T) {
	for _, c := range []struct {
		tty bool
		sig string
	}{{false, "INT"}, {true, "INT"}, {false, "TERM"}} {
		tty := c.tty
		t.Run(fmt.Sprintf("tty=%v,SIG%s", tty, c.sig), func(t *testing.T) {
			f := newRunFixture(t)
			f.interactive = tty
			t.Setenv("TERM", "xterm-256color")
			// The Check interrupts this process, as Ctrl-C would, then
			// would take far longer than the test.
			slow := fmt.Sprintf("sh -c 'kill -%s %d; exec sleep 30'", c.sig, os.Getpid())
			args := []string{"fix Add", "--fast", "--agent", "fake", "--check", slow}
			if !tty {
				args = append(args, "--unattended")
			}
			code, out, errOut := f.run(t, fixScript, args...)
			if code != ExitInfra || strings.Contains(out, "REJECTED") || !strings.Contains(out, "INFRASTRUCTURE STOP") {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			types := recordTypes(t, f.onlyRun(t))
			for _, rec := range types {
				if rec == run.RecVerdict {
					t.Errorf("a cancelled Check wrote a Verdict: %v", types)
				}
			}
		})
	}
}
