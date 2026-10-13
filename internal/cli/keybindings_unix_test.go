//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A FIFO as the file must not hang oge.
func TestKeybindingsFifoIsAProblem(t *testing.T) {
	env := keysEnv(t, "")
	p := keybindingsPath(env)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip(err)
	}
	if _, n := loadKeybindings(env); !strings.Contains(n, "not a regular file") {
		t.Errorf("notice %q for a FIFO", n)
	}
}
