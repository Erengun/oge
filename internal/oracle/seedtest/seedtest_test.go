package seedtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// A Check's go test vets the package under test, and vet needs facts
// about every dependency, the standard library's included. A seed that
// holds those facts lets each Check vet only its own packages; without
// them every Check re-vets the standard library, which costs more than
// compiling and linking the test binary.
func TestWarmSeedHoldsTheStandardLibrarysVetFacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Checks run through /bin/sh; Windows refuses Runs (ADR-0017)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	if err := Warm(filepath.Join(dir, "seed")); err != nil {
		t.Fatal(err)
	}
	// A Check-like build: a different module, so nothing of its own is
	// cached, in a private copy of the seed.
	cache := filepath.Join(dir, "check-cache")
	if out, err := exec.Command("cp", "-R", GoCache(filepath.Join(dir, "seed")), cache).CombinedOutput(); err != nil {
		t.Fatalf("copying the seed: %v\n%s", err, out)
	}
	mod := filepath.Join(dir, "check")
	for name, s := range map[string]string{
		"go.mod":      "module other\n\ngo 1.22\n",
		"add.go":      "package other\n\nfunc Add(a, b int) int { return a + b }\n",
		"add_test.go": "package other\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2, 3) != 5\")\n\t}\n}\n",
	} {
		if err := os.MkdirAll(mod, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mod, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "test", "-x", "-json", "./...")
	cmd.Dir = mod
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GOCACHE=" + cache,
		"GOPATH=" + filepath.Join(dir, "gopath"), "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
	// -x prints each vet tool it runs; only the package under test should
	// need one.
	if n := len(vetRun.FindAllIndex(out, -1)); n != 1 {
		t.Errorf("the Check ran vet %d times; want only its own package vetted\n%s", n, out)
	}
}

// vetRun matches the vet tool's command line in go test -x output.
var vetRun = regexp.MustCompile(`/vet(\.exe)? -`)
