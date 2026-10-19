package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// RecResumed records an oge resume: where the walk re-enters, proved
// from the Ledger. A resume writes it only once the recorded position
// reproduces under the resumed walk's own state — it never trusts what
// it cannot prove.
const RecResumed = "Resumed"

// RefusedError is Resume's refusal: the Run exists but cannot resume —
// it is live, ended, or its Ledger cannot prove a resumable tail.
type RefusedError struct{ Why string }

func (e *RefusedError) Error() string { return e.Why }

// IsRefused reports whether err is a refusal rather than a failure.
func IsRefused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}

func refuse(format string, a ...any) error {
	return &RefusedError{fmt.Sprintf(format, a...)}
}

// tail kinds the walk re-enters at (ADR-0012):
//
//	gate    — a Gate was open when the Run stopped (parked, or abandoned
//	          by an interrupt while it waited)
//	review  — the Ambiguous-file review was between decisions
//	decide  — a Gate decision was recorded but never took effect
//	resolve — an Ambiguous review's promote/drop was recorded without
//	          its AmbiguousResolved
//	route   — a Verdict was recorded but the edge it takes never ran
//	check   — a Check started and never ended (interrupted or killed)
//	verify  — a verifier Attempt died mid-flight or never ran
//	attempt — an implementer Attempt died mid-flight or never ran
//
// A Resumed record is transparent to the tail: the position it names
// stays the position.
type resumeTail struct {
	kind     string
	node     string        // gate/decide/resolve: the Gate's node
	pins     gate.Pins     // gate/decide/resolve: the recorded Pins
	decision gate.Decision // decide/resolve: the recorded decision
	why      []string      // infraend: the ending the walk died before writing
}

// replayed is a stopped Run rebuilt from its Ledger: the frozen Pipeline,
// the Oracle versions, the counts the limits count, and where the walk
// stood.
type replayed struct {
	f       *pipeline.Frozen
	t       task.Task
	isCut   bool
	snap    string
	source  workspace.SnapshotInfo
	srcRepo string // the user's repository the Run came from
	start   string // the implementer's starting revision
	res     *Result
	m       *oracle.Manifest // the latest Oracle version
	mBlob   string
	m0      *oracle.Manifest // v0: the scope and control's
	qaN     int              // verifier Attempts so far
	qaAdded int              // held-out files admitted so far
	cr      *oracle.Result
	verdict string // the latest Verdict's

	w                 *walk
	impl              *Attempt // the last implementer Attempt, as recorded
	implStart         string   // the pending implementer Attempt's start_revision
	implBrief         string   // the pending implementer Attempt's recorded Briefing
	verID             string   // the latest verifier Attempt's id
	verFailure        string   // its recorded failure, when it ended in one
	preflightObserved bool     // the Ledger holds a PreflightObserved
	tail              resumeTail
	head              string
}

// Resume continues a Run whose walk stopped — parked at a Gate, or
// interrupted mid-flight. It replays the Ledger, refuses a state it
// cannot reproduce, records Resumed, and re-enters the walk at that
// position. p.Gates decides a Gate the walk reaches again; nil means
// unattended, and the Run parks where it stood.
func Resume(ctx context.Context, p Params, dir string) (*Result, error) {
	if alive(dir) {
		return nil, refuse("%s is still running", filepath.Base(dir))
	}
	// Replay under the same lock as all subsequent effects: another
	// resume must not advance the Ledger between reconstruction and use.
	release, err := markLive(dir)
	if err != nil {
		if errors.Is(err, errLocked) {
			return nil, refuse("%s is still running", filepath.Base(dir))
		}
		return nil, err
	}
	defer release()
	rt, err := replay(dir)
	if err != nil {
		return nil, err
	}
	if rt.isCut {
		return nil, refuse("%s is a Chat's cut; its Chat continues it", rt.res.ID)
	}
	if rt.tail.kind == "" {
		return nil, refuse("%s has nothing to resume", rt.res.ID)
	}
	if rt.f.Mode == pipeline.Blind {
		return nil, refuse("blind mode isn't built yet")
	}
	var impl, ver pipeline.Stage
	for _, s := range rt.f.Stages {
		switch s.Role {
		case "implementer":
			impl = s
		case "verifier":
			ver = s
		}
	}
	adapter := p.Agents[impl.Agent]
	if adapter == nil {
		return nil, refuse("%v for %s", ErrNoAdapter, impl.Agent)
	}
	var verifier agent.Adapter
	if rt.f.Mode == pipeline.Standard {
		if verifier = p.Agents[ver.Agent]; verifier == nil {
			return nil, refuse("%v for %s", ErrNoAdapter, ver.Agent)
		}
	}
	if p.Observe == nil {
		p.Observe = func(Event) {}
	}
	p.Frozen, p.Task, p.Interactive = rt.f, rt.t, false
	p.Repo = rt.srcRepo

	l, err := ledger.Open(dir)
	if err != nil {
		return nil, refuse("the Run's Ledger: %v", err)
	}
	defer l.Close()
	blobs, err := ledger.OpenBlobs(dir)
	if err != nil {
		return nil, refuse("%s's blobs: %v", rt.res.ID, err)
	}
	repo, err := workspace.OpenRunRepo(filepath.Join(dir, "repo.git"))
	if err != nil {
		return nil, refuse("%s's repository: %v", rt.res.ID, err)
	}
	res := rt.res
	res.Dir = dir
	// The receipt's Duration is this session's, not the parked span.
	res.Started = time.Now()
	// The agents' sandbox denies the Run's own private root, wherever it
	// actually is — not the state dir this invocation happens to use.
	st := *p.State
	st.Private = filepath.Dir(filepath.Dir(dir))
	p.State = &st
	workDir := filepath.Join(st.Work, res.ID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}
	defer oracle.RemoveAll(workDir)

	w := rt.w
	w.repo, w.l, w.p = repo, l, p
	w.repark = true

	// Prove and record re-entry before setup can execute commands or
	// remove stale trees. A refused resume has no walk effects.
	if rt.tail.pins.Gate != "" {
		if err := rt.provePins(w); err != nil {
			return nil, err
		}
	}
	resumed := map[string]any{"kind": rt.tail.kind, "head": rt.head, "oge_version": p.Version}
	if rt.tail.node != "" {
		resumed["gate"] = rt.tail.node
	}
	if err := l.Append(RecResumed, resumed); err != nil {
		return nil, err
	}

	// The cache seed and the Snapshot control build again while a Gate
	// waits; the walk waits on them only where they're used.
	runner := &oracle.Runner{Blobs: blobs, PassEnv: rt.f.Project.PassEnv, Getenv: p.Getenv, SeedTemplate: p.CacheSeedTemplate}
	seedCtx, cancelSeed := context.WithCancel(ctx)
	warm := make(chan warmup, 1)
	attemptCtx, cancelAttempt := context.WithCancelCause(ctx)
	defer cancelAttempt(nil)
	go func() {
		var wu warmup
		// A killed Run can leave its seed and check trees half-written
		// or read-only; the warm rebuilds them, as the dead-Run sweeper
		// does.
		_ = oracle.RemoveAll(filepath.Join(dir, "cache-seed"))
		_ = oracle.RemoveAll(filepath.Join(dir, "checks"))
		wu.seed, wu.setup, wu.err = runner.NewSeed(seedCtx, repo, rt.snap, rt.f.Setup.Run, filepath.Join(dir, "cache-seed"))
		if wu.err != nil || (wu.setup != nil && !wu.setup.Pass) {
			cancelAttempt(errSetupFailed)
		} else {
			wu.control = startControl(seedCtx, runner, wu.seed, repo, rt.m0, rt.snap, rt.f.Setup.Run, dir)
		}
		warm <- wu
	}()

	end := runEnder(ctx, res, &w, l)
	sw := &walker{
		p: p, res: res, l: l, blobs: blobs, repo: repo, runner: runner, adapter: adapter, impl: impl,
		m: rt.m, mBlob: rt.mBlob, workDir: workDir, start: rt.start, end: end,
		warm: warm, joined: true, needPreflight: !rt.preflightObserved,
	}
	// The implementer's protected set is Oracle v0's, as Start's (ADR-0009).
	sw.scope = implementerScope(rt.m0, rt.f)
	if rt.f.Mode == pipeline.Standard {
		sw.qa = &qaStage{p: p, l: l, blobs: blobs, repo: repo, adapter: verifier, stage: ver, runID: res.ID,
			snap: rt.snap, workDir: workDir, m: rt.m, mBlob: rt.mBlob, runner: runner, runDir: dir,
			n: rt.qaN, added: rt.qaAdded}
	}
	defer func() {
		cancelSeed()
		if sw.warm != nil {
			wu := <-sw.warm
			sw.seed, sw.control = wu.seed, wu.control
		}
		sw.control.Stop()
		sw.seed.Close()
	}()

	p.Observe(Event{Kind: EvStarted, Result: res})
	return rt.enter(ctx, attemptCtx, sw, w, workDir)
}

// provePins checks that a Gate's recorded Pins are the ones the replayed
// walk rebuilds for the same node — resume never trusts what it cannot
// reproduce. Every field comes from the replayed state (the Attempt that
// ended, the Candidate under review, the Ambiguous list, the Verdicts,
// the unacknowledged Tamper events): a recorded value that isn't what the
// state rebuilds is a refusal, never a hint to rebuild differently.
func (rt *replayed) provePins(w *walk) error {
	want := rt.tail.pins
	if rt.impl == nil {
		return refuse("%s's recorded Gate has no implementer Attempt", rt.res.ID)
	}
	cur := *rt.impl
	cur.Candidate = rt.res.Candidate
	if err := w.classify(cur.Candidate); err != nil {
		return err
	}
	r, err := w.request(want.Gate, &cur, rt.m.Version, rt.cr)
	if err != nil {
		return err
	}
	got := r.Pins
	if cur.Candidate != rt.impl.Candidate {
		// The Verdicts shown were reached on the checked Candidate, not
		// the resolved one under review (review's own pins rule).
		got.Checked = rt.impl.Candidate
	}
	if !reflect.DeepEqual(got, want) {
		return refuse("%s's recorded Pins don't reproduce under the Ledger's state (recorded %+v, rebuilt %+v)", want.Gate, want, got)
	}
	return nil
}

// holds rebuilds the check node's conditions for the resumed walk.
func (rt *replayed) holds(w *walk) func(string) bool {
	return func(c string) bool {
		return c == "verdict:"+rt.verdict || (c == "tamper_event" && len(w.unacknowledged()) > 0) ||
			(c == "ambiguous_files" && len(w.ambiguous) > 0)
	}
}

// nextSpec is the implementer Attempt a resume runs when it needs one:
// the number after the last, the pending one's start when one died
// mid-flight, and its Workspace by the same naming as a fresh walk.
func (rt *replayed) nextSpec(workDir string) attemptSpec {
	n := rt.w.attempts + 1
	cause, start := "resume", rt.start
	if rt.implStart != "" {
		start = rt.implStart // the interrupted Attempt's own start
	}
	if rt.w.attempts == 0 {
		cause = "first"
	}
	ws := filepath.Join(workDir, "implement")
	if n > 1 {
		ws = filepath.Join(workDir, fmt.Sprintf("implement-%d", n))
	}
	return attemptSpec{n: n, cause: cause, start: start, ws: ws, brief: rt.implBrief}
}

// enter re-enters the walk at the tail the Ledger showed.
func (rt *replayed) enter(ctx, attemptCtx context.Context, sw *walker, w *walk, workDir string) (*Result, error) {
	holds := rt.holds(w)
	nextCheck := rt.res.Checks + 1 // for loop: the number the next Check takes
	lastCheck := rt.res.Checks     // for dispatch: the Check whose Verdict routed here
	if rt.tail.kind == "verdict" {
		// The Check's Verdict was never recorded; write it, then the
		// walk takes its edge as "route" does. An infra Check result ends
		// the Run without a Verdict, as a fresh walk's does.
		if rt.cr == nil {
			return nil, refuse("%s's Check ended without a result the Verdict could be written from", rt.res.ID)
		}
		if rt.cr.Infra != "" {
			return sw.end(InfrastructureStop, rt.cr.Infra)
		}
		verdict := "fail"
		if rt.cr.Pass {
			verdict = "pass"
		}
		if err := sw.l.Append(RecVerdict, map[string]any{"check": rt.res.Checks,
			"verdict": verdict, "candidate": rt.res.Candidate, "oracle_version": rt.m.Version}); err != nil {
			return nil, err
		}
		w.check, rt.verdict = rt.res.Checks, verdict
		rt.tail.kind = "route"
	}
	switch rt.tail.kind {
	case "route":
		if err := w.classify(rt.res.Candidate); err != nil {
			return nil, err
		}
		e, ok := rt.f.Graph.Route("check", holds, w.exhausted)
		if !ok {
			return nil, refuse("the frozen graph has no edge for this Verdict")
		}
		// The Attempt the Verdict's edges see is the one that produced the
		// Candidate under review: the resolved one after an Ambiguous
		// review moved it past the Attempt's own.
		a := rt.impl
		if rt.res.Candidate != "" && rt.res.Candidate != a.Candidate {
			r := *a
			r.Candidate = rt.res.Candidate
			a = &r
		}
		st, err := w.follow(ctx, e, a, rt.m.Version, rt.cr, holds)
		if err != nil {
			return nil, err
		}
		return sw.dispatch(ctx, attemptCtx, w, a, rt.nextSpec(workDir), st, rt.cr, lastCheck)
	case "gate":
		if err := w.classify(rt.tail.pins.Candidate); err != nil {
			return nil, err
		}
		a := rt.impl
		var st step
		var err error
		if rt.tail.node == gateAmbiguous {
			// Re-enter the review at the Candidate it was deciding on.
			orig := rt.tail.pins.Checked
			if orig == "" {
				orig = rt.impl.Candidate
			}
			a = &Attempt{ID: rt.tail.pins.Attempt, Candidate: orig}
			st, err = w.review(ctx, a, rt.tail.pins.Candidate, rt.m.Version, rt.cr)
		} else {
			a = &Attempt{ID: rt.tail.pins.Attempt, Candidate: rt.tail.pins.Candidate}
			st, err = w.follow(ctx, pipeline.Edge{To: rt.tail.node}, a, rt.m.Version, rt.cr, holds)
		}
		if err != nil {
			return nil, err
		}
		return sw.dispatch(ctx, attemptCtx, w, a, rt.nextSpec(workDir), st, rt.cr, lastCheck)
	case "decide":
		// The decision is durable; the walk takes its edge now. The edge
		// leads where the frozen graph says — a Gate chain follows through
		// follow (which opens each next Gate), and the tamper Gate's
		// acknowledged check edge re-routes the Verdict's, as the fresh
		// walk's own edge does.
		d := rt.tail.decision
		a := &Attempt{ID: rt.tail.pins.Attempt, Candidate: rt.tail.pins.Candidate}
		if err := w.classify(rt.tail.pins.Candidate); err != nil {
			return nil, err
		}
		e, ok := rt.f.Graph.Choice(rt.tail.node, d.Choice)
		if !ok {
			return nil, refuse("the %s Gate has no edge for the recorded %q", rt.tail.node, d.Choice)
		}
		st := step{edge: e, gate: rt.tail.node, decision: &d}
		switch e.To {
		case "end", "implement", "verify":
			// dispatch takes them as the recorded decision's.
		case "check":
			if rt.tail.node == "gate.tamper" {
				next, ok := rt.f.Graph.Route("check", holds, w.exhausted)
				if !ok {
					return nil, refuse("the frozen graph has no edge for this Verdict")
				}
				st2, err := w.follow(ctx, next, a, rt.m.Version, rt.cr, holds)
				if err != nil {
					return nil, err
				}
				if st2.gate == "" {
					st2.gate, st2.decision = rt.tail.node, &d
				}
				st = st2
			}
		default:
			// A Gate the decision chains into.
			st2, err := w.follow(ctx, e, a, rt.m.Version, rt.cr, holds)
			if err != nil {
				return nil, err
			}
			if st2.gate == "" {
				st2.gate, st2.decision = rt.tail.node, &d
			}
			st = st2
		}
		return sw.dispatch(ctx, attemptCtx, w, a, rt.nextSpec(workDir), st, rt.cr, lastCheck)
	case "resolve":
		st, err := rt.applyResolved(w)
		if err != nil {
			return nil, err
		}
		if st.edge.To == "" {
			// Files remain undecided: the review re-opens on the Attempt
			// that made the checked Candidate.
			if err := w.classify(rt.res.Candidate); err != nil {
				return nil, err
			}
			a := rt.impl
			st, err = w.review(ctx, a, rt.res.Candidate, rt.m.Version, rt.cr)
			if err != nil {
				return nil, err
			}
		}
		return sw.dispatch(ctx, attemptCtx, w, rt.impl, rt.nextSpec(workDir), st, rt.cr, lastCheck)
	case "review":
		if err := w.classify(rt.res.Candidate); err != nil {
			return nil, err
		}
		st, err := w.review(ctx, rt.impl, rt.res.Candidate, rt.m.Version, rt.cr)
		if err != nil {
			return nil, err
		}
		return sw.dispatch(ctx, attemptCtx, w, rt.impl, rt.nextSpec(workDir), st, rt.cr, lastCheck)
	case "check", "verify":
		// The Candidate the walk was on: the resolved one when the
		// Ambiguous review moved it past the Attempt's own.
		a := rt.impl
		if rt.res.Candidate != "" && rt.res.Candidate != a.Candidate {
			r := *a
			r.Candidate = rt.res.Candidate
			a = &r
		}
		return sw.loop(ctx, attemptCtx, w, a, rt.nextSpec(workDir), rt.tail.kind, nextCheck)
	case "attempt":
		next := rt.nextSpec(workDir)
		if err := os.RemoveAll(next.ws); err != nil {
			return nil, err
		}
		if err := sw.repo.Checkout(next.start, next.ws); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(next.ws, ledger.WorkspaceMarker), nil, 0o600); err != nil {
			return nil, err
		}
		if next.n == 1 {
			// The first Workspace's git sees the Snapshot as HEAD, as a
			// fresh walk's does; later Workspaces never have one.
			if err := sw.repo.InitWorkspaceGit(next.ws); err != nil {
				return nil, err
			}
		}
		return sw.loop(ctx, attemptCtx, w, rt.impl, next, "", nextCheck)
	case "infraend":
		// The Ledger ended where a fresh walk ends the Run
		// Infrastructure-stopped; write the ending it died before
		// writing.
		return sw.end(InfrastructureStop, rt.tail.why...)
	}
	return nil, refuse("no resume path for %q", rt.tail.kind)
}

// applyResolved takes the recorded promote/drop that never took effect:
// the same Candidate resolve and AmbiguousResolved record the review
// writes.
func (rt *replayed) applyResolved(w *walk) (step, error) {
	d := rt.tail.decision
	var drop []string
	if d.Choice == "drop" {
		drop = d.Files
	}
	from := rt.tail.pins.Candidate // the Candidate the Gate decided on
	next, err := w.repo.Resolve(from, drop, fmt.Sprintf("%s %s", d.Choice, strings.Join(d.Files, " ")))
	if err != nil {
		return step{}, err
	}
	res := Resolution{Choice: d.Choice, Files: d.Files, From: from, Candidate: next}
	if err := w.l.Append(RecAmbiguousResolved, map[string]any{"choice": d.Choice, "files": d.Files,
		"from": from, "candidate": next, "attempt": rt.impl.ID}); err != nil {
		return step{}, err
	}
	w.resolved = append(w.resolved, res)
	rt.res.Candidate = next
	if d.Choice == "promote" {
		w.reviewPromoted = true
		for _, f := range d.Files {
			w.promoted[f] = true
		}
	}
	if err := w.classify(next); err != nil {
		return step{}, err
	}
	if len(w.ambiguous) > 0 {
		return step{}, nil // files remain undecided: the review re-opens
	}
	word := "drop"
	if w.reviewPromoted {
		word = "promote"
	}
	e, ok := w.g.Choice(gateAmbiguous, word)
	if !ok {
		return step{}, refuse("the frozen graph has no %s edge from the Ambiguous-file Gate", word)
	}
	return step{gate: gateAmbiguous, decision: &d, edge: e, candidate: next}, nil
}

// replay reads dir's Ledger and rebuilds where the walk stood. Resumed
// records are transparent: the position a resume showed stays the
// position.
func replay(dir string) (*replayed, error) {
	recs, head, err := ledger.ReplayHead(dir)
	if err != nil {
		// Missing, torn or corrupt: the Ledger can't prove a state, so a
		// resume is refused rather than failing mid-walk.
		return nil, refuse("the Run's Ledger: %v", err)
	}
	if len(recs) == 0 {
		return nil, refuse("%s has no Ledger records", filepath.Base(dir))
	}
	blobs, err := ledger.OpenBlobs(dir)
	if err != nil {
		return nil, err
	}
	rt := &replayed{
		res:  &Result{},
		w:    &walk{promoted: map[string]bool{}},
		head: head,
	}
	var openGate *gate.Pins // a GateOpened with no decision yet
	var decided *resumeTail // a GateDecided not yet taken effect
	var checkStarted bool   // a CheckStarted with no CheckEnded
	var checkInterrupted bool
	var pendingImpl bool   // an implementer AttemptStarting with no AttemptEnded
	var pendingVerify bool // a verifier AttemptStarting with no AttemptEnded
	var parked, ended string
	var endWhy []string
	var lastType string // the position records: ends and resumes don't move it
	decode := func(rec ledger.Record, v any) error {
		if err := json.Unmarshal(rec.Data, v); err != nil {
			return fmt.Errorf("record %d (%s): %w", rec.Seq, rec.Type, err)
		}
		return nil
	}
	for _, rec := range recs {
		switch rec.Type {
		case RecResumed, RecRunEnded, RecRunParked:
		default:
			// A record that moves the walk's position also clears a
			// parked Gate's: the parked Run resumed and walked on.
			lastType, parked = rec.Type, ""
		}
		switch rec.Type {
		case RecRunStarted:
			var d struct {
				Run       string    `json:"run"`
				Created   time.Time `json:"created"`
				Source    string    `json:"source"`
				Task      string    `json:"task"`
				Frozen    string    `json:"frozen"`
				FollowUps []string  `json:"follow_ups"`
				Chat      string    `json:"chat"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.res.ID, rt.res.Started, rt.source = d.Run, d.Created, workspace.SnapshotInfo{}
			rt.srcRepo = d.Source
			rt.isCut = d.Chat != ""
			if b, err := blobs.Get(d.Frozen); err == nil {
				rt.f = &pipeline.Frozen{}
				if err := json.Unmarshal(b, rt.f); err != nil {
					return nil, fmt.Errorf("the frozen Pipeline: %w", err)
				}
			}
			if b, err := blobs.Get(d.Task); err == nil {
				rt.t = taskFromBlob(string(b), d.FollowUps)
			}
			if rt.f == nil || rt.t.Text == "" {
				return nil, refuse("%s: the Task or frozen Pipeline blob is unreadable", filepath.Base(dir))
			}
		case RecSnapshotTaken:
			var d struct {
				Commit, Head, Branch string
				Modified, Untracked  int
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.snap = d.Commit
			rt.res.Snapshot = d.Commit
			rt.source = workspace.SnapshotInfo{Head: d.Head, Branch: d.Branch, Modified: d.Modified, Untracked: d.Untracked}
			rt.start = d.Commit // unless FollowUpRebased moves it
		case RecFollowUpRebased:
			var d struct{ Start string }
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.start = d.Start
		case RecOracleVersion:
			var d struct {
				Version int      `json:"version"`
				Blob    string   `json:"manifest"`
				Added   []string `json:"added"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			b, err := blobs.Get(d.Blob)
			if err != nil {
				return nil, fmt.Errorf("the Oracle v%d manifest: %w", d.Version, err)
			}
			m := &oracle.Manifest{}
			if err := json.Unmarshal(b, m); err != nil {
				return nil, fmt.Errorf("the Oracle v%d manifest: %w", d.Version, err)
			}
			if d.Version == 0 {
				rt.m0 = m
			}
			rt.m, rt.mBlob = m, d.Blob
			rt.res.Oracle = d.Version
			rt.qaAdded += len(d.Added)
		case RecAttemptStarting:
			var d struct {
				Attempt       string `json:"attempt"`
				Role          string `json:"role"`
				Cause         string `json:"cause"`
				StartRevision string `json:"start_revision"`
				Briefing      string `json:"briefing"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			decided = nil // an Attempt consumed a pending decision
			rt.w.reviewPromoted = false
			switch d.Role {
			case "verifier":
				rt.qaN++
				rt.verID, rt.verFailure = d.Attempt, ""
				pendingVerify = true
			default:
				rt.w.attempts++
				rt.impl = &Attempt{ID: d.Attempt, Cause: d.Cause}
				rt.implStart = d.StartRevision
				if b, err := blobs.Get(d.Briefing); err == nil {
					rt.implBrief = string(b)
				}
				if d.Cause == "send_back" {
					// dispatch counts every send-back once the Attempt for
					// it starts — decided or automatic alike.
					rt.w.sendBacks++
				}
				pendingImpl = true
			}
		case RecAttemptEnded:
			var d struct {
				Attempt   string `json:"attempt"`
				Exit      string `json:"exit"`
				Failure   string `json:"failure"`
				Stop      string `json:"stop"`
				Candidate string `json:"candidate"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			switch {
			case rt.impl != nil && d.Attempt == rt.impl.ID:
				rt.impl.Exit, rt.impl.Failure, rt.impl.Stop, rt.impl.Candidate = d.Exit, d.Failure, d.Stop, d.Candidate
				rt.res.Attempt, rt.res.Candidate = rt.impl, d.Candidate
				pendingImpl = false
			case d.Attempt == rt.verID && rt.verID != "":
				rt.verFailure = d.Failure
				pendingVerify = false
			}
		case RecCheckStarted:
			var d struct {
				Check     int    `json:"check"`
				Candidate string `json:"candidate"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.res.Checks = d.Check
			rt.res.Candidate = d.Candidate
			checkStarted = true
			decided = nil               // a Check ran where a decision's edge took it
			rt.w.reviewPromoted = false // the review session ended
		case RecCheckEnded:
			var d struct {
				Check       int            `json:"check"`
				Result      *oracle.Result `json:"result"`
				Interrupted bool           `json:"interrupted"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.cr, rt.res.Check = d.Result, d.Result
			checkStarted = false
			checkInterrupted = d.Interrupted
			decided = nil
		case RecVerdict:
			var d struct {
				Check int    `json:"check"`
				V     string `json:"verdict"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.w.check, rt.verdict = d.Check, d.V
			rt.w.reviewPromoted = false
		case RecVerdictReused:
			var d struct {
				Check int `json:"check"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.w.check = d.Check
			rt.w.reviewPromoted = false
		case RecTamperEvent:
			var d struct{ ID, Path, Change string }
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.w.tamper = append(rt.w.tamper, tamperEvent{id: d.ID, path: d.Path, change: d.Change})
		case RecAmbiguousResolved:
			var d struct {
				Choice    string   `json:"choice"`
				Files     []string `json:"files"`
				From      string   `json:"from"`
				Candidate string   `json:"candidate"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			rt.w.resolved = append(rt.w.resolved, Resolution{Choice: d.Choice, Files: d.Files, From: d.From, Candidate: d.Candidate})
			if d.Choice == "promote" {
				rt.w.reviewPromoted = true
				for _, f := range d.Files {
					rt.w.promoted[f] = true
				}
			}
			rt.res.Candidate = d.Candidate
			decided = nil // the resolution consumed it
		case RecGateOpened:
			var d struct {
				Pins    gate.Pins `json:"pins"`
				Choices []string  `json:"choices"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			openGate = &d.Pins
			decided = nil
			if d.Pins.Gate != gateAmbiguous {
				rt.w.reviewPromoted = false
			}
		case RecGateDecided:
			var d struct {
				Pins   gate.Pins `json:"pins"`
				Choice string    `json:"choice"`
				Reason string    `json:"reason"`
				Note   string    `json:"note"`
				Files  []string  `json:"files"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			openGate = nil
			if d.Choice == "acknowledge" {
				rt.w.acked = len(rt.w.tamper)
			}
			decided = &resumeTail{node: d.Pins.Gate, pins: d.Pins,
				decision: gate.Decision{Choice: d.Choice, Reason: d.Reason, Note: d.Note, Files: d.Files}}
		case RecPreflightObserved:
			rt.preflightObserved = true
		case RecGateAbandoned:
			var d struct {
				Pins gate.Pins `json:"pins"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			if decided != nil {
				// A decided Gate can't be abandoned — the Ledger is
				// inconsistent, so the recorded Decision mustn't be
				// silently dropped.
				return nil, refuse("the %s Gate was abandoned after its Decision was recorded", decided.node)
			}
			openGate = &d.Pins
			// Abandoning a Gate pauses the current review; it does not
			// undo promotions already resolved in that review session.
		case RecRunParked:
			var d struct {
				Gate string `json:"gate"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			parked = d.Gate
		case RecRunEnded:
			var d struct {
				Outcome Outcome  `json:"outcome"`
				Why     []string `json:"why"`
			}
			if err := decode(rec, &d); err != nil {
				return nil, err
			}
			ended, endWhy = string(d.Outcome), d.Why
		}
	}
	if rt.f == nil || rt.snap == "" || rt.m0 == nil {
		return nil, refuse("%s's Ledger stops before the Run had a Pipeline, a Snapshot and an Oracle; nothing to resume", filepath.Base(dir))
	}
	rt.w.p = Params{Frozen: rt.f}
	rt.w.g, rt.w.limits, rt.w.snap = rt.f.Graph, rt.f.Limits, rt.snap
	rt.res.Source = rt.source

	// Where the walk stood. A Gate that opened and was never decided, or
	// that parked the Run, is the tail; a decision or resolution that
	// never took effect applies now; a stage mid-flight re-runs. ended is
	// refused unless the Run stopped interrupted; the rest is positional.
	switch {
	case ended != "" && ended != string(InfrastructureStop):
		return nil, refuse("%s already ended %s", rt.res.ID, ended)
	case ended == string(InfrastructureStop) && !isInterrupt(endWhy):
		return nil, refuse("%s ended in an Infrastructure stop (%s), which resume doesn't re-run", rt.res.ID, strings.Join(endWhy, "; "))
	case parked != "":
		if openGate == nil || openGate.Gate != parked {
			return nil, refuse("%s parked at %s, which resume can't re-open yet", rt.res.ID, parked)
		}
		rt.tail = resumeTail{kind: "gate", node: parked, pins: *openGate}
		return rt, nil
	}
	switch {
	case decided != nil:
		// The decision is durable but the edge it takes never ran.
		if decided.node == gateAmbiguous && (decided.decision.Choice == "promote" || decided.decision.Choice == "drop") {
			rt.tail = resumeTail{kind: "resolve", node: decided.node, pins: decided.pins, decision: decided.decision}
		} else {
			rt.tail = *decided
			rt.tail.kind = "decide"
		}
	case openGate != nil:
		rt.tail = resumeTail{kind: "gate", node: openGate.Gate, pins: *openGate}
	case checkStarted || checkInterrupted:
		rt.tail = resumeTail{kind: "check"}
	case pendingVerify:
		rt.tail = resumeTail{kind: "verify"}
	case rt.verFailure == "interrupted":
		rt.tail = resumeTail{kind: "verify"} // the verifier's Attempt died mid-flight
	case rt.verFailure != "":
		// A verifier failure stops the Run with no Verdict (verify.go).
		rt.tail = resumeTail{kind: "infraend", why: []string{"the verifier Attempt failed: " + rt.verFailure}}
	case pendingImpl:
		rt.tail = resumeTail{kind: "attempt"}
	case rt.impl != nil && rt.impl.Stop != "":
		rt.tail = resumeTail{kind: "infraend", why: []string{rt.impl.Stop}}
	case rt.impl != nil && rt.impl.Failure != "" && rt.impl.Failure != "interrupted":
		rt.tail = resumeTail{kind: "infraend", why: []string{"the implementer Attempt failed: " + rt.impl.Failure}}
	case rt.impl != nil && rt.impl.Failure == "interrupted":
		rt.tail = resumeTail{kind: "attempt"}
	case rt.impl != nil && rt.impl.Exit != "" && rt.impl.Exit != "done":
		// A declared Exit whose Gate isn't built ends the Run (walk.go).
		rt.tail = resumeTail{kind: "infraend",
			why: []string{fmt.Sprintf("the implementer declared Exit %q, and its Gate isn't built yet", rt.impl.Exit)}}
	case lastType == RecVerdict:
		// The Verdict stands; the edge it routes to never ran.
		rt.tail = resumeTail{kind: "route"}
	case lastType == RecCheckEnded || lastType == RecControlEnded:
		// The Check's Verdict was never recorded: write it, then route.
		rt.tail = resumeTail{kind: "verdict"}
	case lastType == RecAmbiguousResolved:
		// A resolution landed but the review never continued.
		rt.tail = resumeTail{kind: "review"}
	case rt.impl != nil && rt.impl.Exit == "done" && rt.impl.Candidate != "":
		// The implementer Attempt ended; whatever came next died first.
		if rt.f.Mode == pipeline.Standard {
			rt.tail = resumeTail{kind: "verify"}
		} else {
			rt.tail = resumeTail{kind: "check"}
		}
	default:
		// Nothing of the walk survived: the first Attempt re-runs.
		rt.tail = resumeTail{kind: "attempt"}
	}
	if rt.tail.kind != "attempt" && rt.impl == nil {
		return nil, refuse("%s's Ledger positions the walk where no implementer Attempt exists", rt.res.ID)
	}
	return rt, nil
}

// Peek reads a Run's frozen Pipeline and Task, so a view of a Run that
// hasn't resumed yet can show them. A torn tail is tolerated: the first
// record survived it.
func Peek(dir string) (*pipeline.Frozen, task.Task, error) {
	recs, _, err := ledger.ReplayHead(dir)
	if err != nil && !errors.Is(err, ledger.ErrTornTail) {
		return nil, task.Task{}, fmt.Errorf("the Run's Ledger: %w", err)
	}
	blobs, err := ledger.OpenBlobs(dir)
	if err != nil {
		return nil, task.Task{}, err
	}
	for _, rec := range recs {
		if rec.Type != RecRunStarted {
			continue
		}
		var d struct {
			Task   string `json:"task"`
			Frozen string `json:"frozen"`
		}
		if err := json.Unmarshal(rec.Data, &d); err != nil {
			return nil, task.Task{}, err
		}
		var t task.Task
		var f *pipeline.Frozen
		var d2 struct {
			FollowUps []string `json:"follow_ups"`
		}
		if err := json.Unmarshal(rec.Data, &d2); err != nil {
			return nil, task.Task{}, err
		}
		if b, err := blobs.Get(d.Task); err == nil {
			t = taskFromBlob(string(b), d2.FollowUps)
		}
		if b, err := blobs.Get(d.Frozen); err == nil {
			f = &pipeline.Frozen{}
			if err := json.Unmarshal(b, f); err != nil {
				f = nil
			}
		}
		return f, t, nil
	}
	return nil, task.Task{}, fmt.Errorf("%s has no RunStarted record", filepath.Base(dir))
}

// taskFromBlob rebuilds a Task as Start built it: Parse gives the base
// text's Title and Criteria, then each follow-up adds its own criteria
// (task.FollowUp) — a resumed Run's verifier briefs name them all.
func taskFromBlob(text string, followUps []string) task.Task {
	t := task.Parse(text)
	for _, fu := range followUps {
		t = t.FollowUp(fu)
	}
	return t
}

// isInterrupt reports whether the Run's why is a cancelled-Run one: the
// cancellation itself, or the Attempt the interrupt killed.
func isInterrupt(why []string) bool {
	for _, w := range why {
		if w == interrupted || strings.HasSuffix(w, ": interrupted") {
			return true
		}
	}
	return false
}
