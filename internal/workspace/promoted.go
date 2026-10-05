package workspace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/ledger"
)

// How a judged role sees a Candidate's changes (ADR-0009).
const (
	// ClassAmbiguous is a new file matching no output or test glob: it
	// stays in the Candidate but is withheld from judged roles.
	ClassAmbiguous = "ambiguous"
	// ClassExcluded is a change to Öge's fixed agent, config and
	// instruction list: it never reaches a judged role.
	ClassExcluded = "excluded"
)

// Withheld is a Candidate file a judged role's view leaves out, with its
// git blob id. Its content is never copied into the record.
type Withheld struct {
	Path  string `json:"path"`
	Class string `json:"class"`
	Hash  string `json:"git_blob"`
}

// TODO(#46-decision): Öge's fixed agent/config/instruction list (spec #35:
// Excluded). Instruction files at any depth, and agent config directories.
var (
	excludedNames = []string{"CLAUDE.md", "CLAUDE.local.md", "AGENTS.md", "AGENTS.override.md", "GEMINI.md", ".mcp.json"}
	excludedDirs  = []string{".claude", ".codex", ".gemini", ".cursor", ".oge"}
)

// Excluded reports whether p is on Öge's fixed agent, config and
// instruction list, or is Öge's own Workspace marker.
func Excluded(p string) bool {
	if p == ledger.WorkspaceMarker {
		return true
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if i == len(parts)-1 {
			for _, n := range excludedNames {
				if part == n {
					return true
				}
			}
			break
		}
		for _, d := range excludedDirs {
			if part == d {
				return true
			}
		}
	}
	return false
}

// PromotedView commits the part of candidate a judged role may see, as one
// change on top of snap (ADR-0009): changes to files that already existed,
// and new files promotedNew accepts (the output and test globs). Excluded
// paths keep the Snapshot's version and other new files are left out; both
// are returned as withheld.
func (r *RunRepo) PromotedView(snap, candidate string, promotedNew func(path string) bool) (string, []Withheld, error) {
	promoted, withheld, err := r.Classify(snap, candidate, promotedNew)
	if err != nil {
		return "", nil, err
	}
	var info bytes.Buffer
	for _, c := range promoted {
		if c.NewMode != "" {
			fmt.Fprintf(&info, "%s %s\t%s\x00", c.NewMode, c.NewOID, c.Path)
		} else {
			fmt.Fprintf(&info, "0 %s\t%s\x00", strings.Repeat("0", 40), c.Path)
		}
	}
	idx := filepath.Join(r.Dir, "index-view-"+candidate[:12])
	defer os.Remove(idx)
	if _, err := r.git("", idx, nil, "read-tree", snap); err != nil {
		return "", nil, err
	}
	if info.Len() > 0 {
		if _, err := r.git("", idx, &info, "update-index", "-z", "--index-info"); err != nil {
			return "", nil, err
		}
	}
	tree, err := r.git("", idx, nil, "write-tree")
	if err != nil {
		return "", nil, err
	}
	out, err := r.git("", "", nil, "commit-tree", "--no-gpg-sign", strings.TrimSpace(string(tree)), "-p", snap,
		"-m", "Promoted view of "+candidate[:12])
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(string(out)), withheld, nil
}

// Classify sorts candidate's changes since snap (ADR-0009): the Promoted
// ones, and the withheld ones, Excluded or Ambiguous, in path order. A new
// file is Promoted only when promotedNew accepts it: the output and test
// globs, or a human's promotion. Nothing else decides it, not its
// directory, its extension or how important it looks.
func (r *RunRepo) Classify(snap, candidate string, promotedNew func(path string) bool) ([]Change, []Withheld, error) {
	before, err := r.entries(snap)
	if err != nil {
		return nil, nil, err
	}
	after, err := r.entries(candidate)
	if err != nil {
		return nil, nil, err
	}
	changed, err := r.ChangedFiles(snap, candidate)
	if err != nil {
		return nil, nil, err
	}
	var promoted []Change
	var withheld []Withheld
	for _, p := range changed {
		_, inSnap := before[p]
		a, inCand := after[p]
		switch {
		case Excluded(p):
			if inCand {
				withheld = append(withheld, Withheld{p, ClassExcluded, a.oid})
			}
			continue
		case !inSnap && !promotedNew(p):
			withheld = append(withheld, Withheld{p, ClassAmbiguous, a.oid})
			continue
		}
		c := Change{Path: p}
		if inCand {
			c.NewMode, c.NewOID = a.mode, a.oid
		}
		promoted = append(promoted, c)
	}
	sort.Slice(withheld, func(i, j int) bool { return withheld[i].Path < withheld[j].Path })
	return promoted, withheld, nil
}

// Resolve commits an Ambiguous-file resolution on top of candidate: the
// same tree without drop. Every resolution is a new Candidate, a promotion
// too, whose tree is unchanged (ADR-0013).
func (r *RunRepo) Resolve(candidate string, drop []string, message string) (string, error) {
	idx := filepath.Join(r.Dir, "index-resolve-"+candidate[:12])
	defer os.Remove(idx)
	if _, err := r.git("", idx, nil, "read-tree", candidate); err != nil {
		return "", err
	}
	if len(drop) > 0 {
		var info bytes.Buffer
		for _, p := range drop {
			fmt.Fprintf(&info, "0 %s\t%s\x00", strings.Repeat("0", 40), p)
		}
		if _, err := r.git("", idx, &info, "update-index", "-z", "--index-info"); err != nil {
			return "", err
		}
	}
	tree, err := r.git("", idx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	out, err := r.git("", "", nil, "commit-tree", "--no-gpg-sign", strings.TrimSpace(string(tree)), "-p", candidate, "-m", message)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type treeEntry struct{ mode, oid string }

// entries lists commit's files with their modes and blob ids.
func (r *RunRepo) entries(commit string) (map[string]treeEntry, error) {
	out, err := r.git("", "", nil, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return nil, err
	}
	m := map[string]treeEntry{}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode type oid
		if len(f) == 3 {
			m[p] = treeEntry{f[0], f[2]}
		}
	}
	return m, nil
}

// BlobIDs maps commit's files to their git blob ids.
func (r *RunRepo) BlobIDs(commit string) (map[string]string, error) {
	es, err := r.entries(commit)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(es))
	for p, e := range es {
		out[p] = e.oid
	}
	return out, nil
}
