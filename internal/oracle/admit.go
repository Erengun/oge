package oracle

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"
)

// buildTimeout bounds one compile-only build of a package.
const buildTimeout = 2 * time.Minute

// Unbuildable lists the files next adds to parent that break their
// package's build on candidate, the exact tree the Check uses (Ambiguous
// files included): QA's own defects, which would fail every later Check
// whatever the implementer did (#46). Each added file is built with
// the parent Oracle and the files admitted before it, compile-only
// (`go test -count=1 -run ^$`). A package that doesn't build without the
// addition can't judge it, so the addition is kept: the Check then fails
// on the Candidate's own build. root is removed afterwards.
func (r *Runner) Unbuildable(ctx context.Context, repo Repo, parent, next *Manifest, candidate, setup, root string) ([]Dropped, error) {
	defer RemoveAll(root)
	dir, env, _, err := r.prepareCheck(root)
	if err != nil {
		return nil, err
	}
	if err := repo.Checkout(candidate, dir); err != nil {
		return nil, err
	}
	if setup != "" {
		e, _, err := r.Exec(ctx, setup, dir, env, 10*time.Minute, 1<<20)
		if err != nil || !e.Pass {
			return nil, err // can't build anything: admit, the Check decides
		}
	}
	// The parent Oracle in place of the Candidate's test paths, as a Check
	// lays it down (without attestation: nothing runs).
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel := filepath.ToSlash(rel); MatchAny(next.TestGlobs, rel) || MatchAny(next.TestConfigGlobs, rel) {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	put := func(f File) error {
		if why, err := blockedPath(dir, f.Path); err != nil || why != "" {
			return err
		}
		b, err := r.Blobs.Get(f.Blob)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	}
	for _, f := range append(append([]File(nil), parent.Tests...), parent.Config...) {
		if err := put(f); err != nil {
			return nil, err
		}
	}
	added := map[string]File{}
	for _, f := range next.Tests {
		added[f.Path] = f
	}
	builds := func(pkg string) (bool, error) {
		e, _, err := r.Exec(ctx, "go test -count=1 -run '^$' ./"+pkg, dir, env, buildTimeout, 64<<10)
		return e.Pass, err
	}
	base := map[string]bool{}
	var broken []Dropped
	paths := append([]string(nil), next.Added...)
	sort.Strings(paths)
	for _, p := range paths {
		pkg := path.Dir(p)
		ok, seen := base[pkg]
		if !seen {
			if ok, err = builds(pkg); err != nil {
				return nil, err
			}
			base[pkg] = ok
		}
		if !ok {
			continue
		}
		if err := put(added[p]); err != nil {
			return nil, err
		}
		if ok, err := builds(pkg); err != nil {
			return nil, err
		} else if !ok {
			broken = append(broken, Dropped{p, "QA's test didn't compile against the Candidate; left out"})
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
				return nil, err
			}
		}
	}
	return broken, nil
}
