package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A Go package whose untracked test fails until Add is fixed.
const (
	runConfig = `schema = 1
[project]
test_globs = ["**/*_test.go"]
[[check.commands]]
run    = "go test -json ./..."
report = "go-test-json"
`
	brokenAdd = "package fx\n\nfunc Add(a, b int) int { return 0 }\n"
	addTest   = "package fx\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2, 3) != 5\")\n\t}\n}\n"
)

// runFixture makes the fixture repository and an environment with a
// synthetic HOME (the state root takes its default place under it), a
// private TMPDIR beside it, and PATH holding git, go and the system tools.
// OGE_TEST_SHARED_GOCACHE (honoured only by the ogetest build) gives Checks
// the shared GOCACHE, only to keep tests fast.
func runFixture(t *testing.T) (repo string, env []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not found")
	}
	base := t.TempDir()
	home, tmp := filepath.Join(base, "home"), filepath.Join(base, "tmp")
	repo = filepath.Join(base, "repo")
	for _, d := range []string{home, tmp, repo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env = []string{
		"HOME=" + home, "TMPDIR=" + tmp, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"PATH=" + filepath.Dir(gitPath) + ":" + filepath.Dir(goPath) + ":/usr/bin:/bin",
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GOCACHE=" + goCache, "OGE_TEST_SHARED_GOCACHE=" + goCache,
	}
	write := func(rel, s string) {
		p := filepath.Join(repo, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir, cmd.Env = repo, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	write(".oge/oge.toml", runConfig)
	write("go.mod", "module fx\n\ngo 1.22\n")
	write("add.go", brokenAdd)
	git("add", "-A")
	git("commit", "-q", "-m", "fixture")
	write("add_test.go", addTest) // untracked: part of the Snapshot
	return repo, env
}

func withScript(t *testing.T, env []string, script string) []string {
	path := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return append(env, "OGE_FAKE_SCRIPT="+path)
}

func TestBinaryRunAcceptedExitsZero(t *testing.T) {
	repo, env := runFixture(t)
	env = withScript(t, env, "printf 'package fx\\n\\nfunc Add(a, b int) int { return a + b }\\n' > add.go\n")
	code, out, errOut := runExe(t, testBinary, repo, env, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != 0 || !strings.Contains(out, "ACCEPTED") || !strings.Contains(out, "1 ran · 0 failed") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	// Without a terminal the output is plain lines (ADR-0022).
	if strings.ContainsRune(out+errOut, 0x1b) {
		t.Errorf("a Run without a terminal wrote escape sequences:\n%q", out+errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "add.go")); string(b) != brokenAdd {
		t.Errorf("the user's add.go was written: %q", b)
	}
}

func TestBinaryRunRejectedExitsThree(t *testing.T) {
	repo, env := runFixture(t)
	env = withScript(t, env, "echo 'nothing to do'\n")
	code, out, errOut := runExe(t, testBinary, repo, env, "run", "--fast", "--agent", "fake", "--unattended", "fix Add")
	if code != 3 || !strings.Contains(out, "REJECTED") || !strings.Contains(out, "1 failed: TestAdd") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func TestReleaseBinaryCannotReachTheFake(t *testing.T) {
	repo, env := runFixture(t)
	code, _, errOut := runBinary(t, repo, env, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != 2 || !strings.Contains(errOut, "unknown agent") {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
}

func TestReleaseBinaryRefusesAStandardRun(t *testing.T) {
	repo, env := runFixture(t)
	code, _, errOut := runBinary(t, repo, env, "fix Add", "--agent", "claude", "--unattended")
	if code != 2 || !strings.Contains(errOut, "Standard mode needs a verifier") {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
}

// A fixed Candidate whose Attempt also rewrote the Oracle's test parks,
// exit 10, until the Tamper event is acknowledged.
func TestBinaryRunTamperParksExitsTen(t *testing.T) {
	repo, env := runFixture(t)
	env = withScript(t, env, "printf 'package fx\\n\\nfunc Add(a, b int) int { return a + b }\\n' > add.go\n"+
		"printf 'package fx\\n' > add_test.go\n")
	code, out, errOut := runExe(t, testBinary, repo, env, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != 10 || !strings.Contains(out, "1 protected test change reverted: add_test.go") || !strings.Contains(out, "PARKED") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "add_test.go")); string(b) != addTest {
		t.Errorf("the user's add_test.go was written: %q", b)
	}
}
