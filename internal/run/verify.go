package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/briefing"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/workspace"
)

// ClassOutsideScope is a verifier write outside its Write scope: anything
// but a new file matching the test globs (spec #35). It is discarded and
// recorded, and is never a Tamper event.
const ClassOutsideScope = "outside_write_scope"

// Verifier Exits (ADR-0008).
const (
	ExitExtended    = "extended"
	ExitNoAdditions = "no_additions"
	ExitConflicts   = "conflicts_with_oracle"
)

// QA is what one verifier Attempt did to the Oracle, as the views show it.
type QA struct {
	// Exit is the Exit the walk routed on; Declared is the one the
	// verifier claimed, if any.
	Exit, Declared string
	// HeldOut is how many held-out tests it added, Unmapped how many of
	// them name no criterion, and Files the files they are in.
	HeldOut, Unmapped int
	Files             []string
	Dropped           []oracle.Dropped
	// Withheld are the Candidate files its view left out.
	Withheld []workspace.Withheld
	// Discarded counts its writes outside its Write scope.
	Discarded int
	// Oracle is the version the Check runs next; UnmappedTotal counts its
	// held-out tests that name no criterion.
	Oracle, UnmappedTotal int
}

// qaStage runs the verifier Stage: a fresh Session every Attempt, on a
// fresh sanitised copy of the Candidate (ADR-0009, ADR-0010).
type qaStage struct {
	p       Params
	l       *ledger.Ledger
	blobs   *ledger.Blobs
	repo    *workspace.RunRepo
	adapter agent.Adapter
	stage   pipeline.Stage
	runID   string
	snap    string
	workDir string
	// m is the latest Oracle version and mBlob its manifest's blob.
	m     *oracle.Manifest
	mBlob string
	n     int // verifier Attempts so far
	added int // held-out files added this Run
}

// review runs one verifier Attempt on cand's Candidate and admits its
// additions as a new Oracle version. The step stops the Run, or is empty
// when the walk goes on to the Check.
func (q *qaStage) review(ctx context.Context, w *walk, res *Result, cand *Attempt) (step, error) {
	q.n++
	f := q.p.Frozen
	promotedNew := func(p string) bool {
		return oracle.MatchAny(f.Project.OutputGlobs, p) || oracle.MatchAny(f.Project.TestGlobs, p)
	}
	view, withheld, err := q.repo.PromotedView(q.snap, cand.Candidate, promotedNew)
	if err != nil {
		return step{}, err
	}
	ws := filepath.Join(q.workDir, fmt.Sprintf("verify-%d", q.n))
	if err := q.repo.Checkout(view, ws); err != nil {
		return step{}, err
	}
	defer oracle.RemoveAll(ws)
	if err := os.WriteFile(filepath.Join(ws, ledger.WorkspaceMarker), nil, 0o600); err != nil {
		return step{}, err
	}
	// git diff shows the Promoted change against the Snapshot.
	if err := q.repo.InitWorkspaceGit(ws); err != nil {
		return step{}, err
	}
	files, err := q.repo.Files(view)
	if err != nil {
		return step{}, err
	}
	inView := map[string]bool{}
	for _, p := range files {
		inView[p] = true
	}
	scope := func(p string) string {
		switch {
		case p == ledger.WorkspaceMarker:
			return ClassOgeFile
		case !inView[p] && oracle.MatchAny(f.Project.TestGlobs, p) && !workspace.Excluded(p):
			return ""
		}
		return ClassOutsideScope
	}
	var checks []string
	for _, c := range f.Checks {
		checks = append(checks, c.Run)
	}
	var ids []string
	for _, c := range q.p.Task.Criteria {
		ids = append(ids, c.ID)
	}
	at := attemptSpec{n: q.n, cause: cand.Cause, start: view, ws: ws, base: view, view: view, withheld: withheld,
		brief: briefing.Verifier(q.p.Task, checks, f.Project.TestGlobs), writeGlobs: f.Project.TestGlobs, collect: true}
	a, err := implement(ctx, q.p, q.l, q.blobs, q.repo, q.adapter, q.stage, q.runID, q.snap, at, scope)
	if err != nil {
		return step{}, err
	}
	qa := &QA{Declared: a.Exit, Withheld: withheld, Oracle: q.m.Version}
	for _, r := range a.Reverted {
		if r.Class == ClassOutsideScope {
			qa.Discarded++
		}
	}
	a.QA = qa
	fail := func(why string) (step, error) {
		w.p.Observe(Event{Kind: EvAttempt, Result: res, Attempt: a})
		return step{stop: InfrastructureStop, why: []string{why}}, nil
	}
	switch {
	case a.Stop != "":
		return fail(a.Stop)
	case a.Failure != "":
		// TODO(#40-decision): as for the implementer, an Attempt failure
		// stops the Run until retries exist. A verifier whose envelope
		// fails closed lands here: no Verdict without QA.
		return fail("the verifier Attempt failed: " + a.Failure)
	}

	adds, err := additions(ws, inView, f.Project.TestGlobs)
	if err != nil {
		return step{}, err
	}
	next, nextBlob, dropped, err := oracle.NewVersion(q.m, q.mBlob, a.ID, adds, q.repo, view, ids, q.blobs)
	if err != nil {
		return step{}, err
	}
	qa.Dropped = dropped
	newTests := len(next.HeldOut) - len(q.m.HeldOut)
	qa.HeldOut, qa.Files = newTests, next.Added
	qa.Unmapped = next.Unmapped() - q.m.Unmapped()
	// The Exit is the verifier's Claim; what it added is Öge's own
	// observation, and that decides extended from no_additions.
	qa.Exit = ExitNoAdditions
	switch {
	case a.Exit == ExitConflicts:
		qa.Exit = ExitConflicts
	case len(next.Added) > 0:
		qa.Exit = ExitExtended
	}
	l := f.Limits
	if n := len(next.Added); n > 0 && (q.n > l.OracleGrowthAttempts || n > l.OracleGrowthPerAttempt || q.added+n > l.OracleGrowthPerRun) {
		// TODO(#51): the Oracle-growth Gate holds the additions for a
		// human; until it exists the Run stops, never truncating them.
		if err := q.l.Append(RecObservation, map[string]any{"attempt": a.ID, "kind": "oracle_growth_held",
			"held": next.Added, "attempts": q.n, "run_total": q.added}); err != nil {
			return step{}, err
		}
		return fail(fmt.Sprintf("QA's additions hit an Oracle-growth limit (%d Attempts, %d per Attempt, %d per Run), and the Oracle-growth Gate isn't built yet",
			l.OracleGrowthAttempts, l.OracleGrowthPerAttempt, l.OracleGrowthPerRun))
	}
	if len(next.Added) > 0 {
		q.added += len(next.Added)
		if err := q.l.Append(RecOracleVersion, map[string]any{"version": next.Version, "manifest": nextBlob,
			"tests": len(next.Tests), "parent": q.m.Version, "attempt": a.ID, "added": next.Added, "dropped": dropped,
			"held_out": newTests, "unmapped": qa.Unmapped, "exit": qa.Exit, "declared_exit": a.Exit}); err != nil {
			return step{}, err
		}
		q.m, q.mBlob = next, nextBlob
		qa.Oracle = next.Version
	} else if len(dropped) > 0 {
		if err := q.l.Append(RecObservation, map[string]any{"attempt": a.ID, "kind": "oracle_additions_dropped", "dropped": dropped}); err != nil {
			return step{}, err
		}
	}
	qa.UnmappedTotal = q.m.Unmapped()
	res.QA = qa
	w.p.Observe(Event{Kind: EvAttempt, Result: res, Attempt: a})

	holds := func(c string) bool { return c == "exit:"+qa.Exit }
	e, ok := w.g.Route("verify", holds, w.exhausted)
	if !ok {
		return step{}, fmt.Errorf("the frozen graph has no edge for verifier Exit %q", qa.Exit)
	}
	if e.To == "check" {
		return step{}, nil
	}
	// TODO(#50): the infeasible Gate for conflicts_with_oracle.
	return w.follow(ctx, e, a, q.m.Version, nil, holds)
}

// additions are the new regular files in the verifier's Workspace that
// match the test globs, once its scope check has reverted the rest.
func additions(ws string, inView map[string]bool, globs []string) ([]oracle.Addition, error) {
	var out []oracle.Addition
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || inView[rel] || rel == ledger.WorkspaceMarker || !oracle.MatchAny(globs, rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, oracle.Addition{Path: rel, Data: b})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// fileLine is the "file.go:12: " a Go test failure message starts with.
var fileLine = regexp.MustCompile(`^\S+\.go:[0-9]+: `)

// issues are one line per failing held-out test: its name and its first
// failure message, for the human's terminal ("QA found N issues").
func issues(m *oracle.Manifest, cr *oracle.Result, blobs *ledger.Blobs) []string {
	failing := m.HeldOutFailures(cr)
	if len(failing) == 0 {
		return nil
	}
	first := map[oracle.TestID]string{}
	for _, e := range cr.Commands {
		if e.Report == nil || e.Report.Format != oracle.ReportGoTestJSON || e.Stdout.Blob == "" {
			continue
		}
		raw, err := blobs.Get(e.Stdout.Blob)
		if err != nil {
			continue
		}
		for _, ol := range oracle.SplitGoTestOutput(raw) {
			text := strings.TrimSpace(ol.Text)
			if ol.Test.Name == "" || text == "" || strings.HasPrefix(text, "=== ") || strings.HasPrefix(text, "--- ") {
				continue
			}
			if _, ok := first[ol.Test]; !ok {
				first[ol.Test] = fileLine.ReplaceAllString(text, "")
			}
		}
	}
	var out []string
	for _, h := range failing {
		msg, ok := first[h.Test]
		if !ok && h.Test.Package == "" {
			for id, s := range first {
				if id.Name == h.Test.Name {
					msg, ok = s, true
				}
			}
		}
		if !ok {
			msg = "didn't pass"
		}
		out = append(out, h.Test.Name+": "+msg)
	}
	return out
}

// briefingManifest is an Attempt's Briefing manifest (ADR-0009): each
// item's provenance and hash, the classes denied, the files withheld, the
// observed envelope and whether the Session was fresh. It holds no
// held-out or secret content, only hashes.
func briefingManifest(p Params, a *Attempt, at attemptSpec, brief, instructions string) map[string]any {
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	items := []map[string]string{
		{"item": "task", "source": "the Task", "sha256": sum(p.Task.Text)},
		{"item": "briefing", "source": "built by Öge for the " + a.Role, "sha256": sum(brief)},
	}
	if instructions != "" {
		items = append(items, map[string]string{"item": "repo_instructions", "source": "the Snapshot's CLAUDE.md", "sha256": sum(instructions)})
	}
	denied := []string{"held_out_source"}
	if a.Role == "verifier" {
		items = append(items, map[string]string{"item": "workspace", "source": "the Promoted view of the Candidate", "commit": at.view})
		denied = []string{"implementer_transcript", "implementer_exit", "implementer_claims", "authorship", "prior_verdicts",
			"held_out_source", "ambiguous_files", "excluded_files"}
	} else {
		items = append(items, map[string]string{"item": "workspace", "source": "a copy of revision " + at.start, "commit": at.start})
	}
	withheld := at.withheld
	if withheld == nil {
		withheld = []workspace.Withheld{}
	}
	envelope := a.Envelope
	if envelope == "" {
		envelope = "not reported"
	}
	return map[string]any{"attempt": a.ID, "role": a.Role, "items": items, "denied": denied, "withheld": withheld,
		"envelope": envelope, "session": "fresh"}
}
