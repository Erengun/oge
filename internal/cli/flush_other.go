//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package cli

// flushInput is a no-op where Öge can't flush terminal input.
func flushInput(int) {}
