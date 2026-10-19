//go:build unix

package proc_test

import (
	"bufio"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/erengun/oge/internal/proc"
)

// The shell really forks children: replacing it with `exec sleep` would
// miss the race between a group kill and a fork in progress (#55).
func TestKillWhileTheShellForks(t *testing.T) {
	for i := range 40 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", "echo ready; for n in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16; do sleep 60 >/dev/null 2>&1 & done; wait")
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := proc.Start(cmd); err != nil {
				t.Fatal(err)
			}
			pgid := proc.Group(cmd)
			waited := false
			t.Cleanup(func() {
				if !waited {
					proc.Kill(cmd)
					_ = proc.Wait(cmd)
				}
				if proc.WaitGone(pgid, 0) {
					return
				}
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
				if !proc.WaitGone(pgid, 2*time.Second) {
					t.Errorf("iteration %d left a process group after cleanup", i)
				}
			})
			if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
				t.Fatalf("shell not ready: %q, %v", line, err)
			}
			// Shift the kill across the small fork window, without allowing
			// the shell to create an unbounded number of processes.
			time.Sleep(time.Duration(i%8) * 100 * time.Microsecond)
			proc.Kill(cmd)
			_ = proc.Wait(cmd)
			waited = true
			if !proc.WaitGone(pgid, 500*time.Millisecond) {
				t.Fatalf("iteration %d: a child survived Kill while its shell forked", i)
			}
		})
	}
}
