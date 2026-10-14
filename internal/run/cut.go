package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// A cut is a Chat's turn commit taken through the frozen Pipeline as a
// Run (docs/ux/chat-first.md, slice 6; ADR-0007 as amended): when the
// turns since the last cut changed something Promoted or Ambiguous, the
// newest turn commit becomes an immutable Candidate, and a Run of its own
// enters the walk at QA. It runs in the foreground for now.
// TODO(#260): verify in the background, cancelled by a newer cut.

// cutsDir holds the cuts' disposable directories in a Chat's: the
// implementer is denied reading it, as QA writes held-out tests there.
const cutsDir = "cuts"

// ErrNothingToCut means no turn since the last cut changed anything
// Promoted or Ambiguous: there is no Run, and no wait on Preflight.
var ErrNothingToCut = errors.New("nothing to cut")

// CutParams is what a cut freezes: the Pipeline, the config and the
// Shift+Tab mode current at the cut, and where its Run reports.
type CutParams struct {
	// Frozen is the Pipeline resolved at the cut, in the current mode; the
	// Chat's when nil.
	Frozen *pipeline.Frozen
	// Config is the .oge/oge.toml Frozen was resolved from, or nil. The
	// Snapshot must hold exactly these bytes.
	Config    []byte
	Version   string
	CacheWait time.Duration
	Observe   func(Event)
	Gates     gate.Port
}

// chatPrep is the Preflight a Chat starts when it opens: setup into the
// cache seed, then the Snapshot control of Oracle v0.
type chatPrep struct {
	seed    *oracle.Seed
	setup   *oracle.Execution
	control *oracle.Control
	err     error
}

// cutMark is a cut whose Run reached the walk: the next cut follows it.
type cutMark struct {
	run    string
	commit string
	turn   int
}

// cutEntry is how a cut's Run enters the walk: Start takes the Chat's
// Snapshot and Preflight instead of taking its own, and the cut's
// Candidate in place of an implementer Attempt.
type cutEntry struct {
	chat      string
	chatRepo  string // the Chat repository the commits come from
	work      string // the Chat's cuts directory
	snap      string
	info      workspace.SnapshotInfo
	candidate string // the newest turn commit
	base      string // the last cut's Candidate, or the Snapshot
	from, to  int    // the turn range since the last cut
	follows   string // the last cut's Run
	delivered int    // the Task's first turn, less one
	hashes    []map[string]any
	reverted  map[int][]workspace.Revert
	tamper    []json.RawMessage // the turns' TamperEvent records, verbatim
	prep      chatPrep
}

// prepare starts the Chat's Preflight in the background: setup on the
// Snapshot into the seed, then the Snapshot control, as a Run starts
// them (ADR-0020, ADR-0021). A cut waits for both.
func (c *Chat) prepare(m *oracle.Manifest, snap string) {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancelPrep, c.prepared = cancel, make(chan struct{})
	setup := c.frozen.Setup.Run
	runner := &oracle.Runner{Blobs: c.blobs, PassEnv: c.frozen.Project.PassEnv, Getenv: c.getenv, SeedTemplate: c.seedTmpl}
	go func() {
		defer close(c.prepared)
		pf := &c.prep
		pf.seed, pf.setup, pf.err = runner.NewSeed(ctx, c.repo, snap, setup, filepath.Join(c.Dir, "cache-seed"))
		if pf.err != nil || (pf.setup != nil && !pf.setup.Pass) {
			return
		}
		pf.control = startControl(ctx, runner, pf.seed, c.repo, m, snap, setup, c.Dir)
		select {
		case <-pf.control.Done():
		case <-ctx.Done():
		}
	}()
}

// Cut cuts the Chat's newest turn commit into a Run when the turns since
// the last cut changed something Promoted or Ambiguous (D4), and takes
// it through the walk from QA, blocking until the Run ends or parks. It
// returns ErrNothingToCut otherwise. The cut is refused (fails closed)
// while the Chat's Preflight and Snapshot control haven't finished: ctx
// ending first refuses it.
func (c *Chat) Cut(ctx context.Context, cp CutParams) (*Result, error) {
	c.cutMu.Lock()
	defer c.cutMu.Unlock()
	if err := c.Err(); errors.Is(err, ErrChatClosed) {
		return nil, err
	}
	// The Candidate is a turn commit, never the live Workspace: the
	// Session may be writing to it.
	c.scopeMu.Lock()
	head, to := c.last, c.lastTurn
	reverted := map[int][]workspace.Revert{}
	for n, rs := range c.reverted {
		reverted[n] = append([]workspace.Revert(nil), rs...)
	}
	c.scopeMu.Unlock()
	e := &cutEntry{chat: c.ID, chatRepo: c.repo.Dir, work: filepath.Join(c.work, cutsDir), snap: c.Snapshot, info: c.info,
		candidate: head, base: c.Snapshot, from: 1, to: to}
	if lc := c.lastCut; lc != nil {
		e.base, e.from, e.follows = lc.commit, lc.turn+1, lc.run
	}
	if to < e.from {
		return nil, ErrNothingToCut
	}
	promoted, withheld, err := c.repo.Classify(e.base, head, c.promotedNew)
	if err != nil {
		return nil, err
	}
	ambiguous := false
	for _, w := range withheld {
		ambiguous = ambiguous || w.Class == workspace.ClassAmbiguous
	}
	if len(promoted) == 0 && !ambiguous {
		return nil, ErrNothingToCut
	}
	select {
	case <-c.prepared:
	case <-ctx.Done():
		return &Result{Outcome: Refused, Why: []string{"the Preflight and the Snapshot control hadn't finished; nothing is verified against an unchecked setup"}}, nil
	}
	e.prep = c.prep
	e.reverted = map[int][]workspace.Revert{}
	for n := e.from; n <= to; n++ {
		e.reverted[n] = reverted[n]
	}
	if e.tamper, err = c.tamperRecords(e.from, to); err != nil {
		return nil, err
	}
	// The Task is the user's turns since the last delivery, verbatim
	// (D7): never the agent's summary.
	// TODO(#261): delivery moves e.delivered.
	c.mu.Lock()
	msgs := append([]string(nil), c.msgs[e.delivered:to]...)
	c.mu.Unlock()
	for i, m := range msgs {
		e.hashes = append(e.hashes, map[string]any{"turn": e.delivered + i + 1, "sha256": sha256Hex(m)})
	}
	f := cp.Frozen
	if f == nil {
		f = c.frozen
	}
	res, err := Start(ctx, Params{Repo: c.source, Task: task.Task{Text: msgs[0], FollowUps: msgs[1:]}, Frozen: f, Config: cp.Config,
		Agents: c.agents, State: c.state, Version: cp.Version, Getenv: c.getenv, CacheWait: cp.CacheWait,
		Observe: cp.Observe, Gates: cp.Gates, cut: e})
	if err == nil && res.Outcome != Refused && res.ID != "" {
		c.lastCut = &cutMark{run: res.ID, commit: head, turn: to}
	}
	return res, err
}

// tamperRecords are the Chat log's TamperEvent records of turns from to
// to, verbatim: a cut never hides one.
func (c *Chat) tamperRecords(from, to int) ([]json.RawMessage, error) {
	c.logMu.Lock()
	recs, err := ledger.Replay(c.Dir)
	c.logMu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []json.RawMessage
	for _, r := range recs {
		if r.Type != RecTamperEvent {
			continue
		}
		var d struct{ Turn int }
		if err := json.Unmarshal(r.Data, &d); err != nil {
			return nil, err
		}
		if d.Turn >= from && d.Turn <= to {
			out = append(out, r.Data)
		}
	}
	return out, nil
}

// addTo notes the cut in RunStarted's data: its Chat, the turn range, the
// cut it follows, the Chat repository, and the Task's turn hashes.
func (e *cutEntry) addTo(begun map[string]any) {
	if e == nil {
		return
	}
	begun["chat"], begun["chat_repo"] = e.chat, e.chatRepo
	begun["turns"] = map[string]int{"from": e.from, "to": e.to}
	begun["base"], begun["task_turns"] = e.base, e.hashes
	if e.follows != "" {
		begun["follows"] = e.follows
	}
}

// enter takes the cut's commits into the Run repository, byte for byte.
func (e *cutEntry) enter(repo *workspace.RunRepo) error {
	if err := repo.Import(e.chatRepo, e.snap, workspace.SnapshotRef); err != nil {
		return fmt.Errorf("taking the Chat's Snapshot: %w", err)
	}
	if err := repo.Import(e.chatRepo, e.candidate, "refs/oge/cut"); err != nil {
		return fmt.Errorf("taking the cut's turn commit: %w", err)
	}
	return nil
}

// attempt is the cut as the walk's implementer Attempt, after each of its
// turns' Tamper events is recorded in the Run and kept for its Gate.
func (e *cutEntry) attempt(l *ledger.Ledger, w *walk, repo *workspace.RunRepo, impl pipeline.Stage) (*Attempt, error) {
	a := &Attempt{ID: fmt.Sprintf("chat#%d-%d", e.from, e.to), Stage: impl.Name, Role: "implementer", Agent: impl.Binding(),
		Cause: "cut", Exit: "done", Candidate: e.candidate}
	for _, rec := range e.tamper {
		if err := l.Append(RecTamperEvent, rec); err != nil {
			return nil, err
		}
	}
	for n := e.from; n <= e.to; n++ {
		// The ids recordTamper gave them in the Chat log.
		w.addTamper(&Attempt{ID: fmt.Sprintf("chat#%d", n), Reverted: e.reverted[n]})
		a.Reverted = append(a.Reverted, e.reverted[n]...)
	}
	var err error
	if a.Changed, err = repo.ChangedFiles(e.snap, e.candidate); err != nil {
		return nil, err
	}
	since, err := repo.ChangedFiles(e.base, e.candidate)
	if err != nil {
		return nil, err
	}
	return a, l.Append(RecAttemptEnded, map[string]any{"attempt": a.ID, "cause": "cut", "chat": e.chat, "turns": map[string]int{"from": e.from, "to": e.to},
		"exit": a.Exit, "candidate": a.Candidate, "base": e.base, "changed": nonNilNames(since), "session": "reused"})
}
