// Package run is the orchestrator: Preflight, the Run's private state, and
// the walk of the frozen graph. So far it walks Fast mode's main path,
// implementer → Check (ADR-0019), writing every step ahead to the Ledger
// (ADR-0012).
package run

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/redact"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// Outcome is how a Run ended, or the status it stopped in.
type Outcome string

const (
	Accepted Outcome = "Accepted"
	Rejected Outcome = "Rejected"
	// Refused means Preflight refused the Run.
	Refused Outcome = "Refused"
	// InfrastructureStop is a status: no Verdict could be reached.
	InfrastructureStop Outcome = "Infrastructure stop"
)

// Ledger record types, in the order a Run writes them.
const (
	RecRunStarted        = "RunStarted"
	RecSnapshotTaken     = "SnapshotTaken"
	RecOracleVersion     = "OracleVersion"
	RecPreflightObserved = "PreflightObserved"
	RecAttemptStarting   = "AttemptStarting"
	RecProcessStarted    = "ProcessStarted"
	RecAttemptEnded      = "AttemptEnded"
	RecCacheSeeded       = "CacheSeeded"
	RecCheckStarted      = "CheckStarted"
	RecCheckEnded        = "CheckEnded"
	RecVerdict           = "Verdict"
	RecRunEnded          = "RunEnded"
)

// Params is everything a Run starts from.
type Params struct {
	Repo   string // the user's repository root: read only for the Snapshot
	Task   task.Task
	Frozen *pipeline.Frozen
	// Config is the .oge/oge.toml the Pipeline was resolved from, or nil
	// for a flags-only Run. The Snapshot must hold exactly these bytes.
	Config  []byte
	Agents  map[string]agent.Adapter
	State   *ledger.StateRoot
	Version string
	Getenv  func(string) string
	// CacheSeedTemplate is a build cache the Run's seed starts from. Only
	// tests set it; see cli.Env.CacheSeedTemplate.
	CacheSeedTemplate string
	// Observe receives progress as it happens, for rendering.
	Observe func(Event)
}

// EventKind names a progress event.
type EventKind int

const (
	EvStarted   EventKind = iota // the Run exists; Run id and Snapshot known
	EvPreflight                  // Preflight passed (setup ran on the Snapshot)
	EvAgent                      // an agent's normalised event during an Attempt
	EvAttempt                    // an Attempt ended
	EvCheck                      // a Check ended with a Verdict
)

// Event is one progress event.
type Event struct {
	Kind    EventKind
	Result  *Result
	Agent   agent.Event
	Attempt *Attempt
	Check   *oracle.Result
}

// Attempt is one execution of a Stage.
type Attempt struct {
	ID        string
	Stage     string
	Agent     string
	Exit      string
	Failure   string
	Candidate string
	Changed   []string
}

// Result is what a Run ended with.
type Result struct {
	ID        string
	Dir       string // the Run's private directory
	Outcome   Outcome
	Why       []string // for Refused and InfrastructureStop
	Snapshot  string
	Source    workspace.SnapshotInfo
	Oracle    int
	Attempt   *Attempt
	Check     *oracle.Result
	Started   time.Time
	Duration  time.Duration
	Candidate string
}

// interrupted is why a cancelled Run stopped.
// TODO(#40-decision): ADR-0012's interrupted status and exit 130 come
// later; until then a cancelled Run is an Infrastructure stop.
const interrupted = "interrupted: the Run was cancelled"

// ErrNoAdapter means the Stage's agent has no adapter in this build.
var ErrNoAdapter = errors.New("no adapter")

// Start runs a Fast-mode Run to its Verdict. An error means Öge itself
// failed; the Run's Ledger then ends without RunEnded.
func Start(ctx context.Context, p Params) (*Result, error) {
	f := p.Frozen
	if f.Mode != pipeline.Fast {
		return nil, fmt.Errorf("%s mode needs a verifier, which isn't built yet", f.Mode)
	}
	impl := f.Stages[0]
	adapter := p.Agents[impl.Agent]
	if adapter == nil {
		return nil, fmt.Errorf("%w for %s", ErrNoAdapter, impl.Agent)
	}

	if p.Observe == nil {
		p.Observe = func(Event) {}
	}
	// Preflight's repository checks write nothing, so a refusal leaves no
	// Run behind.
	why, err := workspace.Preflight(p.Repo)
	if err != nil {
		return nil, err
	}
	if len(why) > 0 {
		return &Result{Outcome: Refused, Why: why}, nil
	}

	res := &Result{ID: newID(), Started: time.Now()}
	res.Dir = filepath.Join(p.State.Private, "runs", res.ID)
	workDir := filepath.Join(p.State.Work, res.ID)
	if err := os.MkdirAll(res.Dir, 0o700); err != nil {
		return nil, err
	}
	defer oracle.RemoveAll(workDir) // Workspaces are disposable
	l, err := ledger.Create(res.Dir)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	blobs, err := ledger.OpenBlobs(res.Dir)
	if err != nil {
		return nil, err
	}
	end := func(o Outcome, why ...string) (*Result, error) {
		res.Outcome, res.Why, res.Duration = o, why, time.Since(res.Started)
		if err := l.Append(RecRunEnded, map[string]any{"outcome": o, "why": why}); err != nil {
			return nil, err
		}
		return res, nil
	}

	taskBlob, err := blobs.Put([]byte(p.Task.Text))
	if err != nil {
		return nil, err
	}
	frozenJSON, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	frozenBlob, err := blobs.Put(frozenJSON)
	if err != nil {
		return nil, err
	}
	if err := l.Append(RecRunStarted, map[string]any{
		"run": res.ID, "oge_version": p.Version, "created": res.Started.UTC(), "source": p.Repo,
		"task": taskBlob, "frozen": frozenBlob, "graph_hash": f.Hash, "mode": f.Mode,
	}); err != nil {
		return nil, err
	}

	// Snapshot into the Run repository. Nothing reads p.Repo after this.
	repo, err := workspace.InitRunRepo(filepath.Join(res.Dir, "repo.git"))
	if err != nil {
		return nil, err
	}
	ws := filepath.Join(workDir, "implement")
	snap, info, err := repo.TakeSnapshot(p.Repo, ws)
	if err != nil {
		return nil, err
	}
	res.Snapshot, res.Source = snap, info
	if err := l.Append(RecSnapshotTaken, map[string]any{"commit": snap, "head": info.Head, "branch": info.Branch,
		"modified": info.Modified, "untracked": info.Untracked}); err != nil {
		return nil, err
	}
	p.Observe(Event{Kind: EvStarted, Result: res})

	// The config Öge resolved must be the Snapshot's.
	got, inSnap, err := repo.Show(snap, pipeline.ConfigPath)
	if err != nil {
		return nil, err
	}
	if inSnap != (p.Config != nil) || !bytes.Equal(got, p.Config) {
		return end(Refused, pipeline.ConfigPath+" changed while the Run was starting; run oge again")
	}

	m, mBlob, err := oracle.NewV0(repo, snap, f, blobs)
	if err != nil {
		return nil, err
	}
	res.Oracle = m.Version
	if err := l.Append(RecOracleVersion, map[string]any{"version": m.Version, "manifest": mBlob, "tests": len(m.Tests)}); err != nil {
		return nil, err
	}

	runner := &oracle.Runner{Blobs: blobs, PassEnv: f.Project.PassEnv, Getenv: p.Getenv, SeedTemplate: p.CacheSeedTemplate}
	pre := map[string]any{"checks": []string{"submodules", "lfs", "unmerged", "operation_in_progress"}}
	// Setup runs on the Snapshot into the Run's cache seed, whose warm
	// step then overlaps the implementer's Attempt (ADR-0021).
	seed, setup, err := runner.NewSeed(ctx, repo, snap, f.Setup.Run, filepath.Join(res.Dir, "cache-seed"))
	if err != nil {
		return nil, err
	}
	defer seed.Close()
	if setup != nil {
		e := *setup
		pre["setup"] = e
		if ctx.Err() != nil {
			if err := l.Append(RecPreflightObserved, pre); err != nil {
				return nil, err
			}
			return end(InfrastructureStop, interrupted)
		}
		if !e.Pass {
			if err := l.Append(RecPreflightObserved, pre); err != nil {
				return nil, err
			}
			return end(Refused, fmt.Sprintf("setup %q fails on the Snapshot (%s); fix it before running", f.Setup.Run, e.Why))
		}
	}
	if err := l.Append(RecPreflightObserved, pre); err != nil {
		return nil, err
	}
	p.Observe(Event{Kind: EvPreflight, Result: res})

	// The implementer Attempt.
	a, err := implement(ctx, p, l, blobs, repo, adapter, impl, snap, ws)
	if err != nil {
		return nil, err
	}
	res.Attempt = a
	p.Observe(Event{Kind: EvAttempt, Result: res, Attempt: a})
	if a.Failure != "" {
		// TODO(#40-decision): an Attempt failure should retry on its budget
		// and then reach the bound-exhaustion Gate (ADR-0012); with neither
		// built yet the Run stops with no Verdict.
		return end(InfrastructureStop, "the implementer Attempt failed: "+a.Failure)
	}
	if a.Exit != "done" {
		// TODO(#40-decision): other Exits (e.g. infeasible) route to the
		// infeasible Gate (#43); until then the Run stops with no Verdict.
		return end(InfrastructureStop, fmt.Sprintf("the implementer declared Exit %q, and its Gate isn't built yet", a.Exit))
	}
	res.Candidate = a.Candidate

	// The Check, from the cache seed once it is warm. A cancelled Run
	// never reaches a Verdict: the Check it killed didn't fail.
	if seed != nil {
		waitStart := time.Now()
		warm, werr := seed.Wait()
		rec := map[string]any{"warm": warm, "waited_ms": time.Since(waitStart).Milliseconds()}
		if werr != nil {
			rec["error"] = string(redact.Redact([]byte(werr.Error())))
		}
		if err := l.Append(RecCacheSeeded, rec); err != nil {
			return nil, err
		}
		runner.Seed = seed
	}
	if ctx.Err() != nil {
		return end(InfrastructureStop, interrupted)
	}
	if err := l.Append(RecCheckStarted, map[string]any{"check": 1, "candidate": a.Candidate, "oracle_version": m.Version, "manifest": mBlob}); err != nil {
		return nil, err
	}
	cr, err := runner.Check(ctx, repo, m, a.Candidate, f.Setup.Run, filepath.Join(res.Dir, "checks", "1"))
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		if err := l.Append(RecCheckEnded, map[string]any{"check": 1, "result": cr, "uncontained": true, "interrupted": true}); err != nil {
			return nil, err
		}
		return end(InfrastructureStop, interrupted)
	}
	res.Check = cr
	verdict := "fail"
	if cr.Pass {
		verdict = "pass"
	}
	if err := l.Append(RecCheckEnded, map[string]any{"check": 1, "result": cr, "uncontained": true}); err != nil {
		return nil, err
	}
	if err := l.Append(RecVerdict, map[string]any{"check": 1, "verdict": verdict, "candidate": a.Candidate, "oracle_version": m.Version}); err != nil {
		return nil, err
	}
	p.Observe(Event{Kind: EvCheck, Result: res, Check: cr})
	if cr.Pass {
		return end(Accepted)
	}
	// TODO(#40-decision): a fail Verdict ends the Run Rejected (exit 3), as
	// #40 asks. ADR-0007 reserves Rejected for a human and routes a fail
	// Verdict to a send-back, then the bound-exhaustion Gate (#43).
	return end(Rejected)
}

// implement runs the first implementer Attempt: AttemptStarting before the
// spawn, ProcessStarted right after it, AttemptEnded only once the
// Candidate is committed.
func implement(ctx context.Context, p Params, l *ledger.Ledger, blobs *ledger.Blobs, repo *workspace.RunRepo,
	adapter agent.Adapter, stage pipeline.Stage, snap, ws string) (*Attempt, error) {
	a := &Attempt{ID: stage.Name + "#1", Stage: stage.Name, Agent: stage.Agent}
	spec := agent.LaunchSpec{Role: stage.Role, Model: stage.Model, Workspace: ws, Network: stage.Network}
	if err := l.Append(RecAttemptStarting, map[string]any{
		"attempt": a.ID, "stage": stage.Name, "cause": "first", "start_revision": snap, "session": "fresh",
		"launch_profile": map[string]string{"agent": stage.Agent, "model": stage.Model, "role": stage.Role, "network": stage.Network},
	}); err != nil {
		return nil, err
	}
	sess, err := adapter.Open(ctx, spec)
	if err != nil {
		a.Failure = string(redact.Redact([]byte("launch_failed: " + err.Error())))
		return a, l.Append(RecAttemptEnded, map[string]any{"attempt": a.ID, "failure": a.Failure})
	}
	defer sess.Close()
	proc := sess.Process()
	if err := l.Append(RecProcessStarted, map[string]any{"attempt": a.ID, "pid": proc.PID, "pgid": proc.PGID}); err != nil {
		return nil, err
	}
	// The Briefing is the Task for now; #44 brings Öge's Briefing builder.
	if err := sess.Send(agent.Turn{Text: p.Task.Text}); err != nil {
		a.Failure = "send_failed: " + err.Error()
	}
	var stream bytes.Buffer
	enc := json.NewEncoder(&stream)
	settled := false
	timeout := time.NewTimer(p.Frozen.Limits.StageTimeout)
	defer timeout.Stop()
loop:
	for a.Failure == "" {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break loop
			}
			_ = enc.Encode(ev)
			p.Observe(Event{Kind: EvAgent, Agent: ev, Attempt: a})
			if ev.Kind == agent.TurnSettled {
				settled = true
				a.Exit, a.Failure = ev.Exit, ev.Failure
				break loop
			}
		case <-timeout.C:
			_ = sess.Interrupt()
			a.Failure = "timeout"
		case <-ctx.Done():
			_ = sess.Interrupt()
			a.Failure = "interrupted"
		}
	}
	if !settled && a.Failure == "" {
		a.Failure = "lost_subprocess: the turn never settled"
	}
	_ = sess.Close()
	// Everything the agent said is redacted before it's persisted; the
	// Exit name and failure reach AttemptEnded and RunEnded.why.
	a.Exit = string(redact.Redact([]byte(a.Exit)))
	a.Failure = string(redact.Redact([]byte(a.Failure)))
	streamBlob, err := blobs.Put(redact.Redact(stream.Bytes()))
	if err != nil {
		return nil, err
	}
	ended := map[string]any{"attempt": a.ID, "events": streamBlob, "exit": a.Exit, "failure": a.Failure}
	if a.Failure == "" {
		// TODO(#41): Write-scope comparison, revert and Tamper detection
		// run here, before the Candidate is committed.
		c, err := repo.CommitCandidate(ws, snap, "refs/oge/candidates/c1", "Candidate c1 ("+a.ID+")")
		if err != nil {
			return nil, err
		}
		a.Candidate = c
		if a.Changed, err = repo.ChangedFiles(snap, c); err != nil {
			return nil, err
		}
		ended["candidate"] = c
	}
	return a, l.Append(RecAttemptEnded, ended)
}

// newID is a time-sortable Run id.
func newID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}
