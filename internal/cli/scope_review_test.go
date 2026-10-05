package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	t.Parallel()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not on PATH")
	}
	f := newRunFixture(t)
	f.useConfig(t, cfgGoModProtected)
	pidFile := f.state + ".escaped.pid"
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
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
	dir, committed := raced(t, f, code, out, errOut)
	if !committed {
		return
	}
	if got := candidateFile(t, dir, "add_test.go"); got != fxTest {
		t.Errorf("the Candidate took the late add_test.go: %q", got)
	}
	if got := candidateFile(t, dir, "go.mod"); got != "module fx\n\ngo 1.22\n" {
		t.Errorf("the Candidate took the late go.mod: %q", got)
	}
	// A late write that git saw is a Tamper event, and the Run parks.
	// The writer may also miss the window, and then nothing is late.
	tampers := len(records(t, dir, run.RecTamperEvent))
	if !(code == ExitParked && tampers > 0) && !(code == ExitOK && tampers == 0) {
		t.Fatalf("exit %d, %d Tamper events\nstdout:\n%s\nstderr:\n%s", code, tampers, out, errOut)
	}
}

// raced checks a Run that raced a writer the agent's group kill can't
// reach. There are two safe outcomes. Either a Candidate exists, and the
// caller checks that its protected paths are the Snapshot's. Or the
// Attempt failed closed, because the writer changed a file while Öge was
// comparing or committing it (exit 11), and then there is no Candidate at
// all. It returns the Run's directory and whether a Candidate exists.
func raced(t *testing.T, f *runFixture, code int, out, errOut string) (string, bool) {
	t.Helper()
	dir := f.onlyRun(t)
	refs := gitOut(t, filepath.Join(dir, "repo.git"), "for-each-ref", "refs/oge/candidates/")
	if code == ExitInfra && strings.Contains(out, "the implementer Attempt failed") {
		if refs != "" {
			t.Errorf("a failed Attempt left a Candidate:\n%s", refs)
		}
		return dir, false
	}
	if refs == "" {
		t.Fatalf("exit %d with no Candidate\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	return dir, true
}

// Öge's git operations are byte-exact: a user's own eol attribute on a
// protected file is no Tamper event.
func TestRunHonestEolAttributeIsNoTamper(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, ".gitattributes"), []byte("go.mod text eol=crlf\n"))
	writeFile(t, filepath.Join(f.repo, "go.mod"), []byte("module fx\r\n\r\ngo 1.22\r\n"))
	f.useConfig(t, cfgGoModProtected)
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || strings.Contains(out, "\nscope ") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// An attribute the agent adds can't rewrite a protected blob it never
// touched.
func TestRunAgentAttributesCannotRewriteProtectedBlobs(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	test := strings.Replace(fxTest, "package fx\n", "package fx\n\n// é\n", 1)
	writeFile(t, filepath.Join(f.repo, "add_test.go"), []byte(test))
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")
	script := fixScript + "printf 'add_test.go working-tree-encoding=ISO-8859-1\\n' > .gitattributes\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended", "--output", ".gitattributes")
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
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixScript+"chmod 000 add_test.go\nmkdir locked && echo x > locked/f && chmod 000 locked\n",
		"fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked || !strings.Contains(out, "1 protected test change reverted: add_test.go") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	dir := f.onlyRun(t)
	if got := candidateFile(t, dir, "add_test.go"); got != fxTest {
		t.Errorf("add_test.go: %q", got)
	}
	// Only its permissions changed.
	if r := reverted(t, dir)["add_test.go"]; r["change"] != "mode" || r["before"] != r["after"] || r["tamper"] != true {
		t.Errorf("revert: %v", r)
	}
}

// Tamper events are recorded reverted only once the revert happened.
func TestRunTamperEventRecordsTheRevertOutcome(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// Paths in the scope line are escaped, never stripped, so two distinct
// paths never read as one and no control character reaches the terminal.
func TestRunScopeLineEscapesPaths(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	script := fixScript + "printf x > \".oge/a$(printf '\\033')[2Jb\"\nprintf x > '.oge/a[2Jb'\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked || strings.ContainsRune(out, 0x1b) || !strings.Contains(out, `".oge/a\x1b[2Jb", .oge/a[2Jb`) {
		t.Fatalf("exit %d\nstdout:\n%q\nstderr:\n%s", code, out, errOut)
	}
}

// A symlink an escaped writer adds after the comparison never reaches the
// Candidate, however inward its target text looks: l1 -> l2/.. with
// l2 -> . resolves outside the Workspace's directory.
func TestRunLateSymlinksAreDropped(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not on PATH")
	}
	f := newRunFixture(t)
	pidFile := f.state + ".escaped.pid"
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
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
  unlink "l1", "l2"; symlink(".", "l2") or exit; symlink("l2/..", "l1") or exit;
  select(undef, undef, undef, 0.001);
}'
`
	code, out, errOut := f.run(t, escaped+fixScript+"ln -s add.go kept\n", "fix Add", "--fast", "--agent", "fake", "--unattended", "--output", "kept")
	dir, committed := raced(t, f, code, out, errOut)
	if !committed {
		return
	}
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	files := "\n" + gitOut(t, filepath.Join(dir, "repo.git"), "ls-tree", "-r", "--name-only", "refs/oge/candidates/c1")
	for _, p := range []string{"l1", "l2"} {
		if strings.Contains(files, "\n"+p+"\n") {
			t.Errorf("the Candidate took the late link %s:%s", p, files)
		}
	}
	if !strings.Contains(files, "\nkept\n") {
		t.Errorf("the agent's own link was dropped:%s", files)
	}
}

const wantTestConfigWarning = "! test_config is empty: setup and Candidate code can change go.mod/go.sum that the Check reads"

// A Go project with no test_config is warned, in the dry run and in the
// Run's summary; once test_config is set, it isn't.
func TestRunWarnsWhenTestConfigIsEmpty(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	for _, args := range [][]string{{"--dry-run"}, nil} {
		code, out, errOut := f.run(t, fixScript, append([]string{"fix Add", "--fast", "--agent", "fake", "--unattended"}, args...)...)
		if code != ExitOK || !strings.Contains(out, wantTestConfigWarning) {
			t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
	}
	f.useConfig(t, cfgGoModProtected)
	for _, args := range [][]string{{"--dry-run"}, nil} {
		_, out, _ := f.run(t, fixScript, append([]string{"fix Add", "--fast", "--agent", "fake", "--unattended"}, args...)...)
		if strings.Contains(out, "test_config is empty") {
			t.Errorf("%v: warned with test_config set:\n%s", args, out)
		}
	}
}
