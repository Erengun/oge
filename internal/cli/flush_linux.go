package cli

import "golang.org/x/sys/unix"

// flushInput discards terminal input not yet read (TCIFLUSH).
func flushInput(fd int) { _ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }
