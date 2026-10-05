package run

import (
	"context"
	"path/filepath"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/workspace"
)

// startControl starts the Snapshot control of Oracle version m (ADR-0020)
// in the background, from the Run's cache seed once it is warm, so it
// overlaps the implementer's Attempt. Fast mode has only v0, so there is
// one control per Run; each later Oracle version gets its own. With
// verify = "after", a version's control starts once the verifier has
// produced its tests, still against the Snapshot.
func startControl(ctx context.Context, runner *oracle.Runner, seed *oracle.Seed, repo *workspace.RunRepo, m *oracle.Manifest, snap, setup, runDir string) *oracle.Control {
	r := *runner
	r.Seed = seed
	return r.StartControl(ctx, repo, m, snap, setup, filepath.Join(runDir, "checks", "control"))
}

// recordTripwires records the static tripwires the Candidate's changes
// set off as an Observation (ADR-0020). They never change a Verdict.
func recordTripwires(l *ledger.Ledger, repo *workspace.RunRepo, res *Result, a *Attempt) error {
	show := func(commit string) func(string) ([]byte, bool) {
		return func(p string) ([]byte, bool) {
			b, ok, err := repo.Show(commit, p)
			return b, ok && err == nil
		}
	}
	// Against the Snapshot, so a later Attempt's Candidate is scanned
	// for everything it changes.
	trip := oracle.Tripwires(a.Changed, show(res.Snapshot), show(a.Candidate))
	res.Tripwires = trip
	if len(trip) == 0 {
		return nil
	}
	return l.Append(RecObservation, map[string]any{"attempt": a.ID, "kind": "tripwire", "tripwires": trip,
		"note": "signals, not proof: a simple scan of the changed files"})
}
