package delivery

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/workspace"
)

// planFixture is a user repository, its Snapshot in a Run repository, and
// a Candidate in which change has edited the Workspace.
func planFixture(t *testing.T, files map[string]string, change func(ws string)) (user string, r *Run) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	base := t.TempDir()
	user = filepath.Join(base, "user")
	for rel, s := range files {
		p := filepath.Join(user, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "init", "-q", user)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	dir := filepath.Join(base, "run")
	repo, err := workspace.InitRunRepo(filepath.Join(dir, "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "ws")
	snap, _, err := repo.TakeSnapshot(user, ws)
	if err != nil {
		t.Fatal(err)
	}
	change(ws)
	cand, err := repo.CommitCandidate(ws, snap, "refs/oge/c1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	return user, &Run{ID: "r1", Dir: dir, Source: user, Snapshot: snap, Candidate: cand}
}

// A directory replaced by a symlink since the Snapshot is never written
// through: the write could land outside the repository.
func TestPlanApplyNeverWritesThroughASymlinkedDirectory(t *testing.T) {
	user, r := planFixture(t, map[string]string{"dir/f.txt": "one\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, "dir", "f.txt"), []byte("two\n"), 0o644)
	})
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "f.txt"), []byte("one\n"), 0o644)
	os.RemoveAll(filepath.Join(user, "dir"))
	if err := os.Symlink(outside, filepath.Join(user, "dir")); err != nil {
		t.Fatal(err)
	}
	p, err := PlanApply(r, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Conflicts) != 1 || !strings.Contains(p.Conflicts[0], "dir is no longer a directory") || p.Writes() != 0 {
		t.Fatalf("plan: %+v", p)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "f.txt")); string(b) != "one\n" {
		t.Errorf("written outside the repository: %q", b)
	}
}

// A file deleted since the Snapshot that the Candidate changes, and one
// the Candidate deletes that was changed since, both conflict.
func TestPlanApplyDeleteAgainstChange(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
		os.Remove(filepath.Join(ws, "b.txt"))
	})
	os.Remove(filepath.Join(user, "a.txt"))
	os.WriteFile(filepath.Join(user, "b.txt"), []byte("mine\n"), 0o644)
	p, err := PlanApply(r, user)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Conflicts, "\n")
	for _, want := range []string{"a.txt: deleted in your working tree since the Snapshot, and the Candidate changes it",
		"b.txt: changed in your working tree since the Snapshot, and the Candidate deletes it"} {
		if !strings.Contains(got, want) {
			t.Errorf("conflicts lack %q:\n%s", want, got)
		}
	}
}
