package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

// followScript fixes Add; briefed with a follow-up, it adds Sub to add.go
// only if Add is fixed there already (the first Candidate's, if the
// follow-up continues it).
const followScript = recordTurn + `if [ "$OGE_FAKE_ROLE" = implementer ] && echo "$OGE_FAKE_TURN" | grep -q 'Human follow-up 1 of 1'; then
grep -q 'a + b' add.go && printf '\nfunc Sub(a, b int) int { return a - b }\n' >> add.go
echo "exit: done"
exit 0
fi
`

// followRun is what a test reads of one Run's Ledger.
type followRun struct {
	dir, id, follows, snapshot, start, candidate string
	notes                                        []string
}

func readFollowRun(t *testing.T, dir string) followRun {
	t.Helper()
	recs, err := ledger.Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := followRun{dir: dir}
	for _, rec := range recs {
		var d struct {
			Run, Follows, Commit, Candidate string
			StartRevision                   string   `json:"start_revision"`
			FollowUps                       []string `json:"follow_ups"`
			Role                            string
		}
		if err := json.Unmarshal(rec.Data, &d); err != nil {
			t.Fatal(err)
		}
		switch rec.Type {
		case run.RecRunStarted:
			r.id, r.follows, r.notes = d.Run, d.Follows, d.FollowUps
		case run.RecSnapshotTaken:
			r.snapshot = d.Commit
		case run.RecAttemptStarting:
			if d.Role == "implementer" && r.start == "" {
				r.start = d.StartRevision
			}
		case run.RecRunEnded:
			r.candidate = d.Candidate
		}
	}
	return r
}

// followRuns reads every Run of the fixture, in the order they followed
// one another when they did.
func followRuns(t *testing.T, f *runFixture) map[string]followRun {
	t.Helper()
	runs := map[string]followRun{}
	for _, d := range runDirs(t, f) {
		r := readFollowRun(t, d)
		runs[r.id] = r
	}
	return runs
}

// A message after a Run continues it (#185): a new Run on a fresh Snapshot
// whose implementer starts from the first Run's Candidate, briefed with the
// Task and the follow-up. /new ends the thread: the next Run starts from its
// Snapshot.
func TestBareOgeFollowUpContinuesTheCandidate(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSessionWith(t, f, func(*Env) {
		writeFile(t, filepath.Join(filepath.Dir(f.repo), "agent.sh"), []byte(followScript+fixScript))
	}, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.expect("ÖGE RECEIPT")
	s.expect("(/new for a fresh task)")
	s.type_("add Sub too\r")
	s.expect("> add Sub too")
	s.expect("Continues     Run ")
	s.expect("(/new for a fresh task)")
	runs := followRuns(t, f)
	if len(runs) != 2 {
		t.Fatalf("%d Runs", len(runs))
	}
	var first, second followRun
	for _, r := range runs {
		if r.follows == "" {
			first = r
		} else {
			second = r
		}
	}
	if second.follows != first.id || first.candidate == "" {
		t.Fatalf("the second Run follows %q, not the first (%q)", second.follows, first.id)
	}
	if second.start != first.candidate || second.start == second.snapshot {
		t.Errorf("the follow-up started from %s, not the first Candidate %s (its Snapshot %s)", second.start, first.candidate, second.snapshot)
	}
	if strings.Join(second.notes, "|") != "add Sub too" {
		t.Errorf("RunStarted follow_ups %q", second.notes)
	}
	turn := f.out(t, "implementer-2.turn")
	for _, want := range []string{"fix Add", "Human follow-up 1 of 1", "add Sub too"} {
		if !strings.Contains(turn, want) {
			t.Errorf("the follow-up's Briefing lacks %q:\n%s", want, turn)
		}
	}
	if files, _ := exec.Command("git", "--git-dir", filepath.Join(second.dir, "repo.git"), "show", second.candidate+":add.go").CombinedOutput(); !strings.Contains(string(files), "a + b") || !strings.Contains(string(files), "func Sub") {
		t.Errorf("the follow-up's Candidate doesn't build on the first:\n%s", files)
	}
	if !strings.Contains(s.out.String(), "Continues     Run "+first.id) {
		t.Errorf("no Continues line naming %s", first.id)
	}

	s.type_("/new\r")
	s.composer()
	s.type_("fix Add\r")
	s.expect("ÖGE RECEIPT")
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	runs = followRuns(t, f)
	if len(runs) != 3 {
		t.Fatalf("%d Runs", len(runs))
	}
	for id, r := range runs {
		if id == first.id || id == second.id {
			continue
		}
		if r.follows != "" || r.start != r.snapshot {
			t.Errorf("after /new the Run follows %q and starts from %s, not its Snapshot %s", r.follows, r.start, r.snapshot)
		}
	}
}

// A follow-up's implementer never sees the previous Run's held-out tests:
// they aren't in the Candidate it starts from, its Workspace or its
// Briefing; and the follow-up gets a fresh QA pass of its own (ADR-0009).
func TestBareOgeFollowUpKeepsHeldOutTestsOut(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	objects := "git rev-list --objects --all >> \"$OGE_TEST_OUT/objects-of-implementers\"\n"
	script := verifierThen(negTest+`echo "exit: extended"`, objects+strings.TrimPrefix(followScript, recordTurn)+fixScript)
	s := startHomeSessionWith(t, f, func(*Env) {
		writeFile(t, filepath.Join(filepath.Dir(f.repo), "agent.sh"), []byte(script))
	}, "--agent", "fake")
	s.composer()
	s.type_(standardTask + "\r")
	s.expect("ÖGE RECEIPT")
	s.expect("(/new for a fresh task)")
	// A user edit between Runs: the follow-up rebases (#185).
	writeFile(t, filepath.Join(f.repo, "notes.txt"), []byte("the user's notes\n"))
	s.type_("add Sub too\r")
	s.expect("· rebased onto the current repository")
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.repo), "out", "leak")); err == nil {
		t.Error("an implementer's Workspace held the held-out test")
	}
	for _, name := range []string{"implementer-1.turn", "implementer-2.turn"} {
		if strings.Contains(f.out(t, name), heldOutMarker) {
			t.Errorf("%s holds the held-out test", name)
		}
	}
	if !strings.Contains(f.out(t, "implementer-2.turn"), "Human follow-up 1 of 1") {
		t.Error("the second implementer wasn't briefed as a follow-up")
	}
	f.out(t, "verifier-2.turn") // the follow-up's own, fresh QA pass
	for _, r := range followRuns(t, f) {
		if r.follows == "" {
			continue
		}
		files, err := readTree(r.dir, r.start)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(files, "neg_test.go") {
			t.Errorf("the follow-up's start revision holds the held-out test:\n%s", files)
		}
		out, err := exec.Command("git", "--git-dir", filepath.Join(r.dir, "repo.git"), "rev-list", "--objects", "--all").CombinedOutput()
		if err != nil || strings.Contains(string(out), "neg_test.go") {
			t.Errorf("the follow-up's Run repository reaches the held-out test (%v):\n%s", err, out)
		}
	}
	// Every implementer Workspace's .git, the follow-up's too.
	if objects := f.out(t, "objects-of-implementers"); !strings.Contains(objects, "add.go") || strings.Contains(objects, "neg_test.go") {
		t.Errorf("an implementer Workspace's .git reaches the held-out test, or listed nothing:\n%s", objects)
	}
}

// readTree lists the files of rev in a Run's repository.
func readTree(runDir, rev string) (string, error) {
	out, err := exec.Command("git", "--git-dir", filepath.Join(runDir, "repo.git"), "ls-tree", "-r", "--name-only", rev).CombinedOutput()
	return string(out), err
}

// waitFor polls path until it holds want.
func waitFor(t *testing.T, path, want string) string {
	t.Helper()
	var got []byte
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got, _ = os.ReadFile(path); strings.Contains(string(got), want) {
			return string(got)
		}
	}
	t.Fatalf("%s never held %q:\n%s", path, want, got)
	return ""
}

// The user changed the repository between Runs, apart from the Candidate's
// change (#185): the follow-up rebases the first Candidate onto the new
// Snapshot, says so in its Receipt, and /apply keeps both.
func TestBareOgeFollowUpRebasesOntoTheUsersChange(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSessionWith(t, f, func(*Env) {
		writeFile(t, filepath.Join(filepath.Dir(f.repo), "agent.sh"), []byte(followScript+fixScript))
	}, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	writeFile(t, filepath.Join(f.repo, "notes.txt"), []byte("the user's notes\n"))
	s.type_("add Sub too\r")
	s.expect("· rebased onto the current repository")
	s.ended()
	s.type_("/apply\r")
	add := waitFor(t, filepath.Join(f.repo, "add.go"), "func Sub")
	if !strings.Contains(add, "a + b") {
		t.Errorf("add.go after /apply:\n%s", add)
	}
	if got := readFile(t, filepath.Join(f.repo, "notes.txt")); got != "the user's notes\n" {
		t.Errorf("the user's edit is lost: %q", got)
	}
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	for _, r := range followRuns(t, f) {
		if r.follows != "" && (r.start == r.candidate || r.start == "") {
			t.Errorf("the follow-up started from %q", r.start)
		}
	}
}

// The user changed what the Candidate changed: the follow-up is refused
// before any agent starts, and the thread stays: once the user undoes the
// change, the next message continues the same Run.
func TestBareOgeFollowUpRefusesAConflictingChange(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSessionWith(t, f, func(*Env) {
		writeFile(t, filepath.Join(filepath.Dir(f.repo), "agent.sh"), []byte(followScript+fixScript))
	}, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	writeFile(t, filepath.Join(f.repo, "add.go"), []byte("package fx\n\nfunc Add(a, b int) int { return b + a }\n"))
	s.type_("add Sub too\r")
	s.expect("in a way that conflicts with its result; /new for a fresh task")
	s.composer()
	if screen := s.settledScreen(); !strings.Contains(screen, "continuing Run ") || !strings.Contains(screen, "(/new for a fresh task)") {
		t.Errorf("refused follow-up lost its continuation hint:\n%s", screen)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.repo), "out", "implementer-2.turn")); err == nil {
		t.Error("an implementer started on the refused follow-up")
	}
	writeFile(t, filepath.Join(f.repo, "add.go"), []byte(fxBroken))
	s.type_("add Sub too\r")
	s.expect("Continues     Run ")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	runs := followRuns(t, f)
	if len(runs) != 3 {
		t.Fatalf("%d Runs", len(runs))
	}
	var first string
	for id, r := range runs {
		if r.follows == "" {
			first = id
		}
	}
	for id, r := range runs {
		if id != first && r.follows != first {
			t.Errorf("Run %s follows %q, not %s", id, r.follows, first)
		}
		if id != first && r.candidate != "" && r.start != runs[first].candidate {
			t.Errorf("the resent follow-up started from %s, not %s", r.start, runs[first].candidate)
		}
	}
}

// Which Runs the next message continues (#185): one with a Candidate that
// was Cancelled or stopped by infrastructure too; not one a human rejected
// or applied; and a refused follow-up keeps the thread it was to continue.
func TestNextThread(t *testing.T) {
	prev := &thread{from: run.FollowUp{Run: "r0"}}
	ended := func(o run.Outcome, candidate string) *run.Result {
		return &run.Result{ID: "r1", Dir: "d", Outcome: o, Candidate: candidate, Snapshot: "s"}
	}
	for _, c := range []struct {
		res  *run.Result
		f    runFlags
		want string
	}{
		{ended(run.Accepted, "c"), runFlags{}, "r1"},
		{ended(run.Cancelled, "c"), runFlags{}, "r1"},
		{ended(run.InfrastructureStop, "c"), runFlags{}, "r1"},
		{ended(run.Cancelled, ""), runFlags{}, ""},
		{ended(run.Rejected, "c"), runFlags{}, ""},
		{ended(run.Accepted, "c"), runFlags{apply: true}, ""},
		{ended(run.Accepted, "c"), runFlags{unattended: true}, ""},
		{ended(run.Refused, ""), runFlags{}, "r0"},
	} {
		got := ""
		if th := nextThread(prev, task.Task{}, c.res, c.f); th != nil {
			got = th.from.Run
		}
		if got != c.want {
			t.Errorf("%s (candidate %q, %+v): continues %q, want %q", c.res.Outcome, c.res.Candidate, c.f, got, c.want)
		}
	}
}

// A human Reject at a Gate ends the thread (#185): the next message is a
// fresh Task.
func TestBareOgeRejectEndsTheThread(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake", "--confirm")
	s.composer()
	s.type_("fix Add\r")
	s.expect("Result gate")
	s.type_("reject not this\r")
	s.expect(`you chose "reject"`)
	s.composer()
	if out := s.out.String(); strings.Contains(out[s.mark:], "continuing Run") {
		t.Errorf("a Rejected Run is continued:\n%s", out[s.mark:])
	}
	s.type_("fix Add again\r")
	s.expect("Result gate")
	s.type_("t\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	runs := followRuns(t, f)
	if len(runs) != 2 {
		t.Fatalf("%d Runs", len(runs))
	}
	for _, r := range runs {
		if r.follows != "" {
			t.Errorf("Run %s follows %s after a Reject", r.id, r.follows)
		}
	}
}

// The user protected a path the first Candidate edited: the inherited edit
// would be reverted as the follow-up's Tamper, so the follow-up is refused
// before any agent starts, with no Tamper event (#185).
func TestBareOgeFollowUpRefusesAnInheritedProtectedEdit(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSessionWith(t, f, func(*Env) {
		writeFile(t, filepath.Join(filepath.Dir(f.repo), "agent.sh"), []byte(followScript+fixScript))
	}, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	cfg := strings.Replace(fxConfig, "[project]\n", "[project]\ntest_config = [\"add.go\"]\n", 1)
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	s.type_("add Sub too\r")
	s.expect("its result now touches protected add.go; /new for a fresh task")
	s.composer()
	s.type_("\x03")
	if code := s.wait(); code != ExitRefused {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.repo), "out", "implementer-2.turn")); err == nil {
		t.Error("an implementer started on the refused follow-up")
	}
	for _, d := range runDirs(t, f) {
		recs, err := ledger.Replay(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, rec := range recs {
			if rec.Type == run.RecTamperEvent || rec.Type == run.RecScopeReverted && strings.Contains(string(rec.Data), "add.go") {
				t.Errorf("%s: %s %s", filepath.Base(d), rec.Type, rec.Data)
			}
		}
	}
}
