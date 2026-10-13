//go:build !unix

package cli

import "os/exec"

func setGroup(*exec.Cmd) {}

// killGroup ends the command itself: Windows has no process group to
// signal here.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
