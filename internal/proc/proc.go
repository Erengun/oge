// Package proc starts processes in their own process group and kills the
// whole group. It holds the only platform-specific code.
package proc

import (
	"os/exec"
	"sync"
)

// live are the commands started and not yet waited for, so KillAll can
// reach them when Öge has to stop without waiting.
var (
	mu   sync.Mutex
	live = map[*exec.Cmd]bool{}
)

// Start starts cmd in a new process group. Wait for it with Wait.
func Start(cmd *exec.Cmd) error {
	setGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	mu.Lock()
	live[cmd] = true
	mu.Unlock()
	return nil
}

// Wait waits for cmd, then forgets it.
func Wait(cmd *exec.Cmd) error {
	err := cmd.Wait()
	mu.Lock()
	delete(live, cmd)
	mu.Unlock()
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

// Terminate sends SIGTERM to cmd's process group, so its processes can end
// cleanly before a Kill; where groups aren't supported it kills the
// process.
func Terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		termGroup(cmd)
	}
}
