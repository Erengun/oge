package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
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
// enters the walk at QA. It verifies in the background of the Chat (D3):
// turns go on during QA and the Check, as Chat Attempts outside the Run
// (ADR-0012, as amended), and a newer cut cancels the one in flight.

// cutsDir holds the cuts' disposable directories in a Chat's: the
// implementer is denied reading it, as QA writes held-out tests there.
const cutsDir = "cuts"

// ErrNothingToCut means no turn since the last cut changed anything
// Promoted or Ambiguous: there is no Run, and no wait on Preflight.
var ErrNothingToCut = errors.New("nothing to cut")

// ErrSuperseded is why a cut stopped without a Verdict: a newer cut
// cancelled it, and covers its turns.
var ErrSuperseded = errors.New("superseded by a newer cut")

// WhySuperseded is a superseded cut's Run's stop reason.
const WhySuperseded = "superseded: a newer cut cancelled this one's verification, and covers its turns"

// WhySetupChanged is a cut's Refused reason when its Pipeline changes the
// Chat's Oracle, setup or Checks.
const WhySetupChanged = "the Pipeline at the cut changes the Chat's Oracle, setup or Checks; only the mode can change in a Chat, so start a new one for that"

// CutParams is what a cut freezes, the Pipeline in the Shift+Tab mode
// current at the cut, and where its Run reports. The config is the
// Chat's.
type CutParams struct {
	// Frozen is the Chat's Pipeline resolved again in the current mode;
	// the Chat's when nil. Only the mode may differ: a Pipeline that
	// changes the Chat's Oracle, scope, setup or Checks is refused.
	Frozen    *pipeline.Frozen
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
	dir    string // its Run's directory
	commit string
	turn   int
	prev   *cutMark // the cut it follows
	// resolved are the Consultations its snapshot counted (Dismissed or
	// Shown): Delivered records them so no later cut counts them again.
	resolved []string
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
	// The Receipt's disclosures (D6, D7): the Task refers to agent text
	// QA never sees; HeldOutViewed records in the Task's turns; the
	// chain's verify and send-back cycles, this cut's Check included.
	refers           bool
	heldOutViewed    int
	advice           CutAdvice
	verifies, sendBk int               // over the Task's turns
	tamper           []json.RawMessage // the Task's turns' TamperEvent records, verbatim
	prep             chatPrep
	dir              string // the Chat's directory: the control's blobs are there
	// The control's consults before this cut, so its record is this
	// cut's own; recorded once it is written.
	waits    int
	waited   time.Duration
	recorded bool
	checks   map[checkKey]checkedBy // the Chat's, under its cutMu
}

// checkKey is what a Check result depends on: the tree checked, the
// Oracle version's manifest and the frozen Pipeline, its mode included.
type checkKey struct{ tree, oracle, pipeline string }

// checkedBy is a cut's Check result, and the Run and Check that ran it.
type checkedBy struct {
	run   string
	check int
	cr    *oracle.Result
}

// checkKey is checked's key, on the manifest and frozen Pipeline blobs.
func (e *cutEntry) checkKey(repo *workspace.RunRepo, checked, manifest, frozen string) (checkKey, error) {
	tree, err := repo.TreeOf(checked)
	return checkKey{tree: tree, oracle: manifest, pipeline: frozen}, err
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
// ending first refuses it. Closing the Chat cancels a cut in flight.
//
// One cut verifies at a time, and a newer one cancels the one in flight:
// that one returns ErrSuperseded, with its Run's Result if it had one.
// Turns are never held up by a cut: Send doesn't wait on it.
func (c *Chat) Cut(ctx context.Context, cp CutParams) (*Result, error) {
	defer c.retryRefresh() // runs after cutMu is released: a deferred retake (D10)
	if err := c.Err(); errors.Is(err, ErrChatClosed) {
		return nil, err
	}
	f := cp.Frozen
	if f == nil {
		f = c.frozen
	}
	if why, err := c.sameOracle(f); err != nil || why != "" {
		if err != nil {
			return nil, err
		}
		return &Result{Outcome: Refused, Why: []string{why}}, nil
	}
	// Only a cut that can run supersedes the one in flight.
	c.supersedeMu.Lock()
	lc := c.lastCut
	c.supersedeMu.Unlock()
	if _, err := c.toCut(lc); err != nil {
		return nil, err
	}
	ctx, supersede := context.WithCancelCause(ctx)
	defer supersede(nil)
	c.supersedeMu.Lock()
	if c.supersede != nil {
		c.supersede(ErrSuperseded)
	}
	c.supersede = supersede
	c.supersedeMu.Unlock()
	c.cutMu.Lock()
	defer c.cutMu.Unlock()
	if err := c.Err(); errors.Is(err, ErrChatClosed) {
		return nil, err
	}
	if errors.Is(context.Cause(ctx), ErrSuperseded) {
		return nil, ErrSuperseded // superseded before it began
	}
	e, err := c.toCut(c.lastCut) // the cut it waited on may have moved it
	if err != nil {
		return nil, err
	}
	head, to := e.candidate, e.to
	select {
	case <-c.prepared:
	default:
		// The Preflight and the Snapshot control can take minutes (the
		// frozen Checks run once on the Snapshot); no Run exists to name
		// the wait yet, so the cut itself does (#296).
		if cp.Observe != nil {
			cp.Observe(Event{Kind: EvStage, Step: "preparing"})
		}
	}
	select {
	case <-c.prepared:
	case <-c.stop:
		return nil, ErrChatClosed
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), ErrSuperseded) {
			return nil, ErrSuperseded
		}
		return &Result{Outcome: Refused, Why: []string{"the Preflight and the Snapshot control hadn't finished; nothing is verified against an unchecked setup"}}, nil
	}
	if c.refused != nil {
		// Setup failed on the Snapshot once, and the Snapshot doesn't
		// change: no new Run for every later turn.
		r := *c.refused
		return &r, nil
	}
	e.prep = c.prep
	if c.verified == nil {
		c.verified = map[checkKey]checkedBy{}
	}
	e.checks = c.verified
	e.waits, e.waited = c.prep.control.Waits()
	// The Task's turns are every turn since the last delivery (D7), and
	// so are its Tamper events: one an earlier cut carried, or a late one
	// on a turn already cut, still gates this one. Delivered moves it.
	if e.tamper, err = c.chatRecords(RecTamperEvent, e.delivered+1, to); err != nil {
		return nil, err
	}
	// A view while the Task's turns were sent could reach them (D6).
	viewed, err := c.chatRecords(RecHeldOutViewed, e.delivered, to)
	if err != nil {
		return nil, err
	}
	e.heldOutViewed = len(viewed)
	// The Consultations of the Task's turns, as of this cut (the Tamper
	// window): the Receipt reads them from the Ledger, not the Chat log.
	if e.advice, err = c.chatAdvice(e.delivered+1, to); err != nil {
		return nil, err
	}
	c.mu.Lock()
	// The Task's cycles (D6): its cuts that reached a Verdict, this one
	// included, and the feedback turns sent among its turns.
	e.verifies = 1
	for _, t := range c.verdictCuts {
		if t > e.delivered {
			e.verifies++
		}
	}
	var msgs []string
	for i, m := range c.msgs[e.delivered:to] {
		n := e.delivered + i + 1
		if c.oge[n] && !c.advised[n] && !c.unsent[n] {
			e.sendBk++
		}
		if c.unsent[n] || c.oge[n] {
			continue // it never reached the agent, or it is Öge's (D7)
		}
		msgs = append(msgs, m)
		e.hashes = append(e.hashes, map[string]any{"turn": n, "sha256": sha256Hex(m)})
		// TODO(#262-decision): the Task refers to agent text when one of
		// its turns follows an earlier turn, whose reply the Task can't
		// hold: a structural test, never a reading of the words. It
		// over-discloses, and the Task is never filled in (ADR-0016).
		e.refers = e.refers || n > 1
	}
	c.mu.Unlock()
	if len(msgs) == 0 {
		return nil, ErrNothingToCut // only Öge's turns: no Task to verify
	}
	// Closing the Chat, or its context ending, cancels the cut.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	res, err := Start(ctx, Params{Repo: c.source, Task: task.Task{Text: msgs[0], FollowUps: msgs[1:]}, Frozen: f, Config: c.config,
		Agents: c.agents, State: c.state, Version: cp.Version, Getenv: c.getenv, CacheWait: cp.CacheWait,
		Observe: cp.Observe, Gates: cp.Gates, cut: e})
	if err != nil {
		return nil, err
	}
	// A Run that reached its Verdict before it was superseded completed.
	verdict := res.Check != nil && (res.Outcome == Accepted || res.Outcome == Failed || res.Outcome == Parked)
	if errors.Is(context.Cause(ctx), ErrSuperseded) {
		res.Feedback = "" // the newer cut covers its turns: no feedback (D5)
		if !verdict {
			return res, ErrSuperseded // its turns are the newer cut's
		}
	}
	if verdict {
		c.mu.Lock()
		c.verdictCuts = append(c.verdictCuts, to)
		if c.verdict == nil || to >= c.verdict.turn {
			// The newest Verdict, for an Advisor's Briefing: a superseded
			// cut's too, which a newer cut's replaces.
			c.verdict = &chatVerdict{turn: to, run: res.ID, outcome: res.Outcome, failures: res.failures}
		}
		c.mu.Unlock()
	}
	if res.Outcome == Refused && c.prep.setup != nil && !c.prep.setup.Pass {
		c.refused = res
	}
	// A cut that was cancelled, stopped or refused verified nothing: the
	// next cut covers its turns again.
	switch {
	case ctx.Err() != nil && !verdict, res.Candidate == "", res.Outcome == Refused, res.Outcome == InfrastructureStop, res.Outcome == Cancelled:
	default:
		c.supersedeMu.Lock()
		c.lastCut = &cutMark{run: res.ID, dir: res.Dir, commit: head, turn: to, prev: c.lastCut, resolved: e.advice.resolvedIDs()}
		if c.cuts == nil {
			c.cuts = map[string]*cutMark{}
		}
		c.cuts[res.ID] = c.lastCut
		c.supersedeMu.Unlock()
	}
	return res, nil
}

// toCut is the cut of the turns since lc, or ErrNothingToCut when they
// changed nothing Promoted or Ambiguous.
func (c *Chat) toCut(lc *cutMark) (*cutEntry, error) {
	// The Candidate is a turn commit, never the live Workspace: the
	// Session may be writing to it.
	c.scopeMu.Lock()
	head, to, snap, info := c.last, c.lastTurn, c.Snapshot, c.info
	c.scopeMu.Unlock()
	e := &cutEntry{chat: c.ID, chatRepo: c.repo.Dir, dir: c.Dir, work: filepath.Join(c.work, cutsDir), snap: snap, info: info,
		candidate: head, base: snap, from: 1, to: to}
	if lc != nil {
		e.base, e.from, e.follows = lc.commit, lc.turn+1, lc.run
	}
	c.supersedeMu.Lock()
	e.delivered = c.delivered
	c.supersedeMu.Unlock()
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
	return e, nil
}

// sameOracle is why f can't be a cut's Pipeline: only the Shift+Tab mode
// may change in a Chat, never the Oracle, the implementer's scope, setup
// or the Checks its turns were briefed and held to.
func (c *Chat) sameOracle(f *pipeline.Frozen) (string, error) {
	const why = WhySetupChanged
	if !reflect.DeepEqual(f.Project, c.frozen.Project) || !reflect.DeepEqual(f.Setup, c.frozen.Setup) || !reflect.DeepEqual(f.Checks, c.frozen.Checks) {
		return why, nil
	}
	c.scopeMu.Lock() // a retake swaps both together (chat_refresh.go)
	snap, v0 := c.Snapshot, c.v0
	c.scopeMu.Unlock()
	_, blob, err := oracle.NewV0(c.repo, snap, f, c.blobs)
	if err != nil {
		return "", err
	}
	if blob != v0 {
		return why, nil
	}
	return "", nil
}

// chatRecords are the Chat log's records of typ on turns from to to,
// verbatim: a cut never hides a TamperEvent.
func (c *Chat) chatRecords(typ string, from, to int) ([]json.RawMessage, error) {
	c.logMu.Lock()
	recs, err := ledger.Replay(c.Dir)
	c.logMu.Unlock()
	if err != nil && !errors.Is(err, ledger.ErrTornTail) {
		return nil, err
	}
	var out []json.RawMessage
	for _, r := range recs {
		if r.Type != typ {
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
	// The Chat's directory: its repository, and the blobs its Snapshot
	// control's records refer to, live there.
	begun["chat"], begun["chat_repo"], begun["chat_dir"] = e.chat, e.chatRepo, e.dir
	begun["turns"] = map[string]int{"from": e.from, "to": e.to}
	begun["base"], begun["task_turns"] = e.base, e.hashes
	begun["refers_to_agent_text"], begun["held_out_viewed"] = e.refers, e.heldOutViewed
	begun["cycles"] = map[string]int{"verify": e.verifies, "send_back": e.sendBk}
	if !e.advice.empty() {
		begun["advice"] = e.advice
	}
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

// attempt is the cut as the walk's implementer Attempt, after the Task's
// turns' Tamper events are recorded in the Run, as the Chat log has them,
// and kept for its Gate.
func (e *cutEntry) attempt(l *ledger.Ledger, w *walk, repo *workspace.RunRepo, impl pipeline.Stage) (*Attempt, error) {
	a := &Attempt{ID: fmt.Sprintf("chat#%d-%d", e.from, e.to), Stage: impl.Name, Role: "implementer", Agent: impl.Binding(),
		Cause: "cut", Exit: "done", Candidate: e.candidate}
	for _, rec := range e.tamper {
		var t struct{ ID, Path, Change string }
		if err := json.Unmarshal(rec, &t); err != nil {
			return nil, err
		}
		if err := l.Append(RecTamperEvent, rec); err != nil {
			return nil, err
		}
		w.tamper = append(w.tamper, tamperEvent{t.ID, t.Path, t.Change})
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

// control is the Chat's Snapshot control as this cut's Check consulted
// it, once: the consults and wait since the cut began.
func (e *cutEntry) control(ctl *oracle.Control) (oracle.ControlRecord, bool) {
	n, waited := ctl.Waits()
	if e.recorded || n == e.waits {
		return oracle.ControlRecord{}, false
	}
	e.recorded = true
	rec := ctl.Stop() // it has finished: the cut waited for it
	rec.Consulted, rec.WaitedMs = true, (waited - e.waited).Milliseconds()
	return rec, true
}
