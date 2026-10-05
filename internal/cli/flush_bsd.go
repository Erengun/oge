//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package cli

import "golang.org/x/sys/unix"

// flushInput discards terminal input not yet read (TCIFLUSH).
func flushInput(fd int) { _ = unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, unix.TCIFLUSH) }
