//go:build unix

package proc

import (
	"os/exec"
	"syscall"
	"time"
)

func setGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	setDeathSignal(cmd.SysProcAttr)
}

func killGroup(cmd *exec.Cmd) {
	pgid := -cmd.Process.Pid
	// A fork already in progress can publish its child after SIGKILL
	// walked the group. Sweep again while it still exists, bounded so an
	// unreaped zombie cannot hold up the caller that must call Wait.
	// Escaped groups are not contained; callers still check WaitGone.
	deadline := time.Now().Add(50 * time.Millisecond)
	for {
		if err := syscall.Kill(pgid, syscall.SIGKILL); err != nil {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func group(cmd *exec.Cmd) int {
	g, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Pid // Setpgid made it the group leader
	}
	return g
}

// groupGone reports whether no process is left in group pgid.
func groupGone(pgid int) bool {
	return syscall.Kill(-pgid, 0) == syscall.ESRCH
}

func termGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

func interruptGroup(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}
