package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// accepted runs script to an Accepted Run and returns its id.
func (f *runFixture) accepted(t *testing.T, script string, extra ...string) string {
	t.Helper()
	args := append([]string{"fix Add", "--fast", "--agent", "fake", "--unattended"}, extra...)
	code, out, errOut := f.run(t, script, args...)
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	return filepath.Base(f.onlyRun(t))
}

// deliver runs an oge diff/apply/branch command.
func (f *runFixture) deliver(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return f.run(t, "", args...)
}

// deliveries are the Run's Delivery records, as "kind flag".
func deliveries(t *testing.T, runDir string) []string {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatalf("replaying the Ledger: %v", err)
	}
	var out []string
	for _, r := range recs {
		if r.Type == delivery.RecDelivery {
			var d struct{ Kind, Flag string }
			if err := json.Unmarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, strings.TrimSpace(d.Kind+" "+d.Flag))
		}
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	fxFixed = "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	fxWrong = "package fx\n\nfunc Add(a, b int) int { return a - b }\n"
	// wrongScript changes Add, but not so its test passes.
	wrongScript = "printf 'package fx\\n\\nfunc Add(a, b int) int { return a - b }\\n' > add.go\n"
)

// The happy path: the Run writes nothing, says how to take the result,
// and oge apply lands it in the working tree, unstaged and uncommitted,
// with the Snapshot's untracked test left as it was.
func TestApplyAcceptedOnACleanTree(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	id := filepath.Base(f.onlyRun(t))
	f.assertUntouched(t)
	// Plain lines print the commands the live view's action bar offers.
	if want := "next       oge apply " + id + " · oge diff " + id + " · oge branch " + id + "\n"; !strings.Contains(out, want) {
		t.Errorf("stdout lacks %q:\n%s", want, out)
	}
	head := gitOut(t, f.repo, "rev-parse", "HEAD")

	code, out, errOut = f.deliver(t, "apply")
	if code != ExitOK {
		t.Fatalf("apply: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.HasPrefix(out, "Applied Candidate ") || !strings.Contains(out, " of Run "+id+" to your working tree: 1 changed. Nothing was committed or staged.") || strings.Count(out, "\n") != 1 {
		t.Errorf("apply's confirmation:\n%s", out)
	}
	if got := readFile(t, filepath.Join(f.repo, "add.go")); got != fxFixed {
		t.Errorf("add.go = %q", got)
	}
	if got := readFile(t, filepath.Join(f.repo, "add_test.go")); got != fxTest {
		t.Errorf("the Snapshot's untracked add_test.go changed: %q", got)
	}
	if got := gitOut(t, f.repo, "status", "--porcelain"); got != " M add.go\n?? add_test.go\n" {
		t.Errorf("git status:\n%s", got)
	}
	if got := gitOut(t, f.repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s -> %s", head, got)
	}
	if _, err := os.Stat(f.hookMarker); err == nil {
		t.Error("a hook in the user's repository ran")
	}
	if got := deliveries(t, f.onlyRun(t)); strings.Join(got, "|") != "apply" {
		t.Errorf("Delivery records: %v", got)
	}

	// Again: nothing to change, and no second Delivery.
	code, out, _ = f.deliver(t, "apply", id)
	if code != ExitOK || !strings.Contains(out, "is already in your working tree; nothing to change") {
		t.Errorf("a second apply: exit %d: %s", code, out)
	}
	if got := deliveries(t, f.onlyRun(t)); len(got) != 1 {
		t.Errorf("Delivery records: %v", got)
	}
}

// longFile has lines far enough apart for a three-way merge.
func longFile(lines map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		l := "line " + string(rune('a'+i-1))
		if s, ok := lines[i]; ok {
			l = s
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// Edits made since the Snapshot that the Candidate doesn't touch stay,
// and edits to other lines of a file it changes are merged.
func TestApplyKeepsUnrelatedEditsMadeSinceTheRun(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "long.txt"), []byte(longFile(nil)))
	gitIn(t, f.repo, "add", "long.txt")
	gitIn(t, f.repo, "commit", "-q", "-m", "long")
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")
	f.accepted(t, fixScript+"sed 's/^line b$/agent edit/' long.txt > long.new && mv long.new long.txt\n")

	// The user goes on working: another line of long.txt, a new file, and
	// the Snapshot's untracked test.
	writeFile(t, filepath.Join(f.repo, "long.txt"), []byte(longFile(map[int]string{18: "user edit"})))
	writeFile(t, filepath.Join(f.repo, "notes.txt"), []byte("mine\n"))
	userTest := fxTest + "\n// more\n"
	writeFile(t, filepath.Join(f.repo, "add_test.go"), []byte(userTest))

	code, out, errOut := f.deliver(t, "apply")
	if code != ExitOK || !strings.Contains(out, ": 2 changed · 1 merged with your edits. ") {
		t.Fatalf("apply: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got, want := readFile(t, filepath.Join(f.repo, "long.txt")), longFile(map[int]string{2: "agent edit", 18: "user edit"}); got != want {
		t.Errorf("long.txt:\n%s\nwant:\n%s", got, want)
	}
	for path, want := range map[string]string{"add.go": fxFixed, "notes.txt": "mine\n", "add_test.go": userTest} {
		if got := readFile(t, filepath.Join(f.repo, path)); got != want {
			t.Errorf("%s = %q", path, got)
		}
	}
}

// An edit since the Snapshot on the lines the Candidate changes refuses
// the whole apply, exit 2, with nothing written and nothing recorded.
func TestApplyRefusesConflictingEdits(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "other.txt"), []byte("before\n"))
	gitIn(t, f.repo, "add", "other.txt")
	gitIn(t, f.repo, "commit", "-q", "-m", "other")
	f.accepted(t, fixScript+"echo agent > other.txt\n")
	mine := "package fx\n\nfunc Add(a, b int) int { return b + a }\n"
	writeFile(t, filepath.Join(f.repo, "add.go"), []byte(mine))

	code, out, errOut := f.deliver(t, "apply")
	if code != ExitRefused {
		t.Fatalf("apply: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"not applied: your working tree changed since the Snapshot", "Nothing was written.",
		"add.go: changed in your working tree since the Snapshot, on the same lines the Candidate changes",
		"Commit, stash or undo those edits and run oge apply ",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	// other.txt would have applied cleanly; nothing is written at all.
	if got := readFile(t, filepath.Join(f.repo, "other.txt")); got != "before\n" {
		t.Errorf("other.txt was written: %q", got)
	}
	if got := readFile(t, filepath.Join(f.repo, "add.go")); got != mine {
		t.Errorf("add.go was written: %q", got)
	}
	if got := deliveries(t, f.onlyRun(t)); len(got) != 0 {
		t.Errorf("a refused apply recorded a Delivery: %v", got)
	}
}

// In a repository that checks text out with CRLF, the Snapshot and the
// Candidate hold the working tree's CRLF bytes. Delivery goes through the
// user's attributes: apply changes one line, oge diff shows one line, and
// the branch commits normalised LF blobs.
func TestDeliveryThroughEolCRLFAttributes(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, ".gitattributes"), []byte("*.txt text eol=crlf\n"))
	writeFile(t, filepath.Join(f.repo, "crlf.txt"), []byte("one\r\ntwo\r\nthree\r\n"))
	gitIn(t, f.repo, "config", "user.name", "u")
	gitIn(t, f.repo, "config", "user.email", "u@example.com")
	gitIn(t, f.repo, "add", ".gitattributes", "crlf.txt")
	gitIn(t, f.repo, "commit", "-q", "-m", "crlf")
	if got := gitOut(t, f.repo, "cat-file", "blob", "HEAD:crlf.txt"); got != "one\ntwo\nthree\n" {
		t.Fatalf("the fixture's blob isn't normalised: %q", got)
	}
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")
	id := f.accepted(t, fixScript+`printf 'one\r\nTWO\r\nthree\r\n' > crlf.txt`+"\n")

	code, patch, _ := f.deliver(t, "diff")
	if code != ExitOK || !strings.Contains(patch, "-two\r\n+TWO\r\n") || strings.Contains(patch, "-one") {
		t.Errorf("diff: exit %d\n%q", code, patch)
	}

	code, out, errOut := f.deliver(t, "branch", "crlf-branch")
	if code != ExitOK {
		t.Fatalf("branch: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := gitOut(t, f.repo, "cat-file", "blob", "crlf-branch:crlf.txt"); got != "one\nTWO\nthree\n" {
		t.Errorf("the branch committed %q, not the normalised blob", got)
	}
	if got := gitOut(t, f.repo, "diff", "--numstat", "crlf-branch~1", "crlf-branch", "--", "crlf.txt"); got != "1\t1\tcrlf.txt\n" {
		t.Errorf("the branch's change to crlf.txt: %q", got)
	}

	code, out, errOut = f.deliver(t, "apply", id)
	if code != ExitOK {
		t.Fatalf("apply: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := readFile(t, filepath.Join(f.repo, "crlf.txt")); got != "one\r\nTWO\r\nthree\r\n" {
		t.Errorf("crlf.txt = %q", got)
	}
	if got := gitOut(t, f.repo, "diff", "--numstat", "--", "crlf.txt"); got != "1\t1\tcrlf.txt\n" {
		t.Errorf("git diff after apply shows more than the one line: %q", got)
	}
	if got := deliveries(t, f.onlyRun(t)); strings.Join(got, "|") != "branch|apply" {
		t.Errorf("Delivery records: %v", got)
	}
}

// New, deleted and binary files all land, byte for byte; a directory the
// Candidate empties goes with its last file.
func TestApplyNewDeletedAndBinaryFiles(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "old", "gone.txt"), []byte("bye\n"))
	writeFile(t, filepath.Join(f.repo, "logo.bin"), []byte("\x00\x01\x02old"))
	gitIn(t, f.repo, "add", "old", "logo.bin")
	gitIn(t, f.repo, "commit", "-q", "-m", "files")
	f.statusBefore = gitOut(t, f.repo, "status", "--porcelain")
	f.accepted(t, fixScript+"rm old/gone.txt\nmkdir -p pkg\necho new > pkg/new.txt\nprintf '\\000\\001\\002new\\377' > logo.bin\nprintf '\\000blob' > fresh.bin\nchmod +x pkg/new.txt\n")

	code, patch, _ := f.deliver(t, "diff")
	for _, want := range []string{"Binary files a/logo.bin and b/logo.bin differ", "Binary files /dev/null and b/fresh.bin differ",
		"deleted file mode 100644", "new file mode 100755", "+new\n"} {
		if code != ExitOK || !strings.Contains(patch, want) {
			t.Errorf("diff lacks %q: exit %d\n%s", want, code, patch)
		}
	}

	code, out, errOut := f.deliver(t, "apply")
	if code != ExitOK || !strings.Contains(out, ": 4 changed · 1 deleted. ") {
		t.Fatalf("apply: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if _, err := os.Lstat(filepath.Join(f.repo, "old")); !os.IsNotExist(err) {
		t.Errorf("old/ is still there: %v", err)
	}
	for path, want := range map[string]string{"pkg/new.txt": "new\n", "logo.bin": "\x00\x01\x02new\xff", "fresh.bin": "\x00blob"} {
		if got := readFile(t, filepath.Join(f.repo, path)); got != want {
			t.Errorf("%s = %q", path, got)
		}
	}
	if fi, err := os.Stat(filepath.Join(f.repo, "pkg", "new.txt")); err != nil || fi.Mode()&0o100 == 0 {
		t.Errorf("pkg/new.txt lost its exec bit: %v %v", fi, err)
	}
}

// A binary file changed both since the Snapshot and by the Candidate
// can't be merged: refused.
func TestApplyRefusesABinaryChangedOnBothSides(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "logo.bin"), []byte("\x00old"))
	gitIn(t, f.repo, "add", "logo.bin")
	gitIn(t, f.repo, "commit", "-q", "-m", "bin")
	f.accepted(t, fixScript+"printf '\\000agent' > logo.bin\n")
	writeFile(t, filepath.Join(f.repo, "logo.bin"), []byte("\x00user"))
	code, _, errOut := f.deliver(t, "apply")
	if code != ExitRefused || !strings.Contains(errOut, "logo.bin: a binary file changed in your working tree since the Snapshot") {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
}

// A Rejected Candidate is delivered only with --rejected, after its
// outcome and why it isn't Accepted; never as "verified". --apply on the
// Run itself applies nothing.
func TestApplyRejectedNeedsTheRejectedFlag(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	f.attended("reject the test can't pass\n")
	code, out, errOut := f.run(t, wrongScript, "fix Add", "--fast", "--agent", "fake", "--plain", "--apply")
	if code != ExitRejected {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "--apply applies only an Accepted Candidate; this Run is Rejected, so nothing was applied.") || strings.Contains(out, "next ") {
		t.Errorf("stdout:\n%s", out)
	}
	f.assertUntouched(t)
	f.interactive, f.stdin = false, ""

	for _, args := range [][]string{{"apply"}, {"apply", "--overridden"}, {"branch"}} {
		code, out, errOut = f.deliver(t, args...)
		if code != ExitRefused {
			t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
		for _, want := range []string{"REJECTED   Candidate ", "NOT Accepted", "Öge's Check failed on Candidate ",
			`a human chose "reject" at the bound-exhaustion Gate · reason: the test can't pass`} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%v: stderr lacks %q:\n%s", args, want, errOut)
			}
		}
	}
	if !strings.Contains(errOut, "is Rejected, not Accepted; pass --rejected to deliver it anyway") {
		t.Errorf("stderr:\n%s", errOut)
	}
	f.assertUntouched(t)
	if got := deliveries(t, f.onlyRun(t)); len(got) != 0 {
		t.Errorf("Delivery records: %v", got)
	}

	code, out, errOut = f.deliver(t, "apply", "--rejected")
	if code != ExitOK {
		t.Fatalf("apply --rejected: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.HasPrefix(out, "REJECTED   Candidate ") || !strings.Contains(out, "delivering it anyway, as --rejected asks\nApplied Candidate ") {
		t.Errorf("stdout:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out+errOut), "verified") {
		t.Errorf("a Rejected delivery says verified:\n%s%s", out, errOut)
	}
	if got := readFile(t, filepath.Join(f.repo, "add.go")); got != fxWrong {
		t.Errorf("add.go = %q", got)
	}
	if got := deliveries(t, f.onlyRun(t)); strings.Join(got, "|") != "apply rejected" {
		t.Errorf("Delivery records: %v", got)
	}
}

// oge branch makes a local branch and never checks it out: HEAD, the
// index and the working tree stay. The Snapshot's untracked work is its
// own commit, so the Candidate's commit shows only the agent's change.
func TestBranchNeverChecksOut(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	gitIn(t, f.repo, "config", "user.name", "u")
	gitIn(t, f.repo, "config", "user.email", "u@example.com")
	id := f.accepted(t, fixScript)
	head := gitOut(t, f.repo, "rev-parse", "HEAD")

	code, out, errOut := f.deliver(t, "branch", id)
	if code != ExitOK {
		t.Fatalf("branch: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	name := "oge/" + id
	if !strings.HasPrefix(out, "Created branch "+name+" from Run "+id+": Candidate ") || !strings.Contains(out, "over a commit of the Snapshot's uncommitted work. Not checked out; git switch "+name) {
		t.Errorf("stdout:\n%s", out)
	}
	f.assertUntouched(t)
	if got := gitOut(t, f.repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved")
	}
	if got := gitOut(t, f.repo, "symbolic-ref", "HEAD"); got != "refs/heads/main\n" {
		t.Errorf("HEAD is %s", got)
	}
	if got := gitOut(t, f.repo, "diff", "--name-only", name+"~2", name+"~1"); got != "add_test.go\n" {
		t.Errorf("the Snapshot commit changes %q", got)
	}
	if got := gitOut(t, f.repo, "diff", "--name-only", name+"~1", name); got != "add.go\n" {
		t.Errorf("the Candidate commit changes %q", got)
	}
	if got := gitOut(t, f.repo, "rev-parse", name+"~2"); got != head {
		t.Errorf("the branch isn't on the Snapshot's HEAD")
	}
	if msg := gitOut(t, f.repo, "log", "-1", "--format=%B", name); !strings.Contains(msg, "fix Add") || !strings.Contains(msg, "Öge Run "+id+" · Accepted") {
		t.Errorf("commit message:\n%s", msg)
	}

	// The name order doesn't matter; an existing branch is refused.
	code, _, errOut = f.deliver(t, "branch", name, id)
	if code != ExitRefused || !strings.Contains(errOut, "branch "+name+" already exists") {
		t.Errorf("exit %d: %s", code, errOut)
	}
	code, out, _ = f.deliver(t, "branch", id, "mine")
	if code != ExitOK || !strings.Contains(out, "Created branch mine from Run ") {
		t.Errorf("exit %d: %s", code, out)
	}
	if got := deliveries(t, f.onlyRun(t)); strings.Join(got, "|") != "branch|branch" {
		t.Errorf("Delivery records: %v", got)
	}
}

// --apply applies an Accepted Candidate at the end of the Run.
func TestRunApplyFlagAppliesAnAcceptedCandidate(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended", "--apply")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if strings.Contains(out, "Nothing was written to your repository.") || !strings.Contains(out, "ACCEPTED") ||
		!strings.HasSuffix(out, ": 1 changed. Nothing was committed or staged.\n") || strings.Contains(out, "next ") {
		t.Errorf("stdout:\n%s", out)
	}
	if got := readFile(t, filepath.Join(f.repo, "add.go")); got != fxFixed {
		t.Errorf("add.go = %q", got)
	}
	if got := deliveries(t, f.onlyRun(t)); strings.Join(got, "|") != "apply" {
		t.Errorf("Delivery records: %v", got)
	}
}

// oge diff prints the exact patch without a terminal, and a coloured one
// with a header on a terminal. Viewing records nothing.
func TestDiffPlainAndOnATerminal(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	id := f.accepted(t, fixScript)
	code, out, errOut := f.deliver(t, "diff")
	if code != ExitOK || !strings.HasPrefix(out, "diff --git a/add.go b/add.go\n") || strings.ContainsRune(out, 0x1b) ||
		!strings.Contains(out, "-func Add(a, b int) int { return 0 }\n+func Add(a, b int) int { return a + b }\n") {
		t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	f.interactive = true
	f.setenv("TERM", "xterm-256color")
	f.setenv("NO_COLOR", "")
	code, out, _ = f.deliver(t, "diff", id)
	if code != ExitOK || !strings.HasPrefix(out, "\x1b[") || !strings.Contains(out, "Run "+id+" · Accepted · Candidate ") ||
		!strings.Contains(out, "\x1b[32m+func Add(a, b int) int { return a + b }") {
		t.Errorf("exit %d\n%q", code, out)
	}
	if got := deliveries(t, f.onlyRun(t)); len(got) != 0 {
		t.Errorf("oge diff recorded a Delivery: %v", got)
	}

	f.interactive = false
	code, _, errOut = f.deliver(t, "diff", "19990101T000000-abcdef")
	if code != ExitRefused || !strings.Contains(errOut, "no Run 19990101T000000-abcdef") {
		t.Errorf("exit %d: %s", code, errOut)
	}
}

// An unattended Run never waits on a key, even on a terminal that draws
// the live view: it prints the commands instead of the action bar.
func TestUnattendedRunOnATerminalOffersNoActionBar(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.interactive = true
	f.setenv("TERM", "xterm-256color")
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || !strings.Contains(out, "next       oge apply ") || strings.Contains(out, "[a] apply") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// Whenever oge diff writes to a terminal, even with --plain or a dumb
// TERM, the agent's bytes can't reach it raw: no control characters, no
// bidi overrides. Without a terminal the patch stays exact.
func TestDiffToATerminalIsAlwaysCleaned(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.accepted(t, fixScript+`printf 'title \033]0;pwned\007 \342\200\256evil\n' > note.txt`+"\n")
	f.interactive = true
	for _, c := range []struct {
		term string
		args []string
	}{{"dumb", []string{"diff"}}, {"xterm-256color", []string{"diff", "--plain"}}, {"", []string{"diff"}}} {
		f.setenv("TERM", c.term)
		code, out, errOut := f.deliver(t, c.args...)
		if code != ExitOK || !strings.Contains(out, "+title") || strings.ContainsAny(out, "\x1b\x07\u202e") {
			t.Errorf("TERM=%q %v: exit %d\n%q\n%s", c.term, c.args, code, out, errOut)
		}
	}
	f.interactive = false
	if _, out, _ := f.deliver(t, "diff"); !strings.Contains(out, "\x1b]0;pwned\x07") {
		t.Errorf("the plain patch isn't exact:\n%q", out)
	}
}

// overrideRun rewrites a finished Run's Ledger so it ended Overridden, as
// an override at a Gate would (no Gate offers one yet).
func overrideRun(t *testing.T, runDir string) {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(runDir, ledger.LedgerFile)); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Create(runDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, r := range recs {
		var data any = r.Data
		switch r.Type {
		case run.RecRunEnded:
			var d map[string]any
			json.Unmarshal(r.Data, &d)
			d["outcome"] = run.Overridden
			data = d
		}
		if err := l.Append(r.Type, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Append(run.RecGateDecided, map[string]any{"pins": map[string]any{"gate": "gate.bound_exhaustion"},
		"actor": "human", "choice": "override", "reason": "the flaky test is wrong"}); err != nil {
		t.Fatal(err)
	}
}

// An Overridden Candidate is delivered only with --overridden, after its
// outcome and why; --rejected names the wrong outcome.
func TestApplyOverriddenNeedsTheOverriddenFlag(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.accepted(t, fixScript)
	overrideRun(t, f.onlyRun(t))
	for _, args := range [][]string{{"apply"}, {"apply", "--rejected"}} {
		code, out, errOut := f.deliver(t, args...)
		if code != ExitRefused || !strings.Contains(errOut, "OVERRIDDEN Candidate ") || !strings.Contains(errOut, `a human chose "override"`) {
			t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
	}
	f.assertUntouched(t)
	code, out, errOut := f.deliver(t, "apply", "--overridden")
	if code != ExitOK || !strings.HasPrefix(out, "OVERRIDDEN Candidate ") || !strings.Contains(out, "as --overridden asks\nApplied Candidate ") ||
		strings.Contains(strings.ToLower(out), "verified") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := deliveries(t, f.onlyRun(t)); strings.Join(got, "|") != "apply overridden" {
		t.Errorf("Delivery records: %v", got)
	}
}
