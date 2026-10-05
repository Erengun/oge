package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
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
	// coldSeed starts the Run's cache seed empty, as a real Run does,
	// instead of from the tests' warm template.
	coldSeed bool
	// wrap, when set, wraps the fake adapter.
	wrap func(agent.Adapter) agent.Adapter
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
		Dir:               f.repo,
		Interactive:       func() bool { return f.interactive },
		LookPath:          exec.LookPath,
		Edit:              func(string) error { t.Fatal("editor opened"); return nil },
		GOOS:              runtime.GOOS,
		Version:           "test",
		Getenv:            os.Getenv,
		Agents:            map[string]agent.Adapter{fake.Name: &fake.Adapter{Script: path, Env: []string{"OGE_TEST_STATE=" + f.state}}},
		CacheSeedTemplate: map[bool]string{false: testSeed}[f.coldSeed],
	}
	if f.wrap != nil {
		env.Agents[fake.Name] = f.wrap(env.Agents[fake.Name])
	}
	code := Main(env, args)
	return code, stdout.String(), stderr.String()
}

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
	run.RecAttemptStarting, run.RecProcessStarted, run.RecObservation, run.RecScopeObserved, run.RecScopeReverted, run.RecAttemptEnded,
	run.RecCacheSeeded, run.RecCheckStarted, run.RecCheckEnded, run.RecVerdict, run.RecRunEnded,
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
	if _, err := os.Stat(filepath.Join(dir, "cache-seed")); err == nil {
		t.Error("the cache seed was left behind")
	}

	// The Check started from a private copy of the seed the warm step
	// filled on the Snapshot, and its Evidence says so (ADR-0021).
	var seeded struct {
		Warm *struct {
			Run  string
			Pass bool
		} `json:"warm"`
		Complete bool   `json:"complete"`
		WaitedMs *int64 `json:"waited_ms"`
	}
	var ended struct {
		Result struct {
			Cache   string `json:"cache"`
			CacheMs *int64 `json:"cache_materialise_ms"`
		} `json:"result"`
	}
	recordData(t, dir, run.RecCacheSeeded, &seeded)
	recordData(t, dir, run.RecCheckEnded, &ended)
	if seeded.Warm == nil || !strings.Contains(seeded.Warm.Run, " go list ") || !seeded.Warm.Pass || !seeded.Complete || seeded.WaitedMs == nil {
		t.Errorf("CacheSeeded = %+v, want the passing warm step and the wait", seeded)
	}
	want := map[bool]string{true: oracle.CacheClone, false: ""}[runtime.GOOS == "darwin"]
	if c := ended.Result.Cache; c == oracle.CacheCold || c == "" || (want != "" && c != want) || ended.Result.CacheMs == nil {
		t.Errorf("CheckEnded cache = %q (%v ms), want a seeded cache", c, ended.Result.CacheMs)
	}
}

// recordData decodes the data of the Run's only record of type typ.
func recordData(t *testing.T, runDir, typ string, v any) {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Type == typ {
			if err := json.Unmarshal(r.Data, v); err != nil {
				t.Fatalf("%s: %v", typ, err)
			}
			return
		}
	}
	t.Fatalf("no %s record", typ)
}

func TestRunRejectsWhenTheOracleFails(t *testing.T) {
	f := newRunFixture(t)
	code, out, errOut := f.run(t, cheatScript, "fix Add", "--fast", "--agent", "fake", "--unattended", "-v")
	if code != ExitRejected {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		`[implement #1 fake] "making the test pass"`,
		"[implement #1 fake] Exit: done   (Claim)",
		"check      go test -json ./... · 1 ran · 1 failed: TestAdd · fail (exit 1)",
		"[verdict] FAIL",
		"REJECTED   Candidate ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	f.assertUntouched(t)
	// The rewritten test is reverted and recorded before the Attempt ends.
	tamperOrder := strings.Replace(strings.Join(wantOrder, ","), run.RecScopeReverted, run.RecScopeReverted+","+run.RecTamperEvent, 1)
	if got := strings.Join(recordTypes(t, f.onlyRun(t)), ","); got != tamperOrder {
		t.Errorf("Ledger order:\n got %s\nwant %s", got, tamperOrder)
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
	cfg := fxConfig + "[setup]\nrun = \"sh setup.sh\"\n"
	writeFile(t, filepath.Join(f.repo, "setup.sh"), []byte("printf 'package fx\\n\\nimport \"testing\"\\n\\nfunc TestAdd(t *testing.T) {}\\n' > add_test.go\n"))
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	code, out, errOut := f.run(t, "echo 'nothing to do'\n", "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitRejected || !strings.Contains(out, "1 failed: TestAdd") {
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
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitRejected || !strings.Contains(out, "Oracle tests that never passed (1): fx/sub.TestX") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// A Run cancelled while it waits for its cache seed's warm step stops
// with no Verdict, not with an internal error.
func TestRunCancelledWhileTheSeedWarmsHasNoVerdict(t *testing.T) {
	f := newRunFixture(t)
	f.coldSeed = true // a cold warm step takes seconds
	// Interrupt as Ctrl-C would, once the Attempt has ended and the Run
	// is waiting for the seed.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			logs, _ := filepath.Glob(filepath.Join(f.state, "private", "runs", "*", "ledger.jsonl"))
			for _, l := range logs {
				if b, _ := os.ReadFile(l); bytes.Contains(b, []byte(`"type":"AttemptEnded"`)) {
					if p, err := os.FindProcess(os.Getpid()); err == nil {
						_ = p.Signal(os.Interrupt)
					}
					return
				}
			}
		}
	}()
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitInfra || !strings.Contains(out, "INFRASTRUCTURE STOP") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, rec := range recordTypes(t, f.onlyRun(t)) {
		if rec == run.RecCheckStarted || rec == run.RecVerdict {
			t.Errorf("a Run cancelled before its Check wrote %s", rec)
		}
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

// A Run that Öge couldn't clean up after (a forced stop, a crash) leaves
// its cache seed and Check directories; the next Run sweeps them, but
// never a live Run's, nor any Run's Ledger.
func TestRunSweepsLeftoverCachesOfDeadRuns(t *testing.T) {
	f := newRunFixture(t)
	runs := filepath.Join(f.state, "private", "runs")
	mk := func(id, pid string) {
		for _, p := range []string{"cache-seed/seed/gocache/ab/x-d", "checks/1/gocache/y", "ledger.jsonl"} {
			writeFile(t, filepath.Join(runs, id, p), []byte("x"))
		}
		if err := os.Chmod(filepath.Join(runs, id, "cache-seed", "seed", "gocache", "ab"), 0o500); err != nil {
			t.Fatal(err)
		}
		if pid != "" {
			writeFile(t, filepath.Join(runs, id, "live.pid"), []byte(pid))
		}
	}
	mk("20200101T000000-dead01", "")
	mk("20200101T000000-dead02", "999999999")
	mk("20200101T000000-live01", fmt.Sprint(os.Getpid()))
	defer oracle.RemoveAll(filepath.Join(runs, "20200101T000000-live01"))
	if code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended"); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, id := range []string{"20200101T000000-dead01", "20200101T000000-dead02"} {
		for _, p := range []string{"cache-seed", "checks"} {
			if _, err := os.Stat(filepath.Join(runs, id, p)); err == nil {
				t.Errorf("%s/%s of a dead Run was left", id, p)
			}
		}
		if _, err := os.Stat(filepath.Join(runs, id, "ledger.jsonl")); err != nil {
			t.Errorf("the sweep removed %s's Ledger: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(runs, "20200101T000000-live01", "cache-seed")); err != nil {
		t.Errorf("the sweep removed a live Run's seed: %v", err)
	}
	others, _ := filepath.Glob(filepath.Join(runs, "*", "live.pid"))
	if len(others) != 1 {
		t.Errorf("live.pid files after the Run: %v; want only the live Run's", others)
	}
}

// The implementer's Workspace is a git checkout of the Snapshot: git
// status starts clean there, and the .git never reaches the Candidate.
func TestRunWorkspaceHasAGitCheckoutOfTheSnapshot(t *testing.T) {
	f := newRunFixture(t)
	script := `test -d .git && test -z "$(git status --porcelain)" || exit 7
` + fixScript + `git diff --name-only | grep -qx add.go || exit 8
`
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || !strings.Contains(out, "· 1 file changed") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// cacheSpy records the implementer's tool cache and writes into it.
type cacheSpy struct {
	agent.Adapter
	cache *string
}

func (c cacheSpy) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	*c.cache = spec.Cache
	if err := os.WriteFile(filepath.Join(spec.Cache, "agent-poison"), []byte("x"), 0o600); err != nil {
		return nil, err
	}
	return c.Adapter.Open(ctx, spec)
}

// The implementer's run-private tool cache is never the seed, nor copied
// into it: what the agent writes there never reaches a Check's cache.
func TestRunImplementerCacheNeverReachesTheSeed(t *testing.T) {
	f := newRunFixture(t)
	var cache string
	f.wrap = func(a agent.Adapter) agent.Adapter { return cacheSpy{a, &cache} }
	check := `test ! -e "$GOCACHE/agent-poison" && ! find "$GOCACHE" "$GOMODCACHE" -name agent-poison | grep -q .`
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended", "--check", check)
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if cache == "" || strings.HasPrefix(cache, filepath.Join(f.state, "private")) {
		t.Errorf("the implementer's cache %q is empty or inside private state, where the seed lives", cache)
	}
}
