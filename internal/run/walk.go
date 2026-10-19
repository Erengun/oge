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
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/redact"
	"github.com/erengun/oge/internal/workspace"
)

// runEnder returns the closure that records the Run's outcome in its
// Ledger and returns its Result. w is a pointer to the caller's walk so
// a Run that stops before its walk exists still ends cleanly, and a
// Resumed Run's replayed walk is what the ending sees.
func runEnder(ctx context.Context, res *Result, w **walk, l *ledger.Ledger) func(Outcome, ...string) (*Result, error) {
	return func(o Outcome, why ...string) (*Result, error) {
		superseded := o == InfrastructureStop && errors.Is(context.Cause(ctx), ErrSuperseded)
		if superseded {
			why = []string{WhySuperseded} // a newer cut's Check covers this one's
		}
		res.Outcome, res.Why, res.Duration = o, why, time.Since(res.Started)
		if o != Accepted && *w != nil && res.Candidate != "" {
			// The final Candidate's: a Run that stops before its Check
			// never classified it, and the last list is an earlier one's.
			if err := (*w).classify(res.Candidate); err != nil {
				return nil, err
			}
			res.Unresolved = (*w).ambiguousPaths()
		}
		rec, data := RecRunEnded, map[string]any{"outcome": o, "why": why, "candidate": res.Candidate}
		if o == Parked {
			rec, data = RecRunParked, map[string]any{"gate": res.Gate, "why": why}
		}
		if superseded {
			data["stop"] = "superseded"
		}
		if len(res.Unresolved) > 0 {
			// The Receipt's Not covered line (#63).
			data["unresolved"] = res.Unresolved
		}
		if err := l.Append(rec, data); err != nil {
			return nil, err
		}
		return res, nil
	}
}

// walker is what the walk's loop needs that a fresh Start and a Resume
// supply the same way: the runtime handles, the durable objects, and the
// walk's recorded position. Start fills it as the Run's own setup
// produces each piece; Resume rebuilds it from the Ledger.
type walker struct {
	p       Params
	res     *Result
	l       *ledger.Ledger
	blobs   *ledger.Blobs
	repo    *workspace.RunRepo
	runner  *oracle.Runner
	adapter agent.Adapter
	impl    pipeline.Stage
	qa      *qaStage
	m       *oracle.Manifest
	mBlob   string
	seed    *oracle.Seed
	control *oracle.Control
	scope   func(string) string
	workDir string
	frozen  string // the frozen graph's blob, for a cut's Check reuse
	start   string // the implementer's starting revision (Snapshot or a follow-up's rebased start)
	end     func(Outcome, ...string) (*Result, error)

	// join waits on Preflight (setup, the seed, the control) once, or is
	// nil when the resumed Run already observed it. joined remembers it.
	join   func() (stop bool, r *Result, err error)
	joined bool

	// warm, when set, receives a resumed Run's seed and Snapshot control
	// once: they build while a Gate waits so resume doesn't delay a
	// decision on work the walk may never reach (Start's are joined
	// eagerly, so warm stays nil there).
	warm <-chan warmup
	// needPreflight is set by Resume when the Ledger never recorded a
	// PreflightObserved: the warm's setup Execution becomes it.
	needPreflight bool
}

// warmup is the seed and control a resumed Run rebuilds in the
// background; the walk waits on it only where they're used. setup is the
// Preflight's setup Execution when the Pipeline has one.
type warmup struct {
	seed    *oracle.Seed
	setup   *oracle.Execution
	control *oracle.Control
	err     error
}

// ensureWarm receives the warmup once; a Run that has its seed and
// control already (Start's Preflight) returns straight away. stop means
// the Run ends here: the observed setup failed on the Snapshot, as a
// fresh Run's Preflight join refuses it.
func (s *walker) ensureWarm() (stop bool, r *Result, err error) {
	if s.warm == nil {
		return false, nil, nil
	}
	w := <-s.warm
	s.warm = nil
	if w.err != nil {
		return true, nil, w.err
	}
	s.seed, s.control = w.seed, w.control
	if s.qa != nil {
		s.qa.seed = s.seed
	}
	if s.needPreflight {
		// The interrupted Run never recorded its Preflight's observation;
		// this warm is its observation, in the Ledger order a fresh Run's
		// join writes it.
		s.needPreflight = false
		pre := map[string]any{"checks": []string{"submodules", "lfs", "unmerged", "operation_in_progress"}}
		if w.setup != nil {
			pre["setup"] = *w.setup
		}
		if err := s.l.Append(RecPreflightObserved, pre); err != nil {
			return true, nil, err
		}
		s.p.Observe(Event{Kind: EvPreflight, Result: s.res})
	}
	if w.setup != nil && !w.setup.Pass {
		e := *w.setup
		s.p.Observe(Event{Kind: EvPreflight, Result: s.res, Why: e.Why})
		r, err := s.end(Refused, fmt.Sprintf("setup %q fails on the Snapshot (%s); fix it before running", s.p.Frozen.Setup.Run, e.Why))
		return true, r, err
	}
	return false, nil, nil
}

// joinPreflight joins the Preflight once when the Run has one pending;
// a resumed Run joined long ago, so it returns straight away.
func (s *walker) joinPreflight() (stop bool, r *Result, err error) {
	if s.joined {
		return false, nil, nil
	}
	s.joined = true
	if s.join == nil {
		return false, nil, nil
	}
	return s.join()
}

// loop runs the walk's Check-and-send-back cycle. Start enters it at
// check 1 with the first attemptSpec; Resume enters it mid-graph with
// whatever the Ledger says comes next.
func (s *walker) loop(ctx, attemptCtx context.Context, w *walk, a *Attempt, next attemptSpec, resume string, check int) (*Result, error) {
	var err error
	for ; ; check++ {
		if resume == "" {
			w.attempts++
			a, err = implement(attemptCtx, s.p, s.l, s.blobs, s.repo, s.adapter, s.impl, s.res.ID, w.snap, next, s.scope)
			if err != nil {
				return nil, err
			}
			// A home turn that changed nothing to verify ends here (#254),
			// before Preflight is joined: no QA, Check or Verdict, and no
			// wait on setup (the deferred cleanup stops and drains it), so
			// even a failing setup leaves a reply a reply. Only a first
			// Attempt that ended normally, with no scope revert or Tamper
			// event. Compared against the Run's own start, so a follow-up
			// that left the previous Candidate as it was has replied too.
			// Excluded files never count.
			if s.p.Interactive && w.attempts == 1 && a.Stop == "" && a.Failure == "" && a.Exit == "done" && a.Candidate != "" &&
				len(a.Reverted) == 0 && a.Tamper() == 0 {
				promoted, withheld, err := s.repo.Classify(s.start, a.Candidate, w.promotedNew)
				if err != nil {
					return nil, err
				}
				ambiguous := false
				for _, f := range withheld {
					ambiguous = ambiguous || f.Class == workspace.ClassAmbiguous
				}
				if len(promoted) == 0 && !ambiguous {
					s.res.Attempt, s.res.Candidate = a, a.Candidate
					s.res.Friction = agent.SumFriction(s.res.Friction, a.Friction)
					s.p.Observe(Event{Kind: EvAttempt, Result: s.res, Attempt: a})
					if err := recordTripwires(s.l, s.repo, s.res, a); err != nil {
						return nil, err
					}
					return s.end(Replied)
				}
			}
			if !s.joined || s.warm != nil {
				// Setup's outcome comes before the Attempt's: a setup
				// failure cancelled it, and refuses the Run.
				if stop, r, err := s.joinPreflight(); stop {
					return r, err
				}
				if stop, r, err := s.ensureWarm(); stop {
					return r, err
				}
				if s.qa != nil {
					s.qa.seed = s.seed
				}
			}
			s.res.Attempt = a
			s.res.Friction = agent.SumFriction(s.res.Friction, a.Friction)
			s.p.Observe(Event{Kind: EvAttempt, Result: s.res, Attempt: a})
			if a.Stop != "" {
				return s.end(InfrastructureStop, a.Stop)
			}
			if a.Failure != "" {
				// TODO(#40-decision): an Attempt failure should retry on its
				// budget and then reach the bound-exhaustion Gate (ADR-0012);
				// with neither built yet the Run stops with no Verdict.
				return s.end(InfrastructureStop, "the implementer Attempt failed: "+a.Failure)
			}
			if a.Exit != "done" {
				// TODO(#50): other Exits (e.g. infeasible) route to the
				// infeasible Gate; until then the Run stops with no Verdict.
				return s.end(InfrastructureStop, fmt.Sprintf("the implementer declared Exit %q, and its Gate isn't built yet", a.Exit))
			}
			s.res.Candidate = a.Candidate
			if err := recordTripwires(s.l, s.repo, s.res, a); err != nil {
				return nil, err
			}
			// A Tamper event stays unacknowledged for the rest of the Run,
			// whatever later Attempts do (ADR-0019 #2).
			w.addTamper(a)
		}

		// QA: a fresh verifier on the Promoted view of the Candidate adds
		// held-out tests to a new Oracle version (ADR-0009, ADR-0010).
		if s.qa != nil && resume != "check" {
			if stop, r, err := s.ensureWarm(); stop || err != nil {
				return r, err
			}
			st, err := s.qa.review(ctx, w, s.res, a)
			if err != nil {
				return nil, err
			}
			if st.stop != "" {
				s.res.Gate = st.gate
				return s.end(st.stop, st.why...)
			}
			s.m, s.mBlob = s.qa.m, s.qa.mBlob
			s.res.Oracle = s.m.Version
		}
		resume = ""

		// The Check, from the cache seed once it is warm. A cancelled Run
		// never reaches a Verdict: the Check it killed didn't fail.
		if stop, r, err := s.ensureWarm(); stop || err != nil {
			return r, err
		}
		if s.seed != nil && s.runner.Seed == nil {
			limit := s.p.CacheWait
			if limit <= 0 {
				limit = DefaultCacheWait
			}
			sw := s.seed.WaitFor(limit)
			sw.Error = string(redact.Redact([]byte(sw.Error)))
			if err := s.l.Append(RecCacheSeeded, sw); err != nil {
				return nil, err
			}
			s.runner.Seed = s.seed
		}
		if ctx.Err() != nil {
			return s.end(InfrastructureStop, interrupted)
		}
		checked, err := w.checkedTree(a.Candidate)
		if err != nil {
			return nil, err
		}
		started := map[string]any{"check": check, "candidate": a.Candidate, "oracle_version": s.m.Version, "manifest": s.mBlob}
		if checked != a.Candidate {
			started["checked_tree"] = checked // held-back agent config left out (#107)
		}
		// A cut whose tree an earlier cut of the Chat checked, on the same
		// Oracle and Pipeline, reuses that Check (D3): the Run records
		// whose it is.
		var key checkKey
		var hit *checkedBy
		if s.p.cut != nil {
			if key, err = s.p.cut.checkKey(s.repo, checked, s.mBlob, s.frozen); err != nil {
				return nil, err
			}
			if by, ok := s.p.cut.checks[key]; ok {
				hit = &by
				started["reused_from"] = by.run
			}
		}
		if err := s.l.Append(RecCheckStarted, started); err != nil {
			return nil, err
		}
		if hit == nil {
			// A Check's execution can run minutes: name it while it does.
			s.p.Observe(Event{Kind: EvStage, Result: s.res, Step: "checks"})
		}
		var cr *oracle.Result
		if hit != nil {
			if err := s.l.Append(RecVerdictReused, map[string]any{"check": check, "run": hit.run, "run_check": hit.check,
				"candidate": a.Candidate, "tree": key.tree, "manifest": key.oracle, "frozen": key.pipeline}); err != nil {
				return nil, err
			}
			cr = hit.cr
		} else {
			// With held-out tests, two executions: the visible Oracle in a
			// build no held-out file enters, and the held-out tests on their
			// own (#46); both must pass.
			if cr, err = s.runner.SplitCheck(ctx, s.repo, s.m, checked, s.p.Frozen.Setup.Run, filepath.Join(s.res.Dir, "checks", fmt.Sprint(check)), s.control); err != nil {
				return nil, err
			}
		}
		if hit == nil && ctx.Err() != nil {
			if err := s.l.Append(RecCheckEnded, map[string]any{"check": check, "result": cr, "uncontained": true, "interrupted": true}); err != nil {
				return nil, err
			}
			return s.end(InfrastructureStop, interrupted)
		}
		s.res.Check, s.res.Checks = cr, check
		verdict := "fail"
		if cr.Pass {
			verdict = "pass"
		}
		ended := map[string]any{"check": check, "result": cr, "uncontained": true}
		if hit != nil {
			ended["reused_from"] = hit.run
		}
		if err := s.l.Append(RecCheckEnded, ended); err != nil {
			return nil, err
		}
		if s.p.cut != nil && hit == nil && cr.Infra == "" {
			s.p.cut.checks[key] = checkedBy{run: s.res.ID, check: check, cr: cr}
		}
		// TODO(#73-decision): the Snapshot control is recorded once a Check
		// has consulted it; one no Verdict needed is stopped unrecorded at
		// the end of the Run.
		rec, ok := s.control.Consulted()
		if s.p.cut != nil {
			// The Chat's control, shared by its cuts: this cut's consults.
			rec, ok = s.p.cut.control(s.control)
		}
		if ok {
			if err := s.l.Append(RecControlEnded, rec); err != nil {
				return nil, err
			}
		}
		if cr.Infra != "" {
			return s.end(InfrastructureStop, cr.Infra)
		}
		if err := s.l.Append(RecVerdict, map[string]any{"check": check, "verdict": verdict, "candidate": a.Candidate, "oracle_version": s.m.Version}); err != nil {
			return nil, err
		}
		w.check = check
		ev := Event{Kind: EvCheck, Result: s.res, Check: cr}
		if s.qa != nil {
			ev.Issues, ev.Conflicts = issues(s.m, cr, s.blobs), s.m.HeldOutBuildConflicts(cr)
		}
		s.p.Observe(ev)
		if len(ev.Conflicts) > 0 {
			// QA's held-out test no longer builds against the Candidate. The
			// implementer can't see it, so a send-back can't fix it, and
			// only a human removes an Oracle test (ADR-0007): the Run parks,
			// naming QA and the package, never a held-out name or source.
			// TODO(#46-decision): a Gate offering `remove <test>` is post-M1;
			// until then this parks with no choice to make here.
			var why []string
			for _, pkg := range ev.Conflicts {
				why = append(why, "QA's held-out test in package "+pkg+" no longer builds against the Candidate; the implementer isn't sent back over it, and only a human removes an Oracle test")
			}
			s.res.Gate = gateHeldOutConflict
			return s.end(Parked, why...)
		}

		// Ambiguous files: the Candidate's new files no glob covers and no
		// human has promoted. Accepted needs none (ADR-0013).
		if err := w.classify(a.Candidate); err != nil {
			return nil, err
		}
		if s.p.cut != nil && verdict == "fail" {
			// A Chat sends a failing cut back as a feedback turn in its live
			// Session (D5), never as a fresh implementer: the cut ends with
			// its Verdict, and the Chat sends the turn.
			s.res.failures = verdictFailures(cr, s.blobs, s.m, goSources(s.repo, a.Candidate))
			s.res.Feedback = feedbackTurn(s.res.failures)
			return s.end(Failed, "the Check failed on the cut")
		}
		// TODO(#47): failing own tests become a condition here.
		holds := func(c string) bool {
			return c == "verdict:"+verdict || (c == "tamper_event" && len(w.unacknowledged()) > 0) ||
				(c == "ambiguous_files" && len(w.ambiguous) > 0)
		}
		e, ok := s.p.Frozen.Graph.Route("check", holds, w.exhausted)
		if !ok {
			return nil, fmt.Errorf("the frozen graph has no edge for this Verdict")
		}
		st, err := w.follow(ctx, e, a, s.m.Version, cr, holds)
		if err != nil {
			return nil, err
		}
		return s.dispatch(ctx, attemptCtx, w, a, next, st, cr, check)
	}
}

// dispatch continues the walk from where a Gate's step left it: the Run
// ends, a resolved Candidate goes back through verify/check, or the
// Candidate is sent back to the implementer with its failure and the
// human's note. check is the number of the Check just run. Resume reaches
// the same code from its re-entry point.
func (s *walker) dispatch(ctx, attemptCtx context.Context, w *walk, a *Attempt, next attemptSpec, st step, cr *oracle.Result, check int) (*Result, error) {
	s.res.Gate, s.res.Decision = st.gate, st.decision
	if st.candidate != "" {
		s.res.Candidate = st.candidate
	}
	s.res.Resolutions = w.resolved
	if st.stop != "" {
		return s.end(st.stop, st.why...)
	}
	if st.edge.To == "end" {
		return s.end(Outcome(st.edge.Outcome))
	}
	if st.edge.To == "verify" || st.edge.To == "check" {
		// Every Ambiguous file is resolved: a fresh QA pass if a file
		// was promoted (cause: user request, ADR-0013), then the final
		// Check, on exactly the resolved Candidate.
		r := *a
		r.Candidate, r.Cause = st.candidate, "user_request"
		s.p.Observe(Event{Kind: EvResolved, Result: s.res, Next: st.edge.To})
		return s.loop(ctx, attemptCtx, w, &r, next, st.edge.To, check+1)
	}
	// A send-back Attempt, from the Candidate it sends back.
	d := st.decision
	w.sendBacks++
	s.p.Observe(Event{Kind: EvSendBack, Result: s.res, SendBack: w.sendBacks, SendBacks: s.p.Frozen.Limits.SendBacks, Decision: d})
	next = attemptSpec{n: w.attempts + 1, cause: "send_back", start: a.Candidate,
		extra: sendBackTurnFrom(cr, s.blobs, d, s.m, goSources(s.repo, a.Candidate)), ws: filepath.Join(s.workDir, fmt.Sprintf("implement-%d", w.attempts+1))}
	if err := s.repo.Checkout(a.Candidate, next.ws); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(next.ws, ledger.WorkspaceMarker), nil, 0o600); err != nil {
		return nil, err
	}
	return s.loop(ctx, attemptCtx, w, a, next, "", check+1)
}
