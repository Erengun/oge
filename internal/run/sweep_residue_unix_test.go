//go:build unix

package run

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// sleeper starts a process in a group of its own.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	return cmd
}

func dead(cmd *exec.Cmd) bool {
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

// The sweep kills a recorded group whose process is still the one
// recorded, and nothing it can't tell is its own.
func TestSweepResidueChecksIdentity(t *testing.T) {
	skip := sleeper(t)
	pid := skip.Process.Pid
	start := processStart(pid)
	if start == "" {
		t.Skip("no process start identity on this system")
	}
	for name, p := range map[string]recordedProc{
		"another process's start": {pid, pid, "1.1"},
		"no identity recorded":    {pid, pid, ""},
		"wrong group":             {pid, pid + 1, start},
		"our own process":         {os.Getpid(), syscall.Getpgrp(), processStart(os.Getpid())},
		"pid 1":                   {1, 1, processStart(1)},
	} {
		d := &RestoredDesk{Chats: []*RestoredChat{{ID: "x", procs: []recordedProc{p}}}}
		d.SweepResidue(nil)
		if len(d.Swept) != 0 {
			t.Errorf("%s: swept %+v", name, d.Swept)
		}
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("a process that wasn't ours to kill died: %v", err)
	}
	d := &RestoredDesk{Chats: []*RestoredChat{{ID: "x", procs: []recordedProc{{pid, pid, start}}}}}
	d.SweepResidue(nil)
	if len(d.Swept) != 1 || d.Swept[0].PGID != pid || !dead(skip) {
		t.Errorf("the recorded group lived: swept %+v", d.Swept)
	}
}
