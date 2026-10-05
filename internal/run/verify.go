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

// qaDir holds every verifier Workspace and cache of a Run, under its work
// directory.
const qaDir = "qa"

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
	// runner and seed build QA's additions before they are admitted.
	runner *oracle.Runner
	seed   *oracle.Seed
	runDir string
	n      int // verifier Attempts so far
	added  int // held-out files added this Run
}

// review runs one verifier Attempt on cand's Candidate and admits its
// additions as a new Oracle version. The step stops the Run, or is empty
// when the walk goes on to the Check.
func (q *qaStage) review(ctx context.Context, w *walk, res *Result, cand *Attempt) (step, error) {
	q.n++
	f := q.p.Frozen
	// The output and test globs, and the files a human promoted: one the
	// last verifier never saw is in this one's view (ADR-0013).
	view, withheld, err := q.repo.PromotedView(q.snap, cand.Candidate, w.promotedNew)
	if err != nil {
		return step{}, err
	}
	// QA's Workspace and caches live under one directory the implementer
	// is denied, removed as soon as the Attempt ends: held-out source
	// and its build products never outlast it (#46).
	root := filepath.Join(q.workDir, qaDir)
	ws := filepath.Join(root, fmt.Sprintf("verify-%d", q.n))
	if err := q.repo.Checkout(view, ws); err != nil {
		return step{}, err
	}
	defer oracle.RemoveAll(root)
	// The implementer's Workspaces and caches hold Ambiguous and Excluded
	// files and its residue: QA's sandbox denies them.
	deny := []string{filepath.Join(q.workDir, "cache")}
	if impls, err := filepath.Glob(filepath.Join(q.workDir, "implement*")); err == nil {
		deny = append(deny, impls...)
	}
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
	at := attemptSpec{n: q.n, cause: cand.Cause, start: view, ws: ws, base: view, view: view, withheld: withheld, denyRead: deny,
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
	if len(next.Added) > 0 {
		// QA's own defects never reach the Oracle: a file that breaks its
		// package's build would fail every later Check (#46 review H1).
		// It is built against the Candidate the Check uses, Ambiguous
		// files included, never QA's narrower view.
		rr := *q.runner
		if q.seed != nil {
			rr.Seed = q.seed
		}
		broken, err := rr.Unbuildable(ctx, q.repo, q.m, next, cand.Candidate, f.Setup.Run, filepath.Join(q.runDir, "checks", fmt.Sprintf("qa-%d", q.n)))
		if err != nil {
			return step{}, err
		}
		if len(broken) > 0 {
			var paths []string
			for _, b := range broken {
				paths = append(paths, b.Path)
			}
			if next, nextBlob, err = next.Without(paths, q.blobs); err != nil {
				return step{}, err
			}
			dropped = append(dropped, broken...)
		}
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
	case newTests > 0: // tests, never files: a helper alone adds nothing
		qa.Exit = ExitExtended
	}
	l := f.Limits
	if n := len(next.Added); n > 0 && (q.n > l.OracleGrowthAttempts || n > l.OracleGrowthPerAttempt || q.added+n > l.OracleGrowthPerRun) {
		// TODO(#51): the Oracle-growth Gate holds the additions for a
		// human; until it exists the Run parks there, never truncating
		// them and never calling it an Infrastructure stop.
		if err := q.l.Append(RecObservation, map[string]any{"attempt": a.ID, "kind": "oracle_growth_held",
			"held": next.Added, "attempts": q.n, "run_total": q.added}); err != nil {
			return step{}, err
		}
		w.p.Observe(Event{Kind: EvAttempt, Result: res, Attempt: a})
		return step{gate: "gate.oracle_growth", stop: Parked, why: []string{fmt.Sprintf(
			"Oracle-growth limit reached (%d verifier Attempts, %d additions per Attempt, %d per Run): QA's %d new held-out files are held; #51 adds the Gate where a human admits them",
			l.OracleGrowthAttempts, l.OracleGrowthPerAttempt, l.OracleGrowthPerRun, n)}}, nil
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

// briefingManifest is an Attempt's Briefing manifest (ADR-0009), written
// before it starts: each item's provenance and hash, each class denied and
// what denies it, the files withheld, the paths its sandbox is asked to
// deny reading, and whether the Session is fresh. The observed envelope
// follows in the Attempt's session Observation. It holds no held-out or
// secret content, only hashes.
func briefingManifest(p Params, a *Attempt, at attemptSpec, brief, instructions string, denyRead []string) map[string]any {
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
	// Only what is actually enforced, and by what (#46 review).
	const (
		notBriefed = "never in the Briefing: Öge builds it from the Task"
		sandboxed  = "the agent's sandbox is asked to deny reading these paths (deny_read); only an adapter with a sandbox enforces it"
	)
	type denial struct {
		Class    string `json:"class"`
		Enforced string `json:"enforced"`
	}
	var denied []denial
	if a.Role == "verifier" {
		items = append(items, map[string]string{"item": "workspace", "source": "the Promoted view of the Candidate", "commit": at.view})
		for _, c := range []string{"implementer_transcript", "implementer_exit", "implementer_claims", "authorship", "prior_verdicts"} {
			denied = append(denied, denial{c, notBriefed})
		}
		denied = append(denied,
			denial{"ambiguous_files", "left out of its Workspace (the Promoted view); the implementer's Workspaces: " + sandboxed},
			denial{"excluded_files", "kept at the Snapshot's version in its Workspace; the implementer's Workspaces: " + sandboxed},
			denial{"earlier_held_out_source", "never in any Workspace; Öge's private state: " + sandboxed})
	} else {
		items = append(items, map[string]string{"item": "workspace", "source": "a copy of revision " + at.start, "commit": at.start})
		denied = append(denied, denial{"held_out_source", "never in its Briefing or Workspace (send-backs carry a count and criterion ids); QA's directory and Öge's private state: " + sandboxed})
	}
	withheld := at.withheld
	if withheld == nil {
		withheld = []workspace.Withheld{}
	}
	return map[string]any{"attempt": a.ID, "role": a.Role, "items": items, "denied": denied, "withheld": withheld,
		"deny_read": denyRead, "envelope": "recorded in the Attempt's session Observation", "session": "fresh"}
}
