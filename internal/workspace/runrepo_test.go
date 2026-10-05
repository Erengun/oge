package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX only")
	}
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "a.txt"), "committed\n")
	write(t, filepath.Join(repo, ".gitignore"), "ignored.txt\n")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", "init")
	return repo
}

func TestSnapshotIncludesDirtyAndUntrackedWork(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "a.txt"), "modified\n")
	write(t, filepath.Join(repo, "new", "b.txt"), "untracked\n")
	write(t, filepath.Join(repo, "ignored.txt"), "secret\n")
	statusBefore := gitT(t, repo, "status", "--porcelain")
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)

	state := t.TempDir()
	r, err := InitRunRepo(filepath.Join(state, "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(state, "ws")
	snap, info, err := r.TakeSnapshot(repo, ws)
	if err != nil {
		t.Fatal(err)
	}
	if info.Modified != 1 || info.Untracked != 1 || info.Branch != "main" {
		t.Errorf("info %+v", info)
	}
	for path, want := range map[string]string{"a.txt": "modified\n", "new/b.txt": "untracked\n"} {
		b, ok, err := r.Show(snap, path)
		if err != nil || !ok || string(b) != want {
			t.Errorf("%s in Snapshot: %q %v %v", path, b, ok, err)
		}
	}
	if _, ok, _ := r.Show(snap, "ignored.txt"); ok {
		t.Error("ignored file reached the Snapshot")
	}

	// The implementer edits the Workspace; Öge commits a Candidate.
	write(t, filepath.Join(ws, "a.txt"), "implemented\n")
	cand, err := r.CommitCandidate(ws, snap, "refs/oge/candidates/c1", "Candidate c1")
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := r.ChangedFiles(snap, cand)
	if strings.Join(changed, ",") != "a.txt" {
		t.Errorf("changed %v", changed)
	}
	out := filepath.Join(state, "check")
	if err := r.Checkout(cand, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "a.txt")); string(b) != "implemented\n" {
		t.Errorf("checkout a.txt %q", b)
	}
	if _, err := os.Stat(filepath.Join(out, ".git")); err == nil {
		t.Error("Check directory has a .git")
	}

	if rem, err := r.Remotes(); err != nil || rem != "" {
		t.Errorf("remotes %q %v", rem, err)
	}
	if after := gitT(t, repo, "status", "--porcelain"); after != statusBefore {
		t.Errorf("user's status changed:\n%s\nvs\n%s", after, statusBefore)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a user hook ran")
	}
}

func TestPreflightRefusals(t *testing.T) {
	repo := newRepo(t)
	if why, err := Preflight(repo); err != nil || len(why) != 0 {
		t.Fatalf("clean repo: %v %v", why, err)
	}

	lfs := newRepo(t)
	write(t, filepath.Join(lfs, ".gitattributes"), "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	merge := newRepo(t)
	write(t, filepath.Join(merge, ".git", "MERGE_HEAD"), "0000000000000000000000000000000000000000\n")
	bisect := newRepo(t)
	write(t, filepath.Join(bisect, ".git", "BISECT_LOG"), "\n")
	pick := newRepo(t)
	write(t, filepath.Join(pick, ".git", "CHERRY_PICK_HEAD"), strings.Repeat("0", 40)+"\n")
	rebase := newRepo(t)
	os.MkdirAll(filepath.Join(rebase, ".git", "rebase-merge"), 0o755)
	unmerged := newRepo(t)
	blob := strings.TrimSpace(gitT(t, unmerged, "hash-object", "-w", "a.txt"))
	gitT(t, unmerged, "update-index", "--force-remove", "a.txt")
	cmd := exec.Command("git", "-C", unmerged, "update-index", "--index-info")
	cmd.Stdin = strings.NewReader("100644 " + blob + " 1\ta.txt\n100644 " + blob + " 2\ta.txt\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	sub := newRepo(t)
	gitT(t, sub, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",vendor/lib")

	for repo, want := range map[string]string{lfs: "Git LFS", merge: "a merge", bisect: "a bisect", sub: "submodules",
		pick: "a cherry-pick", rebase: "a rebase", unmerged: "unmerged"} {
		why, err := Preflight(repo)
		if err != nil || len(why) == 0 || !strings.Contains(strings.Join(why, ";"), want) {
			t.Errorf("want %q, got %v %v", want, why, err)
		}
	}
}

func TestPreflightRefusesSymlinksThatLeaveTheTree(t *testing.T) {
	for name, c := range map[string]struct {
		link, target string
		refused      bool
	}{
		"absolute":           {"abs", "/etc", true},
		"escapes":            {"sub/up", "../../outside", true},
		"escapes via a link": {"sub/dot", "../self/..", true},
		"relative, in tree":  {"sub/a", "../a.txt", false},
		"relative, dangling": {"sub/gone", "missing.txt", false},
		"tracked, in tree":   {"tracked", "a.txt", false},
		"tracked, absolute":  {"tracked-abs", "/tmp", true},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t)
			if err := os.Symlink(".", filepath.Join(repo, "self")); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(repo, filepath.FromSlash(c.link))
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.Symlink(c.target, p); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(c.link, "tracked") {
				gitT(t, repo, "add", "-A")
				gitT(t, repo, "commit", "-q", "-m", "link")
			}
			why, err := Preflight(repo)
			if err != nil {
				t.Fatal(err)
			}
			got := contains(why, c.link+" is a symlink")
			if got != c.refused {
				t.Errorf("refused = %v, want %v: %q", got, c.refused, why)
			}
		})
	}
}

// The Workspace gets a minimal Öge-owned .git (ADR-0010, #44): the
// Snapshot as HEAD and index, no remotes, hooks off, nothing pointing at
// Öge's private state. Candidates never take its contents.
func TestWorkspaceGitIsMinimalAndNeverInTheCandidate(t *testing.T) {
	repo := newRepo(t)
	state := t.TempDir()
	r, err := InitRunRepo(filepath.Join(state, "private", "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(state, "ws")
	snap, _, err := r.TakeSnapshot(repo, ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.InitWorkspaceGit(ws); err != nil {
		t.Fatal(err)
	}
	if out := gitT(t, ws, "status", "--porcelain"); out != "" {
		t.Errorf("a fresh Workspace isn't clean: %q", out)
	}
	if head := strings.TrimSpace(gitT(t, ws, "rev-parse", "HEAD")); head != snap {
		t.Errorf("HEAD = %s, want the Snapshot %s", head, snap)
	}
	if out := gitT(t, ws, "remote"); out != "" {
		t.Errorf("remotes: %q", out)
	}
	if out := strings.TrimSpace(gitT(t, ws, "config", "core.hooksPath")); out != os.DevNull {
		t.Errorf("core.hooksPath = %q", out)
	}
	filepath.Walk(filepath.Join(ws, ".git"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), filepath.Join(state, "private")) {
				t.Errorf("%s names Öge's private state", p)
			}
		}
		return nil
	})
	if entries, _ := os.ReadDir(filepath.Join(ws, ".git", "hooks")); len(entries) != 0 {
		t.Errorf("hooks dir isn't empty: %v", entries)
	}

	write(t, filepath.Join(ws, "a.txt"), "changed\n")
	if out := gitT(t, ws, "diff", "--name-only"); out != "a.txt\n" {
		t.Errorf("git diff --name-only = %q", out)
	}
	// What the agent might put in .git, at the top or nested, stays out.
	write(t, filepath.Join(ws, ".git", "agent-note"), "x\n")
	write(t, filepath.Join(ws, "pkg", ".git", "config"), "[core]\n")
	write(t, filepath.Join(ws, "pkg", "b.txt"), "b\n")
	c, err := r.CommitCandidate(ws, snap, "refs/oge/candidates/c1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := r.ChangedFiles(snap, c)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(changed, ","); got != "a.txt,pkg/b.txt" {
		t.Errorf("Candidate changed %s", got)
	}
}
