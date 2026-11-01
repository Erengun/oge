//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// redirectStdio points stdout and stderr at path, a 0600 file opened for
// appending: after a hangup a write to the dead terminal would fail.
func redirectStdio(path string) error {
	return redirectFDs(path, int(os.Stdout.Fd()), int(os.Stderr.Fd()))
}

func redirectFDs(path string, fds ...int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, fd := range fds {
		if err := unix.Dup2(int(f.Fd()), fd); err != nil {
			return err
		}
	}
	return nil
}
