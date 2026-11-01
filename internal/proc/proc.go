// Package proc starts processes in their own process group and kills the
// whole group. It holds the only platform-specific code.
package proc

import (
	"os/exec"
	"sync"
	"time"
)

// live are the commands started and not yet waited for, so KillAll can
// reach them when Öge has to stop without waiting. Each holds what lets
// its forking thread go once it is reaped (nil where there is none).
var (
	mu   sync.Mutex
	live = map[*exec.Cmd]func(){}
)

// Start starts cmd in a new process group. Wait for it with Wait.
func Start(cmd *exec.Cmd) error {
	setGroup(cmd)
	release, err := forkStart(cmd)
	if err != nil {
		return err
	}
	mu.Lock()
	live[cmd] = release
	mu.Unlock()
	return nil
}

// Wait waits for cmd, then forgets it.
func Wait(cmd *exec.Cmd) error {
	err := cmd.Wait()
	mu.Lock()
	release := live[cmd]
	delete(live, cmd)
	mu.Unlock()
	if release != nil {
		release()
	}
	return err
}

// KillAll kills the process group of every command started and not yet
// waited for. A command being waited for still has its process, so its
// group id can't have been reused.
func KillAll() {
	mu.Lock()
	defer mu.Unlock()
	for cmd := range live {
		killGroup(cmd)
	}
}

// Kill kills cmd's process group, or just the process where groups aren't
// supported.
func Kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		killGroup(cmd)
	}
}

// Group is the process group id of a started cmd, or 0 when unknown.
func Group(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	return group(cmd)
}

// WaitGone waits until process group pgid has no process left, or the
// timeout passes, and reports whether it emptied. A zombie still counts
// until its parent reaps it.
func WaitGone(pgid int, timeout time.Duration) bool {
	if pgid <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for !groupGone(pgid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// Terminate sends SIGTERM to cmd's process group, so its processes can end
// cleanly before a Kill; where groups aren't supported it kills the
// process.
func Terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		termGroup(cmd)
	}
}

// Interrupt sends SIGINT to cmd's process group, so its processes can handle
// interruption cleanly; where groups aren't supported it signals the process.
func Interrupt(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return interruptGroup(cmd)
}
