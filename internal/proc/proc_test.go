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

// WaitGone returns once a killed group's last process is gone, including
// a background child the leader left behind.
func TestWaitGoneWaitsForTheWholeGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 >/dev/null 2>&1 &")
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	pgid := Group(cmd)
	_ = Wait(cmd) // the leader exits at once; its sleep stays in the group
	if WaitGone(pgid, 50*time.Millisecond) {
		t.Fatal("the group emptied while its sleep ran")
	}
	Kill(cmd)
	if !WaitGone(pgid, 5*time.Second) {
		t.Fatal("the killed group never emptied")
	}
}
