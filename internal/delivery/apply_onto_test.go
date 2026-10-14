package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/workspace"
)

// chatCuts is a Chat's two cuts as Candidates of one Run repository: the
// first is applied to the user's tree on the Snapshot, and second edits
// the Workspace again into the next. It returns the user's tree, the Run
// of the second cut, and the first cut's Candidate.
func chatCuts(t *testing.T, files map[string]string, first, second func(ws string)) (string, *Run, string) {
	t.Helper()
	var ws string
	user, r := planFixture(t, files, func(w string) { ws = w; first(w) })
	p, err := planApply(r, user, r.Snapshot, r.Candidate)
	if err != nil || len(p.Conflicts) > 0 {
		t.Fatal(err, p.Conflicts)
	}
	if err := p.write(user); err != nil {
		t.Fatal(err)
	}
	second(ws)
	repo := &workspace.RunRepo{Dir: filepath.Join(r.Dir, "repo.git")}
	c2, err := repo.CommitCandidate(ws, r.Candidate, "refs/oge/c2", "c2")
	if err != nil {
		t.Fatal(err)
	}
	c1 := r.Candidate
	r.Candidate = c2
	return user, r, c1
}

func applyOnto(t *testing.T, r *Run, user, base string) *Plan {
	t.Helper()
	p, err := planApply(r, user, base, r.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Conflicts) == 0 {
		if err := p.write(user); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// A Chat's second apply merges on the Candidate last applied: a file the
// first cut created and the second deletes is deleted.
func TestApplyOntoDeletesWhatTheLastApplyCreated(t *testing.T) {
	user, r, c1 := chatCuts(t, map[string]string{"a.txt": "a\n"},
		func(ws string) { os.WriteFile(filepath.Join(ws, "helper.go"), []byte("package x\n"), 0o644) },
		func(ws string) { os.Remove(filepath.Join(ws, "helper.go")) })
	if p := applyOnto(t, r, user, c1); len(p.Conflicts) > 0 {
		t.Fatal(p.Conflicts)
	}
	if _, err := os.Stat(filepath.Join(user, "helper.go")); !os.IsNotExist(err) {
		t.Errorf("helper.go is still there: %v", err)
	}
}

// A second cut reverting part of the first one's edit lands exactly.
func TestApplyOntoTakesAPartialRevert(t *testing.T) {
	user, r, c1 := chatCuts(t, map[string]string{"a.txt": "1\n2\n3\n4\n5\n"},
		func(ws string) { os.WriteFile(filepath.Join(ws, "a.txt"), []byte("1\nTWO\n3\nFOUR\n5\n"), 0o644) },
		func(ws string) { os.WriteFile(filepath.Join(ws, "a.txt"), []byte("1\nTWO\n3\n4\n5\n"), 0o644) })
	if p := applyOnto(t, r, user, c1); len(p.Conflicts) > 0 {
		t.Fatal(p.Conflicts)
	}
	if b, _ := os.ReadFile(filepath.Join(user, "a.txt")); string(b) != "1\nTWO\n3\n4\n5\n" {
		t.Errorf("a.txt is %q, want the second cut's", b)
	}
}

// What the user reverted by hand after the first apply is their edit:
// merged with the second cut, or a conflict, never written over.
func TestApplyOntoKeepsTheUsersRevert(t *testing.T) {
	user, r, c1 := chatCuts(t, map[string]string{"a.txt": "1\n2\n3\n4\n5\n6\n7\n"},
		func(ws string) { os.WriteFile(filepath.Join(ws, "a.txt"), []byte("ONE\n2\n3\n4\n5\n6\n7\n"), 0o644) },
		func(ws string) {
			os.WriteFile(filepath.Join(ws, "a.txt"), []byte("ONE\n2\n3\n4\n5\n6\nSEVEN\n"), 0o644)
		})
	os.WriteFile(filepath.Join(user, "a.txt"), []byte("1\n2\n3\n4\n5\n6\n7\n"), 0o644) // undone by hand
	p := applyOnto(t, r, user, c1)
	b, _ := os.ReadFile(filepath.Join(user, "a.txt"))
	if len(p.Conflicts) == 0 && string(b) != "1\n2\n3\n4\n5\n6\nSEVEN\n" {
		t.Errorf("a.txt is %q: the user's revert was written over", b)
	}
	if strings.HasPrefix(string(b), "ONE") {
		t.Errorf("a.txt is %q: the user's revert was written over", b)
	}
}

// With the base missing from the Run's repository the apply is refused,
// for a branch.
func TestApplyOntoRefusesAMissingBase(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
	})
	r.Outcome = "Accepted"
	_, err := ApplyOnto(r, user, Choice{}, Base{Rev: strings.Repeat("ab", 20)})
	if err == nil || !strings.Contains(err.Error(), "oge branch") {
		t.Fatalf("err %v, want a refusal pointing to a branch", err)
	}
	if b, _ := os.ReadFile(filepath.Join(user, "a.txt")); string(b) != "a\n" {
		t.Errorf("written: %q", b)
	}
}
