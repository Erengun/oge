package run

// Monkey tests for Resume: ledger surgery, forged records, torn tails,
// stale liveness, and concurrent resumes. Every test is independent and
// bounded; none run in parallel (some signal processes / race on dirs).

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/ledger"
)

// monkeyCleanScript edits a declared output file and settles: a Run that
// ends Accepted with no Gate at all (check "true" passes, nothing is
// Ambiguous).
const monkeyCleanScript = `printf 'func Sub(a, b int) int { return a - b }\n' >> add.go
echo "exit: done"
`

// monkeyResume calls Resume under a bounded context.
func monkeyResume(t *testing.T, f *resumeFixture, gates gate.Port, dir string) (*Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return Resume(ctx, f.params(gates), dir)
}

// lastOpenedPins decodes the Pins of the last GateOpened record in dir's
// Ledger — what the parked Gate actually showed.
func lastOpenedPins(t *testing.T, dir string) gate.Pins {
	t.Helper()
	recs, err := ledger.Replay(dir)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var pins gate.Pins
	found := false
	for _, r := range recs {
		if r.Type != RecGateOpened {
			continue
		}
		var d struct {
			Pins gate.Pins `json:"pins"`
		}
		if err := json.Unmarshal(r.Data, &d); err != nil {
			t.Fatal(err)
		}
		pins, found = d.Pins, true
	}
	if !found {
		t.Fatal("no GateOpened record")
	}
	return pins
}

// monkeyAppend adds a record through the Ledger's own chaining.
func monkeyAppend(t *testing.T, dir, typ string, data any) {
	t.Helper()
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatalf("ledger open: %v", err)
	}
	if err := l.Append(typ, data); err != nil {
		t.Fatalf("append %s: %v", typ, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// monkeyTear appends raw bytes (a dead write) to the Ledger with no
// chaining and no trailing newline.
func monkeyTear(t *testing.T, dir, raw string) {
	t.Helper()
	fh, err := os.OpenFile(filepath.Join(dir, ledger.LedgerFile), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(raw); err != nil {
		t.Fatal(err)
	}
	fh.Close()
}

// monkeyTruncateThrough rewrites the Ledger keeping the prefix that ends
// with the LAST record of type recType: a perfectly valid chain, as if
// the Run was killed right after that write.
func monkeyTruncateThrough(t *testing.T, dir, recType string) {
	t.Helper()
	lp := filepath.Join(dir, ledger.LedgerFile)
	b, err := os.ReadFile(lp)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(b), "\n")
	last := -1
	for i, line := range lines {
		var r struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &r) == nil && r.Type == recType {
			last = i
		}
	}
	if last < 0 {
		t.Fatalf("no %s record to truncate through", recType)
	}
	if err := os.WriteFile(lp, []byte(strings.Join(lines[:last+1], "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 1. Resumed twice: a second Resumed record must not confuse the tail —
// the second resume re-opens the parked Gate and parks again, and a third
// resume can still decide it.
func TestMonkeyResumeTwice(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	for i := 1; i <= 2; i++ {
		resN, err := monkeyResume(t, f, nil, res.Dir)
		if err != nil {
			t.Fatalf("resume %d: %v", i, err)
		}
		if resN.Outcome != Parked {
			t.Fatalf("resume %d: want Parked, got %s (%v)", i, resN.Outcome, resN.Why)
		}
		types := resumeTypes(t, res.Dir)
		if countTypes(types, RecResumed) != i || countTypes(types, RecRunParked) != 1+i ||
			countTypes(types, RecGateOpened) != 1+i {
			t.Fatalf("after resume %d, records: %v", i, types)
		}
	}
	// And a third resume still decides the re-opened Gate.
	promote := decider(func(r gate.Request) gate.Decision {
		return gate.Decision{Choice: "promote", Files: r.Files}
	})
	res3, err := monkeyResume(t, f, promote, res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Outcome != Accepted {
		t.Fatalf("third resume: want Accepted, got %s (%v)", res3.Outcome, res3.Why)
	}
	if got := countTypes(resumeTypes(t, res.Dir), RecResumed); got != 3 {
		t.Fatalf("want 3 Resumed records, got %d", got)
	}
}

// 2a. A valid appended record followed by a torn partial write refuses.
func TestMonkeyTornTailAfterValidRecord(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	monkeyAppend(t, res.Dir, RecObservation, map[string]any{"kind": "monkey", "note": "valid record before the tear"})
	monkeyTear(t, res.Dir, `{"format":1,"seq":999,"pre`)
	if _, err := monkeyResume(t, f, nil, res.Dir); !IsRefused(err) {
		t.Fatalf("valid record + torn tail: want refusal, got %v", err)
	}
}

// 2b. The last GOOD record with its final newline stripped: valid JSON,
// unterminated line. Replay accepts it (it verifies); Open treats it as
// torn — so the refusal comes from ledger.Open, not replay.
func TestMonkeyMissingFinalNewline(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	lp := filepath.Join(res.Dir, ledger.LedgerFile)
	b, err := os.ReadFile(lp)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Fatal("fixture Ledger doesn't end in a newline")
	}
	if err := os.WriteFile(lp, b[:len(b)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	res2, err := monkeyResume(t, f, nil, res.Dir)
	if !IsRefused(err) {
		t.Fatalf("unterminated final record: want refusal, got res=%v err=%v", res2, err)
	}
	t.Logf("a complete record with no trailing newline refuses as torn: %v", err)
}

// 3a-i. A GateDecided pinned to a FABRICATED Attempt id: the pins the
// decision claims it was shown don't belong to any Attempt this Run ran.
// Resume should refuse what it can't prove.
func TestMonkeyForgedAttemptPins(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	pins := lastOpenedPins(t, res.Dir)
	pins.Attempt = "attempt-forged-999"
	monkeyAppend(t, res.Dir, RecGateDecided, map[string]any{
		"pins": pins, "actor": "monkey", "choice": "promote", "files": []string{"notes.txt"}})

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if IsRefused(err) {
		t.Logf("forged Attempt pins refused, as they should be: %v", err)
		return
	}
	if err != nil {
		t.Fatalf("forged Attempt pins produced a hard error, not a refusal: %v", err)
	}
	types := resumeTypes(t, res.Dir)
	t.Errorf("MONKEY: a GateDecided pinned to a nonexistent Attempt was applied (outcome %s); records: %v",
		res2.Outcome, types)
}

// 3a-ii. Same surgery but with fabricated Verdicts: this field IS pinned
// to replayed state, so the refusal should fire.
func TestMonkeyForgedVerdictPins(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	pins := lastOpenedPins(t, res.Dir)
	pins.Verdicts = []int{77}
	monkeyAppend(t, res.Dir, RecGateDecided, map[string]any{
		"pins": pins, "actor": "monkey", "choice": "promote", "files": []string{"notes.txt"}})

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if !IsRefused(err) {
		t.Fatalf("forged Verdict pins: want refusal, got res=%v err=%v", res2, err)
	}
}

// 3b-i. Real pins but a choice the Gate doesn't offer.
func TestMonkeyGateDecidedBogusChoice(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	pins := lastOpenedPins(t, res.Dir)
	monkeyAppend(t, res.Dir, RecGateDecided, map[string]any{
		"pins": pins, "actor": "monkey", "choice": "explode"})

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if !IsRefused(err) {
		t.Fatalf("a decision the Gate never offered: want refusal, got res=%v err=%v", res2, err)
	}
}

// 3b-ii. Real pins with "quit": the recorded decision ends the Run
// Cancelled with no human present — durable-decision semantics.
func TestMonkeyGateDecidedQuitApplies(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	pins := lastOpenedPins(t, res.Dir)
	monkeyAppend(t, res.Dir, RecGateDecided, map[string]any{
		"pins": pins, "actor": "monkey", "choice": "quit"})

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil {
		t.Fatalf("a recorded quit applies: %v", err)
	}
	if res2.Outcome != Cancelled {
		t.Fatalf("recorded quit: want Cancelled, got %s (%v)", res2.Outcome, res2.Why)
	}
	if got := countTypes(resumeTypes(t, res.Dir), RecGateOpened); got != 1 {
		t.Errorf("the recorded decision should apply without re-opening the Gate: %v", resumeTypes(t, res.Dir))
	}
}

// 3c. A GateAbandoned appended after the park: the Gate re-opens as the
// tail and the unattended Run parks again.
func TestMonkeyGateAbandonedReopens(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	pins := lastOpenedPins(t, res.Dir)
	monkeyAppend(t, res.Dir, RecGateAbandoned, map[string]any{"pins": pins})

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != Parked {
		t.Fatalf("abandoned-on-parked resume: want Parked, got %s (%v)", res2.Outcome, res2.Why)
	}
	types := resumeTypes(t, res.Dir)
	if countTypes(types, RecGateOpened) != 2 || countTypes(types, RecRunParked) != 2 ||
		countTypes(types, RecResumed) != 1 {
		t.Errorf("records after abandoned re-park: %v", types)
	}
}

// 3d. A recorded promote with NO files field: at a live Gate an empty
// selection means "all shown files", but a recorded decision's files are
// literal — nil files promotes nothing and the review re-opens.
func TestMonkeyPromoteNilFiles(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	pins := lastOpenedPins(t, res.Dir)
	monkeyAppend(t, res.Dir, RecGateDecided, map[string]any{
		"pins": pins, "actor": "monkey", "choice": "promote"}) // no "files" key at all

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil {
		t.Fatalf("promote with nil files: %v", err)
	}
	types := resumeTypes(t, res.Dir)
	if res2.Outcome != Parked {
		t.Fatalf("promote-nothing: want re-parked, got %s (%v); records %v", res2.Outcome, res2.Why, types)
	}
	if countTypes(types, RecAmbiguousResolved) != 1 || countTypes(types, RecGateOpened) != 2 {
		t.Errorf("promote-nothing records: %v", types)
	}
	t.Logf("a recorded promote with nil files promoted nothing and re-opened the review (not promote-all)")
}

// 4a. The interrupt lands after CheckEnded but before the walk routes:
// with nobody at the Gate the Run still parks (errParked wins over the
// cancelled context), and resume continues coherently.
func TestMonkeyCancelAfterCheckEnded(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAt(ctx, f, RecCheckEnded, cancel)
	res, err := Start(ctx, f.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cancel after CheckEnded left the Run %s (%v)", res.Outcome, res.Why)
	if res.Outcome != Parked && res.Outcome != InfrastructureStop {
		t.Fatalf("unexpected stop: %s (%v)", res.Outcome, res.Why)
	}

	promote := decider(func(r gate.Request) gate.Decision {
		return gate.Decision{Choice: "promote", Files: r.Files}
	})
	res2, err := monkeyResume(t, f, promote, res.Dir)
	if err != nil {
		t.Fatalf("resume after %s: %v", res.Outcome, err)
	}
	if res2.Outcome != Accepted {
		t.Fatalf("resumed Run: want Accepted, got %s (%v)", res2.Outcome, res2.Why)
	}
}

// 4b. Interrupt right after the Verdict record: resume must not write a
// second Verdict for the same Check while the Run sits parked.
func TestMonkeyCancelAfterVerdict(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAt(ctx, f, RecVerdict, cancel)
	res, err := Start(ctx, f.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cancel after Verdict left the Run %s (%v)", res.Outcome, res.Why)

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != Parked {
		t.Fatalf("want re-parked, got %s (%v)", res2.Outcome, res2.Why)
	}
	if got := countTypes(resumeTypes(t, res.Dir), RecVerdict); got != 1 {
		t.Errorf("Verdict records after resume: got %d, want exactly 1: %v", got, resumeTypes(t, res.Dir))
	}
}

// 4c-i. Kill the Run between CheckEnded and Verdict (truncated chain =
// valid prefix): resume writes the missing Verdict once, then routes.
func TestMonkeyTruncatedVerdictTail(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), monkeyCleanScript)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := Start(ctx, f.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Accepted {
		t.Fatalf("clean Run: want Accepted, got %s (%v)", res.Outcome, res.Why)
	}
	monkeyTruncateThrough(t, res.Dir, RecCheckEnded)

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil {
		t.Fatalf("verdict-tail resume: %v", err)
	}
	types := resumeTypes(t, res.Dir)
	if res2.Outcome != Accepted {
		t.Fatalf("verdict-tail resume: want Accepted, got %s (%v)", res2.Outcome, res2.Why)
	}
	if got := countTypes(types, RecVerdict); got != 1 {
		t.Errorf("Verdict written %d times; the resume should write exactly the missing one: %v", got, types)
	}
	if countTypes(types, RecCheckStarted) != 1 || countTypes(types, RecRunEnded) != 1 {
		t.Errorf("verdict-tail records: %v", types)
	}
}

// 4c-ii. Kill the Run right after Verdict (truncated chain): resume
// routes the recorded Verdict without re-writing it.
func TestMonkeyTruncatedRouteTail(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), monkeyCleanScript)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := Start(ctx, f.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Accepted {
		t.Fatalf("clean Run: want Accepted, got %s (%v)", res.Outcome, res.Why)
	}
	monkeyTruncateThrough(t, res.Dir, RecVerdict)

	res2, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil {
		t.Fatalf("route-tail resume: %v", err)
	}
	types := resumeTypes(t, res.Dir)
	if res2.Outcome != Accepted {
		t.Fatalf("route-tail resume: want Accepted, got %s (%v)", res2.Outcome, res2.Why)
	}
	if got := countTypes(types, RecVerdict); got != 1 {
		t.Errorf("route-tail wrote a second Verdict: %v", types)
	}
	if countTypes(types, RecCheckStarted) != 1 || countTypes(types, RecRunEnded) != 1 {
		t.Errorf("route-tail records: %v", types)
	}
}

// 5. Runs that are done are done; empty and barely-started dirs refuse
// cleanly, and a forged RunEnded closes a parked Run.
func TestMonkeyEndedRunsRefuse(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	// Reject ends it; resume after that refuses.
	reject := decider(func(r gate.Request) gate.Decision {
		return gate.Decision{Choice: "reject", Reason: "monkey says no"}
	})
	res2, err := monkeyResume(t, f, reject, res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != Rejected {
		t.Fatalf("recorded reject: want Rejected, got %s (%v)", res2.Outcome, res2.Why)
	}
	if _, err := monkeyResume(t, f, nil, res.Dir); !IsRefused(err) {
		t.Errorf("resuming a Rejected Run: want refusal, got %v", err)
	}

	// An empty dir and a dir that doesn't exist.
	if _, err := monkeyResume(t, f, nil, t.TempDir()); err == nil {
		t.Error("an empty dir's resume wants an error")
	} else if !IsRefused(err) {
		t.Logf("empty dir resume error is not a refusal: %v", err)
	}
	if _, err := monkeyResume(t, f, nil, filepath.Join(t.TempDir(), "ghost")); err == nil {
		t.Error("a nonexistent dir's resume wants an error")
	} else {
		t.Logf("nonexistent dir: IsRefused=%v (%v)", IsRefused(err), err)
	}

	// A Ledger holding only RunStarted, with its blobs present: replay
	// reaches the end and finds nothing to resume — clean refusal.
	dir := t.TempDir()
	blobs, err := ledger.OpenBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	taskBlob, err := blobs.Put([]byte("fix Add"))
	if err != nil {
		t.Fatal(err)
	}
	frozenJSON, err := json.Marshal(f.frozen)
	if err != nil {
		t.Fatal(err)
	}
	frozenBlob, err := blobs.Put(frozenJSON)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(RecRunStarted, map[string]any{"run": "baby", "created": time.Now().UTC(),
		"source": f.user, "task": taskBlob, "frozen": frozenBlob}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if _, err := monkeyResume(t, f, nil, dir); !IsRefused(err) {
		t.Errorf("a RunStarted-only Ledger's resume: want refusal, got %v", err)
	}

	// Same one-liner but the blobs it names were never written.
	dir2 := t.TempDir()
	l2, err := ledger.Create(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Append(RecRunStarted, map[string]any{"run": "baby", "created": time.Now().UTC(),
		"source": f.user, "task": "sha256:" + strings.Repeat("0", 64), "frozen": "sha256:" + strings.Repeat("1", 64)}); err != nil {
		t.Fatal(err)
	}
	l2.Close()
	if _, err := monkeyResume(t, f, nil, dir2); err == nil {
		t.Error("a RunStarted naming missing blobs wants an error")
	} else {
		t.Logf("RunStarted with missing blobs: IsRefused=%v (%v)", IsRefused(err), err)
	}
}

// 5b. A forged RunEnded appended to a parked Ledger ends the Run for
// resume purposes — ledger-appending is trusted that much.
func TestMonkeyForgedRunEnded(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	monkeyAppend(t, res.Dir, RecRunEnded, map[string]any{"outcome": Accepted, "why": []string{"forged"}, "candidate": "x"})
	res2, err := monkeyResume(t, f, nil, res.Dir)
	if !IsRefused(err) {
		t.Fatalf("forged RunEnded: want refusal, got res=%v err=%v", res2, err)
	}
	t.Logf("a forged RunEnded closed the Run: %v", err)
}

// 6. Corrupt the blob store and the Run repository: resume must come
// back with an error, never a panic.
func TestMonkeyBlobAndRepoCorruption(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	// Find the frozen Pipeline's blob id in RunStarted and delete it.
	var frozenBlob string
	recs, err := ledger.Replay(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Type == RecRunStarted {
			var d struct {
				Frozen string `json:"frozen"`
			}
			if err := json.Unmarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
			frozenBlob = d.Frozen
		}
	}
	if frozenBlob == "" {
		t.Fatal("no frozen blob in RunStarted")
	}
	blobPath := filepath.Join(res.Dir, "blobs", "sha256", strings.TrimPrefix(frozenBlob, "sha256:"))
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	res2, err := monkeyResume(t, f, nil, res.Dir)
	if err == nil {
		t.Errorf("MONKEY: resume ran with the frozen Pipeline blob deleted (outcome %s)", res2.Outcome)
	} else {
		t.Logf("missing frozen blob: IsRefused=%v (%v)", IsRefused(err), err)
	}

	// A run whose repo.git is gone.
	res3 := parkedRun(t, f)
	if err := os.RemoveAll(filepath.Join(res3.Dir, "repo.git")); err != nil {
		t.Fatal(err)
	}
	res4, err := monkeyResume(t, f, nil, res3.Dir)
	if err == nil {
		t.Errorf("MONKEY: resume ran with repo.git deleted (outcome %s)", res4.Outcome)
	} else {
		t.Logf("missing repo.git: IsRefused=%v (%v)", IsRefused(err), err)
	}
}

// 7. live.pid games: stale or garbage contents must not block a resume;
// a live process id — ours or another Run's — must.
func TestMonkeyLivePid(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	// Garbage bytes: not a pid at all.
	if err := os.WriteFile(filepath.Join(res.Dir, livePID), []byte("not a pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	res2, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil {
		t.Fatalf("resume over garbage live.pid: %v", err)
	}
	if res2.Outcome != Parked {
		t.Fatalf("want Parked, got %s", res2.Outcome)
	}

	// A dead process's pid.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Skip("cannot spawn a probe process")
	}
	if err := os.WriteFile(filepath.Join(res.Dir, livePID), []byte(strconv.Itoa(dead.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	res3, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil {
		t.Fatalf("resume over a dead pid's live.pid: %v", err)
	}
	if res3.Outcome != Parked {
		t.Fatalf("want Parked, got %s", res3.Outcome)
	}

	// A live pid (this process): refuse.
	if err := os.WriteFile(filepath.Join(res.Dir, livePID), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := monkeyResume(t, f, nil, res.Dir); !IsRefused(err) {
		t.Errorf("live.pid naming a live process: want refusal, got %v", err)
	}
	os.Remove(filepath.Join(res.Dir, livePID))
}

// 7b. Resume run A while a DIFFERENT run is live: B's live.pid must not
// affect A, and resuming B itself must refuse.
func TestMonkeyResumeWhileAnotherRunLives(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	resA := parkedRun(t, f)

	f2 := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), "sleep 600\n")
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	done := make(chan struct{})
	var errB error
	go func() {
		defer close(done)
		_, errB = Start(ctxB, f2.params(nil))
	}()

	// Wait until B's run dir exists and its Attempt started, bounded.
	dirB := ""
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && dirB == "" {
		matches, _ := filepath.Glob(filepath.Join(f2.state.Private, "runs", "*"))
		for _, r := range matches {
			if st, err := os.Stat(r); err == nil && st.IsDir() {
				dirB = r
			}
		}
		if dirB == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if dirB == "" {
		cancelB()
		<-done
		t.Fatalf("run B never appeared: %v", errB)
	}
	for time.Now().Before(deadline) && countTypes(safeTypes(dirB), RecAttemptStarting) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if countTypes(safeTypes(dirB), RecAttemptStarting) == 0 {
		cancelB()
		<-done
		t.Fatalf("run B's Attempt never started: %v", errB)
	}

	// B is live: resuming ITS dir refuses; resuming parked A is fine.
	if _, err := monkeyResume(t, f2, nil, dirB); !IsRefused(err) {
		t.Errorf("resuming a live Run: want refusal, got %v", err)
	}
	res2, err := monkeyResume(t, f, nil, resA.Dir)
	if err != nil {
		t.Fatalf("resuming A while B lives: %v", err)
	}
	if res2.Outcome != Parked {
		t.Fatalf("want Parked, got %s", res2.Outcome)
	}
	cancelB()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("run B never stopped after cancel")
	}
}

// barrierGate blocks each Decide until two calls have arrived (or a
// bound elapses), so concurrent resumes interleave their Ledger writes.
type barrierGate struct {
	entered atomic.Int32
	release chan struct{}
	once    sync.Once
}

func (b *barrierGate) Decide(ctx context.Context, r gate.Request) (gate.Decision, error) {
	b.once.Do(func() { b.release = make(chan struct{}) })
	if b.entered.Add(1) >= 2 {
		close(b.release)
	}
	select {
	case <-b.release:
		return gate.Decision{Choice: "promote", Files: r.Files}, nil
	case <-ctx.Done():
		return gate.Decision{}, ctx.Err()
	case <-time.After(60 * time.Second):
		return gate.Decision{}, gate.ErrNoDecision
	}
}

// 8. Two resumes of the SAME parked Run at once: the flock lets one
// through and refuses the other — no interleaved appends, no deadlock.
func TestMonkeyConcurrentResume(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	barrier := &barrierGate{}
	type out struct {
		res *Result
		err error
	}
	outs := make(chan out, 2)
	for i := 0; i < 2; i++ {
		go func() {
			r, err := monkeyResume(t, f, barrier, res.Dir)
			outs <- out{r, err}
		}()
	}
	var results []out
	timeout := time.After(3 * time.Minute)
	for len(results) < 2 {
		select {
		case o := <-outs:
			results = append(results, o)
		case <-timeout:
			t.Fatal("concurrent resumes hung")
		}
	}
	// One wins the lock and re-parks on the barrier's no-decision; the
	// other is refused as still running. Anything else is a bug.
	refused, parked := 0, 0
	for i, o := range results {
		switch {
		case o.err != nil && IsRefused(o.err):
			refused++
		case o.err != nil:
			t.Errorf("resume %d error: %v", i, o.err)
		case o.res.Outcome == Parked:
			parked++
		default:
			t.Errorf("resume %d outcome: %s (%v)", i, o.res.Outcome, o.res.Why)
		}
	}
	if refused != 1 || parked != 1 {
		t.Errorf("concurrent resumes: %d refused, %d parked (want 1 of each)", refused, parked)
	}
	if _, err := ledger.Replay(res.Dir); err != nil {
		t.Errorf("MONKEY: two concurrent resumes corrupted the Ledger: %v", err)
		// And the damage is permanent: resume can never replay again.
		if _, rerr := monkeyResume(t, f, nil, res.Dir); rerr == nil {
			t.Error("MONKEY: a Run with a corrupted Ledger still resumed")
		} else {
			t.Logf("post-corruption resume: IsRefused=%v (%v)", IsRefused(rerr), rerr)
		}
	}
}
