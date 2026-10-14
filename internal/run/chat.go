package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/briefing"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/redact"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// A Chat is bare oge's conversation (docs/ux/chat-first.md, slice 4): one
// Snapshot, one implementer Workspace and one long-lived implementer
// Session, with one Send per turn. Nothing cuts, checks or records a Chat
// Its Chat log, in its private directory, is a Ledger written ahead
// (slice 5). After each turn the same scope check an implementer Attempt
// gets reverts and records protected writes, and a turn with changes is
// cut into a Run (slice 6, cut.go). Nothing opens a Chat but tests until
// the home is wired to it (slice 7).

// Chat log records (docs/ux/chat-first.md, "Ledger and Receipt"). A turn's
// scope check writes ScopeObserved, ScopeReverted and TamperEvent, as an
// Attempt's does, each with the turn.
const (
	RecChatStarted     = "ChatStarted"
	RecTurnStarted     = "TurnStarted"
	RecSessionReopened = "SessionReopened"
	RecHeldOutViewed   = "HeldOutViewed"
)

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
	// CacheSeedTemplate is a build cache the Chat's seed starts from. Only
	// tests set it; see Params.CacheSeedTemplate.
	CacheSeedTemplate string
}

// Chat is an open Chat. Its methods are safe to call concurrently.
type Chat struct {
	ID        string
	Dir       string // its private directory
	Workspace string
	Snapshot  string

	role      string
	work      string // its disposable directory, the Workspace's parent
	brief     string
	instr     string
	deny      []string
	checks    []string
	frozen    *pipeline.Frozen
	source    string // the user's repository
	info      workspace.SnapshotInfo
	state     *ledger.StateRoot
	agents    map[string]agent.Adapter
	getenv    func(string) string
	seedTmpl  string
	repo      *workspace.RunRepo
	blobs     *ledger.Blobs
	protected func(string) string // the implementer's protected set
	adapter   agent.Adapter
	spec      agent.LaunchSpec
	ctx       context.Context // the parent context, for a reopened Session
	idle      time.Duration
	stop      chan struct{} // closed by Close: events go undelivered
	closing   sync.Once
	unctx     func() bool // stops the parent context closing the Chat
	reopening sync.Mutex  // one Session reopens at a time

	logMu sync.Mutex // the Chat log takes one record at a time
	l     *ledger.Ledger

	// scopeMu serializes the scope checks. last is the commit of the
	// Workspace after the last turn's check, the Snapshot at first, and
	// reverted each turn's reverts, for its Tamper event ids.
	scopeMu  sync.Mutex
	last     string
	lastTurn int // the turn whose commit last is, 0 for the Snapshot
	reverted map[int][]workspace.Revert

	// The Preflight and the Snapshot control, started when the Chat opens
	// (cut.go): prepared is closed once both have finished.
	prepared   chan struct{}
	prep       chatPrep
	cancelPrep context.CancelFunc

	cutMu   sync.Mutex // one cut at a time
	lastCut *cutMark   // the last cut that reached a Run's walk

	mu     sync.Mutex
	sess   agent.Session
	pumped chan struct{} // closed when the Session's events end
	turns  int
	msgs   []string // the user's turns, verbatim
	// rebrief is set when the Session was reopened: its first turn gets
	// the re-brief.
	rebrief bool
	closed  bool
	cur     *turnOut // the turn in flight's events
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
// close it, so a late event can't reach a closed channel. Its record
// fields are the pump's.
type turnOut struct {
	mu     sync.Mutex
	ch     chan agent.Event
	closed bool

	n      int          // the turn's number
	rec    sync.Mutex   // guards the record below: the pump writes it
	stream bytes.Buffer // its events, for the AttemptEnded blob
	reply  []string     // what it said
	usage  *agent.Event // its last Usage
}

func newTurnOut() *turnOut { return &turnOut{ch: make(chan agent.Event, 64)} }

// record keeps ev for the turn's AttemptEnded.
func (t *turnOut) record(ev agent.Event) {
	t.rec.Lock()
	defer t.rec.Unlock()
	_ = json.NewEncoder(&t.stream).Encode(ev)
	switch {
	case ev.Kind == agent.Claim && ev.Tool == "":
		t.reply = append(t.reply, ev.Text)
	case ev.Kind == agent.Usage:
		t.usage = &ev
	}
}

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

	c = &Chat{ID: newID(), role: role, idle: p.IdleTimeout, pumped: make(chan struct{}), stop: make(chan struct{}),
		frozen: p.Frozen, adapter: adapter, ctx: ctx, reverted: map[int][]workspace.Revert{},
		source: p.Repo, state: p.State, agents: p.Agents, getenv: getenv, seedTmpl: p.CacheSeedTemplate}
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
	if c.blobs, err = ledger.OpenBlobs(c.Dir); err != nil {
		return nil, err
	}
	frozenJSON, err := json.Marshal(p.Frozen)
	if err != nil {
		return nil, err
	}
	frozenBlob, err := c.blobs.Put(frozenJSON)
	if err != nil {
		return nil, err
	}
	if err := c.log(RecChatStarted, map[string]any{"chat": c.ID, "created": time.Now().UTC(), "source": p.Repo,
		"frozen": frozenBlob, "graph_hash": p.Frozen.Hash, "mode": p.Frozen.Mode, "role": role}); err != nil {
		return nil, err
	}
	repo, err := workspace.InitRunRepo(filepath.Join(c.Dir, "repo.git"))
	if err != nil {
		return nil, err
	}
	c.repo = repo
	c.Workspace = filepath.Join(c.work, "implement")
	snap, info, err := repo.TakeSnapshot(p.Repo, c.Workspace)
	if err != nil {
		return nil, err
	}
	c.Snapshot, c.last, c.info = snap, snap, info
	if err := repo.InitWorkspaceGit(c.Workspace); err != nil {
		return nil, err
	}
	if err := c.log(RecSnapshotTaken, map[string]any{"commit": snap, "head": info.Head, "branch": info.Branch,
		"modified": info.Modified, "untracked": info.Untracked}); err != nil {
		return nil, err
	}
	// Each turn is held to the Oracle version and the config frozen here,
	// as an implementer Attempt is.
	m, mBlob, err := oracle.NewV0(repo, snap, p.Frozen, c.blobs)
	if err != nil {
		return nil, err
	}
	if err := c.log(RecOracleVersion, map[string]any{"version": m.Version, "manifest": mBlob, "tests": len(m.Tests)}); err != nil {
		return nil, err
	}
	c.protected = implementerScope(m, p.Frozen)
	if c.instr, err = repoInstructions(repo, snap); err != nil {
		return nil, err
	}
	var checks []string
	for _, ch := range p.Frozen.Checks {
		checks = append(checks, ch.Run)
	}
	c.checks = checks
	c.brief = briefing.Chat(checks)
	cache := filepath.Join(c.work, "cache", stage.Name)
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, err
	}
	// Öge's private state and QA's directories, where held-out tests are
	// written, a cut's included: the hard denies on top of the auto mode.
	c.deny = []string{p.State.Private, filepath.Join(c.work, qaDir), filepath.Join(c.work, cutsDir)}
	c.spec = agent.LaunchSpec{
		Role: role, Model: stage.Model, Effort: stage.Effort, Profile: stage.Profile, Workspace: c.Workspace,
		Network: stage.Network, RunID: c.ID, RepoInstructions: c.instr, CheckCommands: checks,
		DenyRead: c.deny, Cache: cache, Permissions: agent.AutoPermissions,
		StateRoots: []string{p.State.Dir, p.State.Private, p.State.Work},
	}
	if c.sess, err = adapter.Open(ctx, c.spec); err != nil {
		return nil, err
	}
	proc := c.sess.Process()
	if err := c.log(RecProcessStarted, map[string]any{"chat": c.ID, "pid": proc.PID, "pgid": proc.PGID,
		"launch_profile": launchProfile(stage), "permissions": agent.AutoPermissions}); err != nil {
		c.sess.Close()
		return nil, err
	}
	c.prepare(m, snap)
	c.mu.Lock()
	c.timer = time.AfterFunc(c.idle, c.idleOut)
	c.mu.Unlock()
	go c.pump(c.sess, c.pumped)
	// The Chat lives no longer than its parent context.
	c.mu.Lock()
	c.unctx = context.AfterFunc(ctx, func() { c.Close() })
	c.mu.Unlock()
	return c, nil
}

// log appends one record to the Chat log.
func (c *Chat) log(typ string, data any) error {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	return c.l.Append(typ, data)
}

// Send sends one user turn and returns its events, which end with the
// turn's TurnSettled and are then closed; the TurnSettled carries the
// turn's scope result. A turn sent while another is in flight queues:
// nothing enters a running turn. After an idle timeout or a crash, Send
// first reopens a fresh Session in the same Workspace, which the turn
// re-briefs (#185). The caller must drain the channel, and answers an Ask
// (an event with Ask set) for the turn to go on.
func (c *Chat) Send(ctx context.Context, text string) (<-chan agent.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if !c.closed && (errors.Is(c.err, ErrChatIdle) || errors.Is(c.err, ErrChatLost)) {
		c.mu.Unlock()
		if err := c.reopen(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
	}
	out := newTurnOut()
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

// reopen opens a fresh implementer Session in the same Workspace once the
// last one has ended (the spec's fallback after an idle timeout or a
// crash). The work survives; the conversation's memory doesn't.
func (c *Chat) reopen(ctx context.Context) error {
	c.reopening.Lock()
	defer c.reopening.Unlock()
	c.mu.Lock()
	why, pumped := c.err, c.pumped
	c.mu.Unlock()
	if !errors.Is(why, ErrChatIdle) && !errors.Is(why, ErrChatLost) {
		return nil // reopened meanwhile, or never ended
	}
	select {
	case <-pumped:
	case <-ctx.Done():
		return ctx.Err()
	}
	sess, err := c.adapter.Open(c.ctx, c.spec)
	if err != nil {
		return fmt.Errorf("reopening the chat Session: %w", err)
	}
	reason := "lost"
	if errors.Is(why, ErrChatIdle) {
		reason = "idle"
	}
	c.mu.Lock()
	next := c.turns + 1
	c.mu.Unlock()
	// SessionReopened is the new process's record, in place of
	// ProcessStarted, and is in the log before any turn reaches it.
	proc := sess.Process()
	if err := c.log(RecSessionReopened, map[string]any{"chat": c.ID, "reason": reason, "turn": next,
		"pid": proc.PID, "pgid": proc.PGID, "permissions": agent.AutoPermissions}); err != nil {
		_ = sess.Close()
		c.mu.Lock()
		if !c.closed {
			c.err = fmt.Errorf("the chat log failed: %w", err)
		}
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = sess.Close()
		return ErrChatClosed
	}
	c.sess, c.pumped, c.err, c.rebrief = sess, make(chan struct{}), nil, true
	pumped = c.pumped
	c.mu.Unlock()
	go c.pump(sess, pumped)
	return nil
}

// start begins a turn with c.mu held, and releases it. TurnStarted and
// the turn's manifest are written ahead of the message.
func (c *Chat) start(text string, out *turnOut) error {
	c.turns++
	n := c.turns
	out.n = n
	c.msgs = append(c.msgs, text)
	msgs := append([]string(nil), c.msgs...)
	fresh := n == 1 || c.rebrief
	c.rebrief = false
	sess := c.sess
	c.cur, c.sending = out, out
	c.timer.Stop()
	c.mu.Unlock()
	sent := text
	switch {
	case n == 1:
		sent = text + c.brief
	case fresh:
		sent = briefing.ChatReopened(task.Task{Text: msgs[0], FollowUps: msgs[1:]}, c.checks)
	}
	var err error
	if !fresh {
		err = reuse(c.role) // D1, again at the point of reuse
	}
	if err == nil {
		err = c.log(RecTurnStarted, map[string]any{"chat": c.ID, "turn": n, "message": sha256Hex(text)})
	}
	if err == nil {
		// The manifest is written ahead of the turn (ADR-0009 as amended).
		err = c.log(RecBriefingManifest, c.manifest(n, fresh, msgs, sent))
	}
	if err == nil {
		err = sess.Send(agent.Turn{Text: sent})
	}
	c.mu.Lock()
	var last *agent.Event
	// ran: the message reached the agent, which may have written before
	// its Session ended, so the turn gets its scope check.
	ran := false
	if err != nil {
		last = &agent.Event{Kind: agent.TurnSettled, Failure: "send_failed: " + err.Error()}
	} else if c.err != nil {
		last = &agent.Event{Kind: agent.TurnSettled, Failure: "chat_ended: " + c.err.Error()}
		ran = true
	} else {
		c.sending = nil
	}
	c.mu.Unlock()
	if last != nil {
		ev := c.finish(out, *last, ran)
		c.mu.Lock()
		c.sending = nil
		out.end(&ev)
		c.mu.Unlock()
		c.settled(out)
	}
	return err
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// manifest is a turn's Briefing-manifest entry. A fresh Session's first
// turn carries the chat Briefing (after a reopen, the re-brief with the
// user's turns so far); every later one is the user's message verbatim,
// in the same Session (D1, disclosed).
func (c *Chat) manifest(n int, fresh bool, msgs []string, sent string) map[string]any {
	if fresh {
		a := &Attempt{ID: fmt.Sprintf("chat#%d", n), Role: "implementer"}
		t := task.Task{Text: msgs[0], FollowUps: msgs[1:]}
		m := briefingManifest(Params{Task: t}, a, attemptSpec{start: c.Snapshot}, sent, c.instr, c.deny)
		m["chat"], m["turn"], m["session_reused"] = c.ID, n, false
		return m
	}
	return map[string]any{"chat": c.ID, "turn": n, "attempt": fmt.Sprintf("chat#%d", n), "role": "implementer",
		"items":   []map[string]string{{"item": "turn", "source": "the user's message, verbatim", "sha256": sha256Hex(msgs[n-1])}},
		"session": "reused", "session_reused": true}
}

// pump routes the Session's events to the turn in flight. A settled turn
// gets its scope check before its TurnSettled is delivered.
func (c *Chat) pump(sess agent.Session, pumped chan struct{}) {
	defer close(pumped)
	for ev := range sess.Events() {
		c.mu.Lock()
		out := c.cur
		c.mu.Unlock()
		if out == nil {
			continue
		}
		out.record(ev)
		if ev.Kind == agent.TurnSettled {
			ev = c.finish(out, ev, true)
			out.send(ev, c.stop)
			c.settled(out)
			continue
		}
		out.send(ev, c.stop)
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = ErrChatLost
	}
	cur, sending, why := c.cur, c.sending, c.err
	c.mu.Unlock()
	if cur != nil && cur != sending {
		// The agent ran in this turn: its writes are checked too.
		ev := c.finish(cur, agent.Event{Kind: agent.TurnSettled, Failure: "chat_ended: " + why.Error()}, true)
		cur.end(&ev)
	} else if cur == nil {
		c.finalScope()
	}
	c.mu.Lock()
	c.drop()
	c.mu.Unlock()
}

// finish writes a turn's end to the Chat log: when the agent ran, the
// scope check (ADR-0012: planned, then reverted, then each Tamper event),
// then AttemptEnded. It returns ev with the turn's scope result. A record
// the log can't take ends the Chat.
func (c *Chat) finish(out *turnOut, ev agent.Event, ran bool) agent.Event {
	a := &Attempt{ID: fmt.Sprintf("chat#%d", out.n), Role: c.role}
	err := func() error {
		var changes map[string]any
		if ran {
			ts, ch, err := c.checkScope(a, out.n)
			if err != nil {
				return err
			}
			ev.Scope, changes = ts, ch
		}
		out.rec.Lock()
		stream, reply, usage := append([]byte(nil), out.stream.Bytes()...), strings.Join(out.reply, "\n"), out.usage
		out.rec.Unlock()
		blob, err := c.blobs.Put(redact.Redact(stream))
		if err != nil {
			return err
		}
		ev.Failure = failAttempt(ev.Failure, a.Failure)
		ended := map[string]any{"chat": c.ID, "turn": out.n, "attempt": a.ID, "events": blob,
			"reply":   string(redact.Redact([]byte(reply))),
			"failure": string(redact.Redact([]byte(ev.Failure)))}
		if ev.Stop != "" {
			ended["stop"] = string(redact.Redact([]byte(ev.Stop)))
		}
		if u := usage; u != nil {
			ended["usage"] = map[string]any{"cost_usd": u.CostUSD, "tokens": u.Tokens}
		}
		if changes != nil {
			ended["changes"] = changes
		}
		return c.log(RecAttemptEnded, ended)
	}()
	if err != nil {
		ev.Failure = failAttempt(ev.Failure, "chat_log_failed: "+err.Error())
		c.logFailed(err)
	}
	return ev
}

// logFailed ends the Chat for good on a Chat log failure: it overrides an
// idle timeout or a lost Session, so no Send reopens on a broken log.
func (c *Chat) logFailed(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !errors.Is(c.err, ErrChatClosed) {
		c.err = fmt.Errorf("the chat log failed: %w", err)
	}
}

// finalScope checks the Workspace once more after the Session has ended
// with no turn in flight (an idle timeout, a crash between turns, or
// Close): a protected write made after the last turn's check, by a
// process that outlived it, is reverted and recorded as a late Tamper
// event on the last turn. Nothing is committed.
func (c *Chat) finalScope() {
	c.mu.Lock()
	n := c.turns
	c.mu.Unlock()
	if n == 0 {
		return
	}
	c.scopeMu.Lock()
	defer c.scopeMu.Unlock()
	a := &Attempt{ID: fmt.Sprintf("chat#%d", n), Role: c.role}
	s, err := c.repo.CheckScope(c.Workspace, c.Snapshot, scopeRules(a.Role, c.protected, c.blobs.Put, map[string][]string{}))
	if err != nil || s.Tamper() == 0 {
		return // the next turn's check compares again
	}
	var tamper []workspace.Revert
	for _, r := range s.Reverts {
		if r.Tamper {
			tamper = append(tamper, r)
		}
	}
	err = func() error {
		if err := c.log(RecScopeObserved, map[string]any{
			"chat": c.ID, "turn": n, "attempt": a.ID, "role": a.Role, "state": "late", "final": true,
			"reverted": tamper, "tamper": len(tamper), "enforcement": workspace.RevertOnly,
		}); err != nil {
			return err
		}
		applyErr := s.Apply()
		done := map[string]any{"chat": c.ID, "turn": n, "attempt": a.ID, "reverted": applyErr == nil}
		if applyErr != nil {
			done["error"] = applyErr.Error()
		}
		if err := c.log(RecScopeReverted, done); err != nil {
			return err
		}
		a.Reverted = c.reverted[n]
		c.logMu.Lock()
		defer c.logMu.Unlock()
		return recordTamper(c.l, a, tamper, applyErr == nil, true, map[string]any{"chat": c.ID, "turn": n})
	}()
	if err != nil {
		c.logFailed(err)
	}
}

// checkScope is a turn's scope check: the one an implementer Attempt gets
// (enforceScope), with the turn recorded, while the Session lives on.
// Protected writes are reverted silently and each is a Tamper event; an
// edit that only adds to an Oracle test is kept (#117). The reverted
// Workspace is then committed as the turn's commit, whose changes since
// the last turn's are classified. A comparison or revert the Workspace
// defeats fails the turn (a.Failure); err is a Chat log failure.
//
// The check is against the Snapshot, so a non-additive edit in a later
// turn to an Oracle test file kept additively earlier puts the whole file
// back at the Snapshot's, dropping the earlier addition too: as a
// send-back's Attempt is held to the Snapshot, not the last Candidate.
func (c *Chat) checkScope(a *Attempt, n int) (*agent.TurnScope, map[string]any, error) {
	c.scopeMu.Lock()
	defer c.scopeMu.Unlock()
	defer func() { c.reverted[n] = a.Reverted }()
	turn := map[string]any{"chat": c.ID, "turn": n}
	added := map[string][]string{}
	s, err := c.repo.CheckScope(c.Workspace, c.Snapshot, scopeRules(a.Role, c.protected, c.blobs.Put, added))
	if err != nil {
		a.Failure = failAttempt(a.Failure, "scope_check_failed: "+err.Error())
		return nil, nil, nil
	}
	kept := []KeptTest{}
	for p, oid := range s.Kept {
		kept = append(kept, KeptTest{Path: p, Class: ClassOracleTestAddition, OID: oid, Added: added[p]})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
	if err := c.log(RecScopeObserved, map[string]any{
		"chat": c.ID, "turn": n, "attempt": a.ID, "role": a.Role, "state": "planned", "compared": s.Compared,
		"reverted": nonNil(s.Reverts), "kept": kept, "tamper": s.Tamper(), "enforcement": workspace.RevertOnly,
	}); err != nil {
		return nil, nil, err
	}
	applyErr := s.Apply()
	done := map[string]any{"chat": c.ID, "turn": n, "attempt": a.ID, "reverted": applyErr == nil}
	if applyErr != nil {
		done["error"] = applyErr.Error()
	}
	if err := c.log(RecScopeReverted, done); err != nil {
		return nil, nil, err
	}
	c.logMu.Lock()
	err = recordTamper(c.l, a, s.Reverts, applyErr == nil, false, turn)
	c.logMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	a.Reverted = s.Reverts
	if applyErr != nil {
		a.Failure = failAttempt(a.Failure, "revert_failed: "+applyErr.Error())
		return nil, nil, nil
	}
	commit, late, err := c.repo.CommitScoped(c.Workspace, c.last, c.Snapshot, fmt.Sprintf("Chat %s turn %d", c.ID, n), c.protected, s.Links, s.Kept)
	var addErr *workspace.AddError
	if errors.As(err, &addErr) {
		a.Failure = failAttempt(a.Failure, "turn_commit_failed: "+addErr.Error())
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(late) > 0 {
		// A kept file written again after the comparison is not kept.
		gone := map[string]bool{}
		for _, r := range late {
			gone[r.Path] = true
		}
		pruned := []KeptTest{}
		for _, k := range kept {
			if !gone[k.Path] {
				pruned = append(pruned, k)
			}
		}
		if err := c.log(RecScopeObserved, map[string]any{
			"chat": c.ID, "turn": n, "attempt": a.ID, "role": a.Role, "state": "late", "reverted": late, "kept": pruned,
			"tamper": countTamper(late), "enforcement": workspace.RevertOnly,
		}); err != nil {
			return nil, nil, err
		}
		c.logMu.Lock()
		err = recordTamper(c.l, a, late, true, true, turn)
		c.logMu.Unlock()
		if err != nil {
			return nil, nil, err
		}
		a.Reverted = append(a.Reverted, late...)
		// The Session lives on: the Workspace gets the commit's content
		// back too, so the next turn doesn't start from the late write.
		if err := c.restoreLate(a, n, late); err != nil {
			a.Failure = failAttempt(a.Failure, "revert_failed: "+err.Error())
		}
	}
	if err := c.repo.SetRef(fmt.Sprintf("refs/oge/chat/turns/%d", n), commit); err != nil {
		return nil, nil, err
	}
	// The turn's changes: what it changed since the last turn, and only
	// where that differs from the Snapshot, so putting a file back is no
	// change to cut.
	sinceLast, err := c.repo.ChangedFiles(c.last, commit)
	if err != nil {
		return nil, nil, err
	}
	sinceSnap, err := c.repo.ChangedFiles(c.Snapshot, commit)
	if err != nil {
		return nil, nil, err
	}
	differs := map[string]bool{}
	for _, p := range sinceSnap {
		differs[p] = true
	}
	var changed []string
	for _, p := range sinceLast {
		if differs[p] {
			changed = append(changed, p)
		}
	}
	_, withheld, err := c.repo.Classify(c.Snapshot, commit, c.promotedNew)
	if err != nil {
		return nil, nil, err
	}
	class := map[string]string{}
	for _, w := range withheld {
		class[w.Path] = w.Class
	}
	ts := &agent.TurnScope{Turn: n, Promoted: []string{}, Ambiguous: []string{}, Excluded: []string{}, Tamper: []string{}}
	for _, p := range changed {
		switch {
		case class[p] == workspace.ClassAmbiguous:
			ts.Ambiguous = append(ts.Ambiguous, p)
		case class[p] == workspace.ClassExcluded, workspace.Excluded(p) && !c.promotedNew(p):
			ts.Excluded = append(ts.Excluded, p)
		default:
			ts.Promoted = append(ts.Promoted, p)
		}
	}
	for _, r := range a.Reverted {
		if r.Tamper {
			ts.Tamper = append(ts.Tamper, r.Path)
		}
	}
	c.last, c.lastTurn = commit, n
	return ts, map[string]any{"commit": commit, "promoted": ts.Promoted, "ambiguous": ts.Ambiguous, "excluded": ts.Excluded}, nil
}

// restoreLate puts the Workspace back in scope after late writes,
// through the scope check's own revert, which never writes through a
// link (the agent is still alive). The late Tamper events are recorded
// already; a protected write the check finds beyond late is recorded as
// one more, late, on turn n.
func (c *Chat) restoreLate(a *Attempt, n int, late []workspace.Revert) error {
	noPut := func([]byte) (string, error) { return "", nil }
	s, err := c.repo.CheckScope(c.Workspace, c.Snapshot, scopeRules(a.Role, c.protected, noPut, map[string][]string{}))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range late {
		seen[r.Path] = true
	}
	var more []workspace.Revert
	for _, r := range s.Reverts {
		if r.Tamper && !seen[r.Path] {
			more = append(more, r)
		}
	}
	applyErr := s.Apply()
	if len(more) > 0 {
		c.logMu.Lock()
		err := recordTamper(c.l, a, more, applyErr == nil, true, map[string]any{"chat": c.ID, "turn": n})
		c.logMu.Unlock()
		if err != nil {
			return err
		}
		a.Reverted = append(a.Reverted, more...)
	}
	return applyErr
}

// promotedNew is walk.promotedNew without a human's promotions, which a
// Chat has none of yet.
func (c *Chat) promotedNew(p string) bool {
	pr := c.frozen.Project
	if workspace.Excluded(p) {
		return DeclaredAgentConfig(pr.OutputGlobs, p)
	}
	return oracle.MatchAny(pr.OutputGlobs, p) || oracle.MatchAny(pr.TestGlobs, p)
}

// HeldOutViewed records that the user viewed held-out tests during the
// Chat, for the Receipt's disclosure. what says which. Its turn is the
// last turn started, 0 before the first.
func (c *Chat) HeldOutViewed(what string) error {
	c.mu.Lock()
	n := c.turns
	c.mu.Unlock()
	return c.log(RecHeldOutViewed, map[string]any{"chat": c.ID, "turn": n, "what": what})
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
	sess := c.sess
	c.mu.Unlock()
	_ = sess.Close()
}

// Interrupt ends the turn in flight without ending the Session; queued
// turns then run. Idle, it does nothing.
func (c *Chat) Interrupt() error {
	c.mu.Lock()
	busy, sess := c.cur != nil, c.sess
	c.mu.Unlock()
	if !busy {
		return nil
	}
	return sess.Interrupt()
}

// Err is why the Chat takes no more turns in its current Session:
// ErrChatIdle after the idle timeout and ErrChatLost when the Session
// ended on its own, until the next Send reopens one; ErrChatClosed after
// Close. nil while it is open.
func (c *Chat) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Done is closed once the current Session has ended.
func (c *Chat) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pumped
}

// Close ends the Session and removes the Workspace, which is disposable.
func (c *Chat) Close() error {
	c.closing.Do(func() {
		c.reopening.Lock()
		defer c.reopening.Unlock()
		c.mu.Lock()
		c.closed, c.err = true, ErrChatClosed
		c.timer.Stop()
		unctx, sess, pumped := c.unctx, c.sess, c.pumped
		c.mu.Unlock()
		if unctx != nil {
			unctx()
		}
		close(c.stop)
		_ = sess.Close()
		<-pumped
		c.cancelPrep()
		<-c.prepared
		c.cutMu.Lock() // a cut in flight still uses the seed and the control
		defer c.cutMu.Unlock()
		c.prep.control.Stop()
		c.prep.seed.Close()
		c.logMu.Lock()
		c.l.Close()
		c.logMu.Unlock()
		oracle.RemoveAll(c.work)
	})
	return nil
}
