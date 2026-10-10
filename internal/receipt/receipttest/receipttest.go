// Package receipttest builds synthetic Ledgers, in the record shapes the
// run, gate and delivery packages write, on a fixed clock: the Receipt's
// goldens come from them. Only tests import it.
package receipttest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/receipt"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// T0 is when every synthetic Run starts.
var T0 = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

// RunID is every synthetic Run's id.
const RunID = "20261005T090000-a1b2c3"

// Snapshot is every synthetic Run's Snapshot commit.
const Snapshot = "84ca1bd0e1f2a3b4c5d6e7f8091a2b3c4d5e6f70"

// Builder is a synthetic Ledger, its blobs and its Run repository's
// changed files.
type Builder struct {
	recs    []ledger.Record
	prev    string
	blobs   map[string][]byte
	changed map[string][]string
	files   map[string][]byte // commit+":"+path → content
	// V0 and Manifests are the Oracle manifests' blob ids by version.
	Manifests map[int]string
}

// New starts a Run in mode with the Task "fix Add" and Oracle v0 (the
// visible TestAdd), its Preflight done at 0.4s.
func New(mode pipeline.Mode, weakening ...pipeline.Weakening) *Builder {
	return newRun(mode, nil, weakening...)
}

// NewWithTests is New with project.test_globs set to globs, for a Run
// whose Candidate adds test files (see File).
func NewWithTests(mode pipeline.Mode, globs ...string) *Builder {
	return newRun(mode, globs)
}

func newRun(mode pipeline.Mode, globs []string, weakening ...pipeline.Weakening) *Builder {
	b := &Builder{blobs: map[string][]byte{}, changed: map[string][]string{}, files: map[string][]byte{}, Manifests: map[int]string{}}
	f := pipeline.Frozen{Mode: mode, TrustWeakening: weakening}
	f.Project.TestGlobs = globs
	frozen, _ := json.Marshal(f)
	b.Add(0, run.RecRunStarted, map[string]any{"run": RunID, "oge_version": "test", "created": T0, "source": "/repo",
		"task": b.Blob([]byte("fix Add\n\nAdd returns 0 for every input.\n")), "frozen": b.Blob(frozen), "mode": mode})
	b.Add(100*time.Millisecond, run.RecSnapshotTaken, map[string]any{"commit": Snapshot, "head": "84ca1bd0e1f2", "branch": "main", "modified": 0, "untracked": 1})
	b.Oracle(150*time.Millisecond, 0, "", nil, 0)
	b.Add(400*time.Millisecond, run.RecPreflightObserved, map[string]any{"checks": []string{"submodules"}})
	return b
}

// Blob stores data and returns its id.
func (b *Builder) Blob(data []byte) string {
	sum := sha256.Sum256(data)
	id := "sha256:" + hex.EncodeToString(sum[:])
	b.blobs[id] = data
	return id
}

// Add appends a record at d after T0.
func (b *Builder) Add(d time.Duration, typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	r := ledger.Record{Format: ledger.Format, Seq: len(b.recs), Prev: b.prev, At: T0.Add(d), Type: typ, Data: raw}
	line, _ := json.Marshal(r)
	sum := sha256.Sum256(append(line, '\n'))
	b.prev = hex.EncodeToString(sum[:])
	b.recs = append(b.recs, r)
}

// Records are the Ledger so far; Head is its head.
func (b *Builder) Records() []ledger.Record { return append([]ledger.Record(nil), b.recs...) }
func (b *Builder) Head() string             { return b.prev }

// Receipt builds the Receipt of the Ledger so far.
func (b *Builder) Receipt() *receipt.Receipt {
	return receipt.FromRecords(b.Records(), b.Head(), source{b})
}

// Source reads the Builder's blobs and changed files.
func (b *Builder) Source() receipt.Source { return source{b} }

type source struct{ b *Builder }

func (s source) Blob(id string) ([]byte, error) {
	if d, ok := s.b.blobs[id]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("no blob %s", id)
}

// File sets the content of path at commit, which a Show then returns. A
// path no File sets at the Snapshot is new in the Candidate.
func (b *Builder) File(commit, path, content string) { b.files[commit+":"+path] = []byte(content) }

func (s source) Show(commit, path string) ([]byte, bool, error) {
	d, ok := s.b.files[commit+":"+path]
	return d, ok, nil
}

func (s source) Changed(from, to string) ([]string, error) { return s.b.changed[from+".."+to], nil }

// Oracle records Oracle version v. A version a verifier Attempt added
// holds heldOut, the held-out tests (each "Name" or "Name:AC-1"), of
// which unmapped name no criterion.
func (b *Builder) Oracle(d time.Duration, v int, attempt string, heldOut []oracle.HeldOut, unmapped int) {
	m := oracle.Manifest{Format: 1, Version: v, Expected: []oracle.TestID{{Package: "fx", Name: "TestAdd"}}, HeldOut: heldOut}
	raw, _ := json.Marshal(m)
	id := b.Blob(raw)
	b.Manifests[v] = id
	data := map[string]any{"version": v, "manifest": id, "tests": 1 + len(heldOut)}
	if attempt != "" {
		data["attempt"], data["held_out"], data["unmapped"], data["parent"] = attempt, len(heldOut), unmapped, v-1
	}
	b.Add(d, run.RecOracleVersion, data)
}

// Attempt is one Attempt's records.
type Attempt struct {
	ID, Role, Stage, Cause string
	From, To               time.Duration
	Candidate              string
	Changed                []string // since the Snapshot
	Exit, Failure          string
	// Claims are what the agent said, in order.
	Claims []string
	// HostRequests are its Host requests by policy rule.
	HostRequests map[string]int
	Friction     *agent.Friction
	Reverted     []workspace.Revert
	Withheld     []workspace.Withheld
	Enforcement  string
	// Kept are the Oracle test files it only added to; nil writes no
	// "kept" list, as before #117.
	Kept []run.KeptTest
}

// Attempt appends a's records.
func (b *Builder) Attempt(a Attempt) {
	if a.Role == "" {
		a.Role, a.Stage = "implementer", "implement"
	}
	if a.Cause == "" {
		a.Cause = "first"
	}
	b.Add(a.From, run.RecAttemptStarting, map[string]any{"attempt": a.ID, "stage": a.Stage, "role": a.Role, "cause": a.Cause, "session": "fresh"})
	b.Add(a.From, run.RecBriefingManifest, map[string]any{"attempt": a.ID, "role": a.Role, "withheld": nonNil(a.Withheld)})
	obs := map[string]any{"attempt": a.ID, "kind": "responsiveness", "duration_ms": (a.To - a.From).Milliseconds(), "host_requests": nonNilMap(a.HostRequests)}
	if f := a.Friction; f != nil {
		obs["policy_friction"] = map[string]int{"lost_turns": f.LostTurns, "denied": f.Denied, "envelope_refusals": f.EnvelopeRefusals}
	}
	b.Add(a.To-30*time.Millisecond, run.RecObservation, obs)
	enf := a.Enforcement
	if enf == "" {
		enf = workspace.RevertOnly
	}
	tamper := 0
	for _, r := range a.Reverted {
		if r.Tamper {
			tamper++
		}
	}
	rev := a.Reverted
	if rev == nil {
		rev = []workspace.Revert{}
	}
	scope := map[string]any{"attempt": a.ID, "role": a.Role, "state": "planned", "reverted": rev, "tamper": tamper, "enforcement": enf}
	if a.Kept != nil {
		scope["kept"] = a.Kept
	}
	b.Add(a.To-20*time.Millisecond, run.RecScopeObserved, scope)
	n := 0
	for _, r := range a.Reverted {
		if r.Tamper {
			n++
			b.Add(a.To-10*time.Millisecond, run.RecTamperEvent, map[string]any{"id": fmt.Sprintf("%s/tamper-%d", a.ID, n), "attempt": a.ID,
				"path": r.Path, "class": r.Class, "change": r.Change, "reverted": true, "acknowledged": false})
		}
	}
	var events bytes.Buffer
	enc := json.NewEncoder(&events)
	_ = enc.Encode(agent.Event{Kind: agent.SessionOpened})
	for _, c := range a.Claims {
		_ = enc.Encode(agent.Event{Kind: agent.Claim, Text: c})
	}
	_ = enc.Encode(agent.Event{Kind: agent.TurnSettled, Exit: a.Exit, Failure: a.Failure})
	ended := map[string]any{"attempt": a.ID, "events": b.Blob(events.Bytes()), "exit": a.Exit, "failure": a.Failure}
	if a.Candidate != "" {
		ended["candidate"] = a.Candidate
		b.changed[Snapshot+".."+a.Candidate] = a.Changed
	}
	b.Add(a.To, run.RecAttemptEnded, ended)
}

// Test is one expected test's attested disposition.
type Test struct {
	Name, Attested, Snapshot string
}

// Check appends Check n of candidate on Oracle version v, from..to, with
// the tests' dispositions; its Verdict follows unless the Check reached
// none (infra set).
func (b *Builder) Check(n int, candidate string, v int, from, to time.Duration, tests []Test, extra func(*oracle.Result)) {
	b.Add(from, run.RecCheckStarted, map[string]any{"check": n, "candidate": candidate, "oracle_version": v, "manifest": b.Manifests[v]})
	res := oracle.Result{Pass: true, Cache: oracle.CacheClone, CacheMs: 210, VisibleMs: (to - from).Milliseconds()}
	cmd := oracle.Execution{Run: "go test -json ./...", Pass: true, DurationMs: (to - from).Milliseconds()}
	for _, t := range tests {
		res.Tests = append(res.Tests, oracle.TestResult{TestID: oracle.TestID{Package: "fx", Name: t.Name}, Attested: t.Attested, Snapshot: t.Snapshot})
		if t.Attested != "pass" && t.Attested != "skip" {
			res.Pass, cmd.Pass, cmd.Why = false, false, "exit 1"
		}
	}
	res.Commands = []oracle.Execution{cmd}
	if extra != nil {
		extra(&res)
	}
	b.Add(to-10*time.Millisecond, run.RecCheckEnded, map[string]any{"check": n, "result": res, "uncontained": true})
	if res.Infra != "" {
		return
	}
	verdict := "fail"
	if res.Pass {
		verdict = "pass"
	}
	b.Add(to, run.RecVerdict, map[string]any{"check": n, "verdict": verdict, "candidate": candidate, "oracle_version": v})
}

// Gate opens node at from and, unless choice is "", records the human's
// decision at to.
func (b *Builder) Gate(node string, from, to time.Duration, choice, reason string, tamperIDs ...string) {
	pins := map[string]any{"gate": node, "attempt": "implement#1", "candidate": "", "oracle_version": 0, "verdicts": []int{1}}
	b.Add(from, run.RecGateOpened, map[string]any{"pins": pins, "choices": []string{choice}})
	if choice == "" {
		return
	}
	rec := map[string]any{"pins": pins, "actor": "human", "choice": choice, "reason": reason, "note": ""}
	if len(tamperIDs) > 0 {
		rec["tamper_ids"] = tamperIDs
	}
	b.Add(to, run.RecGateDecided, rec)
}

// Resolve records one Ambiguous-file decision at the review: the Gate,
// the human's choice of files, and the new Candidate it made.
func (b *Builder) Resolve(from, to time.Duration, choice string, files []string, fromCand, cand string) {
	pins := map[string]any{"gate": "gate.ambiguous_file", "attempt": "implement#1", "candidate": fromCand, "files": files}
	b.Add(from, run.RecGateOpened, map[string]any{"pins": pins, "choices": []string{"promote", "drop"}})
	b.Add(to, run.RecGateDecided, map[string]any{"pins": pins, "actor": "human", "choice": choice, "files": files})
	b.Add(to, run.RecAmbiguousResolved, map[string]any{"choice": choice, "files": files, "from": fromCand, "candidate": cand, "attempt": "implement#1"})
	b.changed[Snapshot+".."+cand] = b.changed[Snapshot+".."+fromCand]
}

// Unresolved records a Run's end with Ambiguous files nobody resolved.
func (b *Builder) Unresolved(d time.Duration, o run.Outcome, gate string, files []string, why ...string) {
	if o == run.Parked {
		b.Add(d, run.RecRunParked, map[string]any{"gate": gate, "why": why, "unresolved": files})
		return
	}
	b.Add(d, run.RecRunEnded, map[string]any{"outcome": o, "why": why, "unresolved": files})
}

// End records RunEnded with outcome at d.
func (b *Builder) End(d time.Duration, o run.Outcome, candidate string, why ...string) {
	b.Add(d, run.RecRunEnded, map[string]any{"outcome": o, "why": why, "candidate": candidate})
}

// Park records RunParked at gate.
func (b *Builder) Park(d time.Duration, gate string, why ...string) {
	b.Add(d, run.RecRunParked, map[string]any{"gate": gate, "why": why})
}

// Deliver records a Delivery.
func (b *Builder) Deliver(d time.Duration, kind string, files int) {
	b.Add(d, delivery.RecDelivery, map[string]any{"kind": kind, "files": files, "flag": ""})
}

func nonNil(w []workspace.Withheld) []workspace.Withheld {
	if w == nil {
		return []workspace.Withheld{}
	}
	return w
}

func nonNilMap(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

// Candidates the scenarios use.
const (
	C1 = "6d1231d9f00d1e2f3a4b5c6d7e8f90a1b2c3d4e5"
	C2 = "5a1bd801f27ab642a46d7c5efe8d105508e4a498"
)

var ms = time.Millisecond

// Scenario is a named synthetic Run.
type Scenario struct {
	Name  string
	Build func() *Builder
}

// fastAttempt is the happy-path implementer Attempt: 18 operations
// pre-authorised, 2 denied.
func fastAttempt() Attempt {
	return Attempt{ID: "implement#1", From: 500 * ms, To: 6100 * ms, Candidate: C1, Changed: []string{"add.go"}, Exit: "done",
		Claims: []string{"fixing Add in add.go", "Done. All tests pass."}, HostRequests: map[string]int{"pre_authorised": 18, "outside_role": 2}}
}

// Scenarios are one synthetic Run per outcome variant.
func Scenarios() []Scenario {
	return []Scenario{
		{"accepted-fast", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(fastAttempt())
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "pass", ""}}, nil)
			b.End(7300*ms, run.Accepted, C1)
			return b
		}},
		{"accepted-repaired", func() *Builder {
			// Standard: the implementer says all tests pass, QA's held-out
			// test fails, one send-back repairs it.
			b := New(pipeline.Standard)
			b.Attempt(Attempt{ID: "implement#1", From: 500 * ms, To: 4 * time.Second, Candidate: C1, Changed: []string{"add.go"}, Exit: "done",
				Claims: []string{"fixing Add in add.go", "Done. All tests pass."}, HostRequests: map[string]int{"pre_authorised": 12}})
			b.Attempt(Attempt{ID: "verify#1", Role: "verifier", Stage: "verify", From: 4100 * ms, To: 9 * time.Second, Exit: "extended",
				Claims: []string{"Wrote a held-out test for negative numbers."}, HostRequests: map[string]int{"pre_authorised": 6}})
			b.Oracle(9100*ms, 1, "verify#1", []oracle.HeldOut{{Test: oracle.TestID{Package: "fx", Name: "TestAddNegatives"}, File: "neg_test.go", Attempt: "verify#1", Criteria: []string{"AC-2"}}}, 0)
			b.Check(1, C1, 1, 9200*ms, 13*time.Second, []Test{{"TestAdd", "pass", ""}, {"TestAddNegatives", "fail", ""}}, nil)
			b.Attempt(Attempt{ID: "implement#2", Cause: "send_back", From: 13100 * ms, To: 20 * time.Second, Candidate: C2, Changed: []string{"add.go"}, Exit: "done",
				Claims: []string{"Handled negative numbers too."}})
			b.Attempt(Attempt{ID: "verify#2", Role: "verifier", Stage: "verify", Cause: "send_back", From: 20100 * ms, To: 24 * time.Second, Exit: "no_additions"})
			b.Check(2, C2, 1, 24100*ms, 28*time.Second, []Test{{"TestAdd", "pass", ""}, {"TestAddNegatives", "pass", ""}}, nil)
			b.End(28100*ms, run.Accepted, C2)
			return b
		}},
		{"accepted-degraded", func() *Builder {
			// Standard with a reverted, acknowledged test change, a
			// trust-weakening option, Degraded scope enforcement and
			// coverage gaps the Snapshot already had.
			b := New(pipeline.Standard, pipeline.Weakening{Option: "setup network = on", Source: pipeline.FromProject})
			b.Attempt(Attempt{ID: "implement#1", From: 500 * ms, To: 4 * time.Second, Candidate: C1, Changed: []string{"add.go", "notes/plan.md"}, Exit: "done",
				Claims: []string{"Fixed Add."}, Enforcement: workspace.Degraded,
				Reverted: []workspace.Revert{{Path: "add_test.go", Change: "modified", Class: run.ClassOracleTest, Tamper: true}, {Path: ".oge/oge.toml", Change: "modified", Class: run.ClassOgeConfig, Tamper: true}, {Path: "../escape", Change: "added", Class: workspace.ClassSymlinkEscape}}})
			b.Attempt(Attempt{ID: "verify#1", Role: "verifier", Stage: "verify", From: 4100 * ms, To: 9 * time.Second, Exit: "extended",
				Withheld: []workspace.Withheld{{Path: "notes/plan.md", Class: workspace.ClassAmbiguous}},
				Reverted: []workspace.Revert{{Path: "add.go", Change: "modified", Class: "outside_write_scope"}}})
			b.Oracle(9100*ms, 1, "verify#1", []oracle.HeldOut{
				{Test: oracle.TestID{Package: "fx", Name: "TestAddNegatives"}, File: "neg_test.go", Criteria: []string{"AC-1"}},
				{Test: oracle.TestID{Package: "fx", Name: "TestAddOverflow"}, File: "neg_test.go"}}, 1)
			b.Check(1, C1, 1, 9200*ms, 13*time.Second, []Test{{"TestAdd", "pass", ""}, {"TestAddNegatives", "pass", ""}, {"TestAddOverflow", "pass", ""}, {"TestNeedsTool", "skip", "skip"}},
				func(r *oracle.Result) {
					r.Skipped = []string{"fx.TestNeedsTool"}
					r.NotBuilt = []string{"add_windows_test.go — GOOS=windows only"}
				})
			b.Gate("gate.tamper", 13100*ms, 25100*ms, "acknowledge", "the agent tidied a test; reverted is fine", "implement#1/tamper-1", "implement#1/tamper-2")
			b.End(25200*ms, run.Accepted, C1)
			return b
		}},
		{"rejected", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(fastAttempt())
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "fail", ""}}, nil)
			b.Attempt(Attempt{ID: "implement#2", Cause: "send_back", From: 7300 * ms, To: 11 * time.Second, Candidate: C2, Changed: []string{"add.go"}, Exit: "done",
				Claims: []string{"Fixed it for real this time."}})
			b.Check(2, C2, 0, 11100*ms, 12*time.Second, []Test{{"TestAdd", "fail", ""}}, nil)
			b.Gate("gate.bound_exhaustion", 12100*ms, 30100*ms, "reject", "wrong approach")
			b.End(30200*ms, run.Rejected, C2)
			return b
		}},
		{"overridden", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(fastAttempt())
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "fail", ""}}, nil)
			b.Gate("gate.bound_exhaustion", 7300*ms, 19300*ms, "override", "the flaky test is wrong")
			b.End(19400*ms, run.Overridden, C1)
			return b
		}},
		{"parked", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(fastAttempt())
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "fail", ""}}, nil)
			b.Gate("gate.bound_exhaustion", 7300*ms, 0, "", "")
			b.Park(7400*ms, "gate.bound_exhaustion", "The Check failed and the send-back limit (0) is used up.")
			return b
		}},
		{"cancelled", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(fastAttempt())
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "pass", ""}}, nil)
			b.Gate("gate.result", 7300*ms, 9300*ms, "quit", "")
			b.End(9400*ms, run.Cancelled, C1)
			return b
		}},
		{"infeasible", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(Attempt{ID: "implement#1", From: 500 * ms, To: 3 * time.Second, Exit: "infeasible", Claims: []string{"This can't be done without changing the API."}})
			b.End(3100*ms, run.Infeasible, "", "the implementer declared Exit \"infeasible\"")
			return b
		}},
		{"infrastructure-stop", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(Attempt{ID: "implement#1", From: 500 * ms, To: 6100 * ms, Exit: "", Failure: "timeout"})
			b.End(6200*ms, run.InfrastructureStop, "", "the implementer Attempt failed: timeout")
			return b
		}},
		{"interrupted", func() *Builder {
			b := New(pipeline.Standard)
			b.Attempt(Attempt{ID: "implement#1", From: 500 * ms, To: 2 * time.Second, Failure: "interrupted"})
			b.End(2100*ms, run.InfrastructureStop, "", "interrupted: the Run was cancelled")
			return b
		}},
		{"not-ended", func() *Builder {
			b := New(pipeline.Standard)
			b.Add(500*ms, run.RecAttemptStarting, map[string]any{"attempt": "implement#1", "stage": "implement", "role": "implementer", "cause": "first"})
			return b
		}},
		{"interrupted-after-send-back", func() *Builder {
			// The Check failed C1; the send-back made C2, and the Run was
			// cancelled before any Check judged it.
			b := New(pipeline.Fast)
			b.Attempt(fastAttempt())
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "fail", ""}}, nil)
			b.Attempt(Attempt{ID: "implement#2", Cause: "send_back", From: 7300 * ms, To: 11 * time.Second, Candidate: C2, Changed: []string{"add.go"}, Failure: "interrupted"})
			b.End(11100*ms, run.InfrastructureStop, C2, "interrupted: the Run was cancelled")
			return b
		}},
		{"rejected-unchecked", func() *Builder {
			// Rejected at the Ambiguous-file review of C2 before its Check.
			b := New(pipeline.Fast)
			b.Attempt(fastAttempt())
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "fail", ""}}, nil)
			b.Attempt(Attempt{ID: "implement#2", Cause: "send_back", From: 7300 * ms, To: 11 * time.Second, Candidate: C2, Changed: []string{"add.go", "notes.md"}, Exit: "done"})
			b.Gate("gate.ambiguous_file", 11100*ms, 15*time.Second, "reject", "not this approach")
			b.End(15100*ms, run.Rejected, C2)
			return b
		}},
		{"accepted-ambiguous-resolved", func() *Builder {
			// Standard: QA's view left out two new files; the review dropped
			// one and promoted the other, and the final Check passed.
			b := New(pipeline.Standard)
			b.Attempt(Attempt{ID: "implement#1", From: 500 * ms, To: 4 * time.Second, Candidate: C1, Changed: []string{"add.go", "NOTES.md", "tools/gen.go"}, Exit: "done"})
			b.Attempt(Attempt{ID: "verify#1", Role: "verifier", Stage: "verify", From: 4100 * ms, To: 9 * time.Second, Exit: "no_additions",
				Withheld: []workspace.Withheld{{Path: "NOTES.md", Class: workspace.ClassAmbiguous}, {Path: "tools/gen.go", Class: workspace.ClassAmbiguous}}})
			b.Check(1, C1, 0, 9200*ms, 13*time.Second, []Test{{"TestAdd", "pass", ""}}, nil)
			b.Resolve(13100*ms, 20*time.Second, "drop", []string{"NOTES.md"}, C1, C2)
			const c3 = "9f00d6d1e2f3a4b5c6d7e8f90a1b2c3d4e5f6a7b"
			b.Resolve(20100*ms, 24*time.Second, "promote", []string{"tools/gen.go"}, C2, c3)
			b.Attempt(Attempt{ID: "verify#2", Role: "verifier", Stage: "verify", Cause: "send_back", From: 24100 * ms, To: 28 * time.Second, Exit: "no_additions"})
			b.Check(2, c3, 0, 28100*ms, 31*time.Second, []Test{{"TestAdd", "pass", ""}}, nil)
			b.End(31100*ms, run.Accepted, c3)
			return b
		}},
		{"parked-ambiguous", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(Attempt{ID: "implement#1", From: 500 * ms, To: 6100 * ms, Candidate: C1, Changed: []string{"add.go", "docs/debug.md", "tmp/result.json"}, Exit: "done"})
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "pass", ""}}, nil)
			b.Gate("gate.ambiguous_file", 7300*ms, 0, "", "")
			b.Unresolved(7400*ms, run.Parked, "gate.ambiguous_file", []string{"docs/debug.md", "tmp/result.json"}, "2 new files were not covered by the declared output/test globs:")
			return b
		}},
		{"accepted-implementer-tests", func() *Builder {
			// A new test file the implementer wrote: delivered, never run.
			b := NewWithTests(pipeline.Fast, "*_test.go")
			a := fastAttempt()
			a.Changed = []string{"add.go", "extra_test.go"}
			b.File(C1, "extra_test.go", "package fx\n\nimport \"testing\"\n\nfunc helper() {}\nfunc TestExtraA(t *testing.T) {}\nfunc TestExtraB(t *testing.T) {}\nfunc Testing() {}\n")
			b.Attempt(a)
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "pass", ""}}, nil)
			b.End(7300*ms, run.Accepted, C1)
			return b
		}},
		{"accepted-kept-additions", func() *Builder {
			// Additions to an existing Oracle test file, kept (#117).
			b := New(pipeline.Fast)
			a := fastAttempt()
			a.Changed = []string{"add.go", "add_test.go"}
			a.Kept = []run.KeptTest{{Path: "add_test.go", Class: run.ClassOracleTestAddition, OID: "e69de29", Added: []string{"TestAddZero", "TestAddNeg"}}}
			b.Attempt(a)
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "pass", ""}}, nil)
			b.End(7300*ms, run.Accepted, C1)
			return b
		}},
		{"accepted-kept-additions-resolved", func() *Builder {
			// Kept additions, then an Ambiguous-file drop made the final
			// Candidate: no Attempt committed it, the resolution did.
			b := New(pipeline.Fast)
			a := fastAttempt()
			a.Changed = []string{"add.go", "add_test.go", "scratch.txt"}
			a.Kept = []run.KeptTest{{Path: "add_test.go", Class: run.ClassOracleTestAddition, OID: "e69de29", Added: []string{"TestAddZero"}}}
			b.Attempt(a)
			b.Resolve(6200*ms, 6300*ms, "drop", []string{"scratch.txt"}, C1, C2)
			b.Check(1, C2, 0, 6400*ms, 7400*ms, []Test{{"TestAdd", "pass", ""}}, nil)
			b.End(7500*ms, run.Accepted, C2)
			return b
		}},
		{"accepted-implementer-tests-both", func() *Builder {
			// Kept additions and a new test file together.
			b := NewWithTests(pipeline.Fast, "*_test.go")
			a := fastAttempt()
			a.Changed = []string{"add.go", "add_test.go", "extra_test.go"}
			a.Kept = []run.KeptTest{{Path: "add_test.go", Class: run.ClassOracleTestAddition, OID: "e69de29", Added: []string{"TestAddZero"}}}
			b.File(C1, "extra_test.go", "package fx\n\nimport \"testing\"\n\nfunc TestExtra(t *testing.T) {}\n")
			b.Attempt(a)
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "pass", ""}}, nil)
			b.End(7300*ms, run.Accepted, C1)
			return b
		}},
		{"accepted-delivered", func() *Builder {
			b := New(pipeline.Fast)
			b.Attempt(fastAttempt())
			b.Check(1, C1, 0, 6200*ms, 7200*ms, []Test{{"TestAdd", "pass", ""}}, nil)
			b.End(7300*ms, run.Accepted, C1)
			b.Deliver(60*time.Second, "apply", 1)
			return b
		}},
	}
}

// WriteRun writes the Ledger and its blobs as a Run directory dir, for a
// test that reads a Run from disk. Record times are the write's own.
func (b *Builder) WriteRun(dir string) error {
	l, err := ledger.Create(dir)
	if err != nil {
		return err
	}
	defer l.Close()
	blobs, err := ledger.OpenBlobs(dir)
	if err != nil {
		return err
	}
	for _, data := range b.blobs {
		if _, err := blobs.Put(data); err != nil {
			return err
		}
	}
	for _, r := range b.recs {
		if err := l.Append(r.Type, r.Data); err != nil {
			return err
		}
	}
	return nil
}

// Named is the scenario called name, built.
func Named(name string) *Builder {
	for _, s := range Scenarios() {
		if s.Name == name {
			return s.Build()
		}
	}
	panic("no scenario " + name)
}
