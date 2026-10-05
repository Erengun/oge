package oracle

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BlobLister is a Repo that lists a commit's git blob ids at once.
type BlobLister interface {
	BlobIDs(commit string) (map[string]string, error)
}

// treeGuard checks that a Check directory still holds the Candidate when
// its commands are done: code they ran, or a process one left behind,
// may not rewrite what is judged (#46). Paths the Oracle lays down, and
// what setup changed, are compared with what was there after setup.
type treeGuard struct {
	dir  string
	want map[string]string // path → git blob id
}

func newTreeGuard(repo Repo, commit, dir string, m *Manifest) (*treeGuard, error) {
	g := &treeGuard{dir: dir, want: map[string]string{}}
	var ids map[string]string
	if bl, ok := repo.(BlobLister); ok {
		var err error
		if ids, err = bl.BlobIDs(commit); err != nil {
			return nil, err
		}
	} else {
		files, err := repo.Files(commit)
		if err != nil {
			return nil, err
		}
		ids = map[string]string{}
		for _, p := range files {
			b, ok, err := repo.Show(commit, p)
			if err != nil {
				return nil, err
			}
			if ok {
				ids[p] = gitBlobID(b)
			}
		}
	}
	for p, id := range ids {
		if !MatchAny(m.TestGlobs, p) && !MatchAny(m.TestConfigGlobs, p) {
			g.want[p] = id
		}
	}
	return g, nil
}

// settle takes what setup changed as the Check's starting point.
func (g *treeGuard) settle() {
	for p, id := range g.want {
		if got := g.hash(p); got != id {
			g.want[p] = got
		}
	}
}

// changed are the Candidate paths that no longer hold what they did.
func (g *treeGuard) changed() []string {
	var out []string
	for p, id := range g.want {
		if g.hash(p) != id {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// hash is p's git blob id in the tree ("" when it is gone); a symlink's
// is its target's, as git stores it.
func (g *treeGuard) hash(p string) string {
	full := filepath.Join(g.dir, filepath.FromSlash(p))
	fi, err := os.Lstat(full)
	if err != nil {
		return ""
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(full)
		if err != nil {
			return ""
		}
		return gitBlobID([]byte(t))
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return ""
	}
	return gitBlobID(b)
}

func gitBlobID(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func treeChangedWhy(paths []string) string {
	names := paths
	if len(names) > 5 {
		names = append(names[:5:5], "…")
	}
	return fmt.Sprintf("the Check tree changed during the Check (%d Candidate files: %s); the Verdict can't be trusted", len(paths), strings.Join(names, ", "))
}
