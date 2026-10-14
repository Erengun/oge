package workspace

import (
	"fmt"
	"strings"
)

// Change is one path that differs between two commits of the Run
// repository, as delivery needs it (#54).
type Change struct {
	Path string
	// Status is git's: A added, D deleted, M modified, T type changed.
	Status           byte
	OldMode, NewMode string // "" when absent; 100644, 100755 or 120000
	OldOID, NewOID   string
}

// Changes lists every path that differs between from and to, without
// rename detection, in path order.
func (r *RunRepo) Changes(from, to string) ([]Change, error) {
	out, err := r.git("", "", nil, "diff-tree", "-r", "-z", "--raw", "--no-renames", "--full-index", from, to)
	if err != nil {
		return nil, err
	}
	// Each entry is ":oldmode newmode oldoid newoid status\0path\0".
	parts := strings.Split(string(out), "\x00")
	var cs []Change
	for i := 0; i+1 < len(parts); i += 2 {
		f := strings.Fields(strings.TrimPrefix(parts[i], ":"))
		if len(f) != 5 {
			continue
		}
		c := Change{Path: parts[i+1], Status: f[4][0], OldMode: f[0], NewMode: f[1], OldOID: f[2], NewOID: f[3]}
		if c.OldMode == "000000" {
			c.OldMode, c.OldOID = "", ""
		}
		if c.NewMode == "000000" {
			c.NewMode, c.NewOID = "", ""
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// BlobSize returns a blob's size in bytes, without reading it.
func (r *RunRepo) BlobSize(oid string) (int64, error) {
	out, err := r.git("", "", nil, "cat-file", "-s", oid)
	if err != nil {
		return 0, err
	}
	var n int64
	_, err = fmt.Sscan(strings.TrimSpace(string(out)), &n)
	return n, err
}

// Blob returns a blob's bytes.
func (r *RunRepo) Blob(oid string) ([]byte, error) {
	return r.git("", "", nil, "cat-file", "blob", oid)
}

// PathDiff is the patch of one path between two commits, with git's
// binary stanza unless text is set.
func (r *RunRepo) PathDiff(from, to, path string, text bool) ([]byte, error) {
	args := []string{"diff-tree", "-p", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv"}
	if text {
		args = append(args, "--text")
	}
	return r.git("", "", nil, append(args, from, to, "--", ":(literal)"+path)...)
}
