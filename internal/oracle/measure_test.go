package oracle

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMeasureCacheSeed measures what the cache seed buys (ADR-0021): a
// cold Check, building the seed, a seeded Check and its materialisation,
// and a private copy for filesystems that can't clone. It runs only with
// OGE_MEASURE_CACHE=1 and never touches the network: the module fixture's
// dependencies come from the host's module download cache as a file proxy.
//
//	OGE_MEASURE_CACHE=1 go test ./internal/oracle -run TestMeasureCacheSeed -v
func TestMeasureCacheSeed(t *testing.T) {
	if os.Getenv("OGE_MEASURE_CACHE") == "" {
		t.Skip("set OGE_MEASURE_CACHE=1 to measure")
	}
	fixtures := []struct {
		name  string
		repo  fakeRepo
		setup string
	}{
		{"e2e fixture (one function)", goFixture, ""},
		{"generated (4 packages × 250 functions)", generated(4, 250), ""},
	}
	if repo, setup, err := withDeps(t); err != nil {
		t.Logf("skipping the module fixture: %v", err)
	} else {
		fixtures = append(fixtures, struct {
			name  string
			repo  fakeRepo
			setup string
		}{"module with dependencies (toml, lipgloss)", repo, setup})
	}
	m := &Manifest{Commands: []Command{{Run: "go test -json ./...", Report: ReportGoTestJSON, TimeoutSec: 600, OutputCap: 4 << 20}}}
	ctx := context.Background()
	for _, fx := range fixtures {
		r := newCacheRunner(t)
		r.SeedTemplate = ""
		start := time.Now()
		cold, err := r.Check(ctx, fx.repo, m, "c", fx.setup, filepath.Join(t.TempDir(), "cold"))
		if err != nil || !cold.Pass {
			t.Fatalf("%s: cold Check: %v %+v", fx.name, err, cold)
		}
		coldTime := time.Since(start)

		start = time.Now()
		seed, _, err := r.NewSeed(ctx, fx.repo, "snap", fx.setup, filepath.Join(t.TempDir(), "seed"))
		if err != nil {
			t.Fatal(err)
		}
		if warm, err := seed.Wait(); err != nil || warm == nil || !warm.Pass {
			t.Fatalf("%s: warm: %v %+v", fx.name, err, warm)
		}
		seedTime := time.Since(start)
		files, bytes := treeSize(seed.Dir())

		r.Seed = seed
		var seeded []time.Duration
		var res *Result
		for i := 0; i < 3; i++ {
			start = time.Now()
			res, err = r.Check(ctx, fx.repo, m, "c", fx.setup, filepath.Join(t.TempDir(), fmt.Sprint("seeded", i)))
			if err != nil || !res.Pass {
				t.Fatalf("%s: seeded Check: %v %+v", fx.name, err, res)
			}
			seeded = append(seeded, time.Since(start))
		}

		dst := filepath.Join(t.TempDir(), "copy")
		start = time.Now()
		if err := copyTree(seed.Dir(), dst); err != nil {
			t.Fatal(err)
		}
		copyTime := time.Since(start)
		_ = RemoveAll(dst)
		seed.Close()

		t.Logf("%s:\n  cold Check %v\n  seed (setup + warm) %v, %d files, %.0f MB\n  seeded Checks %v (%s, materialise %d ms on the last)\n  private copy instead of a clone: %v",
			fx.name, coldTime.Round(10*time.Millisecond), seedTime.Round(10*time.Millisecond), files, float64(bytes)/(1<<20),
			roundAll(seeded), res.Cache, res.CacheMs, copyTime.Round(time.Millisecond))
	}
}

func roundAll(ds []time.Duration) []time.Duration {
	for i := range ds {
		ds[i] = ds[i].Round(10 * time.Millisecond)
	}
	return ds
}

func treeSize(dir string) (files int, bytes int64) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				files++
				bytes += info.Size()
			}
		}
		return nil
	})
	return files, bytes
}

// generated is a module of pkgs packages, each with n functions and a
// test per function, importing a spread of the standard library.
func generated(pkgs, n int) fakeRepo {
	repo := fakeRepo{"go.mod": "module gen\n\ngo 1.22\n"}
	for p := 0; p < pkgs; p++ {
		var src, test strings.Builder
		fmt.Fprintf(&src, "package p%d\n\nimport (\n\t\"encoding/json\"\n\t\"net/http\"\n\t\"regexp\"\n\t\"text/template\"\n)\n\n", p)
		fmt.Fprintf(&src, "var _ = json.Marshal\nvar _ = http.StatusOK\nvar _ = regexp.MustCompile\nvar _ = template.New\n\n")
		fmt.Fprintf(&test, "package p%d\n\nimport \"testing\"\n\n", p)
		for i := 0; i < n; i++ {
			fmt.Fprintf(&src, "func F%d(a, b int) int {\n\tif a > b {\n\t\treturn a - b + %d\n\t}\n\treturn b - a + %d\n}\n\n", i, i, i)
			fmt.Fprintf(&test, "func TestF%d(t *testing.T) {\n\tif F%d(2, 1) != %d {\n\t\tt.Fatal()\n\t}\n}\n\n", i, i, 1+i)
		}
		repo[fmt.Sprintf("p%d/p.go", p)] = src.String()
		repo[fmt.Sprintf("p%d/p_test.go", p)] = test.String()
	}
	return repo
}

// withDeps is a small module using toml and lipgloss, with its go.sum,
// and a setup command that downloads its modules from the host's module
// download cache: a local file proxy, so nothing reaches the network.
func withDeps(t *testing.T) (fakeRepo, string, error) {
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return nil, "", err
	}
	proxy := "file://" + filepath.Join(strings.TrimSpace(string(out)), "cache", "download")
	repo := fakeRepo{
		"go.mod": "module dep\n\ngo 1.27\n\nrequire (\n\tcharm.land/lipgloss/v2 v2.0.6\n\tgithub.com/BurntSushi/toml v1.6.0\n)\n",
		"d.go": "package dep\n\nimport (\n\t\"charm.land/lipgloss/v2\"\n\t\"github.com/BurntSushi/toml\"\n)\n\n" +
			"func Render(s string) (string, error) {\n\tvar v struct{ Title string }\n\tif _, err := toml.Decode(s, &v); err != nil {\n\t\treturn \"\", err\n\t}\n\treturn lipgloss.NewStyle().Bold(true).Render(v.Title), nil\n}\n",
		"d_test.go": "package dep\n\nimport \"testing\"\n\nfunc TestRender(t *testing.T) {\n\tif _, err := Render(`Title = \"x\"`); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n",
	}
	dir := t.TempDir()
	if err := repo.Checkout("", dir); err != nil {
		return nil, "", err
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	tidy.Env = append(os.Environ(), "GOPROXY="+proxy, "GOSUMDB=off", "GOFLAGS=-mod=mod", "GOMODCACHE="+filepath.Join(dir, ".mod"), "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := tidy.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("go mod tidy: %v\n%s", err, out)
	}
	defer RemoveAll(filepath.Join(dir, ".mod"))
	for _, f := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, "", err
		}
		repo[f] = string(b)
	}
	return repo, "GOPROXY=" + proxy + " GOSUMDB=off go mod download", nil
}
