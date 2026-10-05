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
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/briefing"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	procs "github.com/erengun/oge/internal/proc"
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
	// Parked is a status: the Run waits for a human decision.
	Parked Outcome = "Parked"
)

// Ledger record types, in the order a Run writes them.
const (
	RecRunStarted        = "RunStarted"
	RecSnapshotTaken     = "SnapshotTaken"
	RecOracleVersion     = "OracleVersion"
	RecPreflightObserved = "PreflightObserved"
	RecAttemptStarting   = "AttemptStarting"
	RecProcessStarted    = "ProcessStarted"
	RecObservation       = "Observation"
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
	// CacheWait bounds how long the Check waits for the seed's warm step
	// (DefaultCacheWait when zero).
	CacheWait time.Duration
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
	EvNotice                     // something the user is told once, in Notice
)

// Event is one progress event.
type Event struct {
	Kind    EventKind
	Result  *Result
	Agent   agent.Event
	Attempt *Attempt
	Check   *oracle.Result
	Notice  string
}

// Attempt is one execution of a Stage.
type Attempt struct {
	ID      string
	Stage   string
	Agent   string
	Exit    string
	Failure string
	// Stop is set when the agent signalled an environmental cause, such
	// as authentication or quota: an Infrastructure stop (ADR-0012).
	Stop      string
	Candidate string
	Changed   []string
	// Reverted are the writes outside the Write scope that Öge undid.
	Reverted []workspace.Revert
	links    map[string]string // the links the scope check let stand
	// FirstActivity is the time from launch to the agent's first visible
	// activity (ADR-0022); zero when it showed none.
	FirstActivity time.Duration
	// Friction is the Attempt's policy friction, summed over its turns
	// (ADR-0019), when its adapter measures it.
	Friction *agent.Friction
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
	// Friction is the Run's policy friction, summed over its Attempts.
	Friction *agent.Friction
}

// DefaultCacheWait bounds the Check's wait for the warm step: past it the
// warm step is stopped and the Check starts from the partial seed, so a
// large module with a fast agent never waits longer than a cold Check.
// TODO(#74-decision): a fixed bound; make it a config key if projects
// need another.
const DefaultCacheWait = 30 * time.Second

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
	release, err := markLive(res.Dir)
	if err != nil {
		return nil, err
	}
	defer release()
	sweepDeadRuns(filepath.Dir(res.Dir), res.Dir)
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
	// The agent's git diff and git status see the Snapshot as HEAD.
	if err := repo.InitWorkspaceGit(ws); err != nil {
		return nil, err
	}
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
	a, err := implement(ctx, p, l, blobs, repo, adapter, impl, res.ID, snap, ws, implementerScope(m, f))
	if err != nil {
		return nil, err
	}
	res.Attempt = a
	res.Friction = agent.SumFriction(res.Friction, a.Friction)
	p.Observe(Event{Kind: EvAttempt, Result: res, Attempt: a})
	if a.Stop != "" {
		return end(InfrastructureStop, a.Stop)
	}
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
		limit := p.CacheWait
		if limit <= 0 {
			limit = DefaultCacheWait
		}
		w := seed.WaitFor(limit)
		w.Error = string(redact.Redact([]byte(w.Error)))
		if err := l.Append(RecCacheSeeded, w); err != nil {
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
		if n := a.Tamper(); n > 0 {
			// TODO(#41-decision): a Tamper event's acknowledgement belongs
			// to the end-of-run review (ADR-0019 #2), whose Gate isn't
			// built yet (#43, #49). Until then the Run parks (exit 10)
			// rather than becoming Accepted without it.
			return end(Parked, tamperWaiting(n))
		}
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
	adapter agent.Adapter, stage pipeline.Stage, runID, snap, ws string, protected func(string) string) (*Attempt, error) {
	a := &Attempt{ID: stage.Name + "#1", Stage: stage.Name, Agent: stage.Agent}
	instructions, err := repoInstructions(repo, snap)
	if err != nil {
		return nil, err
	}
	var checks []string
	for _, c := range p.Frozen.Checks {
		checks = append(checks, c.Run)
	}
	// The agent's tool caches live beside the Workspace, never in it, so
	// they can't become part of the Candidate.
	cache := filepath.Join(filepath.Dir(ws), "cache", stage.Name)
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, err
	}
	spec := agent.LaunchSpec{
		Role: stage.Role, Model: stage.Model, Workspace: ws, Network: stage.Network, RunID: runID,
		RepoInstructions: instructions, CheckCommands: checks, DenyRead: []string{p.State.Private}, Cache: cache,
	}
	brief := briefing.Implementer(p.Task, checks)
	briefingBlob, err := blobs.Put([]byte(brief))
	if err != nil {
		return nil, err
	}
	if err := l.Append(RecAttemptStarting, map[string]any{
		"attempt": a.ID, "stage": stage.Name, "cause": "first", "start_revision": snap, "session": "fresh",
		"launch_profile": map[string]string{"agent": stage.Agent, "model": stage.Model, "role": stage.Role, "network": stage.Network},
		"briefing":       briefingBlob, "repo_instructions": instructions != "",
	}); err != nil {
		return nil, err
	}
	launched := time.Now()
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
	if err := sess.Send(agent.Turn{Text: brief}); err != nil {
		a.Failure = "send_failed: " + err.Error()
	}
	var stream bytes.Buffer
	enc := json.NewEncoder(&stream)
	settled := false
	hosts := map[string]int{}
	var turn *agent.Friction // the turn in flight's friction so far
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
			if a.FirstActivity == 0 && visible(ev) {
				a.FirstActivity = time.Since(launched)
			}
			p.Observe(Event{Kind: EvAgent, Agent: ev, Attempt: a})
			switch ev.Kind {
			case agent.SessionOpened:
				if ev.Session != nil {
					// An environment and capability observation (ADR-0011).
					obs := map[string]any{"attempt": a.ID, "kind": "session",
						"agent_version": ev.Session.AgentVersion, "capabilities": ev.Session.Capabilities,
						"launch_profile": ev.Session.Profile, "envelope": ev.Session.Envelope, "auth_source": ev.Session.AuthSource,
						"since_launch_ms": time.Since(launched).Milliseconds()}
					if r := ev.Session.Residue; r != nil {
						obs["residue"] = r
					}
					if err := l.Append(RecObservation, obs); err != nil {
						return nil, err
					}
					if note := residueNotice(p.State.Private, stage.Agent, ev.Session.Residue); note != "" {
						p.Observe(Event{Kind: EvNotice, Attempt: a, Notice: note})
					}
				}
			case agent.HostRequest:
				if ev.Host != nil {
					hosts[ev.Host.Rule]++
				}
				if ev.Friction != nil {
					turn = ev.Friction // the turn's so far
				}
			case agent.TurnSettled:
				settled = true
				a.Exit, a.Failure, a.Stop = ev.Exit, ev.Failure, ev.Stop
				a.Friction = agent.SumFriction(a.Friction, ev.Friction)
				turn = nil
				break loop
			}
		case <-timeout.C:
			// Nothing read after this counts: a late result can't clear
			// the failure. Close ends the process on its own deadlines.
			_ = sess.Interrupt()
			a.Failure = "timeout"
			break loop
		case <-ctx.Done():
			_ = sess.Interrupt()
			a.Failure = "interrupted"
			break loop
		}
	}
	// A turn that never settled still had its friction.
	a.Friction = agent.SumFriction(a.Friction, turn)
	if !settled && a.Failure == "" {
		a.Failure = "lost_subprocess: the turn never settled"
	}
	_ = sess.Close() // kills the agent's whole process group
	// The scope check below needs the agent's whole tree gone, so nothing
	// writes after it. A tree that outlives the kill fails the Attempt.
	if !procs.WaitGone(proc.PGID, 5*time.Second) && a.Failure == "" {
		a.Failure = "lost_subprocess: the agent's processes outlived the kill"
	}
	// Everything the agent said is redacted before it's persisted; the
	// Exit name and failure reach AttemptEnded and RunEnded.why.
	a.Exit = string(redact.Redact([]byte(a.Exit)))
	a.Failure = string(redact.Redact([]byte(a.Failure)))
	a.Stop = string(redact.Redact([]byte(a.Stop)))
	streamBlob, err := blobs.Put(redact.Redact(stream.Bytes()))
	if err != nil {
		return nil, err
	}
	// How responsive the agent was, and how its Host requests were
	// answered (ADR-0019 metrics, ADR-0022).
	obs := map[string]any{"attempt": a.ID, "kind": "responsiveness", "duration_ms": time.Since(launched).Milliseconds(), "host_requests": hosts}
	if a.FirstActivity > 0 {
		obs["first_activity_ms"] = a.FirstActivity.Milliseconds()
	}
	if f := a.Friction; f != nil {
		obs["policy_friction"] = map[string]int{"lost_turns": f.LostTurns, "denied": f.Denied, "envelope_refusals": f.EnvelopeRefusals}
	}
	if err := l.Append(RecObservation, obs); err != nil {
		return nil, err
	}
	// The scope check runs after every Attempt whose agent ran, failed or
	// not, and before the Candidate is committed.
	if err := enforceScope(l, blobs, repo, a, snap, ws, protected); err != nil {
		return nil, err
	}
	if a.Failure == "" && a.Stop == "" {
		if err := commitCandidate(l, repo, a, snap, ws, protected); err != nil {
			return nil, err
		}
	}
	ended := map[string]any{"attempt": a.ID, "events": streamBlob, "exit": a.Exit, "failure": a.Failure}
	if a.Stop != "" {
		ended["stop"] = a.Stop
	}
	if a.Candidate != "" {
		ended["candidate"] = a.Candidate
	}
	return a, l.Append(RecAttemptEnded, ended)
}

// residueNotice is what to tell the user about an agent's startup residue:
// the first time it appears, and whenever it changes, never on every Run
// (#44). The last-seen residue is kept in the state root, where doctor
// can report it.
func residueNotice(private, agentName string, r *agent.Residue) string {
	path := filepath.Join(private, "agents", agentName+".residue.json")
	var last struct{ Fingerprint string }
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	fp := ""
	if r != nil {
		fp = r.Fingerprint
	}
	if fp == last.Fingerprint {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		b, _ := json.Marshal(map[string]any{"fingerprint": fp, "residue": r, "seen": time.Now().UTC()})
		_ = os.WriteFile(path, b, 0o600)
	}
	if r == nil {
		return agentName + " no longer loads any startup residue"
	}
	var parts []string
	for _, x := range []struct {
		n    int
		what string
	}{{len(r.Plugins), "plugins"}, {len(r.Skills), "skills"}, {len(r.Agents), "subagents"}} {
		if x.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", x.n, x.what))
		}
	}
	return fmt.Sprintf("%s still loads %s when isolated; they can't run a tool Öge doesn't answer. Recorded; shown again only if it changes",
		agentName, strings.Join(parts, ", "))
}

// visible reports whether an agent event shows as activity in the views:
// what the agent says or does, or a request Öge denied.
func visible(ev agent.Event) bool {
	return ev.Kind == agent.Claim || (ev.Kind == agent.HostRequest && ev.Host != nil && ev.Host.Decision != "allow")
}

// repoInstructions is the Snapshot's CLAUDE.md, with whole-line @imports
// of other Snapshot files inlined one level deep.
// TODO(#44-decision): only the root CLAUDE.md, and only whole-line
// "@relative/path" imports (such as "@AGENTS.md"), are injected.
func repoInstructions(repo *workspace.RunRepo, snap string) (string, error) {
	data, ok, err := repo.Show(snap, "CLAUDE.md")
	if err != nil || !ok {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		ref, ok := strings.CutPrefix(strings.TrimSpace(line), "@")
		if !ok || ref == "" || strings.ContainsAny(ref, " \t") || path.IsAbs(ref) {
			continue
		}
		ref = path.Clean(ref)
		if ref == ".." || strings.HasPrefix(ref, "../") {
			continue
		}
		if b, ok, err := repo.Show(snap, ref); err == nil && ok {
			lines[i] = strings.TrimRight(string(b), "\n")
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}

// newID is a time-sortable Run id.
func newID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// tamperWaiting is why a Run with Tamper events and a passing Verdict
// parks.
func tamperWaiting(n int) string {
	events := "1 Tamper event needs"
	if n != 1 {
		events = fmt.Sprintf("%d Tamper events need", n)
	}
	return events + " acknowledging before this Run can be Accepted, and that review isn't built yet"
}
