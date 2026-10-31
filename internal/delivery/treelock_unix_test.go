//go:build unix

package delivery

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// sameRepoRuns is a user repository and two Accepted Runs of it, under one
// private state dir, as two Chats on one repository have: both take their
// Snapshot before either applies.
func sameRepoRuns(t *testing.T, files map[string]string, a, b func(ws string)) (user string, ra, rb *Run) {
	t.Helper()
	base := t.TempDir()
	user = filepath.Join(base, "user")
	for rel, s := range files {
		p := filepath.Join(user, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "init", "-q", user).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	runOf := func(id string, change func(ws string)) *Run {
		dir := filepath.Join(base, "private", "runs", id)
		repo, err := workspace.InitRunRepo(filepath.Join(dir, "repo.git"))
		if err != nil {
			t.Fatal(err)
		}
		ws := filepath.Join(base, "ws-"+id)
		snap, _, err := repo.TakeSnapshot(user, ws)
		if err != nil {
			t.Fatal(err)
		}
		change(ws)
		l, err := ledger.Create(dir)
		if err != nil {
			t.Fatal(err)
		}
		l.Close()
		cand, err := repo.CommitCandidate(ws, snap, "refs/oge/c1", "c1")
		if err != nil {
			t.Fatal(err)
		}
		return &Run{ID: id, Dir: dir, Source: user, Snapshot: snap, Candidate: cand, Outcome: run.Accepted}
	}
	return user, runOf("ra", a), runOf("rb", b)
}

func edit(rel, s string) func(ws string) {
	return func(ws string) { os.WriteFile(filepath.Join(ws, rel), []byte(s), 0o644) }
}

func read(t *testing.T, user, rel string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(user, rel))
	return string(b)
}

type result struct {
	a   *Applied
	err error
}

// race applies ra and then rb to user, forced to interleave: ra stops
// between its plan and its write until rb has started and had time to
// plan. During rb's plan, check runs.
func race(t *testing.T, user string, ra, rb *Run, check func()) (result, result) {
	t.Helper()
	planned, release := make(chan struct{}), make(chan struct{})
	betweenPlanAndWrite = func(r *Run) {
		switch r.ID {
		case ra.ID:
			close(planned)
			<-release
		case rb.ID:
			check()
		}
	}
	t.Cleanup(func() { betweenPlanAndWrite = nil })
	da, db := make(chan result, 1), make(chan result, 1)
	go func() {
		a, err := ApplyOnto(ra, user, Choice{}, Base{})
		da <- result{a, err}
	}()
	<-planned
	go func() {
		a, err := ApplyOnto(rb, user, Choice{}, Base{})
		db <- result{a, err}
	}()
	time.Sleep(200 * time.Millisecond) // rb would plan now, unlocked
	close(release)
	return <-da, <-db
}

const tenLines = "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"

// Two applies to one working tree take turns: the second plans on what the
// first wrote, so both edits land and the second merges three-way.
func TestTreeLockSerialisesTwoApplies(t *testing.T) {
	user, ra, rb := sameRepoRuns(t, map[string]string{"a.txt": tenLines},
		edit("a.txt", strings.Replace(tenLines, "1\n", "ONE\n", 1)),
		edit("a.txt", strings.Replace(tenLines, "10\n", "TEN\n", 1)))
	resA, resB := race(t, user, ra, rb, func() {
		if !strings.HasPrefix(read(t, user, "a.txt"), "ONE\n") {
			t.Error("the second apply planned before the first wrote")
		}
	})
	if resA.err != nil || resB.err != nil {
		t.Fatalf("a: %v, b: %v", resA.err, resB.err)
	}
	if got, want := read(t, user, "a.txt"), "ONE\n2\n3\n4\n5\n6\n7\n8\n9\nTEN\n"; got != want {
		t.Errorf("a.txt is %q, want %q", got, want)
	}
	if resB.a.Plan.Merged != 1 {
		t.Errorf("second apply merged %d path(s), want 1", resB.a.Plan.Merged)
	}
}

// With the same lines edited by both, the second apply is refused and the
// tree holds exactly what the first wrote: nothing of the second's lands.
func TestTreeLockRefusesAnOverlappingSecondApply(t *testing.T) {
	files := map[string]string{"a.txt": tenLines, "b.txt": "b\n"}
	user, ra, rb := sameRepoRuns(t, files,
		edit("a.txt", strings.Replace(tenLines, "5\n", "FIVE\n", 1)),
		func(ws string) {
			edit("a.txt", strings.Replace(tenLines, "5\n", "five\n", 1))(ws)
			edit("b.txt", "B\n")(ws)
		})
	resA, resB := race(t, user, ra, rb, func() { t.Error("the overlapping apply reached its write") })
	if resA.err != nil {
		t.Fatal(resA.err)
	}
	var conflict *ConflictError
	if !errors.As(resB.err, &conflict) {
		t.Fatalf("second apply: %v, want a conflict", resB.err)
	}
	if got, want := read(t, user, "a.txt"), strings.Replace(tenLines, "5\n", "FIVE\n", 1); got != want {
		t.Errorf("a.txt is %q, want the first apply's %q", got, want)
	}
	if got := read(t, user, "b.txt"); got != "b\n" {
		t.Errorf("b.txt is %q: the refused apply wrote", got)
	}
}

// An apply that can't get the working tree in time is refused in plain
// words, and writes nothing. The lock lives in the private state dir.
func TestTreeLockRefusesAfterWaiting(t *testing.T) {
	user, ra, _ := sameRepoRuns(t, map[string]string{"a.txt": "a\n"}, edit("a.txt", "A\n"), func(string) {})
	p := treeLockPath(ra, user)
	if !strings.HasPrefix(p, filepath.Dir(filepath.Dir(ra.Dir))+string(filepath.Separator)) || strings.HasPrefix(p, user) {
		t.Fatalf("lock at %s, want it in the private state dir", p)
	}
	held, err := lockTree(ra, user)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	old := treeLockWait
	treeLockWait = 100 * time.Millisecond
	t.Cleanup(func() { treeLockWait = old })
	_, err = ApplyOnto(ra, user, Choice{}, Base{})
	if !IsRefused(err) || err.Error() != treeBusy {
		t.Fatalf("err %v, want %q", err, treeBusy)
	}
	if got := read(t, user, "a.txt"); got != "a\n" {
		t.Errorf("a.txt is %q: written without the lock", got)
	}
	if _, err := os.Stat(filepath.Join(ra.Dir, "delivery.lock")); !os.IsNotExist(err) {
		t.Errorf("the Run's lock was taken before the tree's: %v", err)
	}
}

// The lock dies with its holder's file: closed, the next apply takes it.
func TestTreeLockIsReleased(t *testing.T) {
	user, ra, _ := sameRepoRuns(t, map[string]string{"a.txt": "a\n"}, edit("a.txt", "A\n"), func(string) {})
	held, err := lockTree(ra, user)
	if err != nil {
		t.Fatal(err)
	}
	held()
	f, err := os.Open(treeLockPath(ra, user))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("still locked: %v", err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if _, err := ApplyOnto(ra, user, Choice{}, Base{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, user, "a.txt"); got != "A\n" {
		t.Errorf("a.txt is %q", got)
	}
}
