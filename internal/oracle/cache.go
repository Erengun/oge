package oracle

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Check cache strategies, recorded per Check (ADR-0021).
const (
	// CacheClone is a copy-on-write clone of the Run's seed.
	CacheClone = "seeded-clone"
	// CacheCopy is a regular private copy of the Run's seed.
	CacheCopy = "seeded-private-copy"
	// CacheCold is an empty private cache.
	CacheCold = "cold"
)

// warmCommand compiles every package and test of the Snapshot's module
// into the seed's build cache without linking or running anything. It
// prints only package errors: a Snapshot that doesn't build (the bug may
// be a compile error) still seeds whatever does.
// TODO(#74-decision): Öge runs this Go-specific warm step itself, beyond
// the project's setup command; a project with no go.mod gets no warm step.
const warmCommand = `go list -e -export -deps -test -f '{{if .Error}}{{.ImportPath}}: {{.Error}}{{end}}' ./...`

// Seed is a Run's warm cache seed (ADR-0021): a build cache and a module
// cache filled on the trusted Snapshot, by the setup command and then by
// Öge's warm step. Checks never use it as their cache; each gets a
// private clone or copy. The warm step runs in the background, so it can
// overlap the implementer's Attempt.
type Seed struct {
	root   string
	done   chan struct{}
	cancel context.CancelFunc
	warm   *Execution
	err    error
}

// Dir holds the seed's caches.
func (s *Seed) Dir() string { return filepath.Join(s.root, "seed") }

// GoCache is the seed's build cache.
func (s *Seed) GoCache() string { return filepath.Join(s.Dir(), "gocache") }

// ModCache is the seed's module cache.
func (s *Seed) ModCache() string { return filepath.Join(s.Dir(), "gomod") }

// Wait waits for the warm step and returns its execution: nil when there
// was none (no go.mod at the Snapshot's root). An error means Öge couldn't
// run it; the seed still holds what setup put there.
func (s *Seed) Wait() (*Execution, error) {
	if s == nil {
		return nil, nil
	}
	<-s.done
	return s.warm, s.err
}

// Close stops the warm step, waits for it, and removes the seed.
func (s *Seed) Close() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
	_ = RemoveAll(s.root)
}

// NewSeed checks the Snapshot out under root and runs setup there (when
// set) with the seed as its caches, then starts the warm step. It returns
// setup's execution; a failing setup returns no seed. With neither a setup
// command nor a go.mod there is nothing to warm, and the seed is nil
// (Checks then run cold). The Snapshot is trusted: no Candidate code ever
// runs with the seed as a writable cache.
func (r *Runner) NewSeed(ctx context.Context, repo Repo, snapshot, setup, root string) (*Seed, *Execution, error) {
	s := &Seed{root: root, done: make(chan struct{})}
	work := filepath.Join(root, "snapshot")
	fail := func(e *Execution, err error) (*Seed, *Execution, error) {
		_ = RemoveAll(root)
		return nil, e, err
	}
	dir, env, err := r.Prepare(work)
	if err != nil {
		return fail(nil, err)
	}
	for _, d := range []string{s.GoCache(), s.ModCache()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fail(nil, err)
		}
	}
	if r.SeedTemplate != "" {
		if _, err := materialise(r.SeedTemplate, s.GoCache()); err != nil {
			return fail(nil, err)
		}
	}
	env["GOCACHE"], env["GOMODCACHE"] = s.GoCache(), s.ModCache()
	if err := repo.Checkout(snapshot, dir); err != nil {
		return fail(nil, err)
	}
	var setupE *Execution
	if setup != "" {
		e, _, err := r.Exec(ctx, setup, dir, env, 10*time.Minute, 1<<20)
		if err != nil {
			return fail(nil, err)
		}
		if setupE = &e; !e.Pass {
			return fail(setupE, nil)
		}
	}
	_, err = os.Stat(filepath.Join(dir, "go.mod"))
	hasModule := err == nil
	if !hasModule && setup == "" {
		return fail(nil, nil)
	}
	wctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go func() {
		defer close(s.done)
		defer RemoveAll(work)
		if !hasModule {
			return
		}
		env["GOPROXY"] = "off" // the warm step never fetches
		e, _, err := r.Exec(wctx, warmCommand, dir, env, 10*time.Minute, 64<<10)
		s.warm, s.err = &e, err
	}()
	return s, setupE, nil
}

// prepareCheck is Prepare plus the Check-local caches: private clones or
// copies of the Run's seed when there is one, else cold. It returns the
// strategy and how long materialising took.
func (r *Runner) prepareCheck(root string) (string, map[string]string, string, int64, error) {
	dir, env, err := r.Prepare(root)
	if err != nil || r.Seed == nil {
		return dir, env, CacheCold, 0, err
	}
	_, _ = r.Seed.Wait() // a failed warm step leaves a partial seed, still valid
	start := time.Now()
	cache := CacheClone
	for src, dst := range map[string]string{r.Seed.GoCache(): env["GOCACHE"], r.Seed.ModCache(): env["GOMODCACHE"]} {
		s, err := materialise(src, dst)
		if err != nil {
			return "", nil, "", 0, err
		}
		cache = weaker(cache, s)
	}
	return dir, env, cache, time.Since(start).Milliseconds(), nil
}

// materialise makes dst a private cache from src: a copy-on-write clone
// where the filesystem supports it, else a regular copy, else an empty
// cache. It never links dst to src.
func materialise(src, dst string) (string, error) {
	if err := RemoveAll(dst); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if cloneTree(src, dst) == nil {
		return CacheClone, nil
	}
	if err := RemoveAll(dst); err != nil {
		return "", err
	}
	if copyTree(src, dst) == nil {
		return CacheCopy, nil
	}
	if err := RemoveAll(dst); err != nil {
		return "", err
	}
	return CacheCold, os.MkdirAll(dst, 0o700)
}

func weaker(a, b string) string {
	rank := map[string]int{CacheClone: 2, CacheCopy: 1, CacheCold: 0}
	if rank[b] < rank[a] {
		return b
	}
	return a
}

// copyTree copies src to a new dst: regular files' contents and modes,
// directories' modes (applied once they are filled) and symlinks as
// symlinks. Nothing is hard-linked.
func copyTree(src, dst string) error {
	type dirMode struct {
		path string
		mode fs.FileMode
	}
	var dirs []dirMode
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, dirMode{out, info.Mode().Perm()})
			return os.Mkdir(out, 0o700)
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, out)
		case d.Type().IsRegular():
			return copyFile(p, out, info.Mode().Perm())
		}
		return nil // sockets, devices and the like aren't cache content
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
