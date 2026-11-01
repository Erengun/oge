//go:build !linux

package proc

import "os/exec"

// forkStart starts cmd. Only Linux has a death signal to hold a thread for.
func forkStart(cmd *exec.Cmd) (release func(), err error) {
	return nil, cmd.Start()
}
