package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runIn runs oge in repo with claude installed and no terminal.
func runIn(t *testing.T, repo string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	env := Env{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		Dir:         repo,
		Interactive: func() bool { return false },
		LookPath: func(name string) (string, error) {
			if name == "claude" {
				return "/fake/bin/claude", nil
			}
			return "", exec.ErrNotFound
		},
		Edit:    func(string) error { return errors.New("no editor") },
		GOOS:    "linux",
		Version: "test",
	}
	code = Main(env, args)
	return code, out.String(), errb.String()
}

func committedRepo(t *testing.T, config string) string {
	t.Helper()
	isolate(t)
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, filepath.Join(repo, ".oge", "oge.toml"), []byte(config))
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "fixture")
	return repo
}

const secret = "hunter2SECRETvalue"

func TestValidationNeverEchoesConfigValues(t *testing.T) {
	config := `schema = 1
[project]
test_globs = ["**/*_test.go"]

[[check.commands]]
run        = "go test -json ./..."
report     = "` + secret + `"
timeout    = "` + secret + `"
output_cap = "` + secret + `"

[pipelines.default.limits]
stage_timeout        = "` + secret + `"
stage_idle_timeout   = "` + secret + `"
host_request_timeout = "` + secret + `"

[pipelines.default.stages.implement]
role  = "` + secret + `"
agent = "` + secret + `"

[pipelines.default.stages.verify]
role  = "` + secret + `"
agent = "` + secret + `"
`
	repo := committedRepo(t, config)
	code, out, errOut := runIn(t, repo, "--dry-run", "fix it")
	if code != ExitRefused {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if strings.Contains(out+errOut, secret) {
		t.Fatalf("a config value is echoed:\nstdout: %s\nstderr: %s", out, errOut)
	}
	for _, key := range []string{
		"check.commands[0].report", "check.commands[0].timeout", "check.commands[0].output_cap",
		"limits.stage_timeout", "limits.stage_idle_timeout", "limits.host_request_timeout",
		"stages.implement.role", "stages.implement.agent", "stages.verify.role", "stages.verify.agent",
	} {
		if !strings.Contains(errOut, key+":") {
			t.Errorf("stderr doesn't name %s:\n%s", key, errOut)
		}
	}
}

func TestTOMLSyntaxErrorReportsPositionOnly(t *testing.T) {
	repo := committedRepo(t, "schema = 1\nverify = "+secret+"\n")
	code, out, errOut := runIn(t, repo, "--dry-run", "fix it")
	if code != ExitRefused {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if strings.Contains(out+errOut, secret) || strings.Contains(out+errOut, "hunter") {
		t.Fatalf("the parser's message is echoed:\n%s", errOut)
	}
	if !strings.Contains(errOut, "not valid TOML at line 2") {
		t.Fatalf("stderr doesn't give the line:\n%s", errOut)
	}
}

func TestConfigSymlinkIsRefused(t *testing.T) {
	isolate(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.toml")
	writeFile(t, outside, []byte("schema = 1\n[project]\ntest_globs = [\"x\"]\n[[check.commands]]\nrun = \"go test -json ./...\"\nreport = \"go-test-json\"\n"))
	repo := t.TempDir()
	initRepo(t, repo)
	if err := os.MkdirAll(filepath.Join(repo, ".oge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".oge", "oge.toml")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "fixture")

	code, out, errOut := runIn(t, repo, "--dry-run", "fix it")
	if code != ExitRefused || !strings.Contains(errOut, "regular file") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if strings.Contains(out+errOut, outside) || strings.Contains(out+errOut, repo) {
		t.Fatalf("output names an absolute path:\n%s%s", out, errOut)
	}
}

func TestConfigRecordedAsSymlinkInGitIsRefused(t *testing.T) {
	isolate(t)
	repo := t.TempDir()
	initRepo(t, repo)
	if err := os.MkdirAll(filepath.Join(repo, ".oge"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, ".oge", "oge.toml")
	if err := os.Symlink("../valid.toml", link); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "fixture")
	// The working tree now holds a regular file, but git still records a
	// symlink at the path.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, link, []byte("schema = 1\n[project]\ntest_globs = [\"x\"]\n[[check.commands]]\nrun = \"go test -json ./...\"\nreport = \"go-test-json\"\n"))

	code, out, errOut := runIn(t, repo, "--dry-run", "fix it")
	if code != ExitRefused || !strings.Contains(errOut, "regular file") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
}

func TestConfigDirectoryIsRefused(t *testing.T) {
	isolate(t)
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, filepath.Join(repo, ".oge", "oge.toml", "inner"), []byte("x"))
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "fixture")

	code, out, errOut := runIn(t, repo, "--dry-run", "fix it")
	if code != ExitRefused || !strings.Contains(errOut, "regular file") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if strings.Contains(out+errOut, repo) {
		t.Fatalf("output names an absolute path:\n%s%s", out, errOut)
	}
}
