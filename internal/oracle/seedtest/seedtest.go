// Package seedtest warms a build cache for tests to pass as a cache seed
// template (cli.Env.CacheSeedTemplate), so each test Run doesn't compile
// the standard library again. Only tests import it.
package seedtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Warm fills dir with the build cache a small Go test package needs: the
// standard library compiled, the way Öge's warm step would, plus what a
// Check's go test adds, offline. go test vets the package under test,
// and vet needs facts about every dependency; with the standard
// library's in the seed, each test Check vets only its own packages
// instead of the whole standard library again, which costs more than
// compiling and linking its test binary.
func Warm(dir string) error {
	mod := filepath.Join(dir, "module")
	files := map[string]string{
		"go.mod":     "module fx\n\ngo 1.22\n",
		"fx.go":      "package fx\n",
		"fx_test.go": "package fx\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
	}
	for name, s := range files {
		if err := os.MkdirAll(mod, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(mod, name), []byte(s), 0o600); err != nil {
			return err
		}
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GOCACHE=" + filepath.Join(dir, "gocache"),
		"GOPATH=" + filepath.Join(dir, "gopath"), "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off"}
	for _, args := range [][]string{
		{"list", "-e", "-export", "-deps", "-test", "./..."},
		// go test's own vet, the analyzers a Check's go test runs.
		{"test", "-count=1", "-run=^$", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir, cmd.Env = mod, env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("warming the test cache seed: %v\n%s", err, out)
		}
	}
	return nil
}

// GoCache is the warmed build cache under dir.
func GoCache(dir string) string { return filepath.Join(dir, "gocache") }
