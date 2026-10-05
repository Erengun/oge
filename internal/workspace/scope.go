package workspace

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	// Tamper reports whether a write to a path of this protected class is
	// a Tamper event; nil means every one is. A role that isn't judged
	// (the verifier) commits ordinary scope violations instead (spec #35).
	Tamper func(class string) bool
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
	// Links are the symlinks the Workspace holds once the reverts are
	// made, with their targets: the only links a Candidate may take.
	Links map[string]string
	ws    string
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
	after, unreadable, err := walkWorkspace(ws)
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
		if unreadable[p] || !sameAsSnapshot(ws, p, before[p], after[p]) {
			plan(p, class, rules.Tamper == nil || rules.Tamper(class))
		}
	}
	for p, mode := range after {
		if mode != modeSymlink || planned[p] != nil {
			continue
		}
		if sameAsSnapshot(ws, p, before[p], mode) {
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
	s.Links = map[string]string{}
	for p, mode := range after {
		if mode != modeSymlink || planned[p] != nil {
			continue
		}
		if t, err := os.Readlink(filepath.Join(ws, filepath.FromSlash(p))); err == nil {
			s.Links[p] = t
		}
	}
	for p, rv := range planned {
		if rv.restore != nil && rv.restore.mode == modeSymlink {
			s.Links[p] = string(rv.restore.data)
		}
	}
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
	unreadable := false
	if afterMode != "" {
		h, err := hashEntry(ws, rv.Path, afterMode)
		if err != nil {
			unreadable = true // the revert removes it all the same
		} else {
			afterData, rv.After, rv.Size = h.head, h.sum, h.size
		}
	}
	switch {
	case rv.restore == nil:
		rv.Change = "added"
	case afterMode == "":
		rv.Change = "deleted"
	case !unreadable && rv.Before == rv.After:
		rv.Change = "mode" // the same content: only its mode or permissions changed
	default:
		rv.Change = "modified"
	}
	switch {
	case afterMode == modeSymlink || (rv.restore != nil && rv.restore.mode == modeSymlink):
		rv.NoPatch = "symlink"
	case afterMode == modeOther:
		rv.NoPatch = "not a regular file"
	case unreadable:
		rv.NoPatch = "unreadable"
	case rv.BeforeSize > patchInputLimit || rv.Size > patchInputLimit:
		rv.NoPatch = "too large"
	case !isText(beforeData) || !isText(afterData):
		rv.NoPatch = "binary"
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
		full := filepath.Join(s.ws, filepath.FromSlash(rv.Path))
		if err := ownerAccess(filepath.Dir(full), 0o700); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
		default:
			if err := ownerAccess(dir, 0o700); err != nil {
				return err
			}
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
// never following a link and skipping any .git. Directories and files the
// agent made unreadable to their owner get owner access back, so the
// comparison and git can read them (git keeps no such permission bits);
// those files are listed in unreadable, since that alone is a change.
func walkWorkspace(ws string) (files map[string]string, unreadable map[string]bool, err error) {
	files, unreadable = map[string]string{}, map[string]bool{}
	if err := ownerAccess(ws, 0o700); err != nil {
		return nil, nil, err
	}
	err = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
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
			return ownerAccess(p, 0o700) // before WalkDir reads it
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
		rel = filepath.ToSlash(rel)
		if mode == modeFile || mode == modeExec {
			if fi.Mode().Perm()&0o400 == 0 {
				unreadable[rel] = true
			}
			if err := ownerAccess(p, 0o600); err != nil {
				return err
			}
		}
		files[rel] = mode
		return nil
	})
	return files, unreadable, err
}

// ownerAccess adds the owner permission bits want to path's, never
// following a link.
func ownerAccess(p string, want os.FileMode) error {
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm()&want == want {
		return err
	}
	return os.Chmod(p, fi.Mode().Perm()|want)
}

// sameAsSnapshot reports whether the Workspace path matches the Snapshot
// entry: both absent, or the same mode and content. A path it can't read
// is changed.
func sameAsSnapshot(ws, rel string, b *entry, afterMode string) bool {
	switch {
	case b == nil || afterMode == "":
		return b == nil && afterMode == ""
	case b.mode != afterMode:
		return false
	}
	h, err := hashEntry(ws, rel, afterMode)
	return err == nil && h.oid == b.oid
}

// hashed is a walked path's content, streamed: its git object id, its
// sha256, its size, and at most patchInputLimit+1 bytes of its start.
type hashed struct {
	oid, sum string
	size     int64
	head     []byte
}

// hashEntry hashes a walked path without following a link: a link's
// target text, a regular file's bytes, nothing for anything else.
func hashEntry(ws, rel, mode string) (hashed, error) {
	full := filepath.Join(ws, filepath.FromSlash(rel))
	var r io.Reader
	var size int64
	switch mode {
	case modeSymlink:
		t, err := os.Readlink(full)
		if err != nil {
			return hashed{}, err
		}
		r, size = strings.NewReader(t), int64(len(t))
	case modeFile, modeExec:
		f, err := os.OpenFile(full, os.O_RDONLY|oNoFollow, 0)
		if err != nil {
			return hashed{}, err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return hashed{}, err
		}
		r, size = f, fi.Size()
	default:
		r = strings.NewReader("")
	}
	g, s := sha1.New(), sha256.New()
	g.Write([]byte("blob " + strconv.FormatInt(size, 10) + "\x00"))
	var head bytes.Buffer
	n, err := io.Copy(io.MultiWriter(g, s, &capped{&head, patchInputLimit + 1}), r)
	if err != nil {
		return hashed{}, err
	}
	if n != size {
		return hashed{}, fmt.Errorf("%s changed while it was read", rel)
	}
	return hashed{oid: hex.EncodeToString(g.Sum(nil)), sum: hex.EncodeToString(s.Sum(nil)), size: n, head: head.Bytes()}, nil
}

// capped keeps the first n bytes written to it.
type capped struct {
	b *bytes.Buffer
	n int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.n - c.b.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		c.b.Write(p[:room])
	}
	return len(p), nil
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
