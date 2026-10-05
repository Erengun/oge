// Package receipt builds a Run's Receipt (ADR-0019 #7, #63): the compact
// end-of-run summary that says why a Run ended the way it did. It is
// computed from the Run's Ledger, its blobs and its Run repository alone,
// never from an agent's word: the one exception is the agent's final
// message, shown verbatim, labelled as a Claim, and only when the Evidence
// disagrees with it. One model renders the terminal text (plain or
// coloured), the Markdown for PRs and the versioned JSON.
package receipt

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// Schema is the version of the --json document (ADR-0015).
const Schema = 1

// Outcomes beyond run.Outcome that a Receipt names.
const (
	// Interrupted is an Infrastructure stop the human caused by
	// cancelling the Run.
	// TODO(#40-decision): ADR-0012's interrupted status isn't recorded as
	// its own outcome yet; the Receipt reads it from RunEnded's why.
	Interrupted = "Interrupted"
	// NotEnded is a Ledger with no RunEnded or RunParked: still running,
	// or Öge itself stopped mid-Run.
	NotEnded = "Not ended"
)

// Receipt is the model every rendering reads. Its JSON form is schema 1.
type Receipt struct {
	Schema  int    `json:"schema"`
	Run     string `json:"run"`
	Mode    string `json:"mode"`
	Task    string `json:"task"`
	Outcome string `json:"outcome"`
	// Headline is the outcome as the Receipt's first line says it.
	Headline string `json:"headline"`
	// Why are the Run's own reasons for a stop, park or refusal.
	Why []string `json:"why,omitempty"`
	// WaitingAt is the Gate a parked Run waits at.
	WaitingAt  string     `json:"waiting_at,omitempty"`
	Candidate  *Candidate `json:"candidate,omitempty"`
	Oracle     int        `json:"oracle_version"`
	Checks     []Check    `json:"checks"`
	QA         *QA        `json:"qa,omitempty"`
	Protected  Protected  `json:"protected"`
	Scope      Scope      `json:"scope"`
	Decisions  []Decision `json:"decisions"`
	Supervised Supervised `json:"supervision"`
	Time       Time       `json:"time"`
	// Claim is the agent's own final message, shown only when the
	// Evidence disagrees with it (Found says how). It is never Evidence.
	Claim *Claim   `json:"claim,omitempty"`
	Found []string `json:"oge_found,omitempty"`
	// Observed are tripwires: signals, not proof (ADR-0020).
	Observed []string `json:"observed,omitempty"`
	// NotCovered is never empty: what this Receipt's Evidence doesn't
	// cover.
	NotCovered []string   `json:"not_covered"`
	Deliveries []Delivery `json:"deliveries"`
	// LedgerHead identifies the Ledger state the Receipt was built from.
	// It is not tamper-proof: whoever can write the Ledger can rewrite it.
	LedgerHead string `json:"ledger_head"`
}

// Candidate is the Candidate the Run ended with.
type Candidate struct {
	Commit       string   `json:"commit"`
	FilesChanged int      `json:"files_changed"`
	Files        []string `json:"files"`
}

// Check is one Check and its Verdict.
type Check struct {
	Number    int    `json:"check"`
	Candidate string `json:"candidate"`
	Oracle    int    `json:"oracle_version"`
	// Verdict is "pass" or "fail"; "" when the Check reached none.
	Verdict string `json:"verdict"`
	// Attested counts the expected Oracle tests by what the attestation
	// channel showed (ADR-0020); HeldOut* count the held-out ones among
	// them.
	Passed        int           `json:"tests_passed"`
	Failed        int           `json:"tests_failed"`
	HeldOut       int           `json:"held_out"`
	HeldOutFailed int           `json:"held_out_failed"`
	Failing       []FailingTest `json:"failing,omitempty"`
	Commands      []Command     `json:"commands"`
	Why           string        `json:"why,omitempty"`
	Infra         string        `json:"infrastructure,omitempty"`
	Skipped       []string      `json:"skipped_on_both,omitempty"`
	NotBuilt      []string      `json:"not_built,omitempty"`
	Stray         int           `json:"stray_attestations,omitempty"`
	VisibleMs     int64         `json:"visible_ms"`
	HeldOutMs     *int64        `json:"heldout_ms,omitempty"`
	Cache         string        `json:"cache"`
	CacheWhy      string        `json:"cache_why,omitempty"`
	CacheMs       int64         `json:"cache_materialise_ms"`
	Uncontained   bool          `json:"uncontained"`
	Interrupted   bool          `json:"interrupted,omitempty"`
}

// FailingTest is an expected test the Check didn't see pass.
type FailingTest struct {
	Test     string   `json:"test"`
	HeldOut  bool     `json:"held_out"`
	Criteria []string `json:"criteria,omitempty"`
}

// Command is one Check command's execution.
type Command struct {
	Run  string `json:"run"`
	Part string `json:"part,omitempty"`
	Pass bool   `json:"pass"`
	Why  string `json:"why,omitempty"`
	Ms   int64  `json:"duration_ms"`
}

// QA is what the verifier added to the Oracle.
type QA struct {
	HeldOutAdded int `json:"held_out_added"`
	Unmapped     int `json:"unmapped"`
	LeftOut      int `json:"additions_left_out"`
	Attempts     int `json:"attempts"`
	// Ambiguous are Candidate files QA's view left out because no output
	// glob matches them.
	Ambiguous []string `json:"ambiguous,omitempty"`
}

// Protected is what happened to writes to protected paths.
type Protected struct {
	Reverted []Tamper `json:"reverted"`
}

// Tamper is one Tamper event.
type Tamper struct {
	ID           string `json:"id"`
	Attempt      string `json:"attempt"`
	Path         string `json:"path"`
	Change       string `json:"change"`
	Acknowledged bool   `json:"acknowledged"`
}

// Scope is the writes outside a Write scope that Öge reverted, other than
// Tamper events.
type Scope struct {
	Reverted []string `json:"reverted"`
	// QADiscarded are the verifier's writes outside its scope.
	QADiscarded []string `json:"qa_discarded"`
	// Degraded are Attempts whose scope enforcement was Degraded.
	Degraded []string `json:"degraded,omitempty"`
}

// Decision is one recorded Gate decision.
type Decision struct {
	Gate   string `json:"gate"`
	Actor  string `json:"actor"`
	Choice string `json:"choice"`
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Supervised is what Öge handled on the human's behalf (positioning:
// supervision summary).
type Supervised struct {
	// Approved are Host requests the Launch profile pre-authorised.
	Approved int `json:"approved_automatically"`
	// Denied are Host requests Öge's policy denied; Cancelled are
	// questions it couldn't put to anyone.
	Denied    int `json:"denied"`
	Cancelled int `json:"cancelled"`
	// SentBack counts send-backs to the implementer.
	SentBack int `json:"sent_back"`
	// Interruptions counts the times a human was asked: Gates decided or
	// abandoned.
	Interruptions int             `json:"human_interruptions"`
	Friction      *agent.Friction `json:"policy_friction,omitempty"`
}

// Time is how long the Run and its stages took, and how much of it was
// the human's.
type Time struct {
	TotalMs int64       `json:"total_ms"`
	Stages  []StageTime `json:"stages"`
	// AttentionMs is human attention time: the sum of every span from a
	// Gate opening to its decision (or its abandonment), the only waits
	// for a human the Ledger records. A Host-request prompt would add
	// its own spans, but none is built yet (#45); a parked Gate nobody
	// was at counts nothing. Time spent before the Run (the Task editor,
	// a first-run config proposal) isn't in the Ledger, so isn't counted.
	AttentionMs int64 `json:"human_attention_ms"`
}

// StageTime is the time spent in one stage, over all its Attempts.
type StageTime struct {
	Stage string `json:"stage"`
	Ms    int64  `json:"ms"`
}

// Claim is the agent's own words, never Evidence.
type Claim struct {
	Label   string `json:"label"` // always "Claim"
	Attempt string `json:"attempt"`
	Exit    string `json:"exit"`
	Text    string `json:"text"`
}

// Delivery is one recorded delivery of the Candidate (#54).
type Delivery struct {
	Kind   string `json:"kind"` // apply | branch
	Flag   string `json:"flag,omitempty"`
	Files  int    `json:"files,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// Source is where a Receipt reads what the Ledger refers to: blobs and
// the Run repository.
type Source interface {
	Blob(id string) ([]byte, error)
	Changed(from, to string) ([]string, error)
}

type runSource struct {
	blobs *ledger.Blobs
	repo  *workspace.RunRepo
}

func (s runSource) Blob(id string) ([]byte, error) {
	if s.blobs == nil {
		return nil, fmt.Errorf("no blob store")
	}
	return s.blobs.Get(id)
}

func (s runSource) Changed(from, to string) ([]string, error) { return s.repo.ChangedFiles(from, to) }

// Build reads the Receipt of the Run in runDir.
func Build(runDir string) (*Receipt, error) {
	recs, head, err := ledger.ReplayHead(runDir)
	if err != nil {
		return nil, fmt.Errorf("reading the Run's Ledger: %w", err)
	}
	blobs, _ := ledger.OpenBlobs(runDir)
	src := runSource{blobs: blobs, repo: &workspace.RunRepo{Dir: filepath.Join(runDir, "repo.git")}}
	return FromRecords(recs, head, src), nil
}

// The record shapes a Receipt reads, as run, gate and delivery write them.
type (
	attemptStarting struct {
		Attempt, Stage, Role, Cause string
	}
	attemptEnded struct {
		Attempt, Candidate, Events, Exit, Failure, Stop string
	}
	observation struct {
		Attempt        string
		Kind           string
		HostRequests   map[string]int  `json:"host_requests"`
		PolicyFriction *friction       `json:"policy_friction"`
		Tripwires      []string        `json:"tripwires"`
		Envelope       string          `json:"envelope"`
		Dropped        json.RawMessage `json:"dropped"`
	}
	friction struct {
		LostTurns        int `json:"lost_turns"`
		Denied           int `json:"denied"`
		EnvelopeRefusals int `json:"envelope_refusals"`
	}
	scopeObserved struct {
		Attempt, Role, State, Enforcement string
		Reverted                          []workspace.Revert
	}
	tamperEvent struct {
		ID, Attempt, Path, Change string
	}
	manifestRec struct {
		Attempt, Role string
		Withheld      []workspace.Withheld
	}
	oracleVersion struct {
		Version  int
		Manifest string
		Attempt  string
		HeldOut  int `json:"held_out"`
		Unmapped int
		Dropped  []json.RawMessage
	}
	checkStarted struct {
		Check         int
		Candidate     string
		OracleVersion int `json:"oracle_version"`
		Manifest      string
	}
	checkEnded struct {
		Check       int
		Result      oracle.Result
		Uncontained bool
		Interrupted bool
	}
	verdictRec struct {
		Check              int
		Verdict, Candidate string
	}
	gateDecided struct {
		Pins                        gate.Pins
		Actor, Choice, Reason, Note string
		TamperIDs                   []string `json:"tamper_ids"`
	}
	gateOpened struct {
		Pins gate.Pins
	}
	runEnded struct {
		Outcome   string
		Why       []string
		Candidate string
		Gate      string
	}
	deliveryRec struct {
		Kind, Flag, Branch string
		Files              int
	}
)

// FromRecords builds the Receipt from a replayed Ledger whose head is
// head, reading blobs and changed files from src.
func FromRecords(recs []ledger.Record, head string, src Source) *Receipt {
	r := &Receipt{Schema: Schema, LedgerHead: head, Checks: []Check{}, Decisions: []Decision{}, Deliveries: []Delivery{},
		Protected: Protected{Reverted: []Tamper{}}, Scope: Scope{Reverted: []string{}, QADiscarded: []string{}}}
	var (
		frozen    *pipeline.Frozen
		snapshot  string
		started   time.Time
		ended     *runEnded
		endedAt   time.Time
		lastAt    time.Time
		setup     *oracle.Execution
		roles     = map[string]string{} // attempt → role
		stages    = map[string]string{} // attempt → stage label
		starts    = map[string]time.Time{}
		checkAt   = map[int]time.Time{}
		stageMs   = map[string]int64{}
		order     []string
		manifests = map[int]*oracle.Manifest{} // check → its manifest
		attempts  []attemptEnded
		tamperIdx = map[string]int{}
		openAt    *time.Time
		abandoned string // the Gate a human was asked at and never answered
		qa        QA
		qaSeen    bool
	)
	addStage := func(label string, d time.Duration) {
		if _, ok := stageMs[label]; !ok {
			order = append(order, label)
		}
		stageMs[label] += d.Milliseconds()
	}
	get := func(raw json.RawMessage, v any) bool { return json.Unmarshal(raw, v) == nil }
	for _, rec := range recs {
		lastAt = rec.At
		switch rec.Type {
		case run.RecRunStarted:
			var d struct{ Run, Task, Frozen, Mode string }
			if !get(rec.Data, &d) {
				continue
			}
			r.Run, r.Mode, started = d.Run, d.Mode, rec.At
			if b, err := src.Blob(d.Task); err == nil {
				r.Task = clean(task.Parse(string(b)).Title)
			}
			if b, err := src.Blob(d.Frozen); err == nil {
				var f pipeline.Frozen
				if json.Unmarshal(b, &f) == nil {
					frozen = &f
				}
			}
		case run.RecSnapshotTaken:
			var d struct{ Commit string }
			get(rec.Data, &d)
			snapshot = d.Commit
		case run.RecPreflightObserved:
			var d struct{ Setup *oracle.Execution }
			get(rec.Data, &d)
			setup = d.Setup
			if !started.IsZero() {
				addStage("preflight", rec.At.Sub(started))
			}
		case run.RecOracleVersion:
			var d oracleVersion
			if !get(rec.Data, &d) {
				continue
			}
			r.Oracle = d.Version
			if d.Attempt != "" {
				qaSeen = true
				qa.HeldOutAdded += d.HeldOut
				qa.Unmapped += d.Unmapped
				qa.LeftOut += len(d.Dropped)
			}
		case run.RecAttemptStarting:
			var d attemptStarting
			if !get(rec.Data, &d) {
				continue
			}
			roles[d.Attempt], starts[d.Attempt] = d.Role, rec.At
			stages[d.Attempt] = d.Stage
			if d.Role == "verifier" {
				stages[d.Attempt] = "QA"
				qa.Attempts++
				qaSeen = true
			}
			if d.Role == "implementer" && d.Cause == "send_back" {
				r.Supervised.SentBack++
			}
		case run.RecBriefingManifest:
			var d manifestRec
			if get(rec.Data, &d) && d.Role == "verifier" {
				qa.Ambiguous = nil
				for _, w := range d.Withheld {
					if w.Class == workspace.ClassAmbiguous {
						qa.Ambiguous = append(qa.Ambiguous, clean(w.Path))
					}
				}
			}
		case run.RecAttemptEnded:
			var d attemptEnded
			if !get(rec.Data, &d) {
				continue
			}
			if t, ok := starts[d.Attempt]; ok {
				addStage(stages[d.Attempt], rec.At.Sub(t))
			}
			if roles[d.Attempt] == "implementer" {
				attempts = append(attempts, d)
			}
		case run.RecObservation:
			var d observation
			if !get(rec.Data, &d) {
				continue
			}
			switch d.Kind {
			case "responsiveness":
				for rule, n := range d.HostRequests {
					switch rule {
					case "pre_authorised":
						r.Supervised.Approved += n
					case "question_unanswerable":
						r.Supervised.Cancelled += n
					default:
						r.Supervised.Denied += n
					}
				}
				if f := d.PolicyFriction; f != nil {
					r.Supervised.Friction = agent.SumFriction(r.Supervised.Friction,
						&agent.Friction{Denied: f.Denied, LostTurns: f.LostTurns, EnvelopeRefusals: f.EnvelopeRefusals})
				}
			case "tripwire":
				for _, t := range d.Tripwires {
					r.Observed = append(r.Observed, clean(t))
				}
			case "session":
				if d.Envelope == "warn" {
					r.Scope.Degraded = append(r.Scope.Degraded, fmt.Sprintf("%s's startup envelope check warned (%s)", stageOf(stages, d.Attempt), clean(d.Attempt)))
				}
			case "oracle_additions_dropped":
				var dropped []json.RawMessage
				_ = json.Unmarshal(d.Dropped, &dropped)
				qa.LeftOut += len(dropped)
			}
			r.NotCovered = append(r.NotCovered, observationNotCovered(d.Kind, rec.Data)...)
		case run.RecScopeObserved:
			var d scopeObserved
			if !get(rec.Data, &d) {
				continue
			}
			if d.Enforcement == workspace.Degraded {
				r.Scope.Degraded = append(r.Scope.Degraded, fmt.Sprintf("Write scope enforcement was Degraded (%s)", clean(d.Attempt)))
			}
			for _, rv := range d.Reverted {
				if rv.Tamper {
					continue // a TamperEvent record says it
				}
				p := clean(rv.Path)
				if d.Role == "verifier" {
					r.Scope.QADiscarded = append(r.Scope.QADiscarded, p)
				} else {
					r.Scope.Reverted = append(r.Scope.Reverted, p)
				}
			}
		case run.RecTamperEvent:
			var d tamperEvent
			if get(rec.Data, &d) {
				tamperIdx[d.ID] = len(r.Protected.Reverted)
				r.Protected.Reverted = append(r.Protected.Reverted, Tamper{ID: d.ID, Attempt: d.Attempt, Path: clean(d.Path), Change: d.Change})
			}
		case run.RecCheckStarted:
			var d checkStarted
			if !get(rec.Data, &d) {
				continue
			}
			checkAt[d.Check] = rec.At
			if b, err := src.Blob(d.Manifest); err == nil {
				var m oracle.Manifest
				if json.Unmarshal(b, &m) == nil {
					manifests[d.Check] = &m
				}
			}
			r.Checks = append(r.Checks, Check{Number: d.Check, Candidate: d.Candidate, Oracle: d.OracleVersion})
		case run.RecCheckEnded:
			var d checkEnded
			if !get(rec.Data, &d) {
				continue
			}
			if t, ok := checkAt[d.Check]; ok {
				addStage("Check", rec.At.Sub(t))
			}
			if c := findCheck(r.Checks, d.Check); c != nil {
				fillCheck(c, &d.Result, manifests[d.Check])
				c.Uncontained, c.Interrupted = d.Uncontained, d.Interrupted
			}
		case run.RecVerdict:
			var d verdictRec
			if get(rec.Data, &d) {
				if c := findCheck(r.Checks, d.Check); c != nil {
					c.Verdict = d.Verdict
				}
			}
		case run.RecGateOpened:
			at := rec.At
			openAt = &at
		case run.RecGateDecided:
			var d gateDecided
			if !get(rec.Data, &d) {
				continue
			}
			abandoned = ""
			r.Decisions = append(r.Decisions, Decision{Gate: d.Pins.Gate, Actor: d.Actor, Choice: clean(d.Choice), Reason: clean(d.Reason), Note: clean(d.Note)})
			if d.Choice == "acknowledge" {
				for _, id := range d.TamperIDs {
					if i, ok := tamperIdx[id]; ok {
						r.Protected.Reverted[i].Acknowledged = true
					}
				}
			}
			r.Supervised.Interruptions++
			if openAt != nil {
				r.Time.AttentionMs += rec.At.Sub(*openAt).Milliseconds()
				openAt = nil
			}
		case run.RecGateAbandoned:
			var d gateOpened
			get(rec.Data, &d)
			abandoned = d.Pins.Gate
			r.Supervised.Interruptions++
			if openAt != nil {
				r.Time.AttentionMs += rec.At.Sub(*openAt).Milliseconds()
				openAt = nil
			}
		case run.RecRunParked:
			var d runEnded
			get(rec.Data, &d)
			d.Outcome = string(run.Parked)
			ended, endedAt = &d, rec.At
		case run.RecRunEnded:
			var d runEnded
			get(rec.Data, &d)
			ended, endedAt = &d, rec.At
		case delivery.RecDelivery:
			var d deliveryRec
			if get(rec.Data, &d) {
				r.Deliveries = append(r.Deliveries, Delivery{Kind: clean(d.Kind), Flag: clean(d.Flag), Files: d.Files, Branch: clean(d.Branch)})
			}
		}
	}
	if r.Mode == "" && frozen != nil {
		r.Mode = string(frozen.Mode)
	}
	if qaSeen {
		r.QA = &qa
	}
	for _, s := range order {
		r.Time.Stages = append(r.Time.Stages, StageTime{Stage: s, Ms: stageMs[s]})
	}
	end := endedAt
	if ended == nil {
		end = lastAt
	}
	if !started.IsZero() {
		r.Time.TotalMs = end.Sub(started).Milliseconds()
	}

	// The Candidate: the one the end names, or the latest committed.
	cand := ""
	for _, a := range attempts {
		if a.Candidate != "" {
			cand = a.Candidate
		}
	}
	if ended != nil && ended.Candidate != "" {
		cand = ended.Candidate
	}
	if cand != "" {
		c := &Candidate{Commit: cand, Files: []string{}}
		if snapshot != "" {
			if files, err := src.Changed(snapshot, cand); err == nil {
				for _, f := range files {
					c.Files = append(c.Files, clean(f))
				}
				c.FilesChanged = len(files)
			}
		}
		r.Candidate = c
	}

	r.outcome(ended, abandoned)
	r.claim(attempts, src)
	r.notCovered(frozen, setup)
	return r
}

func stageOf(stages map[string]string, attempt string) string {
	if s := stages[attempt]; s != "" {
		return s
	}
	return "the agent"
}

func findCheck(cs []Check, n int) *Check {
	for i := range cs {
		if cs[i].Number == n {
			return &cs[i]
		}
	}
	return nil
}

// fillCheck takes a Check's counts from its attested tests, never from a
// report Candidate code can forge (ADR-0020).
func fillCheck(c *Check, res *oracle.Result, m *oracle.Manifest) {
	held := map[oracle.TestID]oracle.HeldOut{}
	if m != nil {
		for _, h := range m.HeldOut {
			held[h.Test] = h
		}
	}
	for _, t := range res.Tests {
		h, isHeld := held[t.TestID]
		if isHeld {
			c.HeldOut++
		}
		switch t.Attested {
		case "pass":
			c.Passed++
		case "skip":
			// counted under Not covered when the Snapshot skipped it too
		default:
			if t.Excluded != "" {
				continue
			}
			c.Failed++
			if isHeld {
				c.HeldOutFailed++
			}
			c.Failing = append(c.Failing, FailingTest{Test: clean(t.TestID.String()), HeldOut: isHeld, Criteria: h.Criteria})
		}
	}
	for _, e := range res.Commands {
		c.Commands = append(c.Commands, Command{Run: clean(e.Run), Part: e.Part, Pass: e.Pass, Why: clean(e.Why), Ms: e.DurationMs})
	}
	if res.Setup != nil && !res.Setup.Pass {
		c.Commands = append([]Command{{Run: clean(res.Setup.Run), Part: "setup", Pass: false, Why: clean(res.Setup.Why), Ms: res.Setup.DurationMs}}, c.Commands...)
	}
	c.Why, c.Infra = clean(res.Why), clean(res.Infra)
	for _, s := range res.Skipped {
		c.Skipped = append(c.Skipped, clean(s))
	}
	for _, s := range res.NotBuilt {
		c.NotBuilt = append(c.NotBuilt, clean(s))
	}
	c.Stray, c.VisibleMs, c.HeldOutMs = res.Stray, res.VisibleMs, res.HeldOutMs
	c.Cache, c.CacheWhy, c.CacheMs = res.Cache, clean(res.CacheWhy), res.CacheMs
}

// last is the latest Check, or nil.
func (r *Receipt) last() *Check {
	if len(r.Checks) == 0 {
		return nil
	}
	return &r.Checks[len(r.Checks)-1]
}

// outcome sets the Outcome, Headline and Why from how the Run ended.
func (r *Receipt) outcome(e *runEnded, abandoned string) {
	if e == nil {
		r.Outcome, r.Headline = NotEnded, "… Not ended: the Ledger has no end yet (the Run is still going, or Öge stopped mid-Run)"
		return
	}
	for _, w := range e.Why {
		r.Why = append(r.Why, clean(w))
	}
	r.Outcome = e.Outcome
	decided := r.lastDecision()
	switch run.Outcome(e.Outcome) {
	case run.Accepted:
		r.Headline = "✓ Accepted"
	case run.Rejected:
		r.Headline = "✗ Not accepted: " + r.rejectedWhy(decided)
	case run.Overridden:
		reason := ""
		if decided != nil {
			reason = decided.Reason
		}
		r.Headline = fmt.Sprintf("! Taken without passing evidence (reason: %s)", orNone(reason))
	case run.Cancelled:
		r.Headline = "■ Cancelled"
		if decided != nil {
			r.Headline += fmt.Sprintf(": you chose %q at the %s", decided.Choice, GateTitle(decided.Gate))
		}
	case run.Infeasible:
		r.Headline = "✗ Infeasible"
	case run.Parked:
		r.WaitingAt = e.Gate
		r.Headline = "… Waiting for you: the " + GateTitle(e.Gate)
	case run.InfrastructureStop:
		verdict := false
		for _, c := range r.Checks {
			verdict = verdict || c.Verdict != ""
		}
		switch {
		case len(e.Why) > 0 && strings.HasPrefix(e.Why[0], "interrupted"):
			r.Outcome, r.Headline = Interrupted, "■ Interrupted (Infrastructure stop): the Run was cancelled"
			r.Why = r.Why[1:]
			if abandoned != "" {
				r.Headline += " at the " + GateTitle(abandoned)
			} else if !verdict {
				r.Headline += " before a Verdict"
			}
		case abandoned != "":
			r.Headline = "■ Infrastructure stop: no decision at the " + GateTitle(abandoned)
		case verdict:
			r.Headline = "■ Infrastructure stop"
		default:
			r.Headline = "■ Infrastructure stop: no Verdict"
		}
	case run.Refused:
		r.Headline = "■ Refused"
	default:
		r.Headline = clean(e.Outcome)
	}
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none given"
	}
	return s
}

func (r *Receipt) lastDecision() *Decision {
	if len(r.Decisions) == 0 {
		return nil
	}
	return &r.Decisions[len(r.Decisions)-1]
}

// rejectedWhy is a Rejected Run's reason: what the last Verdict found,
// then who rejected it.
func (r *Receipt) rejectedWhy(d *Decision) string {
	var parts []string
	if c := r.last(); c != nil && c.Verdict == "fail" {
		s := failureText(c, "still failing")
		if r.Supervised.SentBack > 0 {
			s += " after " + plural(r.Supervised.SentBack, "send-back", "send-backs")
		}
		parts = append(parts, s)
	}
	if d != nil {
		parts = append(parts, fmt.Sprintf("you chose %q at the %s", d.Choice, GateTitle(d.Gate)))
	}
	if len(parts) == 0 {
		return "the Run ended Rejected"
	}
	return strings.Join(parts, "; ")
}

// failureText says what failed in a Check, in counts and named criteria,
// with verb ("failing", "still failing") after the count.
func failureText(c *Check, verb string) string {
	switch {
	case c.Failed > 0 && c.HeldOutFailed == c.Failed:
		return plural(c.Failed, "held-out test", "held-out tests") + " " + verb + criteria(c.Failing)
	case c.Failed > 0 && c.HeldOutFailed > 0:
		return fmt.Sprintf("%s %s (%d held-out)%s", plural(c.Failed, "test", "tests"), verb, c.HeldOutFailed, criteria(c.Failing))
	case c.Failed > 0:
		return plural(c.Failed, "test", "tests") + " " + verb + " (" + names(c.Failing) + ")"
	case c.Why != "":
		return "the Check " + verb + " (" + c.Why + ")"
	}
	for _, e := range c.Commands {
		if !e.Pass {
			return fmt.Sprintf("%s %s (%s)", e.Run, verb, orNone(e.Why))
		}
	}
	return "the Check " + verb
}

func criteria(fs []FailingTest) string {
	seen := map[string]bool{}
	var ids []string
	for _, f := range fs {
		for _, c := range f.Criteria {
			if !seen[c] {
				seen[c] = true
				ids = append(ids, clean(c))
			}
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return " (" + strings.Join(ids, ", ") + ")"
}

func names(fs []FailingTest) string {
	var n []string
	for i, f := range fs {
		if i == 3 {
			n = append(n, "…")
			break
		}
		n = append(n, f.Test)
	}
	return strings.Join(n, ", ")
}

// claim sets the Claim and "Öge found" when the Evidence disagrees with
// what an implementer Attempt claimed.
//
// TODO(#63-decision): the Claim compared is the structured one, Exit
// "done" (the implementer saying it is finished), never a reading of its
// prose. It disagrees when that Attempt's Candidate then failed Öge's
// Check, or when Öge reverted a protected-test change the Attempt made.
// The Receipt shows the latest such Attempt's final message verbatim.
func (r *Receipt) claim(attempts []attemptEnded, src Source) {
	tampered := map[string][]string{}
	for _, t := range r.Protected.Reverted {
		tampered[t.Attempt] = append(tampered[t.Attempt], t.Path)
	}
	failed := map[string]*Check{}
	for i := range r.Checks {
		if c := &r.Checks[i]; c.Verdict == "fail" {
			failed[c.Candidate] = c
		}
	}
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		c := failed[a.Candidate]
		paths := tampered[a.Attempt]
		if a.Exit != "done" || (c == nil || a.Candidate == "") && len(paths) == 0 {
			continue
		}
		r.Claim = &Claim{Label: "Claim", Attempt: clean(a.Attempt), Exit: clean(a.Exit), Text: finalMessage(src, a.Events)}
		if c != nil && a.Candidate != "" {
			r.Found = append(r.Found, fmt.Sprintf("Check #%d failed on its Candidate %s: %s", c.Number, short(a.Candidate), failureText(c, "failing")))
		}
		if len(paths) > 0 {
			r.Found = append(r.Found, fmt.Sprintf("%s reverted: %s", plural(len(paths), "protected-file change", "protected-file changes"), strings.Join(paths, ", ")))
		}
		if after := len(attempts) - 1 - i; after > 0 {
			s := "→ sent back to the implementer"
			if after > 1 {
				s += fmt.Sprintf(" (%d more Attempts)", after)
			}
			if r.Outcome == string(run.Accepted) {
				s += "; the Candidate it then made was Accepted"
			}
			r.Found = append(r.Found, s)
		}
		return
	}
}

// maxClaim bounds the Claim's length on the Receipt.
const maxClaim = 120

// finalMessage is the last thing the agent said (not a tool use) in its
// recorded event stream, already redacted when it was recorded.
func finalMessage(src Source, blob string) string {
	b, err := src.Blob(blob)
	if err != nil {
		return ""
	}
	last := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var e agent.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Kind == agent.Claim && e.Tool == "" && strings.TrimSpace(e.Text) != "" {
			last = e.Text
		}
	}
	return truncate(clean(strings.Join(strings.Fields(last), " ")), maxClaim)
}

// observationNotCovered is the Not covered lines an Observation adds.
// Records later tickets add land here.
// TODO(#97): Ambiguous-file resolutions are Gate decisions, which the
// Receipt already lists; an Ambiguous-file outcome the Evidence doesn't
// cover (a dropped or promoted file nobody reviewed) adds its line here,
// keyed by its Observation kind.
// TODO(#63-decision): "held_out_viewed" is the kind an inspect view that
// shows held-out source to a human records; none is built yet.
func observationNotCovered(kind string, raw json.RawMessage) []string {
	switch kind {
	case "held_out_viewed":
		var d struct{ Files []string }
		_ = json.Unmarshal(raw, &d)
		return []string{fmt.Sprintf("held-out source a human viewed during the Run (%s): those tests are no longer held out from that human", plural(len(d.Files), "file", "files"))}
	}
	return nil
}

// notCovered fills the Not covered list. It is never empty.
func (r *Receipt) notCovered(f *pipeline.Frozen, setup *oracle.Execution) {
	var nc []string
	switch {
	case r.Mode == string(pipeline.Fast):
		nc = append(nc, "No independent tests (fast mode): no QA and no held-out tests")
	case r.QA != nil:
		if r.QA.HeldOutAdded == 0 && len(r.Checks) > 0 {
			nc = append(nc, "QA added no held-out tests")
		}
		if r.QA.Unmapped > 0 {
			nc = append(nc, plural(r.QA.Unmapped, "held-out test names", "held-out tests name")+" no acceptance criterion")
		}
		if n := len(r.QA.Ambiguous); n > 0 {
			nc = append(nc, fmt.Sprintf("%s QA never saw (no output glob matches): %s", plural(n, "new file", "new files"), list(r.QA.Ambiguous)))
		}
	}
	if c := r.last(); c != nil {
		if n := len(c.Skipped); n > 0 {
			nc = append(nc, fmt.Sprintf("Oracle tests skipped on the Snapshot and the Candidate (%d): %s", n, list(c.Skipped)))
		}
		if n := len(c.NotBuilt); n > 0 {
			nc = append(nc, fmt.Sprintf("Oracle test files this machine doesn't build (%d): %s", n, strings.Join(c.NotBuilt, "; ")))
		}
	}
	if f != nil {
		for _, w := range f.TrustWeakening {
			src := string(w.Source)
			if w.Source == pipeline.FromProject {
				src = pipeline.ConfigPath
			}
			nc = append(nc, fmt.Sprintf("trust-weakening option in effect: %s (from %s)", clean(w.Option), clean(src)))
		}
	}
	for _, d := range r.Scope.Degraded {
		nc = append(nc, "Degraded: "+d)
	}
	nc = append(nc, r.NotCovered...) // from Observations
	ran := false
	for _, c := range r.Checks {
		ran = ran || c.Uncontained || c.Verdict != ""
	}
	what := "Checks"
	if setup != nil {
		what = "Setup and Checks"
	}
	if ran {
		nc = append(nc, what+" ran unsandboxed; network not blocked; no isolation against deliberately hostile Candidate code running with your privileges")
	} else {
		nc = append(nc, "No Check reached a Verdict: there is no Evidence on any Candidate")
	}
	r.NotCovered = nc
}

func list(items []string) string {
	if len(items) > 5 {
		return strings.Join(items[:5], ", ") + fmt.Sprintf(" and %d more", len(items)-5)
	}
	return strings.Join(items, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// GateTitle is a Gate node's name as the Receipt says it:
// "bound-exhaustion Gate", "Result gate".
func GateTitle(node string) string {
	switch node {
	case "":
		return "Gate"
	case "gate.result":
		return "Result gate"
	}
	return clean(strings.ReplaceAll(strings.TrimPrefix(node, "gate."), "_", "-")) + " Gate"
}
