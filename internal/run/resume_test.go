package run

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/task"
)

// resumeFixture is a user repository and the Params a Run starts and
// resumes with. The implementer is the fake adapter serving a shell
// script, so a test controls exactly what the Attempt does.
type resumeFixture struct {
	user   string
	state  *ledger.StateRoot
	frozen *pipeline.Frozen
	agents map[string]agent.Adapter
	script string // the implementer's shell script
}

// newResumeFixture builds the fixture: a Go module with fake serving
// script on every implementer Attempt.
func newResumeFixture(t *testing.T, f *pipeline.Frozen, script string) *resumeFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	rf := &resumeFixture{user: t.TempDir()}
	g := &followFixture{user: rf.user}
	g.git(t, "init", "-q", "-b", "main")
	g.write(t, "go.mod", cutMod)
	g.write(t, "add.go", cutAdd)
	g.write(t, "add_test.go", cutAddTest)
	g.git(t, "add", "-A")
	g.git(t, "commit", "-q", "-m", "init")
	d := t.TempDir()
	rf.state = &ledger.StateRoot{Dir: d, Private: filepath.Join(d, "private"), Work: filepath.Join(d, "work")}
	rf.script = filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(rf.script, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	rf.frozen = f
	rf.agents = map[string]agent.Adapter{fake.Name: &fake.Adapter{Script: rf.script}}
	return rf
}

// resumeFrozen is a one-Stage Fast Pipeline whose Check is the module's
// tests, with **/*.go the declared output so a notes file stays
// Ambiguous.
func resumeFrozen(t *testing.T, check string) *pipeline.Frozen {
	t.Helper()
	f, probs := pipeline.Resolve(nil, pipeline.Overrides{Fast: true, Checks: []string{check},
		Tests: []string{"**/*_test.go"}, Outputs: []string{"**/*.go"}}, []string{fake.Name})
	if len(probs) > 0 {
		t.Fatalf("resolve: %v", probs)
	}
	for i := range f.Stages {
		f.Stages[i].Agent = fake.Name
	}
	return f
}

func (f *resumeFixture) params(gates gate.Port) Params {
	return Params{
		Repo: f.user, Task: task.Task{Text: "fix Add"}, Frozen: f.frozen,
		Agents: f.agents, State: f.state, Getenv: os.Getenv,
		Version: "test", Gates: gates, Observe: func(Event) {},
	}
}

// resumeTypes reads one Run's Ledger record types.
func resumeTypes(t *testing.T, dir string) []string {
	t.Helper()
	recs, err := ledger.Replay(dir)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.Type)
	}
	return out
}

func countTypes(types []string, want string) int {
	n := 0
	for _, s := range types {
		if s == want {
			n++
		}
	}
	return n
}

// decider is a Gate port that answers with a fixed function.
type decider func(gate.Request) gate.Decision

func (d decider) Decide(ctx context.Context, r gate.Request) (gate.Decision, error) {
	return d(r), nil
}

// strayScript leaves a file no output glob covers: verdict pass, then
// the Ambiguous-file Gate parks the unattended Run.
const strayScript = `printf 'from the agent\n' > notes.txt
echo "exit: done"
`

// parkedRun runs the stray-script Run unattended until it parks at the
// Ambiguous-file Gate.
func parkedRun(t *testing.T, f *resumeFixture) *Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := Start(ctx, f.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Parked {
		t.Fatalf("want Parked, got %s (%v)", res.Outcome, res.Why)
	}
	return res
}

func TestResumeAParkedGateAsksAgain(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)
	before := resumeTypes(t, res.Dir)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res2, err := Resume(ctx, f.params(nil), res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != Parked {
		t.Fatalf("the re-parked Run: want Parked, got %s", res2.Outcome)
	}
	types := resumeTypes(t, res.Dir)
	if countTypes(types, RecResumed) != 1 || countTypes(types, RecGateOpened) != countTypes(before, RecGateOpened)+1 ||
		countTypes(types, RecRunParked) != 2 {
		t.Errorf("resumed re-park records: %v", types)
	}
}

func TestResumeAParkedGateDecides(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	promote := decider(func(r gate.Request) gate.Decision {
		return gate.Decision{Choice: "promote", Files: r.Files}
	})
	res2, err := Resume(ctx, f.params(promote), res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != Accepted {
		t.Fatalf("after promote: want Accepted, got %s (%v)", res2.Outcome, res2.Why)
	}
	types := resumeTypes(t, res.Dir)
	if countTypes(types, RecResumed) != 1 || countTypes(types, RecAmbiguousResolved) != 1 ||
		countTypes(types, RecCheckStarted) != 2 {
		t.Errorf("records: %v", types)
	}
}

func TestResumeRefuses(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A live Run refuses: live.pid names this process.
	live := filepath.Join(t.TempDir(), "live")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := markLive(live)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Resume(ctx, f.params(nil), live); !IsRefused(err) {
		t.Errorf("a live dir's resume: %v", err)
	}
	// An empty dir has no Ledger at all.
	if _, err := Resume(ctx, f.params(nil), t.TempDir()); err == nil {
		t.Error("an empty dir's resume wants a refusal")
	}
	// An ended Run has nothing to resume.
	promote := decider(func(r gate.Request) gate.Decision {
		return gate.Decision{Choice: "promote", Files: r.Files}
	})
	res2, err := Resume(ctx, f.params(promote), res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != Accepted {
		t.Fatalf("after promote: %s (%v)", res2.Outcome, res2.Why)
	}
	if _, err := Resume(ctx, f.params(nil), res.Dir); !IsRefused(err) {
		t.Errorf("an ended Run's resume: %v", err)
	}
}

// cancelAt cancels once a Run under f has written rec to a Ledger: the
// interrupt lands inside the stage that record begins, wherever the Run
// is in its setup.
func cancelAt(ctx context.Context, f *resumeFixture, rec string, cancel context.CancelFunc) {
	go func() {
		dir := filepath.Join(f.state.Private, "runs")
		for ctx.Err() == nil {
			runs, _ := filepath.Glob(filepath.Join(dir, "*"))
			for _, r := range runs {
				if recs, err := ledger.Replay(r); err == nil {
					for _, rr := range recs {
						if rr.Type == rec {
							cancel()
							return
						}
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

func TestResumeAnInterruptedAttempt(t *testing.T) {
	// The implementer's turn never ends on its own; the cancel below is
	// the interrupt that kills it.
	f := newResumeFixture(t, resumeFrozen(t, "true"), "sleep 600\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAt(ctx, f, RecAttemptStarting, cancel)
	res, err := Start(ctx, f.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != InfrastructureStop {
		t.Fatalf("want Infrastructure stop, got %s (%v)", res.Outcome, res.Why)
	}
	types := resumeTypes(t, res.Dir)
	if countTypes(types, RecAttemptStarting) != 1 || countTypes(types, RecResumed) != 0 {
		t.Fatalf("interrupted Run's records: %v", types)
	}

	// Resume starts a new Attempt over the same start revision; cancel it
	// too, and both interrupts stand in the Ledger.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() {
		for ctx2.Err() == nil {
			if countTypes(safeTypes(res.Dir), RecAttemptStarting) == 2 {
				cancel2()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	res2, err := Resume(ctx2, f.params(nil), res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != InfrastructureStop {
		t.Fatalf("resumed Run: want Infrastructure stop, got %s (%v)", res2.Outcome, res2.Why)
	}
	types = resumeTypes(t, res.Dir)
	if countTypes(types, RecResumed) != 1 || countTypes(types, RecAttemptStarting) != 2 {
		t.Errorf("records after resume: %v", types)
	}
}

// safeTypes reads a Run's record types, tolerating a mid-write Ledger.
func safeTypes(dir string) []string {
	recs, err := ledger.Replay(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.Type)
	}
	return out
}

func TestResumeARecordedDecisionApplies(t *testing.T) {
	// A GateDecided whose edge never ran: the recorded decision applies
	// with nobody to ask — Gates stays nil, so a re-opened Gate would
	// park the Run instead of promoting.
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)

	// The ledger ends parked at the Ambiguous-file Gate; write the
	// decision the park never got, with the GateOpened's own Pins.
	recs, err := ledger.Replay(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var opened struct {
		Pins gate.Pins `json:"pins"`
	}
	for _, r := range recs {
		if r.Type == RecGateOpened {
			if err := json.Unmarshal(r.Data, &opened); err != nil {
				t.Fatal(err)
			}
		}
	}
	l, err := ledger.Open(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(RecGateDecided, map[string]any{
		"pins": opened.Pins, "actor": "human", "choice": "promote", "files": []string{"notes.txt"}}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res2, err := Resume(ctx, f.params(nil), res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != Accepted {
		t.Fatalf("the recorded decision applied: want Accepted, got %s (%v)", res2.Outcome, res2.Why)
	}
	types := resumeTypes(t, res.Dir)
	// Nobody re-opened the Gate: the decision's own edge ran. The Checks
	// keep the Ledger's numbering: the resumed Check is the one after
	// the recorded one, not a skipped number.
	if countTypes(types, RecGateOpened) != 1 || countTypes(types, RecAmbiguousResolved) != 1 ||
		countTypes(types, RecCheckStarted) != 2 {
		t.Errorf("records: %v", types)
	}
	if got := checkNumbers(t, res.Dir); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("check numbers %v, want [1 2]", got)
	}
}

// checkNumbers is each CheckStarted's number, in Ledger order.
func checkNumbers(t *testing.T, dir string) []int {
	t.Helper()
	recs, err := ledger.Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, r := range recs {
		if r.Type != RecCheckStarted {
			continue
		}
		var d struct {
			Check int `json:"check"`
		}
		if err := json.Unmarshal(r.Data, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d.Check)
	}
	return out
}

func TestResumeTornLedgerRefuses(t *testing.T) {
	f := newResumeFixture(t, resumeFrozen(t, "go test -json ./..."), strayScript)
	res := parkedRun(t, f)
	// A torn tail: the last line died mid-write.
	lp := filepath.Join(res.Dir, "ledger.jsonl")
	data, err := os.ReadFile(lp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lp, append(data, []byte(`{"format":1,"seq":999`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := Resume(ctx, f.params(nil), res.Dir); !IsRefused(err) {
		t.Fatalf("a torn Ledger's resume: %v", err)
	}
}

func TestResumeAnInterruptedCheck(t *testing.T) {
	// The Attempt finishes; the Check's sleep is what the interrupt
	// kills. EvStage "checks" marks the Check's start so the cancel lands
	// inside it.
	f := newResumeFixture(t, resumeFrozen(t, "sleep 600"), strayScript)
	ctx, cancel := context.WithCancel(context.Background())
	p := f.params(nil)
	p.Observe = func(ev Event) {
		if ev.Kind == EvStage && ev.Step == "checks" {
			cancel()
		}
	}
	res, err := Start(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != InfrastructureStop {
		t.Fatalf("want Infrastructure stop, got %s (%v)", res.Outcome, res.Why)
	}

	// Resume re-runs the Check the same way.
	ctx2, cancel2 := context.WithCancel(context.Background())
	p2 := f.params(nil)
	p2.Observe = func(ev Event) {
		if ev.Kind == EvStage && ev.Step == "checks" {
			cancel2()
		}
	}
	res2, err := Resume(ctx2, p2, res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != InfrastructureStop {
		t.Fatalf("resumed Run: want Infrastructure stop, got %s (%v)", res2.Outcome, res2.Why)
	}
	types := resumeTypes(t, res.Dir)
	if countTypes(types, RecResumed) != 1 || countTypes(types, RecCheckStarted) != 2 ||
		countTypes(types, RecAttemptStarting) != 1 {
		t.Errorf("records after resume: %v", types)
	}
}
