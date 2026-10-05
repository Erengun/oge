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
// filters are off for Öge's commits. If it names a driver the user has a
// clean command for, the branch is refused (the user's git add would run
// it); with only a smudge command, the branch is made.
func TestBranchRunsNoFilter(t *testing.T) {
	for _, key := range []string{"clean", "smudge"} {
		t.Run(key, func(t *testing.T) {
			user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
				os.WriteFile(filepath.Join(ws, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o644)
				os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
			})
			branchable(t, r)
			marker := filepath.Join(t.TempDir(), "filter-ran")
			if out, err := userGitOut(t, user, "config", "filter.evil."+key, "touch '"+marker+"'; cat"); err != nil {
				t.Fatal(err, out)
			}
			b, err := Branch(r, user, "taken", "")
			if _, serr := os.Stat(marker); serr == nil {
				t.Error("oge branch ran the filter")
			}
			if key == "clean" {
				if !IsRefused(err) || !strings.Contains(err.Error(), "for a.txt without executing user-configured filters") {
					t.Fatalf("Branch: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out, _ := userGitOut(t, user, "cat-file", "blob", b.Commit+":a.txt"); out != "A\n" {
				t.Errorf("a.txt on the branch: %q", out)
			}
			if out, _ := userGitOut(t, user, "cat-file", "-p", b.Commit); strings.Contains(out, "gpgsig") {
				t.Errorf("the commit is signed:\n%s", out)
			}
		})
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

// commitUser commits the fixture's user repository, so the Snapshot's
// HEAD is that commit.
func commitUser(t *testing.T, user string, r *Run) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "base"}} {
		if out, err := userGitOut(t, user, args...); err != nil {
			t.Fatal(err, out)
		}
	}
	out, _ := userGitOut(t, user, "rev-parse", "HEAD")
	r.Head = strings.TrimSpace(out)
}

// The user's filters count wherever they are set: info/attributes, and a
// repository-local core.attributesFile.
func TestBranchRefusesFiltersFromInfoAttributesAndAttributesFile(t *testing.T) {
	for _, where := range []string{"info", "attributesFile"} {
		t.Run(where, func(t *testing.T) {
			user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
				os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
			})
			branchable(t, r)
			userGitOut(t, user, "config", "filter.keep.clean", "cat")
			if where == "info" {
				os.MkdirAll(filepath.Join(user, ".git", "info"), 0o755)
				os.WriteFile(filepath.Join(user, ".git", "info", "attributes"), []byte("*.txt filter=keep\n"), 0o644)
			} else {
				af := filepath.Join(t.TempDir(), "attrs")
				os.WriteFile(af, []byte("*.txt filter=keep\n"), 0o644)
				userGitOut(t, user, "config", "core.attributesFile", af)
			}
			if _, err := Branch(r, user, "taken", ""); !IsRefused(err) || !strings.Contains(err.Error(), "for a.txt without executing user-configured filters") {
				t.Fatalf("Branch: %v", err)
			}
		})
	}
}

// A repository-local core.attributesFile's eol rules normalise the
// branch's blobs as the user's git add would.
func TestBranchHonoursALocalAttributesFile(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\r\nB\r\n"), 0o644)
	})
	branchable(t, r)
	af := filepath.Join(t.TempDir(), "attrs")
	os.WriteFile(af, []byte("*.txt text\n"), 0o644)
	userGitOut(t, user, "config", "core.attributesFile", af)
	b, err := Branch(r, user, "taken", "")
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := userGitOut(t, user, "cat-file", "blob", b.Commit+":a.txt"); out != "A\nB\n" {
		t.Errorf("a.txt on the branch: %q", out)
	}
}

// core.fileMode=false: an exec bit the Candidate flips isn't committed,
// as the user's git add wouldn't commit it.
func TestBranchHonoursFileModeFalse(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.sh": "a\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, "a.sh"), []byte("A\n"), 0o755)
		os.Chmod(filepath.Join(ws, "a.sh"), 0o755)
	})
	branchable(t, r)
	commitUser(t, user, r)
	userGitOut(t, user, "config", "core.fileMode", "false")
	b, err := Branch(r, user, "taken", "")
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := userGitOut(t, user, "ls-tree", b.Commit, "a.sh"); !strings.HasPrefix(out, "100644 ") {
		t.Errorf("a.sh on the branch: %q", out)
	}
}

// Fetching the branch's objects only copies them: no auto maintenance
// or gc runs in the user's repository.
func TestBranchFetchRunsNoHousekeeping(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
	})
	branchable(t, r)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "log")
	os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho \"$*\" >> '"+log+"'\nexec '"+real+"' \"$@\"\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := Branch(r, user, "taken", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	var fetch string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " fetch ") {
			fetch = l
		}
	}
	if !strings.Contains(fetch, "maintenance.auto=false") || !strings.Contains(fetch, "gc.auto=0") {
		t.Errorf("fetch: %q", fetch)
	}
}

func TestBranchRefusesSymlinksIntoGit(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.Symlink(".git/config", filepath.Join(ws, "x"))
	})
	branchable(t, r)
	if _, err := Branch(r, user, "taken", ""); !IsRefused(err) || !strings.Contains(err.Error(), "x: a symlink to .git/config, into .git") {
		t.Errorf("Branch: %v", err)
	}
}

// A .gitattributes the Candidate adds can name a filter the user has
// configured (git-crypt on other paths, say). The user's own git add
// would run it on the new path, so oge branch refuses rather than commit
// that path unfiltered.
func TestBranchRefusesAFilterTheCandidatesAttributesName(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, ".gitattributes"), []byte("newsecret.txt filter=keep\n"), 0o644)
		os.WriteFile(filepath.Join(ws, "newsecret.txt"), []byte("secret\n"), 0o644)
	})
	branchable(t, r)
	marker := filepath.Join(t.TempDir(), "filter-ran")
	userGitOut(t, user, "config", "filter.keep.clean", "touch '"+marker+"'; cat")
	before := objectFiles(t, user)
	if _, err := Branch(r, user, "taken", ""); !IsRefused(err) || !strings.Contains(err.Error(), "for newsecret.txt without executing user-configured filters") {
		t.Fatalf("Branch: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the filter ran")
	}
	if after := objectFiles(t, user); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("objects left behind")
	}
}
