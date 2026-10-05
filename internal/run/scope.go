package run

import (
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
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
			return ClassOracleTest
		case oracle.MatchAny(f.Project.TestConfig, p):
			return ClassTestConfig
		}
		return ""
	}
}

// enforceScope compares the Workspace with the Snapshot once the agent's
// process tree is gone, records what is outside the Write scope, then
// reverts it. The records come first (ADR-0012).
func enforceScope(l *ledger.Ledger, blobs *ledger.Blobs, repo *workspace.RunRepo, a *Attempt, snap, ws string,
	protected func(string) string) error {
	s, err := repo.CheckScope(ws, snap, workspace.ScopeRules{Protected: protected, Put: blobs.Put})
	if err != nil {
		return err
	}
	reverted := s.Reverts
	if reverted == nil {
		reverted = []workspace.Revert{}
	}
	// TODO(#44): an adapter that enforces paths natively reports them, and
	// those paths are native-enforced. The fake enforces nothing.
	if err := l.Append(RecScopeObserved, map[string]any{
		"attempt": a.ID, "role": "implementer", "compared": s.Compared, "reverted": reverted,
		"tamper": s.Tamper(), "enforcement": workspace.RevertOnly,
	}); err != nil {
		return err
	}
	n := 0
	for _, r := range s.Reverts {
		if !r.Tamper {
			continue
		}
		n++
		if err := l.Append(RecTamperEvent, map[string]any{
			"id": fmt.Sprintf("%s/tamper-%d", a.ID, n), "attempt": a.ID, "path": r.Path, "class": r.Class,
			"change": r.Change, "before": r.Before, "after": r.After, "reverted": true, "acknowledged": false,
		}); err != nil {
			return err
		}
	}
	if err := s.Apply(); err != nil {
		return err
	}
	a.Reverted = s.Reverts
	return nil
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
