//go:build unix

package delivery

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockTree holds the working tree at target for one apply's plan and
// write, across processes: two Chats, or two oge processes, applying to
// one repository take turns, so the second plans on what the first
// wrote. It waits up to treeLockWait, then refuses. The OS drops the
// lock when its process dies, so none is ever left behind. The file
// itself stays: removing a locked file would let the next apply lock a
// new one beside a holder of the old.
func lockTree(r *Run, target string) (func(), error) {
	p := treeLockPath(r, target)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(treeLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, refuse(treeBusy)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
