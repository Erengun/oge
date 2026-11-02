//go:build unix

package cli

import (
	"os"
	"testing"
)

// checkPrivateMode checks the Later store is readable by its owner only.
func checkPrivateMode(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("%s: mode %v, %v", p, fi.Mode().Perm(), err)
	}
}
