//go:build unix

package run

import (
	"errors"
	"os"
	"syscall"
)

// lockDesk takes the Desk's lock at p without waiting, as lockTree does
// (internal/delivery): the OS drops it when its process dies, so a crash
// never leaves it held. The file stays: removing a locked file would let
// the next oge lock a new one beside the holder of the old.
func lockDesk(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errDeskHeld
		}
		return nil, err
	}
	return f, nil
}

func unlockDesk(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
}
