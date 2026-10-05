package delivery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/run"
)

func branchable(t *testing.T, r *Run) {
	t.Helper()
	r.Outcome = run.Accepted
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "u"}, {"GIT_AUTHOR_EMAIL", "u@example.com"},
		{"GIT_COMMITTER_NAME", "u"}, {"GIT_COMMITTER_EMAIL", "u@example.com"}, {"GIT_CONFIG_GLOBAL", os.DevNull}, {"GIT_CONFIG_NOSYSTEM", "1"}} {
		t.Setenv(kv[0], kv[1])
	}
}

func userGitOut(t *testing.T, repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// An agent-added .gitattributes can't make oge branch run a filter: the
// user's clean and smudge filters are off for Öge's commits.
func TestBranchRunsNoFilter(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o644)
		os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
	})
	branchable(t, r)
	marker := filepath.Join(t.TempDir(), "filter-ran")
	if out, err := userGitOut(t, user, "config", "filter.evil.clean", "touch '"+marker+"'; cat"); err != nil {
		t.Fatal(err, out)
	}
	b, err := Branch(r, user, "taken", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("oge branch ran the filter")
	}
	if out, _ := userGitOut(t, user, "cat-file", "blob", b.Commit+":a.txt"); out != "A\n" {
		t.Errorf("a.txt on the branch: %q", out)
	}
	if out, _ := userGitOut(t, user, "cat-file", "-p", b.Commit); strings.Contains(out, "gpgsig") {
		t.Errorf("the commit is signed:\n%s", out)
	}
}

func TestBranchRefusesBadNamesLinksOutAndGitlinks(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.Symlink("/etc", filepath.Join(ws, "abs"))
	})
	branchable(t, r)
	for _, name := range []string{"@", "refs/heads/x", "-x", "a..b"} {
		if _, err := Branch(r, user, name, ""); !IsRefused(err) || !strings.Contains(err.Error(), "isn't a valid branch name") {
			t.Errorf("%q: %v", name, err)
		}
	}
	if _, err := Branch(r, user, "x", ""); !IsRefused(err) || !strings.Contains(err.Error(), "abs: a symlink to /etc, outside the repository") {
		t.Errorf("a link out: %v", err)
	}
	if out, _ := userGitOut(t, user, "branch", "--list"); out != "" {
		t.Errorf("branches: %q", out)
	}
}

// objectFiles lists what is in a repository's object store.
func objectFiles(t *testing.T, repo string) []string {
	t.Helper()
	var files []string
	filepath.Walk(filepath.Join(repo, ".git", "objects"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	return files
}

// A clean filter the user's repository applies to a delivered path makes
// oge branch refuse: it won't run the filter, and won't knowingly commit
// something other than what the user's git add would.
func TestBranchRefusesWhenAUserCleanFilterApplies(t *testing.T) {
	for _, c := range []struct {
		name, attrs, driver string
		refused             bool
	}{
		{"filter on a delivered path", "*.txt filter=keep\n", "clean", true},
		{"process filter on a delivered path", "*.txt filter=keep\n", "process", true},
		{"filter on an untouched path", "*.dat filter=keep\n", "clean", false},
		{"filter with no driver", "*.txt filter=nodriver\n", "clean", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			user, r := planFixture(t, map[string]string{"a.txt": "a\n", ".gitattributes": c.attrs}, func(ws string) {
				os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
			})
			branchable(t, r)
			marker := filepath.Join(t.TempDir(), "filter-ran")
			if out, err := userGitOut(t, user, "config", "filter.keep."+c.driver, "touch '"+marker+"'; cat"); err != nil {
				t.Fatal(err, out)
			}
			before := objectFiles(t, user)
			b, err := Branch(r, user, "taken", "")
			if _, serr := os.Stat(marker); serr == nil {
				t.Error("oge branch ran the user's filter")
			}
			if !c.refused {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if out, _ := userGitOut(t, user, "cat-file", "blob", b.Commit+":a.txt"); out != "A\n" {
					t.Errorf("a.txt on the branch: %q", out)
				}
				return
			}
			if !IsRefused(err) || !strings.Contains(err.Error(), "can't safely reproduce this repository's normal git transformation for a.txt without executing user-configured filters") ||
				!strings.Contains(err.Error(), "oge apply r1") {
				t.Fatalf("Branch: %v", err)
			}
			if out, _ := userGitOut(t, user, "branch", "--list"); out != "" {
				t.Errorf("branches: %q", out)
			}
			if after := objectFiles(t, user); strings.Join(after, "\n") != strings.Join(before, "\n") {
				t.Errorf("objects left behind:\n%v\nwere:\n%v", after, before)
			}
		})
	}
}
