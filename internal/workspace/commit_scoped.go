package workspace

import (
	"bytes"
	"sort"
	"strings"
)

// zeroOID removes a path in update-index --index-info.
const zeroOID = "0000000000000000000000000000000000000000"

// CommitScoped commits the Workspace dir as a Candidate on top of parent,
// but never takes a protected path or an escaping link from it: in the
// index git built from dir, every protected path is set back to the
// Snapshot snap's entry, unless it is kept and the index holds exactly
// the content kept (Scope.Kept); and every link that is neither parent's
// nor one of links (the links the scope comparison let stand) is dropped,
// whatever its target text says. Whatever the index held there is a write
// made after the scope comparison (by a process that outlived the agent's
// group, say), and comes back as a Revert. The ref is not updated; SetRef
// does that once the reverts are recorded.
//
// The Candidate's protected content therefore never depends on a race
// with the Workspace: it is the Snapshot's, or the very content the scope
// comparison kept. It is restored from snap, not parent: on a send-back,
// parent may hold an earlier Attempt's kept addition, which a later
// Attempt's rejected edit must not bring back.
func (r *RunRepo) CommitScoped(dir, parent, snap, message string, protected func(string) string, links map[string]string, kept map[string]string) (string, []Revert, error) {
	base, err := r.tree(parent)
	if err != nil {
		return "", nil, err
	}
	orig, err := r.tree(snap)
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
		// set puts b back at p (nil removes it) where the index holds e.
		set := func(p string, e, b *entry, class string, tamper bool) error {
			rv := Revert{Path: p, Class: class, Tamper: tamper, Enforcement: RevertOnly, NoPatch: "written after the comparison"}
			if b != nil {
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
			case b == nil:
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
		for p := range orig {
			paths[p] = true
		}
		sorted := make([]string, 0, len(paths))
		for p := range paths {
			sorted = append(sorted, p)
		}
		sort.Strings(sorted)
		for _, p := range sorted {
			e := have[p]
			if class := protected(p); class != "" {
				o := orig[p]
				switch {
				case same(e, o):
				case e != nil && o != nil && e.mode == o.mode && kept[p] != "" && e.oid == kept[p]:
				default:
					if err := set(p, e, o, class, true); err != nil {
						return err
					}
				}
				continue
			}
			b := base[p]
			if same(e, b) {
				continue
			}
			if e != nil && e.mode == modeSymlink {
				target, err := r.git("", "", nil, "cat-file", "blob", e.oid)
				if err != nil {
					return err
				}
				// A late link is hostile by definition: dropped, never
				// judged by its text (l1 -> l2/.. with l2 -> . escapes).
				if t, ok := links[p]; !ok || t != string(target) {
					if err := set(p, e, b, ClassSymlinkEscape, false); err != nil {
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

// same reports whether two entries are both absent or the same blob.
func same(a, b *entry) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.mode == b.mode && a.oid == b.oid
}

// SetRef points ref at commit.
func (r *RunRepo) SetRef(ref, commit string) error {
	_, err := r.git("", "", nil, "update-ref", ref, commit)
	return err
}
