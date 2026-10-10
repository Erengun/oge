package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseGoTest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		run      string
		patterns []string
		filters  bool
		ok       bool
	}{
		{"go test", []string{"."}, false, true},
		{"go test ./...", []string{"..."}, false, true},
		{"go test -json ./internal/delivery/...", []string{"internal/delivery/..."}, false, true},
		{"go test -count 1 -p 4 -timeout 1m ./a ./b/...", []string{"a", "b/..."}, false, true},
		{"go test -run X ./a", []string{"a"}, true, true},
		{"go test -skip=X -tags x ./a", []string{"a"}, true, true},
		{"go test -v -race -short . ./c", []string{".", "c"}, false, true},
		{"go test ./a -args -foo bar", []string{"a"}, false, true},
		{"go test -weird ./a", nil, false, false},
		{"go test -run", nil, false, false},
		{"go test ./a | tee out", nil, false, false},
		{"go test ./a && go vet ./...", nil, false, false},
		{"FOO=1 go test ./a", nil, false, false},
		{"go test '-run=X' ./a", nil, false, false},
		{"go test ./a/.../b", nil, false, false},
		{"go test ../a", nil, false, false},
		{"make test", nil, false, false},
		{"go vet ./...", nil, false, false},
	}
	for _, c := range cases {
		got, ok := parseGoTest(c.run)
		if ok != c.ok || (ok && (!reflect.DeepEqual(got.patterns, c.patterns) || got.filters != c.filters)) {
			t.Errorf("%q: got %+v ok=%v, want %v filters=%v ok=%v", c.run, got, ok, c.patterns, c.filters, c.ok)
		}
	}
}

func TestGoTestCmdCovers(t *testing.T) {
	t.Parallel()
	c := goTestCmd{patterns: []string{"x/...", "y", "."}}
	for dir, want := range map[string]bool{"x": true, "x/z": true, "xx": false, "y": true, "y/z": false, ".": true, "w": false} {
		if got := c.covers(dir); got != want {
			t.Errorf("covers(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestCheckCoverage(t *testing.T) {
	t.Parallel()
	paths := []string{
		"a/a_test.go", "b/b_test.go", "b/c/c_test.go", "d/d_test.go", "e/e_test.go", "e/e.go",
		"a/testdata/_test.go", "d/testdata/g.golden", "go.mod",
	}
	globs := []string{"**/*_test.go"}
	// Four uncovered dirs: the message names three and counts the rest.
	msg, _ := checkCoverage(paths, globs, []string{"go test ./a/..."})
	for _, want := range []string{"4 protected test files in 4 package dirs (b, b/c, d and 1 more)", "a/**/*_test.go", "./..."} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q lacks %q", msg, want)
		}
	}
	// Any one command covering a dir is enough.
	if msg, _ := checkCoverage(paths, globs, []string{"go test ./a/... ./b/... ./d ./e"}); msg != "" {
		t.Errorf("covered, but refused: %s", msg)
	}
	if msg, _ := checkCoverage(paths, globs, []string{"go test ./a/...", "go test ./..."}); msg != "" {
		t.Errorf("covered by the second command, but refused: %s", msg)
	}
	// An opaque command anywhere means no claim.
	if msg, _ := checkCoverage(paths, globs, []string{"go test ./a/...", "make test"}); msg != "" {
		t.Errorf("opaque command, but refused: %s", msg)
	}
	msg, warns := checkCoverage(paths, globs, []string{"make test"})
	if msg != "" || len(warns) != 1 || !strings.Contains(warns[0], "(d)") || !strings.Contains(warns[0], "testdata/") {
		t.Errorf("testdata warning: %q %q", msg, warns)
	}
	_, warns = checkCoverage(paths, []string{"**/*_test.go", "d/testdata/**"}, []string{"go test -run X ./..."})
	if len(warns) != 1 || !strings.Contains(warns[0], "filters tests") {
		t.Errorf("-run warning only: %q", warns)
	}
}

func TestRunRefusesACheckThatCannotRunTheProtectedTests(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	// Run 1 of #114: globs protect every test, the Check runs one package.
	cfg := "schema = 1\n[project]\ntest_globs = [\"**/*_test.go\"]\n[[check.commands]]\nrun = \"go test -json ./sub/...\"\nreport = \"go-test-json\"\n"
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	marker := filepath.Join(t.TempDir(), "agent-ran")
	code, _, errOut := f.run(t, "touch '"+marker+"'\n", "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitRefused || !strings.Contains(errOut, "the Check can't run 1 protected test file in 1 package dir (.)") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the agent ran although the Check can't run the protected tests")
	}
}
