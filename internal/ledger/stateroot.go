// Package ledger is Öge's persistence: the per-user state root, each Run's
// append-only hash-chained JSONL Ledger and its content-addressed blobs
// (ADR-0014). It serialises records but doesn't define them.
package ledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StateDirEnv is the documented state-root override.
const StateDirEnv = "OGE_STATE_DIR"

// StateRoot is the canonical per-user state root with its two trees.
type StateRoot struct {
	Dir     string
	Private string // Ledgers, blobs, Run repositories, Check directories; 0700
	Work    string // Workspaces; disposable
}

// RefusedError is a state root Öge won't use.
type RefusedError struct{ Dir, Why string }

func (e *RefusedError) Error() string {
	return fmt.Sprintf("state root %s is %s; set %s to a directory outside it", e.Dir, e.Why, StateDirEnv)
}

// DefaultStateDir is where the state root goes without an override:
// $XDG_STATE_HOME/oge, else ~/Library/Application Support/oge on macOS and
// ~/.local/state/oge elsewhere.
func DefaultStateDir(getenv func(string) string) (string, error) {
	if d := getenv(StateDirEnv); d != "" {
		return d, nil
	}
	if d := getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "oge"), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", errors.New("no HOME to put the state root under; set " + StateDirEnv)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "oge"), nil
	}
	return filepath.Join(home, ".local", "state", "oge"), nil
}

// OpenStateRoot canonicalises dir and refuses it if it resolves inside
// repo, under the system temp directory or inside an Öge Workspace; then it
// creates private/ (0700) and work/.
func OpenStateRoot(dir, repo string) (*StateRoot, error) {
	if !filepath.IsAbs(dir) {
		return nil, &RefusedError{dir, "not an absolute path"}
	}
	canon, err := canonical(dir)
	if err != nil {
		return nil, err
	}
	for _, r := range []struct{ base, why string }{
		{repo, "inside the repository"},
		{os.TempDir(), "under the system temp directory"},
	} {
		b, err := canonical(r.base)
		if err == nil && within(canon, b) {
			return nil, &RefusedError{canon, r.why}
		}
	}
	for d := canon; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, WorkspaceMarker)); err == nil {
			return nil, &RefusedError{canon, "inside an Öge Workspace"}
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	s := &StateRoot{Dir: canon, Private: filepath.Join(canon, "private"), Work: filepath.Join(canon, "work")}
	if err := os.MkdirAll(s.Private, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(s.Private, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Work, 0o700); err != nil {
		return nil, err
	}
	return s, nil
}

// WorkspaceMarker is created at the root of every Workspace, so a state
// root can never be placed inside one.
const WorkspaceMarker = ".oge-workspace"

// canonical resolves symlinks in the longest existing prefix of p.
func canonical(p string) (string, error) {
	p = filepath.Clean(p)
	var rest []string
	for {
		r, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(append([]string{r}, rest...)...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}

func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
