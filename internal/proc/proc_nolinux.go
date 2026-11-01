//go:build unix && !linux

package proc

import "syscall"

// No death signal outside Linux: macOS has no Pdeathsig.
func setDeathSignal(*syscall.SysProcAttr) {}
