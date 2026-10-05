//go:build unix

package run

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid names a process, ours or not (EPERM
// means it exists).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
