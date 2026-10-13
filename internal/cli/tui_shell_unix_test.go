//go:build unix

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Cancelling kills the whole process group, not just the shell.
func TestRunShellCancelKillsTheGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "survived")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	res := runShell(ctx, dir, "(sleep 2; echo x > survived) & sleep 30")
	if res.err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("not stopped: %+v after %v", res, time.Since(start))
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a grandchild outlived the cancel")
	}
}
