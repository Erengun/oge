package oracle

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// vetFixture is a module importing a spread of the standard library.
var vetFixture = fakeRepo{
	"go.mod":      "module fx\n\ngo 1.22\n",
	"add.go":      "package fx\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc Add(a, b int) string { return strings.TrimSpace(fmt.Sprint(a + b)) }\n",
	"add_test.go": "package fx\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != \"5\" {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
}

// vetRun matches the vet tool's command line in go test -x output.
var vetRun = regexp.MustCompile(`/vet(\.exe)? -`)

// vetDirs is the directory of each vet run in go test -x output: its
// package's. -x prints a cd line only when the directory changes, so each
// command runs in the last one printed.
func vetDirs(out []byte) []string {
	var dirs []string
	cwd := ""
	for _, line := range strings.Split(string(out), "\n") {
		if d, ok := strings.CutPrefix(line, "cd "); ok {
			cwd = d
		} else if vetRun.MatchString(line) {
			dirs = append(dirs, cwd)
		}
	}
	return dirs
}

// checkVetsOnlyItsOwnPackages runs a Check of repo on r's warm seed and
// fails unless its go test vets only the project's own packages. A
// Check's go test vets the package under test, and vet needs facts for
// every dependency. The warm step caches them with go test's own analyzer
// set, so a Check doesn't vet the standard library again (#112).
//
// Module-cache dependencies are still vetted again in every Check: Go
// keys a package outside GOROOT by its directory, and each Check has its
// own module cache path. See TODO(#112-decision) at warmCommand.
func checkVetsOnlyItsOwnPackages(t *testing.T, r *Runner, repo fakeRepo) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir()) // -x prints real paths
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "check")
	log := filepath.Join(tmp, "x.log")
	m := &Manifest{Commands: []Command{{Run: `go test -x ./... >"` + log + `" 2>&1`, TimeoutSec: 300, OutputCap: 1 << 10}}}
	res, err := r.Check(context.Background(), repo, m, "c", "", root)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(log)
	if !res.Pass {
		t.Fatalf("the Check failed: %+v\n%s", res.Commands, out)
	}
	dirs := vetDirs(out)
	if len(dirs) == 0 {
		t.Fatalf("the Check vetted nothing, though its own package is in a new directory; -x output changed?:\n%s", out)
	}
	tree := filepath.Join(root, "tree")
	for _, dir := range dirs {
		if dir != tree && !strings.HasPrefix(dir, tree+string(filepath.Separator)) {
			t.Errorf("the Check vetted %s; want only its own packages, under %s", dir, tree)
		}
	}
}

// markerFixture's package init and TestMain each leave a file in
// $OGE_TEST_MARKER when they run.
var markerFixture = fakeRepo{
	"go.mod": "module fx\n\ngo 1.22\n",
	"add.go": "package fx\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n)\n\n" +
		"func init() { os.WriteFile(filepath.Join(os.Getenv(\"OGE_TEST_MARKER\"), \"init\"), nil, 0o600) }\n\n" +
		"func Add(a, b int) int { return a + b }\n",
	"add_test.go": "package fx\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n\t\"testing\"\n)\n\n" +
		"func TestMain(m *testing.M) {\n\tos.WriteFile(filepath.Join(os.Getenv(\"OGE_TEST_MARKER\"), \"testmain\"), nil, 0o600)\n\tos.Exit(m.Run())\n}\n\n" +
		"func TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
}

// The warm step compiles and vets the Snapshot but never links or runs
// it: neither a package's init nor a TestMain runs. The same fixture run
// by a Check leaves both markers, so the warm step's silence isn't a
// fixture that never ran.
func TestWarmStepRunsNoRepositoryCode(t *testing.T) {
	r := newCacheRunner(t)
	marker := t.TempDir()
	r.PassEnv = []string{"OGE_TEST_MARKER"}
	r.Getenv = func(k string) string {
		if k == "OGE_TEST_MARKER" {
			return marker
		}
		return os.Getenv(k)
	}
	seed, _, err := r.NewSeed(context.Background(), markerFixture, "snap", "", filepath.Join(t.TempDir(), "seed"))
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if warm, err := seed.Wait(); err != nil || warm == nil || !warm.Pass {
		t.Fatalf("warm step = %+v, %v; want a passing execution", warm, err)
	}
	if left, _ := os.ReadDir(marker); len(left) != 0 {
		t.Fatalf("the warm step ran repository code: it left %v", left)
	}

	r.Seed = seed
	m := &Manifest{Commands: []Command{{Run: "go test ./...", TimeoutSec: 300, OutputCap: 1 << 10}}}
	res, err := r.Check(context.Background(), markerFixture, m, "c", "", filepath.Join(t.TempDir(), "check"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("the Check failed: %+v", res.Commands)
	}
	for _, f := range []string{"init", "testmain"} {
		if _, err := os.Stat(filepath.Join(marker, f)); err != nil {
			t.Errorf("the Check left no %s marker: the fixture doesn't show what it should (%v)", f, err)
		}
	}
}

// go splits its -toolexec value into words itself, so a wrapper path with
// a space or a quote is quoted for go.
func TestToolexecValueQuotesForGo(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{"/s/nolink.sh", "/bin/sh /s/nolink.sh"},
		{"/Application Support/nolink.sh", "/bin/sh '/Application Support/nolink.sh'"},
		{"/it's/nolink.sh", `/bin/sh "/it's/nolink.sh"`},
	} {
		if got, err := toolexecValue(c.path); err != nil || got != c.want {
			t.Errorf("toolexecValue(%q) = %q, %v; want %q", c.path, got, err, c.want)
		}
	}
	if got, err := toolexecValue(`/it's "x"/nolink.sh`); err == nil {
		t.Errorf("toolexecValue of a path with both quotes = %q; want an error", got)
	}
}
