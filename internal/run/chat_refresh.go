package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/workspace"
)

// RecSnapshotRefreshed is a Chat log record (D10): the Chat's Snapshot
// was retaken from the user's repository after an apply.
const RecSnapshotRefreshed = "SnapshotRefreshed"

// Refresh is what RefreshSnapshot did.
type Refresh int

const (
	// RefreshNone: nothing to retake: no apply yet, or the user's tree
	// is the Snapshot's already.
	RefreshNone Refresh = iota
	// RefreshDone: the Snapshot, Oracle v0, the protected set, the
	// Preflight and the Snapshot control are now the new base's.
	RefreshDone
	// RefreshDeferred: a cut was in flight, and keeps its old base. The
	// retake runs when the cuts settle, if the tree is still the delivered one.
	RefreshDeferred
	// RefreshOutside: the user's repository isn't what the Chat
	// delivered, so it changed outside Öge. Nothing was merged or moved.
	RefreshOutside
	// RefreshConflict: a sibling Chat's apply changed a file this Chat
	// has edited since its own apply. Nothing was merged or moved.
	RefreshConflict
)

// OutsideNote is the one line shown when the repository changed outside
// Öge and can't be taken as the new base.
const OutsideNote = "repo changed outside Öge; not merged"

// ConflictNote is the one line shown when another Chat's apply changed
// files this Chat is editing.
const ConflictNote = "another Chat's apply changed files this Chat is editing; not merged"

// Wrote is one path an apply wrote into the user's repository: its mode
// and the git blob id of the bytes written, both "" for a path the apply
// deleted.
type Wrote struct {
	Path string `json:"path"`
	Mode string `json:"mode,omitempty"`
	OID  string `json:"oid,omitempty"`
}

// NoteDelivered tells the Chat what a sibling Chat's apply on the same
// repository wrote, and retakes the Snapshot on it if it can. Only a
// Chat that has applied retakes: its next apply merges on what it
// applied, so a sibling's write never needs to be in its base before.
// A path the sibling wrote counts as Öge's only while it holds exactly
// those bytes; the newest write of a path is the one kept.
func (c *Chat) NoteDelivered(wrote []Wrote) (Refresh, error) {
	c.supersedeMu.Lock()
	if c.siblings == nil {
		c.siblings = map[string]Wrote{}
	}
	for _, w := range wrote {
		c.siblings[w.Path] = w
	}
	c.supersedeMu.Unlock()
	return c.RefreshSnapshot()
}

// RefreshSnapshot retakes the Chat's Snapshot after an apply (D10), but
// only when the user's repository is what Öge delivered: the tree of the
// commit last applied (Delivered's rev), or of the Snapshot once it was
// retaken on that apply, but for paths a sibling Chat's apply wrote since
// (NoteDelivered), each holding exactly the bytes it wrote. The Workspace
// holds no edit of the user's, and Öge never merges one in, so any other
// tree is an outside change: RefreshOutside, and nothing moves. Taking it
// as the base would turn the user's own edit into a diff the Implementer
// appears to have made.
//
// The Workspace is carried onto the new base: what the Chat edited since
// its apply stays, and a sibling's paths are written in, so neither reads
// as the Implementer's on the next turn. A sibling's path this Chat has
// edited differently is RefreshConflict, and nothing is written or moved.
// Neither is a pending edit to a path the new base protects (it would
// read as Tamper), nor a cut not yet applied (its Run holds the old
// base): the base is kept, and the next apply evens it out. A turn in
// flight may be writing the Workspace, so the retake waits for it.
//
// A cut in flight keeps its base (it captured the old Snapshot, Oracle and
// Preflight by value) and the retake is deferred: Cut retries it once it
// settles. A retake changes the base, not the chain: c.feedbacks, c.chain
// and c.autos stay as they are (the user's turn ends a chain, never an
// apply), and the turn numbers, Delivered and the advice window are
// untouched.
func (c *Chat) RefreshSnapshot() (Refresh, error) {
	if err := c.Err(); errors.Is(err, ErrChatClosed) {
		return RefreshNone, err
	}
	rev, runDir := c.Applied()
	if rev == "" {
		return RefreshNone, nil
	}
	if why, err := workspace.Preflight(c.source); err != nil || len(why) > 0 {
		return RefreshNone, nil // a repo Öge can't snapshot now: keep the base, say nothing
	}
	scratch := filepath.Join(c.work, "retake")
	defer oracle.RemoveAll(scratch)
	commit, info, err := c.repo.RetakeSnapshot(c.source, scratch)
	if err != nil {
		return RefreshNone, err
	}
	if err := c.repo.Import(filepath.Join(runDir, "repo.git"), rev, "refs/oge/chat/applied"); err != nil {
		return RefreshNone, fmt.Errorf("taking the applied commit: %w", err)
	}
	have, err := c.repo.TreeOf(commit)
	if err != nil {
		return RefreshNone, err
	}
	c.scopeMu.Lock()
	cur, snap, on := c.Snapshot, c.info, c.retakenOn
	c.scopeMu.Unlock()
	// base is what Öge delivered last: rev, or the Snapshot retaken on it
	// (a sibling's apply taken since is in it, and rev lacks it).
	base := rev
	if on == rev {
		base = cur
	}
	want, err := c.repo.TreeOf(base)
	if err != nil {
		return RefreshNone, err
	}
	curTree, err := c.repo.TreeOf(cur)
	if err != nil {
		return RefreshNone, err
	}
	var sibling []string // the paths a sibling Chat's apply wrote
	switch {
	case have == curTree:
		c.setRefreshDue(false)
		c.forgetSiblings() // what they wrote is in the base, or was changed since
		return RefreshNone, nil
	case have == want:
		c.forgetSiblings() // no path holds what a sibling wrote
	default:
		if sibling, err = c.siblingPaths(base, commit); err != nil {
			return RefreshNone, err
		}
		if sibling == nil {
			c.setRefreshDue(false)
			return RefreshOutside, nil
		}
	}
	if !c.cutMu.TryLock() {
		c.setRefreshDue(true)
		return RefreshDeferred, nil
	}
	defer c.cutMu.Unlock()
	c.setRefreshDue(false)
	if err := c.Err(); errors.Is(err, ErrChatClosed) {
		return RefreshNone, err
	}
	m, mBlob, err := oracle.NewV0(c.repo, commit, c.frozen, c.blobs)
	if err != nil {
		return RefreshNone, err
	}
	// The Preflight swap and the checks below are one step: no turn
	// commits, and Close can't start, between them.
	c.prepMu.Lock()
	defer c.prepMu.Unlock()
	if c.prepClosed {
		return RefreshNone, ErrChatClosed
	}
	c.scopeMu.Lock()
	defer c.scopeMu.Unlock()
	// Another retake may have won while this one read: then base is stale,
	// and nothing is logged twice.
	if c.Snapshot != cur {
		return RefreshNone, nil
	}
	protected := implementerScope(m, c.frozen)
	last, write, undo, r, err := c.rebaseWorkspace(base, commit, sibling, protected)
	if err != nil || r != RefreshDone {
		return r, err
	}
	// The last cut's commit is where the next cut's Implementer changes
	// start (toCut). When the chain moves onto the new base, so does it:
	// only a cut fully applied can, as the new base itself, and a cut not
	// yet applied keeps the old base.
	c.supersedeMu.Lock()
	lc := c.lastCut
	c.supersedeMu.Unlock()
	moved := last != c.last
	if moved && lc != nil {
		if t, err := c.repo.TreeOf(lc.commit); err != nil {
			return RefreshNone, err
		} else if t != want {
			return RefreshNone, nil
		}
	}
	rec := map[string]any{"commit": commit, "prev": cur, "applied": rev, "run": filepath.Base(runDir),
		"head": info.Head, "branch": info.Branch, "modified": info.Modified, "untracked": info.Untracked,
		"oracle_version": m.Version, "manifest": mBlob, "tests": len(m.Tests), "prev_head": snap.Head}
	if len(sibling) > 0 {
		rec["sibling"], rec["last"] = sibling, last
	}
	if len(write) > 0 {
		// The Workspace is written with no turn in flight, and none
		// starts until it is. A write that fails is put back, and so is
		// the whole when the record can't be written: the Workspace stays
		// what c.last holds.
		c.mu.Lock()
		if c.cur != nil {
			c.mu.Unlock()
			c.setRefreshDue(true)
			return RefreshDeferred, nil
		}
		err := writeWorkspace(c.Workspace, write, undo)
		if err == nil {
			// The record says what the new base is before it is.
			if err = c.log(RecSnapshotRefreshed, rec); err != nil {
				unwriteWorkspace(c.Workspace, undo)
			}
		}
		c.mu.Unlock()
		if err != nil {
			return RefreshNone, err
		}
	} else if err := c.log(RecSnapshotRefreshed, rec); err != nil {
		return RefreshNone, err
	}
	if err := c.repo.SetRef(workspace.SnapshotRef, commit); err != nil {
		return RefreshNone, err
	}
	c.cancelPrep()
	<-c.prepared
	c.prep.control.Stop()
	c.prep.seed.Close()
	c.prep, c.refused = chatPrep{}, nil
	c.Snapshot, c.info, c.v0, c.protected, c.last, c.retakenOn = commit, info, mBlob, protected, last, rev
	c.supersedeMu.Lock()
	if moved && lc != nil {
		nl := *lc
		nl.commit = commit
		c.lastCut = &nl
		if c.cuts[lc.run] == lc {
			c.cuts[lc.run] = &nl
		}
	}
	c.siblings = nil // in the base now
	c.supersedeMu.Unlock()
	c.prepare(m, commit)
	return RefreshDone, nil
}

// forgetSiblings drops what sibling Chats' applies wrote: the base holds
// it now. A path a sibling wrote and something changed since must never
// read as Öge's later, when it holds those bytes again.
func (c *Chat) forgetSiblings() {
	c.supersedeMu.Lock()
	c.siblings = nil
	c.supersedeMu.Unlock()
}

// siblingPaths are the paths the tree of commit, the user's repository
// now, differs from base's on, when each is one a sibling Chat's apply
// wrote and holds exactly what it wrote. nil: some other change is in it.
// A path that no longer holds what a sibling wrote is forgotten: putting
// those bytes back by hand later is the user's edit, never Öge's.
func (c *Chat) siblingPaths(base, commit string) ([]string, error) {
	changes, err := c.repo.Changes(base, commit)
	if err != nil {
		return nil, err
	}
	c.supersedeMu.Lock()
	defer c.supersedeMu.Unlock()
	var paths []string
	other := false
	holds := map[string]bool{}
	for _, ch := range changes {
		w, ok := c.siblings[ch.Path]
		if !ok || w.Mode != ch.NewMode || w.OID != ch.NewOID {
			other = true
			continue
		}
		holds[ch.Path] = true
		paths = append(paths, ch.Path)
	}
	for p := range c.siblings {
		if !holds[p] {
			delete(c.siblings, p)
		}
	}
	if other {
		return nil, nil
	}
	return paths, nil
}

// rebaseWorkspace carries the Workspace, last committed as c.last, from
// base, what Öge last delivered, onto commit, the new base, with scopeMu
// held. last is the Workspace's commit on the new base, write what the
// Workspace needs written to hold it (the sibling's paths), and undo what
// puts each of those back. r is RefreshDone when the retake can go on.
func (c *Chat) rebaseWorkspace(base, commit string, sibling []string, protected func(string) string) (last string, write, undo []wsWrite, r Refresh, err error) {
	lastTree, err := c.repo.TreeOf(c.last)
	if err != nil {
		return "", nil, nil, RefreshNone, err
	}
	want, err := c.repo.TreeOf(base)
	if err != nil {
		return "", nil, nil, RefreshNone, err
	}
	last = c.last
	switch {
	case lastTree == want && len(sibling) == 0:
		return last, nil, nil, RefreshDone, nil
	case lastTree == want:
		last = commit // nothing pending: the Workspace is the new base
	default:
		if len(sibling) > 0 {
			// A sibling's path this Chat edited too, and to other bytes, is
			// never merged.
			mine, err := c.repo.ChangedFiles(base, c.last)
			if err != nil {
				return "", nil, nil, RefreshNone, err
			}
			differ, err := c.repo.ChangedFiles(c.last, commit)
			if err != nil {
				return "", nil, nil, RefreshNone, err
			}
			if overlap(sibling, mine, differ) {
				return "", nil, nil, RefreshConflict, nil
			}
		}
		moved, _, conflicted, err := c.repo.Rebase(base, commit, c.last)
		switch {
		case errors.Is(err, workspace.ErrGitTooOld):
			return "", nil, nil, RefreshNone, nil // keep the base; the next apply evens it out
		case err != nil:
			return "", nil, nil, RefreshNone, err
		case conflicted:
			return "", nil, nil, RefreshConflict, nil
		}
		pending, err := c.repo.ChangedFiles(commit, moved)
		if err != nil {
			return "", nil, nil, RefreshNone, err
		}
		for _, p := range pending {
			if protected(p) != "" {
				return "", nil, nil, RefreshNone, nil
			}
		}
		last = moved
	}
	changes, err := c.repo.Changes(c.last, last)
	if err != nil {
		return "", nil, nil, RefreshNone, err
	}
	for _, ch := range changes {
		if wsPathBad(c.Workspace, ch.Path) {
			return "", nil, nil, RefreshConflict, nil
		}
		w, u := wsWrite{path: ch.Path, mode: ch.NewMode}, wsWrite{path: ch.Path, mode: ch.OldMode}
		if ch.NewMode != "" {
			if w.data, err = c.repo.Blob(ch.NewOID); err != nil {
				return "", nil, nil, RefreshNone, err
			}
		}
		if ch.OldMode != "" {
			if u.data, err = c.repo.Blob(ch.OldOID); err != nil {
				return "", nil, nil, RefreshNone, err
			}
		}
		write, undo = append(write, w), append(undo, u)
	}
	return last, write, undo, RefreshDone, nil
}

// overlap reports whether a path is in each of the lists.
func overlap(a, b, c []string) bool {
	in := func(l []string, p string) bool { return slices.Contains(l, p) }
	for _, p := range a {
		if in(b, p) && in(c, p) {
			return true
		}
	}
	return false
}

// wsWrite is one path rebaseWorkspace writes into the Workspace: its
// mode and bytes (a link's target), or no mode to delete it.
type wsWrite struct {
	path, mode string
	data       []byte
}

// wsPathBad reports whether rel can't be written in the Workspace at ws:
// a directory on the way that is a link, or no directory, could lead the
// write elsewhere, and a directory at rel isn't Öge's to remove.
func wsPathBad(ws, rel string) bool {
	parts := strings.Split(rel, "/")
	dir := ws
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return false
		case err != nil || !fi.IsDir():
			return true
		}
	}
	fi, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(rel)))
	return err == nil && fi.IsDir()
}

// writeWorkspace makes the writes in the Workspace at ws. When one fails,
// the paths it reached are put back as undo (one for each write) says.
func writeWorkspace(ws string, writes, undo []wsWrite) error {
	for i, w := range writes {
		if err := writeOne(ws, w); err != nil {
			unwriteWorkspace(ws, undo[:i+1])
			return err
		}
	}
	return nil
}

// unwriteWorkspace puts back what writeWorkspace wrote, as undo says.
func unwriteWorkspace(ws string, undo []wsWrite) {
	for i := len(undo) - 1; i >= 0; i-- {
		_ = writeOne(ws, undo[i])
	}
}

// writeOne makes one write in the Workspace at ws.
func writeOne(ws string, w wsWrite) error {
	full := filepath.Join(ws, filepath.FromSlash(w.path))
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if w.mode == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	switch w.mode {
	case "120000":
		return os.Symlink(filepath.FromSlash(string(w.data)), full)
	case "100755":
		return os.WriteFile(full, w.data, 0o755)
	default:
		return os.WriteFile(full, w.data, 0o644)
	}
}

func (c *Chat) setRefreshDue(v bool) {
	c.supersedeMu.Lock()
	c.refreshDue = v
	c.supersedeMu.Unlock()
}

// retryRefresh runs a retake a cut in flight deferred, once the cuts have
// settled. An outside change found now is not shown (it was not found at
// the apply): the Chat keeps its base, and the next apply says it.
func (c *Chat) retryRefresh() {
	c.supersedeMu.Lock()
	due := c.refreshDue
	c.supersedeMu.Unlock()
	if due {
		_, _ = c.RefreshSnapshot()
	}
}

// snapshot is the Chat's current Snapshot: a retake replaces it.
func (c *Chat) snapshot() string {
	c.scopeMu.Lock()
	defer c.scopeMu.Unlock()
	return c.Snapshot
}
