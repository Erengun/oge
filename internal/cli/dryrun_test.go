package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files (go test ./internal/cli -update)")

// Each directory under testdata/dryrun is one case:
//
//	args         one argument per line (required)
//	oge.toml     committed as .oge/oge.toml
//	files/       copied into the repository and committed
//	untracked/   copied into the repository, not committed
//	installed    agents on PATH, one per line (default: claude)
//	interactive  present: a human is at a terminal
//	stdin        what the human types or pipes
//	editor       what the human writes in $EDITOR
//	norepo       present: the working directory is not a git repository
//	want.golden  exit code, stdout and stderr
func TestDryRunGoldens(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("testdata", "dryrun", "*"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no cases: %v", err)
	}
	for _, dir := range cases {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			got := runCase(t, dir)
			golden := filepath.Join(dir, "want.golden")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if got != string(want) {
				t.Errorf("output differs from %s (run with -update to accept)\n--- got\n%s\n--- want\n%s", golden, got, want)
			}
		})
	}
}

func runCase(t *testing.T, dir string) string {
	isolate(t)
	repo := t.TempDir()
	if !exists(filepath.Join(dir, "norepo")) {
		initRepo(t, repo)
		if b, err := os.ReadFile(filepath.Join(dir, "oge.toml")); err == nil {
			writeFile(t, filepath.Join(repo, ".oge", "oge.toml"), b)
		}
		copyTree(t, filepath.Join(dir, "files"), repo)
		gitIn(t, repo, "add", "-A")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "fixture")
		copyTree(t, filepath.Join(dir, "untracked"), repo)
	}

	args := strings.Split(strings.TrimRight(read(t, filepath.Join(dir, "args")), "\n"), "\n")
	installed := []string{"claude"}
	if s, err := os.ReadFile(filepath.Join(dir, "installed")); err == nil {
		installed = strings.Fields(string(s))
	}
	editor, editorErr := os.ReadFile(filepath.Join(dir, "editor"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))

	var stdout, stderr bytes.Buffer
	env := Env{
		Stdin: bytes.NewReader(stdin), Stdout: &stdout, Stderr: &stderr,
		Dir:         repo,
		Getenv:      func(string) string { return "" },
		Interactive: func() bool { return exists(filepath.Join(dir, "interactive")) },
		LookPath: func(name string) (string, error) {
			for _, a := range installed {
				if a == name {
					return "/fake/bin/" + name, nil
				}
			}
			return "", exec.ErrNotFound
		},
		Edit: func(path string) error {
			if editorErr != nil {
				return errors.New("no editor in this case")
			}
			return os.WriteFile(path, editor, 0o644)
		},
		GOOS:    "linux",
		Version: "test",
	}
	code := Main(env, args)
	out := fmt.Sprintf("exit: %d\n--- stdout\n%s--- stderr\n%s", code, stdout.String(), stderr.String())
	return strings.ReplaceAll(out, repo, "$REPO")
}

// isolate gives the test a synthetic HOME/XDG and keeps the user's git
// config out of the fixture repositories.
func isolate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func initRepo(t *testing.T, dir string) {
	gitIn(t, dir, "init", "-q", "-b", "main")
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if !exists(src) {
		return
	}
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		writeFile(t, filepath.Join(dst, rel), b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
