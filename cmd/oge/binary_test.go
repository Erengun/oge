package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests run the built binary in temp git repositories with a synthetic
// HOME/XDG and a PATH holding only git and a stub `claude` that is never
// executed. stdin is not a terminal.

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oge-bin-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "oge")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func fixtureRepo(t *testing.T, config string) (repo string, env []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("binary fixtures use a POSIX stub agent")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	home, repo, bin := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\necho 'stub agent must never run' >&2\nexit 99\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	env = []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"PATH=" + bin, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir, cmd.Env = repo, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if config != "" {
		if err := os.MkdirAll(filepath.Join(repo, ".oge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".oge", "oge.toml"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", "fixture")
	return repo, env
}

func runBinary(t *testing.T, repo string, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir, cmd.Env = repo, env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

const validConfig = `schema = 1
[project]
test_globs = ["**/*_test.go"]
[[check.commands]]
run    = "go test -json ./..."
report = "go-test-json"
`

func TestBinaryDryRunValidExitsZero(t *testing.T) {
	repo, env := fixtureRepo(t, validConfig)
	code, out, errOut := runBinary(t, repo, env, "fix the login bug", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"Task       fix the login bug", "implement ─▶ verify (after) ─▶ Check", "(installed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestBinaryDryRunInvalidExitsTwo(t *testing.T) {
	repo, env := fixtureRepo(t, "schema = 1\n[pipelines.default.gates]\ntamper = false\n")
	code, _, errOut := runBinary(t, repo, env, "run", "--dry-run", "fix it")
	if code != 2 || !strings.Contains(errOut, "tamper Gate is mandatory") {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
}

func TestBinaryWithoutTerminalOrConfigRefuses(t *testing.T) {
	repo, env := fixtureRepo(t, "")
	code, _, errOut := runBinary(t, repo, env, "--dry-run", "fix it")
	if code != 2 || !strings.Contains(errOut, "no terminal") {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(repo, ".oge")); !os.IsNotExist(err) {
		t.Fatalf(".oge was created without a review: %v", err)
	}
}
