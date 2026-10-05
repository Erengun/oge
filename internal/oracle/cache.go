package oracle

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
// It runs niced, on about half the cores, so it doesn't slow the
// implementer it overlaps.
var warmCommand = fmt.Sprintf(`nice -n 10 go list -p %d -e -export -deps -test -f '{{if .Error}}{{.ImportPath}}: {{.Error}}{{end}}' ./...`,
	max(1, runtime.NumCPU()/2))

// Replaceable for tests, to force each fallback.
var (
	cloneTree = cloneTreeOS
	copyTree  = copyTreeGo
)

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
	// stopped is set when WaitFor stopped the warm step at its limit.
	stopped bool
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

// Warmth is how a wait for the warm step ended.
type Warmth struct {
	Warm     *Execution `json:"warm"`
	Complete bool       `json:"complete"` // false: stopped at the limit, the seed is partial
	WaitedMs int64      `json:"waited_ms"`
	Error    string     `json:"error,omitempty"`
}

// WaitFor waits up to limit for the warm step. Past it, the warm step is
// stopped and the seed is used as it is: Go's cache entries are
// self-checking, so a partial seed only means more cache misses.
func (s *Seed) WaitFor(limit time.Duration) Warmth {
	start := time.Now()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-s.done:
	case <-t.C:
		s.stopped = true
		s.cancel()
		<-s.done
	}
	w := Warmth{Warm: s.warm, Complete: !s.stopped, WaitedMs: time.Since(start).Milliseconds()}
	if s.err != nil {
		w.Error = s.err.Error()
	}
	return w
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
		if _, _, err := materialise(r.SeedTemplate, s.GoCache()); err != nil {
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
		if hasModule {
			env["GOPROXY"] = "off" // the warm step never fetches
			e, _, err := r.Exec(wctx, warmCommand, dir, env, 10*time.Minute, 64<<10)
			s.warm, s.err = &e, err
		}
		// Checks run uncontained (ADR-0010) and can reach the seed by its
		// path; read-only, a stray write fails instead of poisoning every
		// later Check's cache, which Go trusts.
		if err := setModes(s.Dir(), 0o500, 0o400); err != nil && s.err == nil {
			s.err = err
		}
	}()
	return s, setupE, nil
}

// checkCache is how a Check's caches were made.
type checkCache struct {
	strategy, why string
	ms            int64
}

// prepareCheck is Prepare plus the Check-local caches: private, writable
// clones or copies of the Run's seed when there is one, else cold.
func (r *Runner) prepareCheck(root string) (string, map[string]string, checkCache, error) {
	dir, env, err := r.Prepare(root)
	if err != nil || r.Seed == nil {
		return dir, env, checkCache{strategy: CacheCold, why: "no cache seed"}, err
	}
	_, _ = r.Seed.Wait() // a failed warm step leaves a partial seed, still valid
	start := time.Now()
	c := checkCache{strategy: CacheClone}
	for _, p := range [][2]string{{r.Seed.GoCache(), env["GOCACHE"]}, {r.Seed.ModCache(), env["GOMODCACHE"]}} {
		s, why, err := materialise(p[0], p[1])
		if err != nil {
			return "", nil, checkCache{}, err
		}
		if weaker(c.strategy, s) != c.strategy || (c.why == "" && why != "") {
			c.why = why
		}
		c.strategy = weaker(c.strategy, s)
	}
	c.ms = time.Since(start).Milliseconds()
	return dir, env, c, nil
}

// materialise makes dst a private, writable cache from src: a
// copy-on-write clone where the filesystem supports it, else a regular
// copy, else an empty cache. It never links dst to src. why says why it
// fell back, if it did.
func materialise(src, dst string) (strategy, why string, err error) {
	if err := RemoveAll(dst); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", "", err
	}
	cerr := cloneTree(src, dst)
	if cerr == nil {
		return CacheClone, "", setModes(dst, 0o700, 0o600)
	}
	if err := RemoveAll(dst); err != nil {
		return "", "", err
	}
	perr := copyTree(src, dst)
	if perr == nil {
		return CacheCopy, "no clone: " + short(cerr), setModes(dst, 0o700, 0o600)
	}
	if err := RemoveAll(dst); err != nil {
		return "", "", err
	}
	return CacheCold, "no clone: " + short(cerr) + "; no copy: " + short(perr), os.MkdirAll(dst, 0o700)
}

func short(err error) string { return truncate(err.Error(), 120) }

// setModes sets every directory under dir (dir included) to dirMode and
// every regular file to fileMode. Directories are opened up first, so a
// read-only tree can be walked and changed.
func setModes(dir string, dirMode, fileMode fs.FileMode) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, p)
			return os.Chmod(p, 0o700)
		case d.Type().IsRegular():
			return os.Chmod(p, fileMode)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], dirMode); err != nil {
			return err
		}
	}
	return nil
}

func weaker(a, b string) string {
	rank := map[string]int{CacheClone: 2, CacheCopy: 1, CacheCold: 0}
	if rank[b] < rank[a] {
		return b
	}
	return a
}

// copyTreeGo copies src to a new dst: regular files' contents and modes,
// directories' modes (applied once they are filled) and symlinks as
// symlinks. Nothing is hard-linked.
func copyTreeGo(src, dst string) error {
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
