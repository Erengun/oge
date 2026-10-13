package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// RunStarted names the Run a follow-up continues and the human's messages;
// a fresh Run's names neither (#185).
func TestRunStartedFollows(t *testing.T) {
	fresh := map[string]any{}
	addFollows(fresh, Params{})
	if len(fresh) != 0 {
		t.Errorf("a fresh Run's RunStarted has %v", fresh)
	}
	begun := map[string]any{}
	addFollows(begun, Params{Follows: &FollowUp{Run: "r1"}, Task: task.Task{FollowUps: []string{"add Sub too"}}})
	if begun["follows"] != "r1" || !reflect.DeepEqual(begun["follow_ups"], []string{"add Sub too"}) {
		t.Errorf("RunStarted %v", begun)
	}
}

// followFixture is a first Run whose Candidate appends TestZero to the
// Oracle test file and fixes add.go, and the user's repository.
type followFixture struct {
	user string
	from FollowUp
}

func newFollowFixture(t *testing.T) *followFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	f := &followFixture{user: t.TempDir()}
	f.git(t, "init", "-q", "-b", "main")
	f.write(t, "add.go", "package fx\n\nfunc Add(a, b int) int { return a - b }\n")
	f.write(t, "add_test.go", additiveBase)
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "init")
	dir := t.TempDir()
	repo, err := workspace.InitRunRepo(filepath.Join(dir, "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(t.TempDir(), "ws")
	snap, _, err := repo.TakeSnapshot(f.user, ws)
	if err != nil {
		t.Fatal(err)
	}
	writeIn(t, ws, "add.go", "package fx\n\nfunc Add(a, b int) int { return a + b }\n")
	writeIn(t, ws, "add_test.go", additiveBase+testZero)
	c, err := repo.CommitCandidate(ws, snap, "refs/oge/candidate", "c1")
	if err != nil {
		t.Fatal(err)
	}
	f.from = FollowUp{Run: "r1", Dir: dir, Candidate: c, Snapshot: snap}
	return f
}

func (f *followFixture) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.user, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (f *followFixture) write(t *testing.T, path, s string) { writeIn(t, f.user, path, s) }

func writeIn(t *testing.T, dir, path, s string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// follow takes the second Run's Snapshot of the user's repository and
// where its implementer starts.
func (f *followFixture) follow(t *testing.T) (repo *workspace.RunRepo, snap, start string, rebased bool, refusal string) {
	t.Helper()
	repo, err := workspace.InitRunRepo(filepath.Join(t.TempDir(), "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err = repo.TakeSnapshot(f.user, filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatal(err)
	}
	start, rebased, refusal, err = followUpStart(repo, &f.from, snap)
	if err != nil {
		t.Fatal(err)
	}
	return repo, snap, start, rebased, refusal
}

// An unchanged repository: the implementer starts from the first Candidate
// itself.
func TestFollowUpStartsFromTheCandidate(t *testing.T) {
	f := newFollowFixture(t)
	_, _, start, rebased, refusal := f.follow(t)
	if start != f.from.Candidate || rebased || refusal != "" {
		t.Errorf("start %s rebased=%v refusal %q, want the Candidate %s", start, rebased, refusal, f.from.Candidate)
	}
}

// The user edited the Oracle test file between Runs: the Candidate is
// rebased onto the new Snapshot, keeping both edits, and its kept test
// addition is still additive against that Snapshot, not a Tamper.
func TestFollowUpRebasesOntoAChangedRepository(t *testing.T) {
	f := newFollowFixture(t)
	edited := strings.Replace(additiveBase, `t.Fatal("Sub")`, `t.Fatal("Sub(3, 2) != 1")`, 1)
	f.write(t, "add_test.go", edited)
	f.write(t, "README", "user's\n")
	repo, snap, start, rebased, refusal := f.follow(t)
	if !rebased || refusal != "" || start == f.from.Candidate {
		t.Fatalf("start %s rebased=%v refusal %q", start, rebased, refusal)
	}
	if out, err := exec.Command("git", "--git-dir", repo.Dir, "rev-parse", start+"^").Output(); err != nil || strings.TrimSpace(string(out)) != snap {
		t.Errorf("the start revision's parent is %s, not the Snapshot %s (%v)", out, snap, err)
	}
	test, _, _ := repo.Show(start, "add_test.go")
	if string(test) != edited+testZero {
		t.Errorf("rebased add_test.go:\n%s\nwant the user's edit and TestZero", test)
	}
	if add, _, _ := repo.Show(start, "add.go"); !strings.Contains(string(add), "a + b") {
		t.Errorf("the Candidate's fix is lost:\n%s", add)
	}
	if readme, ok, _ := repo.Show(start, "README"); !ok || string(readme) != "user's\n" {
		t.Errorf("the user's new file is lost: %q", readme)
	}
	before, _, _ := repo.Show(snap, "add_test.go")
	if added, ok := additiveTestEdit("add_test.go", before, test); !ok || !reflect.DeepEqual(added, []string{"TestZero"}) {
		t.Errorf("against the new Snapshot, the kept addition is additive=%v %v", ok, added)
	}
}

// The user changed what the Candidate changed: the follow-up is refused.
func TestFollowUpConflictIsRefused(t *testing.T) {
	f := newFollowFixture(t)
	f.write(t, "add.go", "package fx\n\nfunc Add(a, b int) int { return b + a }\n")
	_, _, start, rebased, refusal := f.follow(t)
	if refusal != "your repository changed since Run r1 in a way that conflicts with its result; /new for a fresh task" || start != "" || rebased {
		t.Errorf("start %q rebased=%v refusal %q, want a conflict", start, rebased, refusal)
	}
}

// A git without merge-tree --merge-base can't rebase: the follow-up is
// refused, saying so; an unchanged repository needs no rebase and goes on.
func TestFollowUpOnOldGitIsRefused(t *testing.T) {
	f := newFollowFixture(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeIn(t, bin, "git", "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'git version 2.39.5'; exit 0; fi\nexec '"+real+"' \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, _, start, _, refusal := f.follow(t); start != f.from.Candidate || refusal != "" {
		t.Errorf("unchanged repository on old git: start %q refusal %q", start, refusal)
	}
	f.write(t, "README", "user's\n")
	_, _, start, _, refusal := f.follow(t)
	if refusal != "continuing Run r1 needs git 2.40 or newer to rebase onto your changed repository; /new for a fresh task" || start != "" {
		t.Errorf("start %q refusal %q", start, refusal)
	}
}
