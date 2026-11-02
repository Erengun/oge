//go:build unix

package run

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether pid names a process, ours or not (EPERM
// means it exists).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// sweepGroup kills the process group pgid when pid is still the process
// a Chat log recorded: it started at start, and it leads that group.
func sweepGroup(pid, pgid int, start string) bool {
	if start == "" || pid <= 1 || pgid <= 1 || pid == os.Getpid() || pgid == syscall.Getpgrp() {
		return false
	}
	if processStart(pid) != start {
		return false // gone, or the pid belongs to another process now
	}
	if g, err := syscall.Getpgid(pid); err != nil || g != pgid {
		return false
	}
	return syscall.Kill(-pgid, syscall.SIGKILL) == nil
}
