package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The release binary delivers a Run the test binary made: oge diff
// prints the plain patch without a terminal, oge branch makes a branch it
// never checks out, and oge apply writes the working tree only.
func TestBinaryDeliversAnAcceptedRun(t *testing.T) {
	repo, env := runFixture(t)
	env = withScript(t, env, "printf 'package fx\\n\\nfunc Add(a, b int) int { return a + b }\\n' > add.go\n")
	if code, out, errOut := runExe(t, testBinary, repo, env, "fix Add", "--fast", "--agent", "fake", "--unattended"); code != 0 || !strings.Contains(out, "next       oge apply ") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, env
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return string(out)
	}

	code, out, errOut := runBinary(t, repo, env, "diff")
	if code != 0 || !strings.HasPrefix(out, "diff --git a/add.go b/add.go\n") || strings.ContainsRune(out, 0x1b) {
		t.Fatalf("diff: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	env = append(env, "GIT_AUTHOR_NAME=u", "GIT_AUTHOR_EMAIL=u@example.com", "GIT_COMMITTER_NAME=u", "GIT_COMMITTER_EMAIL=u@example.com")
	code, out, errOut = runBinary(t, repo, env, "branch", "taken")
	if code != 0 || !strings.HasPrefix(out, "Created branch taken: ") {
		t.Fatalf("branch: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := git("symbolic-ref", "--short", "HEAD"); got == "taken\n" {
		t.Error("oge branch checked the branch out")
	}
	if got := git("status", "--porcelain"); got != "?? add_test.go\n" {
		t.Errorf("oge branch changed the working tree:\n%s", got)
	}

	code, out, errOut = runBinary(t, repo, env, "apply")
	if code != 0 || !strings.HasPrefix(out, "Applied Candidate ") {
		t.Fatalf("apply: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "add.go")); !strings.Contains(string(b), "return a + b") {
		t.Errorf("add.go = %q", b)
	}
	if got := git("status", "--porcelain"); got != " M add.go\n?? add_test.go\n" {
		t.Errorf("git status after apply:\n%s", got)
	}
	if got := git("diff", "--cached", "--name-only"); got != "" {
		t.Errorf("apply staged %s", got)
	}
}
