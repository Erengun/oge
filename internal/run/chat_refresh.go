package run

import (
	"errors"
	"fmt"
	"path/filepath"

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
)

// OutsideNote is the one line shown when the repository changed outside
// Öge and can't be taken as the new base.
const OutsideNote = "repo changed outside Öge; not merged"

// RefreshSnapshot retakes the Chat's Snapshot after an apply (D10), but
// only when the user's repository is exactly what the Chat delivered: its
// Snapshot tree equals the tree of the commit last applied (Delivered's
// rev). The Workspace holds no edit of the user's, and Öge never merges
// one in, so any other tree is an outside change: RefreshOutside, and
// nothing moves. Taking it as the base would turn the user's own edit into
// a diff the Implementer appears to have made.
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
	switch {
	case have == curTree:
		c.setRefreshDue(false)
		return RefreshNone, nil
	case have != want:
		c.setRefreshDue(false)
		return RefreshOutside, nil
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
	lastTree, err := c.repo.TreeOf(c.last)
	if err != nil {
		return RefreshNone, err
	}
	if lastTree != want {
		return RefreshNone, nil
	}
	// Written ahead: the record says what the new base is before it is.
	if err := c.log(RecSnapshotRefreshed, map[string]any{"commit": commit, "prev": cur, "applied": rev, "run": filepath.Base(runDir),
		"head": info.Head, "branch": info.Branch, "modified": info.Modified, "untracked": info.Untracked,
		"oracle_version": m.Version, "manifest": mBlob, "tests": len(m.Tests), "prev_head": snap.Head}); err != nil {
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
	c.Snapshot, c.info, c.v0, c.protected = commit, info, mBlob, implementerScope(m, c.frozen)
	c.prepare(m, commit)
	return RefreshDone, nil
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
