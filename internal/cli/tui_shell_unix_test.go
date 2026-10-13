//go:build unix

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Cancelling stops the whole process group, not just the shell: a
// background job that ignores SIGINT is killed after the grace period.
func TestRunShellCancelKillsTheGroup(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	res := runShell(ctx, dir, "sleep 30 & echo $! > pid; wait")
	if res.err == nil {
		t.Fatalf("not stopped: %+v", res)
	}
	b, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a grandchild outlived the cancel")
		}
	}
}
