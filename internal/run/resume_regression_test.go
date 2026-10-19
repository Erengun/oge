package run

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/task"
)

func TestResumeNoDecisionPreservesReviewPromotion(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), "touch first.txt second.txt\necho 'exit: done'\n")
	res := parkedRun(t, f)
	calls := 0
	port := reviewThenEOF{decide: func(r gate.Request) (gate.Decision, error) {
		calls++
		if calls == 1 {
			return gate.Decision{Choice: "promote", Files: r.Files[:1]}, nil
		}
		return gate.Decision{}, gate.ErrNoDecision
	}}
	resumed, err := monkeyResume(t, f, port, res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Outcome != Parked || calls != 2 {
		t.Fatalf("want a partial review re-parked on EOF, got %s after %d calls", resumed.Outcome, calls)
	}
	rt, err := replay(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rt.w.reviewPromoted {
		t.Fatal("EOF lost the current review's promotion; remaining drops would skip QA")
	}
	resumed, err = monkeyResume(t, f, decider(func(r gate.Request) gate.Decision {
		return gate.Decision{Choice: "drop", Files: r.Files}
	}), res.Dir)
	if err != nil || resumed.Outcome != Accepted {
		t.Fatalf("continue partial review: result=%+v error=%v", resumed, err)
	}
}

type reviewThenEOF struct {
	decide func(gate.Request) (gate.Decision, error)
}

func (p reviewThenEOF) Decide(_ context.Context, r gate.Request) (gate.Decision, error) {
	return p.decide(r)
}

func TestResumeSetupRunsAfterResumedRecord(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "resuming")
	frozen := resumeFrozen(t, "go test -json ./...")
	f := newResumeFixture(t, frozen, monkeyCleanScript)
	// On the fresh Run setup succeeds. On resume, setup checks that the
	// written-ahead re-entry record already exists before any command runs.
	frozen.Setup.Run = "if test -f '" + marker + "'; then grep -q '\"type\":\"Resumed\"' '" + f.state.Private + "'/runs/*/ledger.jsonl; fi"
	res := completedRunForResume(t, f)
	monkeyTruncateThrough(t, res.Dir, RecAttemptStarting)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	resumed, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil || resumed.Outcome != Accepted {
		t.Fatalf("setup ran before written-ahead resume: result=%+v error=%v", resumed, err)
	}
}

func completedRunForResume(t *testing.T, f *resumeFixture) *Result {
	t.Helper()
	res, err := Start(context.Background(), f.params(nil))
	if err != nil || res.Outcome != Accepted {
		t.Fatalf("initial Run: result=%+v error=%v", res, err)
	}
	return res
}

func TestResumeSetupFailureRefusesBeforeAttemptFailure(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fail-setup")
	frozen := resumeFrozen(t, "go test -json ./...")
	frozen.Setup.Run = "test ! -f '" + marker + "'"
	f := newResumeFixture(t, frozen, monkeyCleanScript)
	res := completedRunForResume(t, f)
	monkeyTruncateThrough(t, res.Dir, RecAttemptStarting)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.script, []byte("sleep 600\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resumed, err := monkeyResume(t, f, nil, res.Dir)
	if err != nil || resumed.Outcome != Refused || !strings.Contains(strings.Join(resumed.Why, " "), "setup") {
		t.Fatalf("setup failure must refuse, not report the cancelled Attempt: result=%+v error=%v", resumed, err)
	}
}

func TestResumeForgedPinsHaveNoWarmupEffects(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)
	pins := lastOpenedPins(t, res.Dir)
	pins.Attempt = "forged"
	monkeyAppend(t, res.Dir, RecGateDecided, map[string]any{
		"pins": pins, "choice": "promote", "files": []string{"notes.txt"},
	})
	checks := filepath.Join(res.Dir, "checks")
	if err := os.MkdirAll(checks, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(checks, "untouched")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := monkeyResume(t, f, nil, res.Dir); !IsRefused(err) {
		t.Fatalf("forged pins must refuse: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("refusal ran warmup cleanup before proving pins: %v", err)
	}
	if countTypes(resumeTypes(t, res.Dir), RecResumed) != 0 {
		t.Fatal("a refused resume must not record re-entry")
	}
}

func TestFindResumableFiltersAndListsCandidates(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)
	private := t.TempDir()
	copyState := func(name string) string {
		t.Helper()
		dir := filepath.Join(private, "runs", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(res.Dir, "ledger.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ledger.jsonl"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(filepath.Join(dir, "blobs"), os.DirFS(filepath.Join(res.Dir, "blobs"))); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	parked := copyState("prefix-parked")
	ended := copyState("prefix-ended")
	monkeyAppend(t, ended, RecRunEnded, map[string]any{"outcome": Accepted})
	if got, err := FindResumable(private, "prefix-"); err != nil || got != parked {
		t.Fatalf("unique resumable prefix: got %q, %v", got, err)
	}
	if got, err := FindResumable(private, "prefix-ended"); err != nil || got != ended {
		t.Fatalf("exact ended id must preserve detailed refusal: got %q, %v", got, err)
	}
	copyState("prefix-other")
	if _, err := FindResumable(private, "prefix-"); !IsRefused(err) ||
		!strings.Contains(err.Error(), "prefix-parked") || !strings.Contains(err.Error(), "prefix-other") {
		t.Fatalf("ambiguous prefix must list candidates: %v", err)
	}
}

func TestResumeTaskReplaysFollowUpCriteria(t *testing.T) {
	text := "Fix Add\n\n## Acceptance criteria\n- Add sums\n"
	followUps := []string{"Add Sub\n\n## Acceptance criteria\n- Sub subtracts\n", "Tidy up"}
	want := task.Parse(text)
	for _, fu := range followUps {
		want = want.FollowUp(fu)
	}
	if got := taskFromBlob(text, followUps); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed task %+v, want %+v", got, want)
	}
}
