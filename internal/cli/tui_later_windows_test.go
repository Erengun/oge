//go:build windows

package cli

import (
	"os"
	"testing"
)

// checkPrivateMode has nothing to check on Windows, where Unix modes
// don't apply; the file must still exist.
func checkPrivateMode(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Error(err)
	}
}
