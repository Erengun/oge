// Package workspace owns the Snapshot and, later, the Run repository and
// Workspaces (ADR-0010).
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotRepository means the directory is not inside a git work tree.
var ErrNotRepository = errors.New("not inside a git repository")

// RepoRoot returns the top level of the git work tree containing dir.
func RepoRoot(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ErrNotRepository
	}
	return strings.TrimSpace(string(out)), nil
}

// SnapshotFile is what a Snapshot taken now would hold at a path.
type SnapshotFile struct {
	Data []byte
	// InSnapshot is false when the path is absent from the Snapshot.
	InSnapshot bool
	// Ignored is true when the file exists in the working tree but git
	// ignores it, so the Snapshot leaves it out.
	Ignored bool
}

// ErrNotRegular means the path is in the Snapshot but is not a regular
// file: a symlink, which could point outside the Snapshot, or a directory.
var ErrNotRegular = errors.New("not a regular file")

// ReadSnapshotFile reads rel (slash-separated, relative to root) as the
// Snapshot contains it. The Snapshot is the working tree's tracked files
// plus untracked files git doesn't ignore, uncommitted changes included
// (GLOSSARY: Snapshot; ADR-0010). Nothing is written to the repository.
// It returns ErrNotRegular, never following a link, unless rel is a
// regular file both in the working tree and in git's index.
func ReadSnapshotFile(root, rel string) (SnapshotFile, error) {
	out, err := git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", rel)
	if err != nil {
		return SnapshotFile{}, err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	fi, lerr := os.Lstat(path)
	switch {
	case errors.Is(lerr, fs.ErrNotExist):
		return SnapshotFile{}, nil // deleted in the working tree, or never there
	case lerr != nil:
		return SnapshotFile{}, lerr
	case !fi.Mode().IsRegular():
		return SnapshotFile{}, ErrNotRegular
	case len(bytes.TrimRight(out, "\x00")) == 0:
		return SnapshotFile{Ignored: true}, nil
	}
	staged, err := git(root, "ls-files", "-z", "--stage", "--", rel)
	if err != nil {
		return SnapshotFile{}, err
	}
	for _, entry := range bytes.Split(staged, []byte{0}) {
		if bytes.HasPrefix(entry, []byte("120000 ")) {
			return SnapshotFile{}, ErrNotRegular
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SnapshotFile{}, err
	}
	return SnapshotFile{Data: data, InSnapshot: true}, nil
}

func git(dir string, args ...string) ([]byte, error) {
	// Read-only against the user's repository: no optional index
	// refresh, no fsmonitor daemon.
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
