package workspace

import (
	"bytes"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// zeroOID removes a path in update-index --index-info.
const zeroOID = "0000000000000000000000000000000000000000"

// CommitScoped commits the Workspace dir as a Candidate on top of parent,
// but never takes a protected path or an escaping link from it: in the
// index git built from dir, every protected path is set back to parent's
// entry and every new link out of the tree is dropped. Whatever the index
// held there is a write made after the scope comparison (by a process that
// outlived the agent's group, say), and comes back as a Revert. The ref
// is not updated; SetRef does that once the reverts are recorded.
//
// The Candidate's protected content therefore never depends on a race
// with the Workspace: it is the parent's by construction.
func (r *RunRepo) CommitScoped(dir, parent, message string, protected func(string) string) (string, []Revert, error) {
	base, err := r.tree(parent)
	if err != nil {
		return "", nil, err
	}
	var late []Revert
	fix := func(index string) error {
		out, err := r.git(dir, index, nil, "ls-files", "-s", "-z")
		if err != nil {
			return err
		}
		have := map[string]*entry{}
		for _, rec := range strings.Split(string(out), "\x00") {
			meta, p, ok := strings.Cut(rec, "\t")
			if f := strings.Fields(meta); ok && len(f) == 3 {
				have[p] = &entry{mode: f[0], oid: f[1]}
			}
		}
		var info bytes.Buffer
		set := func(p string, e *entry, class string, tamper bool) error {
			rv := Revert{Path: p, Class: class, Tamper: tamper, Enforcement: RevertOnly, NoPatch: "written after the comparison"}
			if b := base[p]; b != nil {
				data, err := r.content(b)
				if err != nil {
					return err
				}
				rv.Before, rv.BeforeSize = sha256Hex(data), int64(len(data))
				info.WriteString(b.mode + " " + b.oid + "\t" + p + "\x00")
			} else {
				info.WriteString("0 " + zeroOID + "\t" + p + "\x00")
			}
			if e != nil {
				data, err := r.git("", "", nil, "cat-file", "blob", e.oid)
				if err != nil {
					return err
				}
				rv.After, rv.Size = sha256Hex(data), int64(len(data))
			}
			switch {
			case base[p] == nil:
				rv.Change = "added"
			case e == nil:
				rv.Change = "deleted"
			default:
				rv.Change = "modified"
			}
			late = append(late, rv)
			return nil
		}
		paths := map[string]bool{}
		for p := range have {
			paths[p] = true
		}
		for p := range base {
			paths[p] = true
		}
		sorted := make([]string, 0, len(paths))
		for p := range paths {
			sorted = append(sorted, p)
		}
		sort.Strings(sorted)
		for _, p := range sorted {
			e, b := have[p], base[p]
			same := e != nil && b != nil && e.mode == b.mode && e.oid == b.oid
			if same || (e == nil && b == nil) {
				continue
			}
			if class := protected(p); class != "" {
				if err := set(p, e, class, true); err != nil {
					return err
				}
				continue
			}
			if e != nil && e.mode == modeSymlink {
				target, err := r.git("", "", nil, "cat-file", "blob", e.oid)
				if err != nil {
					return err
				}
				t := string(target)
				if filepath.IsAbs(t) || strings.HasPrefix(t, "/") || outside(filepath.FromSlash(path.Join(path.Dir(p), t))) {
					if err := set(p, e, ClassSymlinkEscape, false); err != nil {
						return err
					}
				}
			}
		}
		if info.Len() == 0 {
			return nil
		}
		_, err = r.git(dir, index, &info, "update-index", "-z", "--index-info")
		return err
	}
	commit, err := r.commitDir(dir, parent, message, false, fix)
	return commit, late, err
}

// SetRef points ref at commit.
func (r *RunRepo) SetRef(ref, commit string) error {
	_, err := r.git("", "", nil, "update-ref", ref, commit)
	return err
}
