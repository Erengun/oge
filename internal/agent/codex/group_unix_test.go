//go:build unix

package codex

import (
	"syscall"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

// codex runs in its own process group, never Öge's: a hangup to Öge's
// group doesn't reach it, and Öge kills the whole group (#55). The other
// adapters' groups are asserted in internal/proc.
func TestCodexRunsInItsOwnGroup(t *testing.T) {
	h := &harness{t: t}
	script := probeThen(t, "command_approval.jsonl")
	asImplementer(t, script)
	s, err := h.open(script, agent.LaunchSpec{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pid := s.Process().PID
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("pid %d: %v", pid, err)
	}
	if pgid == syscall.Getpgrp() || pgid != pid {
		t.Errorf("pid %d is in group %d; Öge's is %d", pid, pgid, syscall.Getpgrp())
	}
}
