package workspace

import (
	"path/filepath"
	"testing"
)

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
	if parent := gitT(t, r.Dir, "rev-parse", got+"^"); parent != s2+"\n" {
		t.Errorf("rebased onto %s, want %s", parent, s2)
	}

	write(t, filepath.Join(repo, "a.txt"), "uno\ntwo\nthree\nfour\nfive\n")
	s3 := snap("s3")
	if got, moved, conflicted, err := r.Rebase(s1, s3, c1); err != nil || !moved || !conflicted || got != "" {
		t.Errorf("conflicting edit: %q moved=%v conflicted=%v err=%v", got, moved, conflicted, err)
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
