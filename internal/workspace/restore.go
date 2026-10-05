package workspace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Restore commits candidate with each of paths put back to its version in
// snap: the content it had there, or absent (#107). It is the tree that
// is checked and delivered by default when those paths are held back.
// A path whose Snapshot version can't stand beside the rest of the tree,
// a file where the tree now has a directory or under what is now a file,
// isn't restored: it is returned in clashes and keeps the Candidate's
// version. With nothing to restore, the commit is candidate itself.
func (r *RunRepo) Restore(snap, candidate string, paths []string, message string) (commit string, clashes []string, err error) {
	if len(paths) == 0 {
		return candidate, nil, nil
	}
	before, err := r.entries(snap)
	if err != nil {
		return "", nil, err
	}
	after, err := r.entries(candidate)
	if err != nil {
		return "", nil, err
	}
	final := make(map[string]treeEntry, len(after))
	for p, e := range after {
		final[p] = e
	}
	for _, p := range paths {
		if e, ok := before[p]; ok {
			final[p] = e
		} else {
			delete(final, p)
		}
	}
	var restore []string
	for _, p := range paths {
		if _, ok := final[p]; ok && clash(final, p) {
			clashes = append(clashes, p)
			continue
		}
		restore = append(restore, p)
	}
	sort.Strings(clashes)
	if len(restore) == 0 {
		return candidate, clashes, nil
	}
	var info bytes.Buffer
	for _, p := range restore {
		if e, ok := before[p]; ok {
			fmt.Fprintf(&info, "%s %s\t%s\x00", e.mode, e.oid, p)
		} else {
			fmt.Fprintf(&info, "0 %s\t%s\x00", strings.Repeat("0", 40), p)
		}
	}
	idx := filepath.Join(r.Dir, "index-restore-"+candidate[:12])
	defer os.Remove(idx)
	if _, err := r.git("", idx, nil, "read-tree", candidate); err != nil {
		return "", nil, err
	}
	if _, err := r.git("", idx, &info, "update-index", "-z", "--index-info"); err != nil {
		return "", nil, err
	}
	tree, err := r.git("", idx, nil, "write-tree")
	if err != nil {
		return "", nil, err
	}
	out, err := r.git("", "", nil, "commit-tree", "--no-gpg-sign", strings.TrimSpace(string(tree)), "-p", candidate, "-m", message)
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(string(out)), clashes, nil
}

// clash reports whether the file p can't stand in a tree of files: one
// of its parents is a file, or other files are under it.
func clash(files map[string]treeEntry, p string) bool {
	for d := p; strings.Contains(d, "/"); {
		d = d[:strings.LastIndex(d, "/")]
		if _, ok := files[d]; ok {
			return true
		}
	}
	for q := range files {
		if strings.HasPrefix(q, p+"/") {
			return true
		}
	}
	return false
}
