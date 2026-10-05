package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
)

type scopeFixture struct {
	r     *RunRepo
	ws    string
	snap  string
	blobs map[string][]byte
}

// newScopeFixture snapshots a repository holding prot/a.txt (protected)
// and free.txt into a Workspace.
func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	repo := newRepo(t)
	write(t, filepath.Join(repo, "prot", "a.txt"), "one\ntwo\nthree\n")
	write(t, filepath.Join(repo, "free.txt"), "free\n")
	write(t, filepath.Join(repo, ".gitignore"), "ignored.txt\nprot/ignored/\n")
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
	return &scopeFixture{r: r, ws: ws, snap: snap, blobs: map[string][]byte{}}
}

func (f *scopeFixture) check(t *testing.T) *Scope {
	t.Helper()
	s, err := f.r.CheckScope(f.ws, f.snap, ScopeRules{
		Protected: func(p string) string {
			switch {
			case p == ledger.WorkspaceMarker:
				return "oge_file"
			case strings.HasPrefix(p, "prot/"), p == "a.txt":
				return "prot"
			}
			return ""
		},
		Put: func(b []byte) (string, error) {
			id := "blob" + string(rune('0'+len(f.blobs)))
			f.blobs[id] = b
			return id, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func byPath(s *Scope) map[string]Revert {
	m := map[string]Revert{}
	for _, r := range s.Reverts {
		m[r.Path] = r
	}
	return m
}

func TestScopeCleanWorkspaceRevertsNothing(t *testing.T) {
	f := newScopeFixture(t)
	write(t, filepath.Join(f.ws, "free.txt"), "changed\n")
	write(t, filepath.Join(f.ws, "new.txt"), "new\n")
	if s := f.check(t); len(s.Reverts) != 0 || s.Compared == 0 {
		t.Fatalf("reverts %v, compared %d", s.Reverts, s.Compared)
	}
}

// The comparison ignores .gitignore: an ignored file at a protected path
// is still found and removed.
func TestScopeFindsIgnoredFiles(t *testing.T) {
	f := newScopeFixture(t)
	write(t, filepath.Join(f.ws, "prot", "ignored", "x.txt"), "x\n")
	s := f.check(t)
	r, ok := byPath(s)["prot/ignored/x.txt"]
	if !ok || r.Change != "added" || !r.Tamper || r.Before != "" || r.Size != 2 || r.Enforcement != RevertOnly {
		t.Fatalf("reverts: %+v", s.Reverts)
	}
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.ws, "prot", "ignored", "x.txt")); err == nil {
		t.Error("the ignored file stayed")
	}
}

func TestScopeRestoresContentAndMode(t *testing.T) {
	f := newScopeFixture(t)
	p := filepath.Join(f.ws, "prot", "a.txt")
	write(t, p, "one\nTWO\nthree\n")
	if err := os.Remove(filepath.Join(f.ws, ledger.WorkspaceMarker)); err != nil {
		t.Fatal(err)
	}
	s := f.check(t)
	m := byPath(s)
	r := m["prot/a.txt"]
	if r.Change != "modified" || len(r.Before) != 64 || len(r.After) != 64 || r.Before == r.After {
		t.Fatalf("revert: %+v", r)
	}
	patch := string(f.blobs[r.Patch])
	if !strings.HasPrefix(patch, "--- a/prot/a.txt\n+++ b/prot/a.txt\n@@") || !strings.Contains(patch, "-two\n+TWO\n") || strings.Contains(patch, f.ws) {
		t.Errorf("patch:\n%s", patch)
	}
	if m[ledger.WorkspaceMarker].Change != "deleted" {
		t.Errorf("marker: %+v", m[ledger.WorkspaceMarker])
	}
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one\ntwo\nthree\n" {
		t.Errorf("restored %q", b)
	}
	if _, err := os.Stat(filepath.Join(f.ws, ledger.WorkspaceMarker)); err != nil {
		t.Error("the Workspace marker wasn't restored")
	}

	// A mode change alone is a change too.
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	s = f.check(t)
	if r := byPath(s)["prot/a.txt"]; r.Change != "mode" || r.NoPatch != "content unchanged" {
		t.Fatalf("mode change: %+v", s.Reverts)
	}
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode())
	}
	if s := f.check(t); len(s.Reverts) != 0 {
		t.Errorf("still differs after Apply: %+v", s.Reverts)
	}
}

// Patches never hold binary content, are redacted and are capped.
func TestScopePatchLimits(t *testing.T) {
	f := newScopeFixture(t)
	write(t, filepath.Join(f.ws, "prot", "bin"), "a\x00b")
	write(t, filepath.Join(f.ws, "prot", "secret.txt"), "API_TOKEN=hunter2hunter2\n")
	write(t, filepath.Join(f.ws, "prot", "big.txt"), strings.Repeat("a line of text\n", 4000))
	m := byPath(f.check(t))
	if r := m["prot/bin"]; r.Patch != "" || r.NoPatch != "binary" || r.Size != 3 {
		t.Errorf("binary: %+v", r)
	}
	if p := f.blobs[m["prot/secret.txt"].Patch]; bytes.Contains(p, []byte("hunter2")) || !bytes.Contains(p, []byte("REDACTED")) {
		t.Errorf("secret patch: %s", p)
	}
	if r := m["prot/big.txt"]; !r.Truncated || len(f.blobs[r.Patch]) != PatchCap {
		t.Errorf("big: truncated %v, %d bytes", r.Truncated, len(f.blobs[r.Patch]))
	}
}

// A link the agent put on a protected path's directory is removed, and the
// path is restored inside the Workspace, never through the link.
func TestScopeNeverRestoresThroughALink(t *testing.T) {
	f := newScopeFixture(t)
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(f.ws, "prot")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.ws, "prot")); err != nil {
		t.Fatal(err)
	}
	s := f.check(t)
	m := byPath(s)
	if m["prot"].Class != ClassSymlinkEscape || m["prot/a.txt"].Change != "deleted" {
		t.Fatalf("reverts: %+v", s.Reverts)
	}
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(outside); len(left) != 0 {
		t.Errorf("wrote outside the Workspace: %v", left)
	}
	if fi, err := os.Lstat(filepath.Join(f.ws, "prot")); err != nil || !fi.IsDir() {
		t.Errorf("prot is not a directory again: %v %v", fi, err)
	}
}

// A file replacing a protected file's place with a directory is cleared.
func TestScopeDirectoryInTheWay(t *testing.T) {
	f := newScopeFixture(t)
	p := filepath.Join(f.ws, "prot", "a.txt")
	os.Remove(p)
	write(t, filepath.Join(p, "inner.txt"), "x\n")
	s := f.check(t)
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one\ntwo\nthree\n" {
		t.Errorf("restored %q", b)
	}
}

func TestScopeLinksInsideTheWorkspaceStay(t *testing.T) {
	f := newScopeFixture(t)
	os.Symlink("free.txt", filepath.Join(f.ws, "rel"))
	os.Symlink("missing/../free.txt", filepath.Join(f.ws, "dangling"))
	os.Symlink("../../etc", filepath.Join(f.ws, "esc"))
	m := byPath(f.check(t))
	if _, ok := m["rel"]; ok {
		t.Error("an inside link was reverted")
	}
	if m["esc"].Class != ClassSymlinkEscape || m["esc"].NoPatch != "symlink" {
		t.Errorf("esc: %+v", m["esc"])
	}
}

func TestScopeRefusesAReplacedWorkspaceRoot(t *testing.T) {
	f := newScopeFixture(t)
	if err := os.RemoveAll(f.ws); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), f.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.CheckScope(f.ws, f.snap, ScopeRules{Protected: func(string) string { return "p" }}); err == nil {
		t.Fatal("a Workspace root that is a link was accepted")
	}
}

// On a case-insensitive filesystem, renaming a protected file to another
// spelling must still restore it under its exact name.
func TestScopeCaseInsensitiveRename(t *testing.T) {
	f := newScopeFixture(t)
	if _, err := os.Stat(filepath.Join(f.ws, "FREE.TXT")); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	// An unprotected spelling of a protected root file.
	if err := os.Rename(filepath.Join(f.ws, "a.txt"), filepath.Join(f.ws, "A.txt")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.ws, "prot", "a.txt")
	if err := os.Rename(p, filepath.Join(f.ws, "prot", "A.TXT")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.ws, "prot", "A.TXT"), "changed\n")
	s := f.check(t)
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	names, _ := os.ReadDir(filepath.Join(f.ws, "prot"))
	var got []string
	for _, n := range names {
		got = append(got, n.Name())
	}
	if strings.Join(got, ",") != "a.txt" {
		t.Errorf("prot/ lists %v", got)
	}
	if m := byPath(s); m["A.txt"].Class != ClassInTheWay {
		t.Errorf("A.txt: %+v", m["A.txt"])
	}
	if _, err := os.Lstat(filepath.Join(f.ws, "a.txt")); err != nil {
		t.Error(err)
	}
	root, _ := os.ReadDir(f.ws)
	for _, n := range root {
		if n.Name() == "A.txt" {
			t.Error("A.txt is still listed")
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "one\ntwo\nthree\n" {
		t.Errorf("restored %q", b)
	}
}

// Links that appear after the comparison never reach the Candidate,
// however inward their text looks; the links it let stand do.
func TestCommitScopedDropsLateLinks(t *testing.T) {
	f := newScopeFixture(t)
	if err := os.Symlink("free.txt", filepath.Join(f.ws, "kept")); err != nil {
		t.Fatal(err)
	}
	s := f.check(t)
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	// Late: l1 -> l2/.. with l2 -> . resolves above the Workspace; and a
	// late write to a protected file.
	os.Symlink(".", filepath.Join(f.ws, "l2"))
	os.Symlink("l2/..", filepath.Join(f.ws, "l1"))
	write(t, filepath.Join(f.ws, "prot", "a.txt"), "late\n")
	c, late, err := f.r.CommitScoped(f.ws, f.snap, "c", func(p string) string {
		if strings.HasPrefix(p, "prot/") {
			return "prot"
		}
		return ""
	}, s.Links)
	if err != nil {
		t.Fatal(err)
	}
	files, err := f.r.Files(c)
	if err != nil {
		t.Fatal(err)
	}
	got := "\n" + strings.Join(files, "\n") + "\n"
	if strings.Contains(got, "\nl1\n") || strings.Contains(got, "\nl2\n") || !strings.Contains(got, "\nkept\n") {
		t.Errorf("Candidate files:%s", got)
	}
	if b, _, _ := f.r.Show(c, "prot/a.txt"); string(b) != "one\ntwo\nthree\n" {
		t.Errorf("prot/a.txt in the Candidate: %q", b)
	}
	m := map[string]Revert{}
	for _, r := range late {
		m[r.Path] = r
	}
	if m["l1"].Class != ClassSymlinkEscape || m["l2"].Class != ClassSymlinkEscape || !m["prot/a.txt"].Tamper {
		t.Errorf("late: %+v", late)
	}
}
