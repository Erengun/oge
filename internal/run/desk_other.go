//go:build !unix

package run

import "os"

// lockDesk has no cross-process lock off unix: Öge refuses Windows
// (ADR-0017), and this keeps the package building there.
func lockDesk(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
}

func unlockDesk(f *os.File) { f.Close() }

func processStart(int) string { return "" }
