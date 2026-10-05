package workspace

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/redact"
)

// Enforcement classes: what kept a path in its Write scope (ADR-0010).
const (
	NativeEnforced = "native-enforced" // the agent's own sandbox or permissions
	RevertOnly     = "revert-only"     // only Öge's comparison after the Attempt
	Degraded       = "degraded"
)

// Revert classes that are not protected paths, so not Tamper events.
const (
	// ClassSymlinkEscape is a symlink whose target is absolute or
	// resolves outside the Workspace.
	ClassSymlinkEscape = "symlink_escape"
	// ClassInTheWay is a file standing where a protected path is restored:
	// on one of its parent directories, under it, or the same file under
	// another name on a case-insensitive filesystem.
	ClassInTheWay = "in_the_way"
)

// Patch limits for a reverted write (ADR-0011): text only, redacted, capped.
const (
	patchInputLimit = 256 << 10
	PatchCap        = 16 << 10
)

// ScopeRules is a Role kind's Write scope over a Workspace.
type ScopeRules struct {
	// Protected names the protected class of a slash-separated path, or ""
	// when the role may write it. A write to a protected path is a Tamper
	// event.
	Protected func(path string) string
	// Enforcement names a path's enforcement class; nil means revert-only.
	Enforcement func(path string) string
	// Put stores a patch and returns its blob id.
	Put func([]byte) (string, error)
}

// Revert is one write outside the Write scope, which Öge undoes: Evidence
// of an observed change, not a Claim (ADR-0011). Hashes are sha256 of the
// content ("" when the path is absent); a symlink's content is its target.
type Revert struct {
	Path        string `json:"path"`
	Change      string `json:"change"` // added, modified or deleted
	Class       string `json:"class"`
	Tamper      bool   `json:"tamper"`
	Before      string `json:"before"`
	After       string `json:"after"`
	BeforeSize  int64  `json:"before_size"`
	Size        int64  `json:"size"` // the reverted content's size
	Patch       string `json:"patch,omitempty"`
	NoPatch     string `json:"no_patch,omitempty"` // why there is no patch
	Truncated   bool   `json:"patch_truncated,omitempty"`
	Enforcement string `json:"enforcement"`

	restore *entry // the Snapshot's entry to put back; nil removes the path
	present bool   // the comparison found the path in the Workspace
}

// Scope is the comparison of a Workspace with its start state.
type Scope struct {
	Compared int // paths compared, ignored files included
	Reverts  []Revert
	ws       string
}

// Tamper counts the Tamper events among the reverts.
func (s *Scope) Tamper() int {
	n := 0
	for _, r := range s.Reverts {
		if r.Tamper {
			n++
		}
	}
	return n
}

// entry is a file as git records it: mode 100644, 100755 or 120000 (or
// "other" for a FIFO, socket or device), and for the Snapshot side its
// object id and content.
type entry struct {
	mode string
	oid  string
	data []byte
}

const (
	modeFile    = "100644"
	modeExec    = "100755"
	modeSymlink = "120000"
	modeOther   = "other"
)

// CheckScope compares every file in the Workspace ws, ignored ones
// included, with the Snapshot snap, and plans the reverts of writes
// outside rules. Run it only after the agent's process tree is gone. It
// changes nothing; Apply makes the reverts, so they can be recorded first.
//
// Paths are compared byte for byte as the filesystem lists them, with no
// case folding. Links are never followed; a Workspace .git (the agent's
// own repository) is ignored and never reaches a Candidate (ADR-0010).
func (r *RunRepo) CheckScope(ws, snap string, rules ScopeRules) (*Scope, error) {
	if fi, err := os.Lstat(ws); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("the Workspace %s is no longer a directory", ws)
	}
	before, err := r.tree(snap)
	if err != nil {
		return nil, err
	}
	before[ledger.WorkspaceMarker] = &entry{mode: modeFile, oid: gitOID(nil)}
	after, err := walkWorkspace(ws)
	if err != nil {
		return nil, err
	}
	s := &Scope{ws: ws}
	planned := map[string]*Revert{}
	plan := func(p, class string, tamper bool) {
		if planned[p] == nil {
			planned[p] = &Revert{Path: p, Class: class, Tamper: tamper, restore: before[p]}
		}
	}

	union := map[string]bool{}
	for p := range before {
		union[p] = true
	}
	for p := range after {
		union[p] = true
	}
	s.Compared = len(union)
	for p := range union {
		class := rules.Protected(p)
		if class == "" {
			continue
		}
		same, err := sameAsSnapshot(ws, p, before[p], after[p])
		if err != nil {
			return nil, err
		}
		if !same {
			plan(p, class, true)
		}
	}
	for p, mode := range after {
		if mode != modeSymlink || planned[p] != nil {
			continue
		}
		if same, err := sameAsSnapshot(ws, p, before[p], mode); err != nil {
			return nil, err
		} else if same {
			continue // the Snapshot's own link, which Preflight checked
		}
		if out, err := linkLeavesTree(ws, p); err != nil {
			return nil, err
		} else if out {
			plan(p, ClassSymlinkEscape, false)
		}
	}
	// What stands where a protected path goes back.
	var targets []string
	for p, rv := range planned {
		if rv.restore != nil {
			targets = append(targets, p)
		}
	}
	sort.Strings(targets)
	for _, p := range targets {
		blocked := false
		for q := path.Dir(p); q != "."; q = path.Dir(q) {
			if _, ok := after[q]; ok {
				plan(q, ClassInTheWay, false)
				blocked = true
			}
		}
		for q := range after {
			if strings.HasPrefix(q, p+"/") {
				plan(q, ClassInTheWay, false)
				blocked = true
			}
		}
		if _, ok := after[p]; ok || blocked {
			continue
		}
		// The path is gone, yet something answers to its name: the same
		// file under another name, on a case-insensitive filesystem.
		full := filepath.Join(ws, filepath.FromSlash(p))
		fi, err := os.Lstat(full)
		if err != nil {
			continue
		}
		dir := path.Dir(p)
		names, err := os.ReadDir(filepath.Dir(full))
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			other := path.Join(dir, n.Name())
			if oi, err := os.Lstat(filepath.Join(filepath.Dir(full), n.Name())); err == nil && os.SameFile(fi, oi) && other != p {
				for q := range after {
					if q == other || strings.HasPrefix(q, other+"/") {
						plan(q, ClassInTheWay, false)
					}
				}
			}
		}
	}

	for _, rv := range planned {
		if err := r.describe(ws, rv, after[rv.Path], rules); err != nil {
			return nil, err
		}
		s.Reverts = append(s.Reverts, *rv)
	}
	sort.Slice(s.Reverts, func(i, j int) bool { return s.Reverts[i].Path < s.Reverts[j].Path })
	return s, nil
}

// describe fills in a revert's change, hashes, sizes, patch and
// enforcement class.
func (r *RunRepo) describe(ws string, rv *Revert, afterMode string, rules ScopeRules) error {
	rv.present = afterMode != ""
	rv.Enforcement = RevertOnly
	if rules.Enforcement != nil {
		rv.Enforcement = rules.Enforcement(rv.Path)
	}
	var beforeData, afterData []byte
	if rv.restore != nil {
		var err error
		if beforeData, err = r.content(rv.restore); err != nil {
			return err
		}
		rv.Before, rv.BeforeSize = sha256Hex(beforeData), int64(len(beforeData))
	}
	if afterMode != "" {
		var err error
		if afterData, err = readEntry(ws, rv.Path, afterMode); err != nil {
			return err
		}
		rv.After, rv.Size = sha256Hex(afterData), int64(len(afterData))
	}
	switch {
	case rv.restore == nil:
		rv.Change = "added"
	case afterMode == "":
		rv.Change = "deleted"
	default:
		rv.Change = "modified"
	}
	switch {
	case afterMode == modeSymlink || (rv.restore != nil && rv.restore.mode == modeSymlink):
		rv.NoPatch = "symlink"
	case afterMode == modeOther:
		rv.NoPatch = "not a regular file"
	case !isText(beforeData) || !isText(afterData):
		rv.NoPatch = "binary"
	case len(beforeData) > patchInputLimit || len(afterData) > patchInputLimit:
		rv.NoPatch = "too large"
	case bytes.Equal(beforeData, afterData):
		rv.NoPatch = "content unchanged" // a mode change
	default:
		patch, err := r.diff(rv.Path, beforeData, afterData, rv.restore != nil, afterMode != "")
		if err != nil {
			return err
		}
		if len(patch) == 0 {
			rv.NoPatch = "no textual difference"
			return nil
		}
		patch = redact.Redact(patch)
		if len(patch) > PatchCap {
			patch, rv.Truncated = patch[:PatchCap], true
		}
		if rv.Patch, err = rules.Put(patch); err != nil {
			return err
		}
	}
	return nil
}

// Apply makes the planned reverts: every path the comparison found that
// must go is removed, deepest first, then each Snapshot entry is written
// back, never through a link. Only walked paths are removed: their parent
// directories were real directories, so a removal can't follow a link.
func (s *Scope) Apply() error {
	rs := append([]Revert(nil), s.Reverts...)
	sort.Slice(rs, func(i, j int) bool { return len(rs[i].Path) > len(rs[j].Path) })
	for _, rv := range rs {
		if !rv.present {
			continue
		}
		if err := os.Remove(filepath.Join(s.ws, filepath.FromSlash(rv.Path))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for _, rv := range s.Reverts {
		if rv.restore == nil {
			continue
		}
		if err := s.restore(rv.Path, rv.restore); err != nil {
			return fmt.Errorf("restoring %s: %w", rv.Path, err)
		}
	}
	return nil
}

func (s *Scope) restore(rel string, e *entry) error {
	full := filepath.Join(s.ws, filepath.FromSlash(rel))
	dir := s.ws
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory", dir)
		}
	}
	if fi, err := os.Lstat(full); err == nil {
		if fi.IsDir() {
			if err := os.RemoveAll(full); err != nil {
				return err
			}
		} else if err := os.Remove(full); err != nil {
			return err
		}
	}
	switch e.mode {
	case modeSymlink:
		if err := os.Symlink(string(e.data), full); err != nil {
			return err
		}
	default:
		perm := os.FileMode(0o644)
		if e.mode == modeExec {
			perm = 0o755
		}
		if rel == ledger.WorkspaceMarker {
			perm = 0o600
		}
		out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			return err
		}
		_, err = out.Write(e.data)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if err := os.Chmod(full, perm); err != nil { // past the umask
			return err
		}
	}
	// The name must now be listed exactly; a filesystem that answers to
	// another spelling of it would put the wrong path in the Candidate.
	names, err := os.ReadDir(filepath.Dir(full))
	if err != nil {
		return err
	}
	for _, n := range names {
		if n.Name() == filepath.Base(full) {
			return nil
		}
	}
	return fmt.Errorf("the filesystem lists it under another name")
}

// tree reads a commit's entries, with their content for later restores.
func (r *RunRepo) tree(commit string) (map[string]*entry, error) {
	out, err := r.git("", "", nil, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	t := map[string]*entry{}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode type oid
		if len(f) != 3 || f[1] != "blob" {
			continue
		}
		t[p] = &entry{mode: f[0], oid: f[2]}
	}
	return t, nil
}

// content loads a Snapshot entry's bytes once.
func (r *RunRepo) content(e *entry) ([]byte, error) {
	if e.data == nil && e.oid != gitOID(nil) {
		b, err := r.git("", "", nil, "cat-file", "blob", e.oid)
		if err != nil {
			return nil, err
		}
		e.data = b
	}
	return e.data, nil
}

// walkWorkspace lists every non-directory under ws with its git mode,
// never following a link and skipping any .git.
func walkWorkspace(ws string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == ws {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(ws, p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		mode := modeOther
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			mode = modeSymlink
		case fi.Mode().IsRegular() && fi.Mode()&0o100 != 0:
			mode = modeExec
		case fi.Mode().IsRegular():
			mode = modeFile
		}
		files[filepath.ToSlash(rel)] = mode
		return nil
	})
	return files, err
}

// sameAsSnapshot reports whether the Workspace path matches the Snapshot
// entry: both absent, or the same mode and content.
func sameAsSnapshot(ws, rel string, b *entry, afterMode string) (bool, error) {
	switch {
	case b == nil || afterMode == "":
		return b == nil && afterMode == "", nil
	case b.mode != afterMode:
		return false, nil
	}
	data, err := readEntry(ws, rel, afterMode)
	if err != nil {
		return false, err
	}
	return gitOID(data) == b.oid, nil
}

// readEntry reads a walked path's content without following a link: a
// link's target text, a regular file's bytes, nothing for anything else.
func readEntry(ws, rel, mode string) ([]byte, error) {
	full := filepath.Join(ws, filepath.FromSlash(rel))
	switch mode {
	case modeSymlink:
		t, err := os.Readlink(full)
		return []byte(t), err
	case modeFile, modeExec:
		f, err := os.OpenFile(full, os.O_RDONLY|oNoFollow, 0)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		var b bytes.Buffer
		_, err = b.ReadFrom(f)
		return b.Bytes(), err
	}
	return nil, nil
}

// gitOID is the SHA-1 object id git gives a blob of data.
func gitOID(data []byte) string {
	h := sha1.New()
	h.Write([]byte("blob " + strconv.Itoa(len(data)) + "\x00"))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// isText is git's binary test (no NUL in the first 8000 bytes) plus valid
// UTF-8, so a patch never carries binary content.
func isText(b []byte) bool {
	head := b
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) < 0 && utf8.Valid(b)
}

// diff is a unified diff of a path's Snapshot and reverted content, made
// by git in a private scratch directory inside the Run repository.
func (r *RunRepo) diff(rel string, before, after []byte, hasBefore, hasAfter bool) ([]byte, error) {
	tmp, err := os.MkdirTemp(r.Dir, "scope-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	a, b := os.DevNull, os.DevNull
	if hasBefore {
		a = filepath.Join(tmp, "a")
		if err := os.WriteFile(a, before, 0o600); err != nil {
			return nil, err
		}
	}
	if hasAfter {
		b = filepath.Join(tmp, "b")
		if err := os.WriteFile(b, after, 0o600); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command("git", "diff", "--no-index", "--text", "--no-color", "--no-ext-diff", "--no-textconv", "-U3", "--", a, b)
	cmd.Dir = tmp
	cmd.Env = append(scrubGitEnv(os.Environ()), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("git diff: %v", err)
	}
	i := bytes.Index(out, []byte("\n@@"))
	if i < 0 {
		return nil, nil
	}
	from, to := "a/"+rel, "b/"+rel
	if !hasBefore {
		from = "/dev/null"
	}
	if !hasAfter {
		to = "/dev/null"
	}
	return append([]byte("--- "+from+"\n+++ "+to+"\n"), out[i+1:]...), nil
}
