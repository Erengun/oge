package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
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

// Checks on demand run the Check commands in the user's own directory,
// are logged as not isolated, and make no Candidate, Snapshot or Receipt.
func TestChecksOnTreeRunInPlaceAndClaimNoIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	cwd := t.TempDir()
	st := &ledger.StateRoot{Dir: t.TempDir(), Private: t.TempDir(), Work: filepath.Join(t.TempDir(), "work")}
	c, err := OpenInPlaceChat(context.Background(), InPlaceParams{Adapter: &inPlaceAdapter{}, Provider: "codex", ID: "s1", Cwd: cwd, State: st})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f := &pipeline.Frozen{Checks: []pipeline.CheckCommand{
		{Run: "echo ok > touched.txt", Timeout: time.Minute, OutputCap: 1 << 16},
		{Run: "echo broken >&2; exit 2", Timeout: time.Minute, OutputCap: 1 << 16},
	}}
	res, err := c.ChecksOnTree(context.Background(), TreeChecksParams{Frozen: f, Getenv: func(k string) string {
		if k == "PATH" {
			return os.Getenv("PATH")
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass || len(res.Commands) != 2 || !res.Commands[0].Pass || res.Commands[1].Pass || res.Commands[1].ExitCode != 2 {
		t.Fatalf("results %+v", res.Commands)
	}
	if res.Dir != cwd || res.Commands[0].Cwd != cwd {
		t.Errorf("ran in %q / %q, want %q", res.Dir, res.Commands[0].Cwd, cwd)
	}
	if _, err := os.Stat(filepath.Join(cwd, "touched.txt")); err != nil {
		t.Errorf("the command didn't run on the user's tree: %v", err)
	}
	if len(res.Tails[0]) != 0 || len(res.Tails[1]) == 0 || res.Tails[1][0] != "broken" {
		t.Errorf("tails %q", res.Tails)
	}
	recs, err := ledger.Replay(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var logged bool
	for _, r := range recs {
		switch r.Type {
		case RecTreeChecks:
			logged = true
			var d map[string]any
			if err := json.Unmarshal(r.Data, &d); err != nil || d["isolated"] != false || d["in_place"] != true {
				t.Errorf("record %v %v", d, err)
			}
		case RecSnapshotTaken, RecOracleVersion, RecDelivered:
			t.Errorf("an isolation record in an in-place Chat: %s", r.Type)
		}
	}
	if !logged {
		t.Error("the run isn't in the Chat log")
	}
	// An isolated Chat refuses it.
	if _, err := (&Chat{}).ChecksOnTree(context.Background(), TreeChecksParams{Frozen: f}); err == nil {
		t.Error("an isolated Chat ran Checks on demand")
	}
}
