// Package delivery takes a Run's final Candidate into the user's own
// repository (#54): its diff, applied to the working tree, or a branch.
// It reads only the immutable Candidate data in the Run's private state
// (ADR-0010), so it works after every Workspace is gone, and it never
// commits to, checks out, merges or pushes the user's branch.
package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// Ledger records delivery writes. RecDelivery is written once the plan is
// known to land and before anything in the user's repository changes;
// RecDeliveryFailed follows it if the writes then fail (ADR-0012).
const (
	RecDelivery       = "Delivery"
	RecDeliveryFailed = "DeliveryFailed"
)

// Run is what delivery needs of a finished (or stopped) Run, rebuilt from
// its Ledger.
type Run struct {
	ID, Dir string
	Source  string // the user's repository the Snapshot came from
	Title   string // the Task's title
	// Snapshot is the Snapshot commit in the Run repository; Head and
	// Branch are the user's HEAD and branch it was taken on.
	Snapshot, Head, Branch string
	Dirty                  bool // the Snapshot held uncommitted or untracked work
	// Candidate is the Run's final Candidate: the one its end names, or
	// the latest one while it hasn't ended.
	Candidate string
	Oracle    int
	// Outcome is how the Run ended; "" while it hasn't (running, parked,
	// or stopped without RunEnded).
	Outcome run.Outcome
	Parked  bool
	// Why says why the Candidate wasn't Accepted: the last Verdict and the
	// decision that ended the Run.
	Why []string
	// Deliveries counts the Deliveries already recorded.
	Deliveries int
}

func (r *Run) repo() *workspace.RunRepo {
	return &workspace.RunRepo{Dir: filepath.Join(r.Dir, "repo.git")}
}

// Short is a revision's first seven characters.
func Short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// Load rebuilds the Run in dir from its Ledger.
func Load(dir string) (*Run, error) {
	recs, err := ledger.Replay(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the Run's Ledger: %w", err)
	}
	r := &Run{Dir: dir, ID: filepath.Base(dir)}
	var verdict struct {
		Check     int
		Verdict   string
		Candidate string
	}
	var decided struct {
		Pins struct {
			Gate string
		}
		Choice, Reason string
	}
	for _, rec := range recs {
		switch rec.Type {
		case run.RecRunStarted:
			var d struct{ Run, Source, Task string }
			if err := json.Unmarshal(rec.Data, &d); err != nil {
				return nil, err
			}
			if d.Run != "" {
				r.ID = d.Run
			}
			r.Source = d.Source
			if blobs, err := ledger.OpenBlobs(dir); err == nil {
				if b, err := blobs.Get(d.Task); err == nil {
					r.Title = task.Parse(string(b)).Title
				}
			}
		case run.RecSnapshotTaken:
			var d struct {
				Commit, Head, Branch string
				Modified, Untracked  int
			}
			if err := json.Unmarshal(rec.Data, &d); err != nil {
				return nil, err
			}
			r.Snapshot, r.Head, r.Branch = d.Commit, d.Head, d.Branch
			r.Dirty = d.Modified > 0 || d.Untracked > 0
		case run.RecOracleVersion:
			var d struct{ Version int }
			_ = json.Unmarshal(rec.Data, &d)
			r.Oracle = d.Version
		case run.RecAttemptEnded:
			var d struct{ Candidate string }
			_ = json.Unmarshal(rec.Data, &d)
			if d.Candidate != "" {
				r.Candidate = d.Candidate
			}
		case run.RecVerdict:
			_ = json.Unmarshal(rec.Data, &verdict)
			if verdict.Candidate != "" {
				r.Candidate = verdict.Candidate
			}
		case run.RecGateDecided:
			_ = json.Unmarshal(rec.Data, &decided)
		case run.RecRunParked:
			r.Parked = true
		case run.RecRunEnded:
			var d struct {
				Outcome   run.Outcome
				Candidate string
			}
			if err := json.Unmarshal(rec.Data, &d); err != nil {
				return nil, err
			}
			r.Outcome, r.Parked = d.Outcome, false
			if d.Candidate != "" {
				r.Candidate = d.Candidate
			}
		case RecDelivery:
			r.Deliveries++
		}
	}
	if verdict.Verdict != "" {
		r.Why = append(r.Why, fmt.Sprintf("Öge's Check %s on Candidate %s (Verdict #%d, Oracle v%d)",
			map[string]string{"pass": "passed", "fail": "failed"}[verdict.Verdict], Short(verdict.Candidate), verdict.Check, r.Oracle))
	}
	if decided.Choice != "" {
		gate := strings.ReplaceAll(strings.TrimPrefix(decided.Pins.Gate, "gate."), "_", "-")
		line := fmt.Sprintf("a human chose %q at the %s Gate", decided.Choice, gate)
		if decided.Reason != "" {
			line += " · reason: " + decided.Reason
		}
		r.Why = append(r.Why, line)
	}
	return r, nil
}

// RefusedError is a delivery Öge won't make, and what to do instead. The
// CLI maps it to exit 2.
type RefusedError struct{ Why string }

func (e *RefusedError) Error() string { return e.Why }

func refuse(format string, a ...any) error { return &RefusedError{fmt.Sprintf(format, a...)} }

// IsRefused reports whether err is a refusal rather than a failure.
func IsRefused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}

// Find returns the Run named id (a full Run id or a unique prefix), or
// with id "" the newest Run whose source is repoRoot (ADR-0014).
func Find(private, repoRoot, id string) (*Run, error) {
	runs := filepath.Join(private, "runs")
	entries, err := os.ReadDir(runs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && (id == "" || strings.HasPrefix(e.Name(), id)) {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // newest first
	if id != "" {
		for _, n := range names {
			if n == id {
				return Load(filepath.Join(runs, n))
			}
		}
		switch len(names) {
		case 0:
			return nil, refuse("no Run %s", id)
		case 1:
			return Load(filepath.Join(runs, names[0]))
		}
		return nil, refuse("%s names %d Runs; give more of the id", id, len(names))
	}
	canon := canonical(repoRoot)
	for _, n := range names {
		r, err := Load(filepath.Join(runs, n))
		if err == nil {
			if canonical(r.Source) == canon {
				return r, nil
			}
			continue
		}
		// An unreadable Run that may be this repository's newest is
		// refused, never skipped for an older one.
		if src, ok := peekSource(filepath.Join(runs, n)); !ok || canonical(src) == canon {
			return nil, refuse("the latest Run, %s, can't be read (%v); give a Run id to deliver another", n, err)
		}
	}
	return nil, refuse("no Run of this repository yet; run oge \"<task>\" first")
}

func canonical(p string) string {
	if c, err := filepath.EvalSymlinks(p); err == nil {
		return c
	}
	return filepath.Clean(p)
}

// peekSource reads the source repository from a Ledger's first record
// alone, for a Ledger that doesn't replay in full.
func peekSource(dir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, ledger.LedgerFile))
	if err != nil {
		return "", false
	}
	first, _, _ := strings.Cut(string(b), "\n")
	var rec struct {
		Type string
		Data struct{ Source string }
	}
	if json.Unmarshal([]byte(first), &rec) != nil || rec.Type != run.RecRunStarted || rec.Data.Source == "" {
		return "", false
	}
	return rec.Data.Source, true
}
