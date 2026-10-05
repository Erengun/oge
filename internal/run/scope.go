package run

import (
	"errors"
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/redact"
	"github.com/erengun/oge/internal/workspace"
)

// Ledger records of the post-Attempt scope check (ADR-0007, ADR-0011).
const (
	// RecScopeObserved is written after every Attempt whose agent ran,
	// before its reverts are made and before the Candidate is committed.
	RecScopeObserved = "ScopeObserved"
	// RecTamperEvent is one write to a protected path by the role being
	// judged. It stays in the Ledger whatever happens next.
	RecTamperEvent = "TamperEvent"
	// RecScopeReverted says whether the planned reverts were made.
	RecScopeReverted = "ScopeReverted"
)

// Protected classes of the implementer's protected set.
const (
	ClassOracleTest = "oracle_test" // a test in the Oracle version
	ClassTestConfig = "test_config" // project.test_config
	ClassOgeConfig  = "oge_config"  // .oge/
	ClassOgeFile    = "oge_file"    // Öge's own files in the Workspace
)

// implementerScope is the implementer's protected set: the whole Workspace
// is in its Write scope except these (spec #35, ADR-0010).
//
// TODO(#41-decision): the spec's "fixed agent/config/instruction list"
// (CLAUDE.md, AGENTS.md, .claude/ and the like) is not protected yet. Repo
// instructions reach agents only from the Snapshot (#44), so an edit can't
// steer a later role; protecting them would make every such edit block
// Accepted. The agent's own .git is ignored rather than protected, as
// ADR-0010 says, so `git init` or `git commit` in the Workspace is no
// Tamper event.
func implementerScope(m *oracle.Manifest, f *pipeline.Frozen) func(string) string {
	tests := map[string]bool{}
	for _, t := range m.Tests {
		tests[t.Path] = true
	}
	return func(p string) string {
		switch {
		case p == ledger.WorkspaceMarker:
			return ClassOgeFile
		case p == ".oge" || strings.HasPrefix(p, ".oge/"):
			return ClassOgeConfig
		case tests[p]:
			// TODO(#41-decision): only the Oracle version's own tests are
			// protected. A new file matching the test globs is the
			// implementer's own test (spec #35: Promoted; ADR-0010: negative
			// authority only) and the Check's overlay drops it anyway, so
			// protecting it would block Accepted on ordinary TDD.
			return ClassOracleTest
		case oracle.MatchAny(f.Project.TestConfig, p):
			return ClassTestConfig
		}
		return ""
	}
}

// enforceScope compares the Workspace with the Snapshot once the agent's
// process tree is gone, records what is outside the Write scope, then
// reverts it (ADR-0012: the plan is recorded first). A comparison or
// revert that the Workspace's content defeats fails the Attempt, not Öge;
// every Tamper event found reaches the Ledger either way.
func enforceScope(l *ledger.Ledger, blobs *ledger.Blobs, repo *workspace.RunRepo, a *Attempt, snap, ws string,
	protected func(string) string) error {
	s, err := repo.CheckScope(ws, snap, workspace.ScopeRules{Protected: protected, Put: blobs.Put})
	if err != nil {
		a.Failure = failAttempt(a.Failure, "scope_check_failed: "+err.Error())
		return nil
	}
	// TODO(#44): an adapter that enforces paths natively reports them, and
	// those paths are native-enforced. The fake enforces nothing.
	if err := l.Append(RecScopeObserved, map[string]any{
		"attempt": a.ID, "role": "implementer", "state": "planned", "compared": s.Compared,
		"reverted": nonNil(s.Reverts), "tamper": s.Tamper(), "enforcement": workspace.RevertOnly,
	}); err != nil {
		return err
	}
	applyErr := s.Apply()
	done := map[string]any{"attempt": a.ID, "reverted": applyErr == nil}
	if applyErr != nil {
		done["error"] = applyErr.Error()
	}
	if err := l.Append(RecScopeReverted, done); err != nil {
		return err
	}
	if err := recordTamper(l, a, s.Reverts, applyErr == nil, false); err != nil {
		return err
	}
	a.Reverted = s.Reverts
	if applyErr != nil {
		a.Failure = failAttempt(a.Failure, "revert_failed: "+applyErr.Error())
	}
	return nil
}

// commitCandidate commits the reverted Workspace as the Attempt's
// Candidate. Protected paths and escaping links come from the Snapshot by
// construction, so a write that raced the comparison can't reach the
// Candidate; one that did is recorded before the Candidate's ref is set.
func commitCandidate(l *ledger.Ledger, repo *workspace.RunRepo, a *Attempt, snap, ws string, protected func(string) string) error {
	c, late, err := repo.CommitScoped(ws, snap, "Candidate c1 ("+a.ID+")", protected)
	var addErr *workspace.AddError
	if errors.As(err, &addErr) {
		a.Failure = failAttempt(a.Failure, "candidate_commit_failed: "+addErr.Error())
		return nil
	}
	if err != nil {
		return err
	}
	if len(late) > 0 {
		if err := l.Append(RecScopeObserved, map[string]any{
			"attempt": a.ID, "role": "implementer", "state": "late", "reverted": late,
			"tamper": countTamper(late), "enforcement": workspace.RevertOnly,
		}); err != nil {
			return err
		}
		if err := recordTamper(l, a, late, true, true); err != nil {
			return err
		}
		a.Reverted = append(a.Reverted, late...)
	}
	if err := repo.SetRef("refs/oge/candidates/c1", c); err != nil {
		return err
	}
	a.Candidate = c
	a.Changed, err = repo.ChangedFiles(snap, c)
	return err
}

// recordTamper writes one TamperEvent per protected path among rs.
func recordTamper(l *ledger.Ledger, a *Attempt, rs []workspace.Revert, reverted, late bool) error {
	n := a.Tamper()
	for _, r := range rs {
		if !r.Tamper {
			continue
		}
		n++
		rec := map[string]any{
			"id": fmt.Sprintf("%s/tamper-%d", a.ID, n), "attempt": a.ID, "path": r.Path, "class": r.Class,
			"change": r.Change, "before": r.Before, "after": r.After, "reverted": reverted, "acknowledged": false,
		}
		if late {
			rec["late"] = true // written after the comparison; kept out of the Candidate's commit
		}
		if err := l.Append(RecTamperEvent, rec); err != nil {
			return err
		}
	}
	return nil
}

func failAttempt(prev, why string) string {
	if prev != "" {
		return prev
	}
	return string(redact.Redact([]byte(why)))
}

func nonNil(rs []workspace.Revert) []workspace.Revert {
	if rs == nil {
		return []workspace.Revert{}
	}
	return rs
}

func countTamper(rs []workspace.Revert) int {
	n := 0
	for _, r := range rs {
		if r.Tamper {
			n++
		}
	}
	return n
}

// Tamper counts the Attempt's Tamper events.
func (a *Attempt) Tamper() int {
	n := 0
	for _, r := range a.Reverted {
		if r.Tamper {
			n++
		}
	}
	return n
}
