package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noScratch fails when a Rebase left its scratch git dir behind.
func noScratch(t *testing.T, r *RunRepo) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(r.Dir, "merge-*")); len(left) > 0 {
		t.Errorf("Rebase left %v", left)
	}
}

// A follow-up carries the previous Candidate over to a fresh Snapshot
// (#185): unchanged when the repository is, merged three ways when it
// changed elsewhere, and a conflict when it changed the same lines.
func TestRebaseCarriesACandidateToANewSnapshot(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "a.txt"), "one\ntwo\nthree\nfour\nfive\n")
	gitT(t, repo, "commit", "-qam", "lines")
	r, err := InitRunRepo(filepath.Join(t.TempDir(), "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	snap := func(name string) string {
		s, _, err := r.TakeSnapshot(repo, filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s1 := snap("s1")
	ws := filepath.Join(t.TempDir(), "ws")
	if err := r.Checkout(s1, ws); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, "a.txt"), "ONE\ntwo\nthree\nfour\nfive\n")
	c1, err := r.CommitCandidate(ws, s1, "refs/oge/c1", "c1")
	if err != nil {
		t.Fatal(err)
	}

	same := snap("same")
	if got, moved, conflicted, err := r.Rebase(s1, same, c1); err != nil || moved || conflicted || got != c1 {
		t.Errorf("unchanged repository: %s moved=%v conflicted=%v err=%v, want %s itself", got, moved, conflicted, err, c1)
	}

	write(t, filepath.Join(repo, "a.txt"), "one\ntwo\nthree\nfour\nFIVE\n")
	write(t, filepath.Join(repo, "b.txt"), "user\n")
	s2 := snap("s2")
	got, moved, conflicted, err := r.Rebase(s1, s2, c1)
	if err != nil || !moved || conflicted {
		t.Fatalf("moved=%v conflicted=%v err=%v", moved, conflicted, err)
	}
	if a, _, _ := r.Show(got, "a.txt"); string(a) != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Errorf("merged a.txt = %q, want both edits", a)
	}
	if b, ok, _ := r.Show(got, "b.txt"); !ok || string(b) != "user\n" {
		t.Errorf("the user's new file is lost: %q", b)
	}
	noScratch(t, r)
	if parent := gitT(t, r.Dir, "rev-parse", got+"^"); parent != s2+"\n" {
		t.Errorf("rebased onto %s, want %s", parent, s2)
	}

	write(t, filepath.Join(repo, "a.txt"), "uno\ntwo\nthree\nfour\nfive\n")
	s3 := snap("s3")
	if got, moved, conflicted, err := r.Rebase(s1, s3, c1); err != nil || moved || !conflicted || got != "" {
		t.Errorf("conflicting edit: %q moved=%v conflicted=%v err=%v", got, moved, conflicted, err)
	}
	noScratch(t, r)
}

// The three-way merge carries modes, links, deletions and CRLF bytes from
// either side, byte for byte.
func TestRebaseCarriesModesLinksDeletesAndCRLF(t *testing.T) {
	repo := newRepo(t)
	crlf := "one\r\ntwo\r\nthree\r\nfour\r\nfive\r\n"
	write(t, filepath.Join(repo, "crlf.txt"), crlf)
	write(t, filepath.Join(repo, "run.sh"), "echo hi\n")
	write(t, filepath.Join(repo, "theirs-gone.txt"), "x\n")
	write(t, filepath.Join(repo, "ours-gone.txt"), "y\n")
	if err := os.Symlink("a.txt", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "kinds")
	r, err := InitRunRepo(filepath.Join(t.TempDir(), "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	s1, _, err := r.TakeSnapshot(repo, filepath.Join(t.TempDir(), "s1"))
	if err != nil {
		t.Fatal(err)
	}
	// The Candidate: makes run.sh executable, deletes one file, edits the
	// CRLF file's first line.
	ws := filepath.Join(t.TempDir(), "ws")
	if err := r.Checkout(s1, ws); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(ws, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(ws, "theirs-gone.txt"))
	write(t, filepath.Join(ws, "crlf.txt"), strings.Replace(crlf, "one", "ONE", 1))
	c1, err := r.CommitCandidate(ws, s1, "refs/oge/c1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	// The user: retargets the link, deletes another file, edits the CRLF
	// file's last line.
	os.Remove(filepath.Join(repo, "link"))
	if err := os.Symlink("crlf.txt", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(repo, "ours-gone.txt"))
	write(t, filepath.Join(repo, "crlf.txt"), strings.Replace(crlf, "five", "FIVE", 1))
	s2, _, err := r.TakeSnapshot(repo, filepath.Join(t.TempDir(), "s2"))
	if err != nil {
		t.Fatal(err)
	}
	got, moved, conflicted, err := r.Rebase(s1, s2, c1)
	if err != nil || !moved || conflicted {
		t.Fatalf("moved=%v conflicted=%v err=%v", moved, conflicted, err)
	}
	noScratch(t, r)
	tree := gitT(t, r.Dir, "ls-tree", "-r", got)
	for _, want := range []string{"100755 blob", "\trun.sh", "120000 blob"} {
		if !strings.Contains(tree, want) {
			t.Errorf("merged tree lacks %q:\n%s", want, tree)
		}
	}
	if !strings.Contains(tree, "100755 blob "+strings.Fields(gitT(t, r.Dir, "rev-parse", c1+":run.sh"))[0]+"\trun.sh") {
		t.Errorf("run.sh isn't executable in the merged tree:\n%s", tree)
	}
	for _, gone := range []string{"theirs-gone.txt", "ours-gone.txt"} {
		if strings.Contains(tree, gone) {
			t.Errorf("%s came back:\n%s", gone, tree)
		}
	}
	if link, _, _ := r.Show(got, "link"); string(link) != "crlf.txt" {
		t.Errorf("link targets %q, want the user's crlf.txt", link)
	}
	if b, _, _ := r.Show(got, "crlf.txt"); string(b) != strings.NewReplacer("one", "ONE", "five", "FIVE").Replace(crlf) {
		t.Errorf("crlf.txt = %q", b)
	}
}

func TestGitAtLeast(t *testing.T) {
	for out, want := range map[string]bool{
		"git version 2.50.1 (Apple Git-155)\n": true, "git version 2.40.0\n": true, "git version 2.39.5\n": false,
		"git version 1.9.0\n": false, "git version 3.0.0\n": true, "git version 2.45.2.windows.1\n": true, "nonsense": true,
	} {
		if got := gitAtLeast(out, 2, 40); got != want {
			t.Errorf("gitAtLeast(%q) = %v", out, got)
		}
	}
}
