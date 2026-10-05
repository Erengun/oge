package oracle

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The two executions of a Check whose Oracle holds held-out tests (#46).
// Invariant: protected QA code that isn't deliberately hostile never
// alters the execution environment that proves the visible Oracle passes.
// (Code deliberately hostile with the user's privileges, such as a
// detached daemon, is outside ADR-0020's threat model; the tree guard in
// treeguard.go only detects its cheapest form.) So the visible Oracle (v0's
// tests and test configuration) runs in its own Check directory, cache
// copy and process tree with no held-out file in its build, and the
// held-out tests run in another; each must pass and attest on its own.
const (
	PartVisible = "visible"
	PartHeldOut = "held-out"
)

// Visible is m without its held-out tests.
func (m *Manifest) Visible() *Manifest {
	out := *m
	out.Tests, out.Expected, out.HeldOut, out.Added = nil, nil, nil, nil
	held := map[TestID]bool{}
	for _, f := range m.Tests {
		if f.HeldOut {
			for _, id := range f.Expected {
				held[id] = true
			}
			continue
		}
		out.Tests = append(out.Tests, f)
	}
	for _, id := range m.Expected {
		if !held[id] {
			out.Expected = append(out.Expected, id)
		}
	}
	return &out
}

// HeldOutOnly is m's held-out tests with its commands and test
// configuration, and none of its visible tests.
func (m *Manifest) HeldOutOnly() *Manifest {
	out := *m
	out.Tests, out.Expected = nil, nil
	for _, f := range m.Tests {
		if f.HeldOut {
			out.Tests = append(out.Tests, f)
			out.Expected = append(out.Expected, f.Expected...)
		}
	}
	return &out
}

// SplitCheck runs m on candidate: CheckAgainst on the visible Oracle and,
// when m holds held-out tests, concurrently and separately on them, each
// in its own directory under root. The Verdict is pass only if both pass.
func (r *Runner) SplitCheck(ctx context.Context, repo Repo, m *Manifest, candidate, setup, root string, control *Control) (*Result, error) {
	start := time.Now()
	if len(m.HeldOut) == 0 {
		res, err := r.CheckAgainst(ctx, repo, m, candidate, setup, root, control)
		if res != nil {
			res.VisibleMs = time.Since(start).Milliseconds()
		}
		return res, err
	}
	var (
		wg              sync.WaitGroup
		vis, held       *Result
		visErr, heldErr error
		visMs, heldMs   int64
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		vis, visErr = r.CheckAgainst(ctx, repo, m.Visible(), candidate, setup, filepath.Join(root, "visible"), control)
		visMs = time.Since(start).Milliseconds()
	}()
	go func() {
		defer wg.Done()
		held, heldErr = r.CheckAgainst(ctx, repo, m.HeldOutOnly(), candidate, setup, filepath.Join(root, "held-out"), control)
		heldMs = time.Since(start).Milliseconds()
	}()
	wg.Wait()
	defer RemoveAll(root)
	if visErr != nil {
		return nil, visErr
	}
	if heldErr != nil {
		return nil, heldErr
	}
	return merge(vis, held, visMs, heldMs), nil
}

// merge is one Result from a split Check's two.
func merge(vis, held *Result, visMs, heldMs int64) *Result {
	res := &Result{Pass: vis.Pass && held.Pass, Cache: vis.Cache, CacheWhy: vis.CacheWhy, CacheMs: vis.CacheMs,
		Setup: vis.Setup, VisibleMs: visMs, HeldOutMs: &heldMs, Stray: vis.Stray + held.Stray}
	if res.Setup == nil || res.Setup.Pass {
		if held.Setup != nil && !held.Setup.Pass {
			res.Setup = held.Setup
		}
	}
	for _, part := range []struct {
		name string
		r    *Result
	}{{PartVisible, vis}, {PartHeldOut, held}} {
		for _, e := range part.r.Commands {
			e.Part = part.name
			res.Commands = append(res.Commands, e)
		}
		res.Tests = append(res.Tests, part.r.Tests...)
		res.Missing = append(res.Missing, part.r.Missing...)
		res.Skipped = append(res.Skipped, part.r.Skipped...)
		res.NotBuilt = append(res.NotBuilt, part.r.NotBuilt...)
	}
	var why, infra []string
	if vis.Why != "" {
		why = append(why, vis.Why)
	}
	if held.Why != "" {
		why = append(why, "held-out: "+held.Why)
	}
	if vis.Infra != "" {
		infra = append(infra, vis.Infra)
	}
	if held.Infra != "" {
		infra = append(infra, "held-out: "+held.Infra)
	}
	res.Why, res.Infra = strings.Join(why, "; "), strings.Join(infra, "; ")
	return res
}
