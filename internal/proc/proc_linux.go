//go:build linux

package proc

import "syscall"

// setDeathSignal makes the kernel SIGKILL the group leader when Öge dies,
// even by SIGKILL, so an agent or a Check never outlives it in its own
// process group (#55). The leader's children still need the group kill.
func setDeathSignal(a *syscall.SysProcAttr) {
	a.Pdeathsig = syscall.SIGKILL
}
