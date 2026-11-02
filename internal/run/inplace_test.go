package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/ledger"
)

type inPlaceAdapter struct {
	spec  agent.LaunchSpec
	opens int
}

func (a *inPlaceAdapter) Open(_ context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	a.spec = spec
	a.opens++
	return &inPlaceSession{ev: make(chan agent.Event)}, nil
}

type inPlaceSession struct{ ev chan agent.Event }

func (s *inPlaceSession) Process() agent.Process     { return agent.Process{} }
func (s *inPlaceSession) Send(agent.Turn) error      { return nil }
func (s *inPlaceSession) Events() <-chan agent.Event { return s.ev }
func (s *inPlaceSession) Interrupt() error           { return nil }
func (s *inPlaceSession) Close() error               { close(s.ev); return nil }

// An in-place Chat runs where the session lives, makes no Workspace and
// no Snapshot, refuses a cut, and closing it leaves the directory alone.
func TestInPlaceChatLeavesTheDirectoryAlone(t *testing.T) {
	cwd := t.TempDir()
	keep := filepath.Join(cwd, "mine.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &ledger.StateRoot{Dir: t.TempDir(), Private: t.TempDir(), Work: filepath.Join(t.TempDir(), "work")}
	ad := &inPlaceAdapter{}
	c, err := OpenInPlaceChat(context.Background(), InPlaceParams{Adapter: ad, Provider: "claude", ID: "s1", Cwd: cwd, Profile: "work", State: st})
	if err != nil {
		t.Fatal(err)
	}
	if ad.spec.Resume == nil || ad.spec.Resume.ID != "s1" || ad.spec.Workspace != cwd || ad.spec.Profile != "work" || ad.spec.Permissions != "" {
		t.Errorf("opened with %+v", ad.spec)
	}
	if !c.InPlace() || c.Snapshot != "" || c.Workspace != cwd {
		t.Errorf("in place %v, Snapshot %q, Workspace %q", c.InPlace(), c.Snapshot, c.Workspace)
	}
	if _, err := c.Cut(context.Background(), CutParams{}); !errors.Is(err, ErrInPlace) {
		t.Errorf("Cut: %v", err)
	}
	if _, err := c.Consult(context.Background(), "q"); !errors.Is(err, ErrInPlace) {
		t.Errorf("Consult: %v", err)
	}
	// Idle, it is adopted again, never reopened: the one-writer check runs again.
	c.mu.Lock()
	c.err = ErrChatIdle
	c.mu.Unlock()
	if _, err := c.Send(context.Background(), "again"); !errors.Is(err, ErrInPlaceIdle) || ad.opens != 1 {
		t.Errorf("Send after idle: %v, opened %d times", err, ad.opens)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "mine" {
		t.Errorf("the user's file after Close: %q, %v", b, err)
	}
	recs, err := ledger.Replay(c.Dir)
	if err != nil || len(recs) == 0 || recs[0].Type != RecChatStarted {
		t.Fatalf("log: %v %v", recs, err)
	}
}
