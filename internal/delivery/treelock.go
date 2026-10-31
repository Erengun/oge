package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"time"
)

// treeLockWait is how long an apply waits for another apply to the same
// working tree to finish before it is refused.
var treeLockWait = 10 * time.Second

// betweenPlanAndWrite, when set, runs in ApplyOnto after the plan is made
// and before anything is recorded or written. Tests use it to interleave
// two applies.
var betweenPlanAndWrite func(r *Run)

// treeContended, when set, runs in lockTree the first time it finds the
// working tree held by another apply. Tests use it to see the second
// apply wait.
var treeContended func(r *Run)

// treeBusy is the refusal when another apply holds the working tree. The
// OS drops the lock when its holder dies, so waiting is the only step.
const treeBusy = "another Öge apply to this repository is still running; apply again once it finishes"

// treeLockPath is the lock file for the working tree at target: in the
// private state dir, never in the user's repository. r.Dir is
// private/runs/<id>, so every Run of the repository finds the same file.
func treeLockPath(r *Run, target string) string {
	sum := sha256.Sum256([]byte(canonical(target)))
	return filepath.Join(filepath.Dir(filepath.Dir(r.Dir)), "locks", "tree-"+hex.EncodeToString(sum[:16])+".lock")
}
