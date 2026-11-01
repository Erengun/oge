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
// commit last applied (Delivered's rev), but for paths a sibling Chat's
// apply wrote (NoteDelivered), each holding exactly the bytes it wrote.
// The Workspace holds no edit of the user's, and Öge never merges one in,
// so any other tree is an outside change: RefreshOutside, and nothing
// moves. Taking it as the base would turn the user's own edit into a diff
// the Implementer appears to have made.
//
// The Workspace is carried onto the new base: what the Chat edited since
// its apply stays, and a sibling's paths are written in, so neither reads
// as the Implementer's on the next turn. A sibling's path this Chat has
// edited differently is RefreshConflict, and nothing is written or moved.
// Neither is a pending edit to a path the new base protects (it would
// read as Tamper): the base is kept, and the next apply evens it out. A
// turn in flight may be writing the Workspace, so the retake waits for it.
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
	want, err := c.repo.TreeOf(rev)
	if err != nil {
		return RefreshNone, err
	}
	c.scopeMu.Lock()
	cur, snap := c.Snapshot, c.info
	c.scopeMu.Unlock()
	curTree, err := c.repo.TreeOf(cur)
	if err != nil {
		return RefreshNone, err
	}
	var sibling []string // the paths a sibling Chat's apply wrote
	switch {
	case have == curTree:
		c.setRefreshDue(false)
		return RefreshNone, nil
	case have != want:
		if sibling, err = c.siblingPaths(rev, commit); err != nil {
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
	// The Workspace must be what was delivered too: a turn after the cut,
	// or a hold-back, leaves it different, and edits to a file the new
	// base protects would read as Tamper. The next apply evens it out.
	// Another retake may have won while this one read: then the base is
	// already this tree, and nothing is logged twice.
	if now, err := c.repo.TreeOf(c.Snapshot); err != nil {
		return RefreshNone, err
	} else if now == have {
		return RefreshNone, nil
	}
	protected := implementerScope(m, c.frozen)
	last, write, r, err := c.rebaseWorkspace(rev, commit, sibling, protected)
	if err != nil || r != RefreshDone {
		return r, err
	}
	rec := map[string]any{"commit": commit, "prev": cur, "applied": rev, "run": filepath.Base(runDir),
		"head": info.Head, "branch": info.Branch, "modified": info.Modified, "untracked": info.Untracked,
		"oracle_version": m.Version, "manifest": mBlob, "tests": len(m.Tests), "prev_head": snap.Head}
	if len(sibling) > 0 {
		rec["sibling"], rec["last"] = sibling, last
	}
	if len(write) > 0 {
		// The Workspace is written with no turn in flight, and none
		// starts until it is.
		c.mu.Lock()
		if c.cur != nil {
			c.mu.Unlock()
			c.setRefreshDue(true)
			return RefreshDeferred, nil
		}
		// Written ahead: the record says what the new base is before it is.
		err := c.log(RecSnapshotRefreshed, rec)
		if err == nil {
			err = writeWorkspace(c.Workspace, write)
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
	c.Snapshot, c.info, c.v0, c.protected, c.last = commit, info, mBlob, protected, last
	c.prepare(m, commit)
	return RefreshDone, nil
}

// siblingPaths are the paths the tree of commit, the user's repository
// now, differs from rev's on, when each is one a sibling Chat's apply
// wrote and holds exactly what it wrote. nil: some other change is in it.
func (c *Chat) siblingPaths(rev, commit string) ([]string, error) {
	changes, err := c.repo.Changes(rev, commit)
	if err != nil {
		return nil, err
	}
	c.supersedeMu.Lock()
	defer c.supersedeMu.Unlock()
	var paths []string
	for _, ch := range changes {
		w, ok := c.siblings[ch.Path]
		if !ok || w.Mode != ch.NewMode || w.OID != ch.NewOID {
			return nil, nil
		}
		paths = append(paths, ch.Path)
	}
	return paths, nil
}

// rebaseWorkspace carries the Workspace, last committed as c.last, from
// rev, the commit last applied, onto commit, the new base, with scopeMu
// held. last is the Workspace's commit on the new base, and write what
// the Workspace needs written to hold it: the sibling's paths. r is
// RefreshDone when the retake can go on.
func (c *Chat) rebaseWorkspace(rev, commit string, sibling []string, protected func(string) string) (last string, write []wsWrite, r Refresh, err error) {
	lastTree, err := c.repo.TreeOf(c.last)
	if err != nil {
		return "", nil, RefreshNone, err
	}
	want, err := c.repo.TreeOf(rev)
	if err != nil {
		return "", nil, RefreshNone, err
	}
	last = c.last
	switch {
	case lastTree == want && len(sibling) == 0:
		return last, nil, RefreshDone, nil
	case lastTree == want:
		last = commit // nothing pending: the Workspace is the new base
	default:
		if len(sibling) > 0 {
			// A sibling's path this Chat edited too, and to other bytes, is
			// never merged.
			mine, err := c.repo.ChangedFiles(rev, c.last)
			if err != nil {
				return "", nil, RefreshNone, err
			}
			differ, err := c.repo.ChangedFiles(c.last, commit)
			if err != nil {
				return "", nil, RefreshNone, err
			}
			if overlap(sibling, mine, differ) {
				return "", nil, RefreshConflict, nil
			}
		}
		moved, _, conflicted, err := c.repo.Rebase(rev, commit, c.last)
		switch {
		case errors.Is(err, workspace.ErrGitTooOld):
			return "", nil, RefreshNone, nil // keep the base; the next apply evens it out
		case err != nil:
			return "", nil, RefreshNone, err
		case conflicted:
			return "", nil, RefreshConflict, nil
		}
		pending, err := c.repo.ChangedFiles(commit, moved)
		if err != nil {
			return "", nil, RefreshNone, err
		}
		for _, p := range pending {
			if protected(p) != "" {
				return "", nil, RefreshNone, nil
			}
		}
		last = moved
	}
	changes, err := c.repo.Changes(c.last, last)
	if err != nil {
		return "", nil, RefreshNone, err
	}
	for _, ch := range changes {
		if wsPathBad(c.Workspace, ch.Path) {
			return "", nil, RefreshConflict, nil
		}
		w := wsWrite{path: ch.Path, mode: ch.NewMode}
		if ch.NewMode != "" {
			if w.data, err = c.repo.Blob(ch.NewOID); err != nil {
				return "", nil, RefreshNone, err
			}
		}
		write = append(write, w)
	}
	return last, write, RefreshDone, nil
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

// writeWorkspace makes the writes in the Workspace at ws.
func writeWorkspace(ws string, writes []wsWrite) error {
	for _, w := range writes {
		full := filepath.Join(ws, filepath.FromSlash(w.path))
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if w.mode == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		var err error
		switch w.mode {
		case "120000":
			err = os.Symlink(filepath.FromSlash(string(w.data)), full)
		case "100755":
			err = os.WriteFile(full, w.data, 0o755)
		default:
			err = os.WriteFile(full, w.data, 0o644)
		}
		if err != nil {
			return err
		}
	}
	return nil
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
