// Package proc starts processes in their own process group and kills the
// whole group. It holds the only platform-specific code.
package proc

import "os/exec"

// Start starts cmd in a new process group.
func Start(cmd *exec.Cmd) error {
	setGroup(cmd)
	return cmd.Start()
}

// Kill kills cmd's process group, or just the process where groups aren't
// supported.
func Kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		killGroup(cmd)
	}
}

// Group is the process group id of a started cmd, or 0 when unknown.
func Group(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	return group(cmd)
}
