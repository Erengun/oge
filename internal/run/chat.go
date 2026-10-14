package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/briefing"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// A Chat is bare oge's conversation (docs/ux/chat-first.md, slice 4): one
// Snapshot, one implementer Workspace and one long-lived implementer
// Session, with one Send per turn. Nothing cuts, checks or records a Chat
// log yet (slices 5 and 6), and nothing opens one but tests until the
// home is wired to it (slice 7).

// ChatFlag is the hidden environment flag a Chat opens behind: "1" opens
// one. TODO(#256-decision): slice 7 flips it when the home is wired.
const ChatFlag = "OGE_CHAT"

// DefaultIdleTimeout is how long a Chat Session may sit with no turn in
// flight before it is closed. It replaces StageTimeout for the Session.
const DefaultIdleTimeout = 30 * time.Minute

var (
	ErrChatDisabled = errors.New("chat isn't enabled (" + ChatFlag + ")")
	// ErrSessionReuse is D1: only an implementer Session may be reused.
	// A verifier's (QA's) Session is always fresh.
	ErrSessionReuse = errors.New("only an implementer Session may be reused")
	ErrChatClosed   = errors.New("the chat is closed")
	ErrChatIdle     = errors.New("the chat Session was idle too long and was closed")
	ErrChatLost     = errors.New("the chat Session ended")
)

// ChatParams is what a Chat opens from.
type ChatParams struct {
	Repo   string // the user's repository root: read only for the Snapshot
	Frozen *pipeline.Frozen
	Agents map[string]agent.Adapter
	State  *ledger.StateRoot
	Getenv func(string) string
	// Role is the Role kind whose Session the Chat keeps; "" is the
	// implementer, the only one allowed (D1).
	Role string
	// IdleTimeout is DefaultIdleTimeout when zero.
	IdleTimeout time.Duration
}

// Chat is an open Chat. Its methods are safe to call concurrently.
type Chat struct {
	ID        string
	Dir       string // its private directory
	Workspace string
	Snapshot  string

	role    string
	work    string // its disposable directory, the Workspace's parent
	brief   string
	instr   string
	deny    []string
	l       *ledger.Ledger
	sess    agent.Session
	idle    time.Duration
	pumped  chan struct{} // closed when the Session's events end
	stop    chan struct{} // closed by Close: events go undelivered
	closing sync.Once
	unctx   func() bool // stops the parent context closing the Chat

	mu    sync.Mutex
	turns int
	cur   *turnOut // the turn in flight's events
	// sending is the turn whose message is being written: if the Session
	// ends meanwhile, the write's outcome labels it, not drop.
	sending *turnOut
	queue   []queuedTurn
	err     error // why the Chat takes no more turns
	timer   *time.Timer
}

type queuedTurn struct {
	text string
	out  *turnOut
}

// turnOut is one turn's event channel. Only its methods send on it or
// close it, so a late event can't reach a closed channel.
type turnOut struct {
	mu     sync.Mutex
	ch     chan agent.Event
	closed bool
}

func newTurnOut() *turnOut { return &turnOut{ch: make(chan agent.Event, 64)} }

// send delivers ev unless the turn has ended or stop is closed.
func (t *turnOut) send(ev agent.Event, stop <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	select {
	case t.ch <- ev:
	case <-stop:
	}
}

// end closes the channel, after last if it fits without blocking.
func (t *turnOut) end(last *agent.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	if last != nil {
		select {
		case t.ch <- *last:
		default:
		}
	}
	t.closed = true
	close(t.ch)
}

// reuse is D1: a Session may take a second turn only as the implementer.
func reuse(role string) error {
	if role != "implementer" {
		return fmt.Errorf("%w: refusing to reuse the %s's Session", ErrSessionReuse, role)
	}
	return nil
}

// OpenChat takes the Snapshot into a Chat repository, makes the
// implementer Workspace as an implementer Attempt does, and starts the
// implementer Session in the agent's auto permission mode under Öge's
// hard denies (D2).
func OpenChat(ctx context.Context, p ChatParams) (c *Chat, err error) {
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv(ChatFlag) != "1" {
		return nil, ErrChatDisabled
	}
	role := p.Role
	if role == "" {
		role = "implementer"
	}
	if err := reuse(role); err != nil {
		return nil, err
	}
	var stage pipeline.Stage
	for _, s := range p.Frozen.Stages {
		if s.Role == role {
			stage = s
		}
	}
	adapter := p.Agents[stage.Agent]
	if adapter == nil {
		return nil, fmt.Errorf("%w for %s", ErrNoAdapter, stage.Agent)
	}
	why, err := workspace.Preflight(p.Repo)
	if err != nil {
		return nil, err
	}
	if len(why) > 0 {
		return nil, fmt.Errorf("refused: %s", strings.Join(why, "; "))
	}

	c = &Chat{ID: newID(), role: role, idle: p.IdleTimeout, pumped: make(chan struct{}), stop: make(chan struct{})}
	if c.idle <= 0 {
		c.idle = DefaultIdleTimeout
	}
	c.Dir = filepath.Join(p.State.Private, "chats", c.ID)
	c.work = filepath.Join(p.State.Work, "chats", c.ID)
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			if c.l != nil {
				c.l.Close()
			}
			oracle.RemoveAll(c.work)
		}
	}()
	if c.l, err = ledger.Create(c.Dir); err != nil {
		return nil, err
	}
	repo, err := workspace.InitRunRepo(filepath.Join(c.Dir, "repo.git"))
	if err != nil {
		return nil, err
	}
	c.Workspace = filepath.Join(c.work, "implement")
	snap, info, err := repo.TakeSnapshot(p.Repo, c.Workspace)
	if err != nil {
		return nil, err
	}
	c.Snapshot = snap
	if err := repo.InitWorkspaceGit(c.Workspace); err != nil {
		return nil, err
	}
	if err := c.l.Append(RecSnapshotTaken, map[string]any{"commit": snap, "head": info.Head, "branch": info.Branch,
		"modified": info.Modified, "untracked": info.Untracked}); err != nil {
		return nil, err
	}
	if c.instr, err = repoInstructions(repo, snap); err != nil {
		return nil, err
	}
	var checks []string
	for _, ch := range p.Frozen.Checks {
		checks = append(checks, ch.Run)
	}
	c.brief = briefing.Chat(checks)
	cache := filepath.Join(c.work, "cache", stage.Name)
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, err
	}
	// Öge's private state and QA's directory, where held-out tests are
	// written: the hard denies on top of the auto mode.
	c.deny = []string{p.State.Private, filepath.Join(c.work, qaDir)}
	spec := agent.LaunchSpec{
		Role: role, Model: stage.Model, Effort: stage.Effort, Profile: stage.Profile, Workspace: c.Workspace,
		Network: stage.Network, RunID: c.ID, RepoInstructions: c.instr, CheckCommands: checks,
		DenyRead: c.deny, Cache: cache, Permissions: agent.AutoPermissions,
		StateRoots: []string{p.State.Dir, p.State.Private, p.State.Work},
	}
	if c.sess, err = adapter.Open(ctx, spec); err != nil {
		return nil, err
	}
	proc := c.sess.Process()
	if err := c.l.Append(RecProcessStarted, map[string]any{"chat": c.ID, "pid": proc.PID, "pgid": proc.PGID,
		"launch_profile": launchProfile(stage), "permissions": agent.AutoPermissions}); err != nil {
		c.sess.Close()
		return nil, err
	}
	c.mu.Lock()
	c.timer = time.AfterFunc(c.idle, c.idleOut)
	c.mu.Unlock()
	go c.pump()
	// The Chat lives no longer than its parent context.
	c.mu.Lock()
	c.unctx = context.AfterFunc(ctx, func() { c.Close() })
	c.mu.Unlock()
	return c, nil
}

// Send sends one user turn and returns its events, which end with the
// turn's TurnSettled and are then closed. A turn sent while another is in
// flight queues: nothing enters a running turn. The caller must drain the
// channel, and answers an Ask (an event with Ask set) for the turn to go
// on.
func (c *Chat) Send(ctx context.Context, text string) (<-chan agent.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := newTurnOut()
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	if c.cur != nil {
		c.queue = append(c.queue, queuedTurn{text, out})
		c.mu.Unlock()
		return out.ch, nil
	}
	err := c.start(text, out)
	return out.ch, err
}

// start begins a turn with c.mu held, and releases it.
func (c *Chat) start(text string, out *turnOut) error {
	c.turns++
	n := c.turns
	c.cur, c.sending = out, out
	c.timer.Stop()
	c.mu.Unlock()
	sent := text
	if n == 1 {
		sent = text + c.brief
	}
	var err error
	if n > 1 {
		err = reuse(c.role) // D1, again at the point of reuse
	}
	if err == nil {
		// The manifest is written ahead of the turn (ADR-0009 as amended).
		err = c.l.Append(RecBriefingManifest, c.manifest(n, text, sent))
	}
	if err == nil {
		err = c.sess.Send(agent.Turn{Text: sent})
	}
	c.mu.Lock()
	c.sending = nil
	ended := err != nil || c.err != nil
	if err != nil {
		out.end(&agent.Event{Kind: agent.TurnSettled, Failure: "send_failed: " + err.Error()})
	} else if c.err != nil {
		out.end(&agent.Event{Kind: agent.TurnSettled, Failure: "chat_ended: " + c.err.Error()})
	}
	c.mu.Unlock()
	if ended {
		c.settled(out)
	}
	return err
}

// manifest is a turn's Briefing-manifest entry. The first turn carries the
// chat Briefing in a fresh Session; every later one is the user's message
// verbatim, in the same Session (D1, disclosed).
func (c *Chat) manifest(n int, msg, sent string) map[string]any {
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	if n == 1 {
		a := &Attempt{ID: fmt.Sprintf("chat#%d", n), Role: "implementer"}
		m := briefingManifest(Params{Task: task.Task{Text: msg}}, a, attemptSpec{start: c.Snapshot}, sent, c.instr, c.deny)
		m["chat"], m["turn"], m["session_reused"] = c.ID, n, false
		return m
	}
	return map[string]any{"chat": c.ID, "turn": n, "attempt": fmt.Sprintf("chat#%d", n), "role": "implementer",
		"items":   []map[string]string{{"item": "turn", "source": "the user's message, verbatim", "sha256": sum(msg)}},
		"session": "reused", "session_reused": true}
}

// pump routes the Session's events to the turn in flight.
func (c *Chat) pump() {
	defer close(c.pumped)
	for ev := range c.sess.Events() {
		c.mu.Lock()
		out := c.cur
		c.mu.Unlock()
		if out == nil {
			continue
		}
		out.send(ev, c.stop)
		if ev.Kind == agent.TurnSettled {
			c.settled(out)
		}
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = ErrChatLost
	}
	c.drop()
	c.mu.Unlock()
}

// settled ends turn t and starts the next queued one, or starts the
// idle clock.
func (c *Chat) settled(t *turnOut) {
	t.end(nil)
	c.mu.Lock()
	if c.cur != t {
		c.mu.Unlock()
		return // already dropped
	}
	c.cur = nil
	if c.err != nil {
		c.drop()
		c.mu.Unlock()
		return
	}
	if len(c.queue) > 0 {
		q := c.queue[0]
		c.queue = c.queue[1:]
		_ = c.start(q.text, q.out)
		return
	}
	c.timer.Reset(c.idle)
	c.mu.Unlock()
}

// drop settles the turn in flight and every queued one as failed, with
// c.mu held and c.err set.
func (c *Chat) drop() {
	last := &agent.Event{Kind: agent.TurnSettled, Failure: "chat_ended: " + c.err.Error()}
	if c.cur != nil && c.cur != c.sending {
		c.cur.end(last)
	}
	c.cur = nil
	for _, q := range c.queue {
		q.out.end(last)
	}
	c.queue = nil
}

func (c *Chat) idleOut() {
	c.mu.Lock()
	if c.cur != nil || c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = ErrChatIdle
	c.mu.Unlock()
	_ = c.sess.Close()
}

// Interrupt ends the turn in flight without ending the Session; queued
// turns then run. Idle, it does nothing.
func (c *Chat) Interrupt() error {
	c.mu.Lock()
	busy := c.cur != nil
	c.mu.Unlock()
	if !busy {
		return nil
	}
	return c.sess.Interrupt()
}

// Err is why the Chat takes no more turns: ErrChatIdle after the idle
// timeout, ErrChatClosed after Close, ErrChatLost when the Session ended
// on its own. nil while it is open.
func (c *Chat) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Done is closed once the Session has ended.
func (c *Chat) Done() <-chan struct{} { return c.pumped }

// Close ends the Session and removes the Workspace, which is disposable.
func (c *Chat) Close() error {
	c.closing.Do(func() {
		c.mu.Lock()
		if c.err == nil {
			c.err = ErrChatClosed
		}
		c.timer.Stop()
		unctx := c.unctx
		c.mu.Unlock()
		if unctx != nil {
			unctx()
		}
		close(c.stop)
		_ = c.sess.Close()
		<-c.pumped
		c.l.Close()
		oracle.RemoveAll(c.work)
	})
	return nil
}
