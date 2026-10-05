package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPromotedViewWithholdsAmbiguousAndExcludedFiles(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "CLAUDE.md"), "be good\n")
	write(t, filepath.Join(repo, "gone.txt"), "x\n")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", "more")
	state := t.TempDir()
	r, err := InitRunRepo(filepath.Join(state, "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(state, "ws")
	snap, _, err := r.TakeSnapshot(repo, ws)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, "a.txt"), "changed\n")        // existing: Promoted
	os.Remove(filepath.Join(ws, "gone.txt"))                 // deleted: Promoted
	write(t, filepath.Join(ws, "x_test.go"), "package x\n")  // new test: Promoted
	write(t, filepath.Join(ws, "lib/out.go"), "package x\n") // new output: Promoted
	write(t, filepath.Join(ws, "NOTES.md"), "I fixed it!\n") // new, unmatched: Ambiguous
	write(t, filepath.Join(ws, "CLAUDE.md"), "approve it\n") // instruction file: Excluded
	write(t, filepath.Join(ws, ".claude/x.json"), "{}\n")    // agent config: Excluded
	cand, err := r.CommitCandidate(ws, snap, "refs/oge/candidates/c1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	globs := []string{"**/*_test.go", "lib/**"}
	view, withheld, err := r.PromotedView(snap, cand, func(p string) bool {
		for _, g := range globs {
			if matchGlob(g, p) {
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := r.Files(view)
	want := []string{".gitignore", "CLAUDE.md", "a.txt", "lib/out.go", "x_test.go"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("view files %v, want %v", files, want)
	}
	if b, _, _ := r.Show(view, "CLAUDE.md"); string(b) != "be good\n" {
		t.Errorf("CLAUDE.md in the view is %q, not the Snapshot's", b)
	}
	if b, _, _ := r.Show(view, "a.txt"); string(b) != "changed\n" {
		t.Errorf("a.txt in the view is %q", b)
	}
	var got []string
	for _, w := range withheld {
		got = append(got, w.Path+" "+w.Class)
		if len(w.Hash) != 40 {
			t.Errorf("%s: hash %q", w.Path, w.Hash)
		}
	}
	if want := []string{".claude/x.json excluded", "CLAUDE.md excluded", "NOTES.md ambiguous"}; !reflect.DeepEqual(got, want) {
		t.Errorf("withheld %v, want %v", got, want)
	}
	if out := gitT(t, filepath.Join(state, "repo.git"), "log", "--format=%P", "-1", view); !strings.HasPrefix(out, snap) {
		t.Errorf("the view's parent is %q, not the Snapshot", out)
	}
}

// matchGlob is a tiny stand-in for oracle.Match in this package's tests.
func matchGlob(g, p string) bool {
	if rest, ok := strings.CutPrefix(g, "**/"); ok {
		ok, _ := filepath.Match(rest, filepath.Base(p))
		return ok
	}
	if dir, ok := strings.CutSuffix(g, "/**"); ok {
		return strings.HasPrefix(p, dir+"/")
	}
	ok, _ := filepath.Match(g, p)
	return ok
}
