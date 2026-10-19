//go:build unix

package run

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// processAlive reports whether pid names a process, ours or not (EPERM
// means it exists).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// lockRun takes the Run's exclusive lock, nonblocking: a second Start or
// Resume of the same Run fails instead of racing its Ledger.
func lockRun(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return errLocked
		}
		return err
	}
	return nil
}

func unlockRun(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// runLocked reports whether dir's Run lock is held, without waiting.
func runLocked(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, liveLock), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil
}
