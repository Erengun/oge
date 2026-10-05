package oracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle/seedtest"
)

// fakeRepo checks out the same files for every commit.
type fakeRepo map[string]string

func (r fakeRepo) Files(string) ([]string, error) {
	var out []string
	for p := range r {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (r fakeRepo) Show(_, p string) ([]byte, bool, error) {
	s, ok := r[p]
	return []byte(s), ok, nil
}

func (r fakeRepo) Checkout(_, dir string) error {
	for p, s := range r {
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(s), 0o644); err != nil {
			return err
		}
	}
	return nil
}

var goFixture = fakeRepo{
	"go.mod":      "module fx\n\ngo 1.22\n",
	"add.go":      "package fx\n\nfunc Add(a, b int) int { return a + b }\n",
	"add_test.go": "package fx\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
}

func newCacheRunner(t *testing.T) *Runner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Checks run through /bin/sh; Windows refuses Runs (ADR-0017)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	blobs, err := ledger.OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{Blobs: blobs, Getenv: os.Getenv, SeedTemplate: testSeed}
}

// testSeed is a warm build cache most tests' seeds start from, so each
// doesn't compile the standard library again.
var testSeed string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oge-oracle-seed-")
	if err == nil && seedtest.Warm(dir) == nil {
		testSeed = seedtest.GoCache(dir)
	}
	code := m.Run()
	if dir != "" {
		_ = RemoveAll(dir)
	}
	os.Exit(code)
}

// treeHash hashes every path, mode and file content under dir.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h.Write([]byte(rel + "\x00" + info.Mode().String() + "\x00"))
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

func TestSeedIsWarmedFromTheSnapshot(t *testing.T) {
	r := newCacheRunner(t)
	r.SeedTemplate = "" // from cold, to see the warm step fill it
	seed, setup, err := r.NewSeed(context.Background(), goFixture, "snap", "", filepath.Join(t.TempDir(), "seed"))
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if setup != nil {
		t.Errorf("setup ran with no setup command: %+v", setup)
	}
	warm, err := seed.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if warm == nil || !warm.Pass {
		t.Fatalf("warm step = %+v, want a passing execution", warm)
	}
	if n := countFiles(t, seed.GoCache()); n < 50 {
		t.Errorf("the seed's build cache holds %d files; the warm step didn't fill it", n)
	}
	if !strings.Contains(strings.Join(warm.EnvNames, ","), "GOPROXY") {
		t.Errorf("the warm step's env %v doesn't pin GOPROXY (it must stay offline)", warm.EnvNames)
	}
}

func TestSetupOnTheSnapshotFillsTheSeed(t *testing.T) {
	r := newCacheRunner(t)
	setupCmd := `test -n "$GOCACHE" && echo from-setup > "$GOCACHE/setup-marker" && mkdir -p "$GOMODCACHE/m" && echo m > "$GOMODCACHE/m/marker"`
	seed, setup, err := r.NewSeed(context.Background(), fakeRepo{"README": "no go here\n"}, "snap", setupCmd, filepath.Join(t.TempDir(), "seed"))
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if setup == nil || !setup.Pass {
		t.Fatalf("setup = %+v, want a passing execution", setup)
	}
	if warm, err := seed.Wait(); err != nil || warm != nil {
		t.Errorf("warm = %+v, %v; want no warm step without a go.mod", warm, err)
	}
	for _, p := range []string{filepath.Join(seed.GoCache(), "setup-marker"), filepath.Join(seed.ModCache(), "m", "marker")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("setup's write didn't reach the seed: %v", err)
		}
	}
}

func TestNoSeedWithoutSetupOrGoModule(t *testing.T) {
	r := newCacheRunner(t)
	seed, _, err := r.NewSeed(context.Background(), fakeRepo{"README": "x\n"}, "snap", "", filepath.Join(t.TempDir(), "seed"))
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		t.Errorf("seed = %+v, want none: nothing could warm it", seed)
	}
	seed.Close() // a nil seed is safe to close
	r.Seed = seed
	res, err := r.Check(context.Background(), fakeRepo{"README": "x\n"}, &Manifest{Commands: []Command{{Run: "true", TimeoutSec: 60, OutputCap: 1 << 10}}}, "c", "", filepath.Join(t.TempDir(), "check"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Cache != CacheCold {
		t.Errorf("Cache = %q, want %q", res.Cache, CacheCold)
	}
}

// A Candidate that writes into its Check-local cache must not reach the
// seed or the next Check.
func TestCheckCacheIsAPrivateCopyOfTheSeed(t *testing.T) {
	r := newCacheRunner(t)
	seed, _, err := r.NewSeed(context.Background(), goFixture, "snap", "", filepath.Join(t.TempDir(), "seed"))
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if _, err := seed.Wait(); err != nil {
		t.Fatal(err)
	}
	before := treeHash(t, seed.Dir())
	r.Seed = seed

	poison := &Manifest{Commands: []Command{{
		Run: `set -e; echo poison > "$GOCACHE/poison"; chmod -R u+w "$GOCACHE" "$GOMODCACHE";` +
			` for f in $(find "$GOCACHE" -type f -name '*-d' | head -5); do echo poison > "$f"; done;` +
			` echo poison > "$GOMODCACHE/poison"; case "$GOCACHE" in *"` + seed.Dir() + `"*) exit 9;; esac`,
		TimeoutSec: 60, OutputCap: 1 << 10,
	}}}
	first, err := r.Check(context.Background(), goFixture, poison, "c1", "", filepath.Join(t.TempDir(), "check1"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Pass {
		t.Fatalf("the poisoning Check failed: %+v", first.Commands)
	}
	want := CacheClone
	if runtime.GOOS != "darwin" {
		want = "" // a clone or a private copy, depending on the filesystem
	}
	if first.Cache == CacheCold || (want != "" && first.Cache != want) {
		t.Errorf("Cache = %q, want a seeded cache (%q on darwin)", first.Cache, CacheClone)
	}

	if after := treeHash(t, seed.Dir()); after != before {
		t.Error("a Candidate's writes to its Check cache reached the seed")
	}
	next := &Manifest{Commands: []Command{
		{Run: `test ! -e "$GOCACHE/poison" && test ! -e "$GOMODCACHE/poison" && ! grep -rqx poison "$GOCACHE"`, TimeoutSec: 60, OutputCap: 1 << 10},
		{Run: "go test -json ./...", Report: ReportGoTestJSON, TimeoutSec: 120, OutputCap: 1 << 20},
	}}
	second, err := r.Check(context.Background(), goFixture, next, "c2", "", filepath.Join(t.TempDir(), "check2"))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Pass {
		t.Errorf("the next Check saw the poison or failed: %+v", second.Commands)
	}
}

func TestMaterialiseFallsBackToAPrivateCopy(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "ro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "ro", "f"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	defer RemoveAll(src)
	dst := filepath.Join(t.TempDir(), "dst")
	defer RemoveAll(dst)
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "ro", "f")); err != nil || string(b) != "x" {
		t.Errorf("copy = %q, %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "ro")); err != nil || fi.Mode().Perm() != 0o555 {
		t.Errorf("copied dir mode = %v, %v; want the seed's 0555", fi.Mode(), err)
	}
}

func TestCacheMaterialisationIsTimed(t *testing.T) {
	r := newCacheRunner(t)
	seed, _, err := r.NewSeed(context.Background(), fakeRepo{"README": "x\n"}, "snap", `echo x > "$GOCACHE/x"`, filepath.Join(t.TempDir(), "seed"))
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	r.Seed = seed
	start := time.Now()
	res, err := r.Check(context.Background(), fakeRepo{"README": "x\n"}, &Manifest{Commands: []Command{{Run: `test -e "$GOCACHE/x"`, TimeoutSec: 60, OutputCap: 1 << 10}}}, "c", "", filepath.Join(t.TempDir(), "check"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass || res.Cache == CacheCold {
		t.Errorf("Pass = %v, Cache = %q; want the seed's file in the Check cache", res.Pass, res.Cache)
	}
	if res.CacheMs < 0 || time.Duration(res.CacheMs)*time.Millisecond > time.Since(start) {
		t.Errorf("CacheMs = %d, not a plausible duration", res.CacheMs)
	}
}
