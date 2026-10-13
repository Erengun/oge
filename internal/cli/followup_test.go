package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
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
	script := verifierThen(negTest+`echo "exit: extended"`, strings.TrimPrefix(followScript, recordTurn)+fixScript)
	s := startHomeSessionWith(t, f, func(*Env) {
		writeFile(t, filepath.Join(filepath.Dir(f.repo), "agent.sh"), []byte(script))
	}, "--agent", "fake")
	s.composer()
	s.type_(standardTask + "\r")
	s.expect("ÖGE RECEIPT")
	s.expect("(/new for a fresh task)")
	s.type_("add Sub too\r")
	s.expect("Continues     Run ")
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
	}
}

// readTree lists the files of rev in a Run's repository.
func readTree(runDir, rev string) (string, error) {
	out, err := exec.Command("git", "--git-dir", filepath.Join(runDir, "repo.git"), "ls-tree", "-r", "--name-only", rev).CombinedOutput()
	return string(out), err
}
