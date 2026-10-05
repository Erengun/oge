package delivery

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
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
	l, err := ledger.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
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

// Paths that differ only in case or Unicode normalisation are one file
// on many filesystems: applying both could delete the one written.
func TestPlanApplyRefusesPathsThatFoldEqual(t *testing.T) {
	user, r := planFixture(t, map[string]string{"Readme.txt": "a\n"}, func(string) {})
	// Built with plumbing: a case-insensitive filesystem can't make it.
	gd := filepath.Join(r.Dir, "repo.git")
	gitc := func(stdin string, args ...string) string {
		cmd := exec.Command("git", append([]string{"--git-dir=" + gd, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	tree := strings.Replace(gitc("", "ls-tree", r.Snapshot), "\tReadme.txt", "\tREADME.txt", 1)
	tree = gitc(tree+"\n", "mktree")
	r.Candidate = gitc("", "commit-tree", tree, "-p", r.Snapshot, "-m", "rename")
	p, err := PlanApply(r, user)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Conflicts, "\n"); !strings.Contains(got, "Readme.txt: differs from README.txt only in case or Unicode normalisation") {
		t.Fatalf("conflicts: %q", got)
	}
	if fold("cafe\u0301.txt") != fold("CAF\u00c9.txt") {
		t.Error("fold doesn't normalise")
	}
}

// A Candidate symlink that points outside the repository is never
// written, as the Snapshot never copies one.
func TestPlanApplyRefusesSymlinksOutOfTheRepository(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.Symlink("/etc", filepath.Join(ws, "abs"))
		os.Symlink("../../outside", filepath.Join(ws, "up"))
		os.Symlink("a.txt", filepath.Join(ws, "ok"))
	})
	p, err := PlanApply(r, user)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Conflicts, "\n")
	for _, want := range []string{"abs: a symlink to /etc, outside the repository", "up: a symlink to ../../outside, outside the repository"} {
		if !strings.Contains(got, want) {
			t.Errorf("conflicts lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ok:") {
		t.Errorf("a link inside the repository conflicts:\n%s", got)
	}
}

// Writing a symlink never removes a user file beside it, and leftovers
// of a write that was killed are swept.
func TestApplyWritesSymlinksWithoutTouchingNeighbours(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.Symlink("a.txt", filepath.Join(ws, "link"))
		os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
	})
	os.WriteFile(filepath.Join(user, "link.oge-tmp"), []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(user, ".a.txt.oge-0123456789ab"), []byte("left over"), 0o600)
	// A user file that only looks like one is never swept.
	os.WriteFile(filepath.Join(user, ".a.txt.oge-notes"), []byte("mine"), 0o600)
	p, err := PlanApply(r, user)
	if err != nil || len(p.Conflicts) > 0 {
		t.Fatal(err, p.Conflicts)
	}
	if err := p.write(user); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(user, "link.oge-tmp")); err != nil || string(b) != "mine" {
		t.Errorf("link.oge-tmp: %q %v", b, err)
	}
	if target, err := os.Readlink(filepath.Join(user, "link")); err != nil || target != "a.txt" {
		t.Errorf("link -> %q %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(user, ".a.txt.oge-0123456789ab")); !os.IsNotExist(err) {
		t.Errorf("the leftover temp file stayed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(user, ".a.txt.oge-notes")); err != nil || string(b) != "mine" {
		t.Errorf("a user file was swept: %q %v", b, err)
	}
}

// A gitlink (a nested repository) in the Candidate is a named refusal,
// not an internal error.
func TestDeliveryRefusesAGitlink(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		sub := filepath.Join(ws, "sub")
		os.MkdirAll(sub, 0o755)
		os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o644)
		for _, args := range [][]string{{"init", "-q"}, {"add", "f"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "x"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = sub
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
		}
	})
	changes, _ := r.repo().Changes(r.Snapshot, r.Candidate)
	if len(changes) != 1 || changes[0].NewMode != "160000" {
		t.Skipf("no gitlink in the Candidate: %+v", changes)
	}
	if _, err := PlanApply(r, user); !IsRefused(err) || !strings.Contains(err.Error(), "sub is a submodule") {
		t.Errorf("PlanApply: %v", err)
	}
	// oge diff still shows it, marked.
	if d, err := Diff(r); err != nil || !strings.Contains(string(d), "+Subproject commit ") ||
		!strings.Contains(string(d), "# Öge: sub is a submodule (gitlink); oge apply and oge branch refuse it") {
		t.Errorf("Diff: %v\n%s", err, d)
	}
}

// A Candidate symlink into .git is never delivered.
func TestPlanApplyRefusesSymlinksIntoGit(t *testing.T) {
	user, r := planFixture(t, map[string]string{"d/a.txt": "a\n"}, func(ws string) {
		os.Symlink(".git/config", filepath.Join(ws, "x"))
		os.Symlink("../.GIT/hooks", filepath.Join(ws, "d", "y"))
	})
	p, err := PlanApply(r, user)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Conflicts, "\n")
	for _, want := range []string{"x: a symlink to .git/config, into .git", "d/y: a symlink to ../.GIT/hooks, into .git"} {
		if !strings.Contains(got, want) {
			t.Errorf("conflicts lack %q:\n%s", want, got)
		}
	}
}

func TestCleanDropsInvisibleCharacters(t *testing.T) {
	if got := Clean([]byte("a\u200bb\u200cc\u200dd\u2028e\u2029f\ufeffg\u202eh\u2066i\n")); got != "abcdefghi\n" {
		t.Errorf("Clean = %q", got)
	}
}
