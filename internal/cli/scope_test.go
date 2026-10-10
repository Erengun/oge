package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// records returns the data of every Ledger record of type typ.
func records(t *testing.T, runDir, typ string) []map[string]any {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatalf("replaying the Ledger: %v", err)
	}
	var out []map[string]any
	for _, r := range recs {
		if r.Type != typ {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(r.Data, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// reverted returns the scope observation's reverted paths, by path.
func reverted(t *testing.T, runDir string) map[string]map[string]any {
	t.Helper()
	obs := records(t, runDir, run.RecScopeObserved)
	if len(obs) != 1 {
		t.Fatalf("want one %s record, got %d", run.RecScopeObserved, len(obs))
	}
	out := map[string]map[string]any{}
	list, _ := obs[0]["reverted"].([]any)
	for _, r := range list {
		m := r.(map[string]any)
		out[m["path"].(string)] = m
	}
	return out
}

func candidateFile(t *testing.T, runDir, path string) string {
	t.Helper()
	return gitOut(t, filepath.Join(runDir, "repo.git"), "show", "refs/oge/candidates/c1:"+path)
}

// tamperFix fixes Add but also rewrites the Oracle's test.
const tamperFix = cheatScript + fixScript

// An implementer write to an Oracle test is reverted and recorded as a
// Tamper event. The reverted Candidate passes, but the Run can't become
// Accepted until a human acknowledges the event.
func TestRunTamperWithAnOracleTestIsRevertedAndBlocksAccepted(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, tamperFix, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"scope      1 protected test change reverted: add_test.go",
		"check      go test -json ./... · 1 ran · 0 failed · pass",
		"Waiting for you: the tamper Gate",
		"an Attempt wrote to a protected file. Öge reverted it and recorded a Tamper event, which must be acknowledged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✓ Accepted") {
		t.Errorf("a Run with an unacknowledged Tamper event was Accepted:\n%s", out)
	}
	f.assertUntouched(t)

	dir := f.onlyRun(t)
	if got := candidateFile(t, dir, "add_test.go"); got != fxTest {
		t.Errorf("the Candidate kept the rewritten test: %q", got)
	}
	r := reverted(t, dir)["add_test.go"]
	if r == nil {
		t.Fatalf("add_test.go not recorded as reverted")
	}
	for _, k := range []string{"before", "after"} {
		if s, _ := r[k].(string); len(s) != 64 {
			t.Errorf("%s hash = %v", k, r[k])
		}
	}
	if r["tamper"] != true || r["class"] != "oracle_test" || r["enforcement"] != "revert-only" || r["change"] != "modified" {
		t.Errorf("revert record: %v", r)
	}
	if r["size"] == nil {
		t.Errorf("revert record lacks size: %v", r)
	}
	patchID, _ := r["patch"].(string)
	if patchID == "" {
		t.Fatalf("no patch recorded: %v", r)
	}
	blobs, err := ledger.OpenBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := blobs.Get(patchID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "-\tif Add(2, 3) != 5 {") || !strings.Contains(string(patch), "+++ b/add_test.go") {
		t.Errorf("patch:\n%s", patch)
	}

	tampers := records(t, dir, run.RecTamperEvent)
	if len(tampers) != 1 || tampers[0]["path"] != "add_test.go" || tampers[0]["attempt"] != "implement#1" || tampers[0]["reverted"] != true {
		t.Errorf("Tamper events: %v", tampers)
	}
	parked := records(t, dir, run.RecRunParked)
	if len(parked) != 1 || parked[0]["gate"] != "gate.tamper" || len(records(t, dir, run.RecRunEnded)) != 0 {
		t.Errorf("RunParked: %v", parked)
	}
}

// A Tamper event doesn't change a failing Verdict: the fail outranks it,
// and the Candidate goes back until the bound-exhaustion Gate.
func TestRunTamperWithAFailingCheckGoesBack(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	code, out, errOut := f.run(t, cheatScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked || !strings.Contains(out, "scope      1 protected test change reverted: add_test.go") ||
		!strings.Contains(out, "Waiting for you: the bound-exhaustion Gate") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// .oge/ and the listed test configuration are protected too.
func TestRunWritesToOgeConfigAndTestConfigAreReverted(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	cfg := fxConfig + "\n[[check.commands]]\nrun = \"true\"\n"
	cfg = strings.Replace(cfg, "[project]\n", "[project]\ntest_config = [\"go.mod\"]\n", 1)
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	gitIn(t, f.repo, "add", "-A", ".oge")
	gitIn(t, f.repo, "commit", "-q", "-m", "config")
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")

	script := fixScript + "echo '# gone' > .oge/oge.toml\nrm go.mod\necho x > .oge/new.txt\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "scope      3 protected changes reverted: .oge/new.txt, .oge/oge.toml, go.mod") {
		t.Errorf("stdout:\n%s", out)
	}
	dir := f.onlyRun(t)
	if got := candidateFile(t, dir, ".oge/oge.toml"); got != cfg {
		t.Errorf("the Candidate kept the rewritten config: %q", got)
	}
	if got := candidateFile(t, dir, "go.mod"); got != "module fx\n\ngo 1.22\n" {
		t.Errorf("go.mod in the Candidate: %q", got)
	}
	r := reverted(t, dir)
	if r["go.mod"]["class"] != "test_config" || r["go.mod"]["change"] != "deleted" || r["go.mod"]["after"] != "" {
		t.Errorf("go.mod: %v", r["go.mod"])
	}
	if r[".oge/oge.toml"]["class"] != "oge_config" || r[".oge/new.txt"]["change"] != "added" {
		t.Errorf("records: %v", r)
	}
	if n := len(records(t, dir, run.RecTamperEvent)); n != 3 {
		t.Errorf("%d Tamper events, want 3", n)
	}
}

// A symlink out of the Workspace is removed and recorded; it is a scope
// revert, not a Tamper event, so the Run can still be Accepted.
func TestRunSymlinkEscapeIsRemoved(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	outside := filepath.Join(filepath.Dir(f.repo), "outside")
	writeFile(t, filepath.Join(outside, "secret.txt"), []byte("host file\n"))
	script := fixScript + "ln -s '" + outside + "' out\nln -s ../../../.. up\nmkdir -p d && ln -s ../out d/via\nln -s add.go in\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended", "--output", "in")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "scope      3 symlinks out of the Workspace removed: d/via, out, up") {
		t.Errorf("stdout:\n%s", out)
	}
	dir := f.onlyRun(t)
	files := gitOut(t, filepath.Join(dir, "repo.git"), "ls-tree", "-r", "--name-only", "refs/oge/candidates/c1")
	for _, p := range []string{"out", "up", "d/via"} {
		if strings.Contains("\n"+files, "\n"+p+"\n") {
			t.Errorf("the Candidate kept %s:\n%s", p, files)
		}
	}
	if !strings.Contains(files, "in\n") {
		t.Errorf("an in-Workspace symlink was dropped:\n%s", files)
	}
	r := reverted(t, dir)
	if r["out"]["class"] != "symlink_escape" || r["out"]["tamper"] == true {
		t.Errorf("out: %v", r["out"])
	}
	if len(records(t, dir, run.RecTamperEvent)) != 0 {
		t.Error("a symlink escape was recorded as a Tamper event")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "host file\n" {
		t.Error("the host file changed")
	}
}

// Replacing a test directory with a symlink out of the Workspace: the
// link is removed and the Oracle test restored, never through the link.
func TestRunRevertNeverWritesThroughASymlink(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "sub", "x.go"), []byte("package sub\n"))
	writeFile(t, filepath.Join(f.repo, "sub", "x_test.go"), []byte("package sub\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n"))
	outside := filepath.Join(filepath.Dir(f.repo), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "rm -rf sub && ln -s '" + outside + "' sub\n" + fixScript
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if left, _ := os.ReadDir(outside); len(left) != 0 {
		t.Errorf("the revert wrote outside the Workspace: %v", left)
	}
	if code != ExitParked || !strings.Contains(out, "1 protected test change reverted: sub/x_test.go") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	dir := f.onlyRun(t)
	if got := candidateFile(t, dir, "sub/x_test.go"); !strings.Contains(got, "TestX") {
		t.Errorf("sub/x_test.go in the Candidate: %q", got)
	}
	if r := reverted(t, dir)["sub"]; r == nil || r["change"] != "added" {
		t.Errorf("the blocking link: %v", r)
	}
}

// The agent's whole process tree is gone before the comparison, so a
// background writer can't change a protected file after its revert. (It
// rewrites the test: an addition would be kept, #117.)
func TestRunScopeCheckRunsAfterTheAgentTreeIsKilled(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	script := "( while :; do echo 'package fx' > add_test.go; sleep 0.01; done ) >/dev/null 2>&1 &\nsleep 0.1\n" + fixScript
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	dir, committed := raced(t, f, code, out, errOut)
	if !committed {
		return
	}
	if code != ExitParked {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := candidateFile(t, dir, "add_test.go"); got != fxTest {
		t.Errorf("the Candidate's add_test.go: %q", got)
	}
}

// New tests the implementer writes are its own, not the Oracle's, and the
// Workspace's .git (Öge's copy, which the agent may still write) is
// ignored: no scope or Tamper record, never in the Candidate.
func TestRunNewTestsAndAgentGitAreInScope(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	script := fixScript + "printf 'package fx\\n' > more_test.go\ntest -d .git/objects || exit 7\necho x > .git/HEAD && echo y > .git/objects/junk\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || strings.Contains(out, "\nscope ") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	dir := f.onlyRun(t)
	if len(reverted(t, dir)) != 0 {
		t.Error("an in-scope write was reverted")
	}
	if n := len(records(t, dir, run.RecTamperEvent)); n != 0 {
		t.Errorf("the Workspace .git caused %d Tamper events", n)
	}
	if files := gitOut(t, filepath.Join(dir, "repo.git"), "ls-tree", "-r", "--name-only", "refs/oge/candidates/c1"); strings.Contains(files, ".git/") {
		t.Errorf("the Candidate holds the Workspace .git:\n%s", files)
	}
}

// appendTest adds a test to the Oracle's test file, one that would fail
// if the Check ran it.
const appendTest = `printf '\nfunc TestAddZero(t *testing.T) {\n\tt.Fatal("never run by the Check")\n}\n' >> add_test.go
`

// An edit that only adds a test to an Oracle test file is the
// implementer's own test (#117): no revert, no Tamper event, no Gate. The
// Candidate delivers it, while the Check runs the Oracle's version.
func TestRunAdditiveOracleTestEditIsKept(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixScript+appendTest, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || strings.Contains(out, "\nscope ") || strings.Contains(out, "Gate") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "check      go test -json ./... · 1 ran · 0 failed · pass") {
		t.Errorf("the Check didn't run only the Oracle's test:\n%s", out)
	}
	dir := f.onlyRun(t)
	if got := candidateFile(t, dir, "add_test.go"); !strings.HasPrefix(got, fxTest) || !strings.Contains(got, "func TestAddZero") {
		t.Errorf("the Candidate lost the addition: %q", got)
	}
	if len(reverted(t, dir)) != 0 || len(records(t, dir, run.RecTamperEvent)) != 0 {
		t.Errorf("the addition was reverted or a Tamper event")
	}
	kept, _ := records(t, dir, run.RecScopeObserved)[0]["kept"].([]any)
	if len(kept) != 1 {
		t.Fatalf("kept: %v", kept)
	}
	k := kept[0].(map[string]any)
	if k["path"] != "add_test.go" || k["class"] != "oracle_test_addition" || k["tamper"] != false ||
		len(k["added"].([]any)) != 1 || k["added"].([]any)[0] != "TestAddZero" {
		t.Errorf("kept record: %v", k)
	}
}

// Attempt 1 adds a test but leaves Add broken, so the Candidate it sends
// back holds the addition. Attempt 2 rewrites the test: that is reverted
// to the Snapshot's content, not to Attempt 1's, and is a Tamper event.
const addThenRewrite = `case "$OGE_FAKE_TURN" in
*"Add(2, 3) != 5"*) ;;
*) echo "adding a test"; ` + appendTest + ` exit 0 ;;
esac
` + tamperFix

func TestRunRewriteAfterAKeptAdditionGoesBackToTheSnapshot(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, addThenRewrite, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked || !strings.Contains(out, "Waiting for you: the tamper Gate") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	dir := f.onlyRun(t)
	repo := filepath.Join(dir, "repo.git")
	if got := gitOut(t, repo, "show", "refs/oge/candidates/c1:add_test.go"); !strings.Contains(got, "func TestAddZero") {
		t.Errorf("c1 lacks the kept addition: %q", got)
	}
	if got := gitOut(t, repo, "show", "refs/oge/candidates/c2:add_test.go"); got != fxTest {
		t.Errorf("c2:add_test.go differs from the Snapshot's: %q", got)
	}
	tampers := records(t, dir, run.RecTamperEvent)
	if len(tampers) != 1 || tampers[0]["attempt"] != "implement#2" || tampers[0]["late"] != nil {
		t.Errorf("Tamper events: %v", tampers)
	}
}

// The reviewer's scenario: the agent empties the Oracle's test on its
// first turn, then fixes Add once sent back. The rewrite is reverted and
// recorded, so the lineage is clean, but the Run is never Accepted: it
// stops at the Tamper Gate, attended, or parks there.
const emptyThenFix = `case "$OGE_FAKE_TURN" in
*"Add(2, 3) != 5"*) ;;
*) echo "emptying the test"; : > add_test.go; exit 0 ;;
esac
` + fixScript

func TestRunTamperThenAFixIsNeverAccepted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		stdin string
		args  []string
		code  int
		want  []string
	}{
		{"unattended", "", []string{"--unattended"}, ExitParked, []string{"Waiting for you: the tamper Gate"}},
		{"attended", "quit\n", []string{"--plain"}, ExitCancelled, []string{
			"tamper Gate", "a protected file change was reverted", "  reverted: add_test.go (modified)",
			"  acknowledge   ",
			"  reject        end the Run Rejected", "  q  quit       end the Run Cancelled",
			"decision   quit · recorded at the tamper Gate", "Result        ■ Cancelled",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newRunFixture(t)
			if c.stdin != "" {
				f.attended(c.stdin)
			}
			code, out, errOut := f.run(t, emptyThenFix, append([]string{"fix Add", "--fast", "--agent", "fake"}, c.args...)...)
			if code != c.code || strings.Contains(out, "✓ Accepted") {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			for _, w := range append(c.want, "send back  1 of 3", "check      go test -json ./... · 1 ran · 0 failed · pass") {
				if !strings.Contains(out, w) {
					t.Errorf("stdout lacks %q:\n%s", w, out)
				}
			}
			if strings.Contains(out, "  t  take") {
				t.Errorf("the Tamper Gate offered take:\n%s", out)
			}
			dir := f.onlyRun(t)
			for _, ref := range []string{"refs/oge/candidates/c1", "refs/oge/candidates/c2"} {
				if got := gitOut(t, filepath.Join(dir, "repo.git"), "show", ref+":add_test.go"); got != fxTest {
					t.Errorf("%s:add_test.go differs from the Snapshot's: %q", ref, got)
				}
			}
			tampers := records(t, dir, run.RecTamperEvent)
			if len(tampers) != 1 || tampers[0]["attempt"] != "implement#1" {
				t.Errorf("Tamper events: %v", tampers)
			}
		})
	}
}

// Acknowledging the Tamper events (full word, with a reason) lets the
// passing Verdict on the current, reverted Candidate take its normal edge
// to Accepted (ADR-0019 #2). The Ledger keeps the event and the decision.
func TestRunAcknowledgedTamperIsAccepted(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.attended("a\nack\nacknowledge\n\nmy edit to the test was a mistake\n")
	code, out, errOut := f.run(t, tamperFix, "fix Add", "--fast", "--agent", "fake", "--plain")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, w := range []string{
		"  reverted: add_test.go (modified)",
		`type "acknowledge" in full`, "a reason is required for acknowledge",
		"decision   acknowledge · recorded at the tamper Gate · reason: my edit to the test was a mistake",
		"Result        ✓ Accepted",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
	if n := strings.Count(out, `type "acknowledge" in full`); n != 2 {
		t.Errorf("a and ack: refused %d times", n)
	}
	dir := f.onlyRun(t)
	got := strings.Join(recordTypes(t, dir), ",")
	if !strings.Contains(got, run.RecTamperEvent) || !strings.HasSuffix(got, run.RecGateOpened+","+run.RecGateDecided+","+run.RecRunEnded) {
		t.Errorf("Ledger order: %s", got)
	}
	d := records(t, dir, run.RecGateDecided)
	ids, _ := d[0]["tamper_ids"].([]any)
	if len(d) != 1 || d[0]["choice"] != "acknowledge" || len(ids) != 1 || ids[0] != "implement#1/tamper-1" {
		t.Errorf("GateDecided: %v", d)
	}
	if got := candidateFile(t, dir, "add_test.go"); got != fxTest {
		t.Errorf("the Accepted Candidate's test isn't the Snapshot's: %q", got)
	}
}
