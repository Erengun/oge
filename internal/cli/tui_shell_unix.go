//go:build unix

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

// setGroup puts the command in a process group of its own, so Ctrl+C can
// end everything it started.
func setGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// interruptGroup asks the group to stop; killGroup does not ask.
func interruptGroup(cmd *exec.Cmd) error { return signalGroup(cmd, syscall.SIGINT) }

func killGroup(cmd *exec.Cmd) error { return signalGroup(cmd, syscall.SIGKILL) }

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if err == syscall.ESRCH {
		return os.ErrProcessDone
	}
	return err
}
