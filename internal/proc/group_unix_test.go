//go:build unix

package proc_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/agy"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/agent/fake"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
)

// Every agent runs in its own process group, never Öge's: a hangup to
// Öge's group doesn't reach it, and Öge kills the whole group (#55,
// Away slice 2). Codex's is asserted in its own tests, which hold its
// scripted handshake (TestCodexRunsInItsOwnGroup).
func TestAdaptersRunInTheirOwnGroup(t *testing.T) {
	t.Parallel()
	scen := filepath.Join(t.TempDir(), "scenario.json")
	if err := (multiturn.Scenario{}).Save(scen); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(script, []byte("echo '{\"say\":\"hi\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		a    agent.Adapter
	}{
		{"fake", fake.New(script)},
		{"claude", &claude.Adapter{Path: multiturn.BuildClaude(t), CloseGrace: time.Second, StartTimeout: 10 * time.Second,
			Environ: func() []string { return append(os.Environ(), "OGE_FAKE_CLAUDE_SCENARIO="+scen) }}},
		{"agy", &agy.Adapter{Path: multiturn.BuildAgy(t), Timeout: 5 * time.Second, CloseGrace: time.Second,
			Environ: os.Environ}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, err := c.a.Open(context.Background(), agent.LaunchSpec{Role: "implementer", Workspace: t.TempDir()})
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
				t.Errorf("%s: pid %d is in group %d; Öge's is %d", c.name, pid, pgid, syscall.Getpgrp())
			}
		})
	}
}

// A Check command runs in its own process group too.
func TestChecksRunInTheirOwnGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blobs, err := ledger.OpenBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	r := &oracle.Runner{Blobs: blobs, Getenv: os.Getenv}
	out := filepath.Join(dir, "pgid")
	e, _, err := r.Exec(context.Background(), "ps -o pgid= -p $$ > "+out, dir, nil, 10*time.Second, 1<<20)
	if err != nil || !e.Pass {
		t.Fatalf("the Check didn't run: %+v %v", e, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("ps printed %q", b)
	}
	if pgid == syscall.Getpgrp() {
		t.Errorf("the Check ran in Öge's group %d", pgid)
	}
}
