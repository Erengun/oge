//go:build unix

package proc

import (
	"os/exec"
	"testing"
	"time"
)

// KillAll reaches a started command's whole group: the shell and the
// sleep it forked.
func TestKillAllKillsEveryLiveGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Wait(cmd) }()
	time.Sleep(100 * time.Millisecond)
	KillAll()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the command exited cleanly")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("KillAll didn't kill the command")
	}
	mu.Lock()
	n := len(live)
	mu.Unlock()
	if n != 0 {
		t.Errorf("%d commands still live after Wait", n)
	}
}
