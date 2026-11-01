package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/ledger"
)

// An adopted native session runs as an in-place Chat (docs/ux/sessions.md,
// ADR-0025): the agent's own session, resumed with its original settings,
// in its real working directory. Öge makes no Snapshot, Workspace, Oracle
// or Candidate for it, so there is nothing to scope-check, cut or apply,
// and nothing here claims the work was isolated.

// ErrInPlace is what a cut, an apply or a Consultation says of an in-place
// Chat.
var ErrInPlace = errors.New("this Chat runs in place on your working tree: nothing is isolated, so there is no cut or apply")

// ErrInPlaceIdle is an adopted session that idled out or ended: it is
// adopted again, not reopened, so the one-writer check runs again.
var ErrInPlaceIdle = errors.New("the adopted session ended or was idle and closed: adopt it again from /sessions")

// RecCoverageReported is what the agent reported of its own permission
// settings at start, and the coverage line Öge showed for it.
const RecCoverageReported = "CoverageReported"

// InPlaceParams is what an in-place Chat opens from.
type InPlaceParams struct {
	Adapter  agent.Adapter
	Provider string // "claude", "codex"
	ID       string // the agent's own session id
	Cwd      string // the session's real working directory
	Profile  string // the session's profile name; "" for the default
	State    *ledger.StateRoot
	// IdleTimeout is DefaultIdleTimeout when zero.
	IdleTimeout time.Duration
}

// OpenInPlaceChat resumes a native session, in place.
func OpenInPlaceChat(ctx context.Context, p InPlaceParams) (c *Chat, err error) {
	if p.Adapter == nil || p.ID == "" || !filepath.IsAbs(p.Cwd) {
		return nil, errors.New("an adopted session needs its adapter, id and working directory")
	}
	if st, err := os.Stat(p.Cwd); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("the session's working directory %s is gone", p.Cwd)
	}
	c = &Chat{ID: newID(), role: "implementer", idle: p.IdleTimeout, pumped: make(chan struct{}), stop: make(chan struct{}),
		adapter: p.Adapter, ctx: ctx, inPlace: true, source: p.Cwd, Workspace: p.Cwd, state: p.State,
		unsent: map[int]bool{}, oge: map[int]bool{}, advised: map[int]bool{}}
	if c.idle <= 0 {
		c.idle = DefaultIdleTimeout
	}
	c.Dir = filepath.Join(p.State.Private, "chats", c.ID)
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil && c.l != nil {
			c.l.Close()
		}
	}()
	if c.l, err = ledger.Create(c.Dir); err != nil {
		return nil, err
	}
	if c.blobs, err = ledger.OpenBlobs(c.Dir); err != nil {
		return nil, err
	}
	if err := c.log(RecChatStarted, map[string]any{"chat": c.ID, "created": time.Now().UTC(), "source": p.Cwd,
		"in_place": true, "provider": p.Provider, "native_id": p.ID, "role": "implementer"}); err != nil {
		return nil, err
	}
	c.spec = agent.LaunchSpec{Role: "implementer", Workspace: p.Cwd, Profile: p.Profile, Resume: &agent.Resume{ID: p.ID}}
	if c.sess, err = p.Adapter.Open(ctx, c.spec); err != nil {
		return nil, err
	}
	proc := c.sess.Process()
	if err := c.log(RecProcessStarted, map[string]any{"chat": c.ID, "pid": proc.PID, "pgid": proc.PGID,
		"in_place": true, "permissions": "the session's own settings"}); err != nil {
		c.sess.Close()
		return nil, err
	}
	c.mu.Lock()
	c.timer = time.AfterFunc(c.idle, c.idleOut)
	c.mu.Unlock()
	go c.pump(c.sess, c.pumped)
	c.mu.Lock()
	c.unctx = context.AfterFunc(ctx, func() { c.Close() })
	c.mu.Unlock()
	return c, nil
}

// InPlace reports whether the Chat is an adopted session running on the
// user's working tree.
func (c *Chat) InPlace() bool { return c.inPlace }

// RecordCoverage writes the coverage line Öge showed to the Chat log,
// with what the agent reported. It never records supervision.
func (c *Chat) RecordCoverage(line, mode, sandbox string) error {
	return c.log(RecCoverageReported, map[string]any{"chat": c.ID, "line": line, "mode": mode, "sandbox": sandbox})
}
