//go:build linux

package proc

import (
	"os/exec"
	"runtime"
	"syscall"
)

// setDeathSignal makes the kernel SIGKILL the group leader when Öge dies,
// even by SIGKILL, so an agent or a Check never outlives it in its own
// process group (#55). The leader's children still need the group kill.
func setDeathSignal(a *syscall.SysProcAttr) {
	a.Pdeathsig = syscall.SIGKILL
}

// forkedOn, when set (by tests), is told the forking thread's id.
var forkedOn func(tid int)

// forkStart starts cmd from a goroutine locked to its thread, and keeps
// that thread until Wait has reaped the child. The death signal fires
// when the thread that forked the child ends, not the process
// (go.dev/issue/27505), and Go ends a thread when a goroutine exits still
// locked to it. A pool thread could be ended that way by any code, so the
// forking thread never goes back to the pool: the goroutine exits locked
// once the child is reaped, and the thread ends with it. If that thread is
// the main one, Go parks it for good instead, which costs one idle thread.
func forkStart(cmd *exec.Cmd) (release func(), err error) {
	started := make(chan error, 1)
	reaped := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		if forkedOn != nil {
			forkedOn(syscall.Gettid())
		}
		err := cmd.Start()
		started <- err
		if err == nil {
			<-reaped
		}
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return func() { close(reaped) }, nil
}
