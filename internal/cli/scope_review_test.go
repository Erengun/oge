package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/erengun/oge/internal/run"
)

// useConfig commits cfg as the fixture's .oge/oge.toml.
func (f *runFixture) useConfig(t *testing.T, cfg string) {
	t.Helper()
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	gitIn(t, f.repo, "add", "-A", ".oge")
	gitIn(t, f.repo, "commit", "-q", "-m", "config")
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")
}

var cfgGoModProtected = strings.Replace(fxConfig, "[project]\n", "[project]\ntest_config = [\"go.mod\"]\n", 1)

// A writer that escaped the agent's process group (setsid, double fork)
// and rewrites protected files after the comparison never reaches the
// Candidate: its late writes are Tamper events too.
func TestRunEscapedWriterCannotRaceTheCandidate(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not on PATH")
	}
	f := newRunFixture(t)
	f.useConfig(t, cfgGoModProtected)
	pidFile := f.state + ".escaped.pid"
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	escaped := `perl -MPOSIX -e '
fork and exit; setsid(); fork and exit;
open STDIN, "</dev/null"; open STDOUT, ">/dev/null"; open STDERR, ">/dev/null";
open P, ">", "$ENV{OGE_TEST_STATE}.escaped.pid"; print P "$$\n"; close P;
for (1..1000) {
  last if system("grep -q ScopeObserved $ENV{OGE_TEST_STATE}/private/runs/*/ledger.jsonl") == 0;
  select(undef, undef, undef, 0.005);
}
for (1..2000) {
  open F, ">", "add_test.go" or exit; print F "package fx\n"; close F;
  open F, ">", "go.mod" or exit; print F "module fx\n\ngo 1.22\n// late\n"; close F;
  select(undef, undef, undef, 0.001);
}'
`
	code, out, errOut := f.run(t, escaped+fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	dir := f.onlyRun(t)
	if got := candidateFile(t, dir, "add_test.go"); got != fxTest {
		t.Errorf("the Candidate took the late add_test.go: %q", got)
	}
	if got := candidateFile(t, dir, "go.mod"); got != "module fx\n\ngo 1.22\n" {
		t.Errorf("the Candidate took the late go.mod: %q", got)
	}
	if code != ExitParked || len(records(t, dir, run.RecTamperEvent)) == 0 {
		t.Fatalf("exit %d, Tamper events %v\nstdout:\n%s\nstderr:\n%s", code, records(t, dir, run.RecTamperEvent), out, errOut)
	}
}

// Öge's git operations are byte-exact: a user's own eol attribute on a
// protected file is no Tamper event.
func TestRunHonestEolAttributeIsNoTamper(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, ".gitattributes"), []byte("go.mod text eol=crlf\n"))
	writeFile(t, filepath.Join(f.repo, "go.mod"), []byte("module fx\r\n\r\ngo 1.22\r\n"))
	f.useConfig(t, cfgGoModProtected)
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || strings.Contains(out, "scope") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// An attribute the agent adds can't rewrite a protected blob it never
// touched.
func TestRunAgentAttributesCannotRewriteProtectedBlobs(t *testing.T) {
	f := newRunFixture(t)
	test := strings.Replace(fxTest, "package fx\n", "package fx\n\n// é\n", 1)
	writeFile(t, filepath.Join(f.repo, "add_test.go"), []byte(test))
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")
	script := fixScript + "printf 'add_test.go working-tree-encoding=ISO-8859-1\\n' > .gitattributes\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := candidateFile(t, f.onlyRun(t), "add_test.go"); got != test {
		t.Errorf("the Candidate's add_test.go: %q", got)
	}
}

// A protected file the agent made unreadable counts as changed and is
// restored; it is no internal error.
func TestRunUnreadableProtectedFileIsReverted(t *testing.T) {
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixScript+"chmod 000 add_test.go\nmkdir locked && echo x > locked/f && chmod 000 locked\n",
		"fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked || !strings.Contains(out, "1 protected test change reverted: add_test.go") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := candidateFile(t, f.onlyRun(t), "add_test.go"); got != fxTest {
		t.Errorf("add_test.go: %q", got)
	}
}

// Tamper events are recorded reverted only once the revert happened.
func TestRunTamperEventRecordsTheRevertOutcome(t *testing.T) {
	f := newRunFixture(t)
	f.run(t, tamperFix, "fix Add", "--fast", "--agent", "fake", "--unattended")
	dir := f.onlyRun(t)
	obs := records(t, dir, run.RecScopeObserved)
	if len(obs) != 1 || obs[0]["state"] != "planned" {
		t.Errorf("ScopeObserved: %v", obs)
	}
	types := strings.Join(recordTypes(t, dir), ",")
	if !strings.Contains(types, run.RecScopeObserved+","+run.RecScopeReverted+","+run.RecTamperEvent) {
		t.Errorf("Ledger order: %s", types)
	}
}

// The Check reads the Snapshot's test configuration, laid down after
// setup like the Oracle's tests, whatever Candidate code did to it.
func TestRunCheckReadsTheSnapshotsTestConfig(t *testing.T) {
	f := newRunFixture(t)
	want := filepath.Join(filepath.Dir(f.repo), "go.mod.want")
	writeFile(t, want, []byte("module fx\n\ngo 1.22\n"))
	writeFile(t, filepath.Join(f.repo, "setup.sh"), []byte("true\n"))
	cfg := cfgGoModProtected + "[[check.commands]]\nrun = \"cmp go.mod " + want + "\"\n[setup]\nrun = \"sh setup.sh\"\n"
	f.useConfig(t, cfg)
	script := fixScript + "printf 'echo \"// via setup\" >> go.mod\\n' > setup.sh\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}
