//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// setGroup hands cmd /C its line as it is: Go's argument quoting would
// mangle the quotes cmd expects.
func setGroup(cmd *exec.Cmd) {
	if len(cmd.Args) == 3 && cmd.Args[0] == "cmd" && cmd.Args[1] == "/C" {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd /C " + cmd.Args[2]}
	}
}

func interruptGroup(cmd *exec.Cmd) error { return killGroup(cmd) }

// killGroup ends the command itself: there is no process group to signal here.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
