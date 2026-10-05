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

// Warm fills dir with the build cache a small Go test package needs, the
// way Öge's warm step would: compiled, never linked or run, and offline.
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
	cmd := exec.Command("go", "list", "-e", "-export", "-deps", "-test", "./...")
	cmd.Dir = mod
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GOCACHE=" + filepath.Join(dir, "gocache"),
		"GOPATH=" + filepath.Join(dir, "gopath"), "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("warming the test cache seed: %v\n%s", err, out)
	}
	return nil
}

// GoCache is the warmed build cache under dir.
func GoCache(dir string) string { return filepath.Join(dir, "gocache") }
