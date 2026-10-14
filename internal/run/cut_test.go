package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/workspace"
)

const (
	cutMod     = "module fx\n\ngo 1.22\n"
	cutAdd     = "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	cutAddTest = "package fx\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add\")\n\t}\n}\n"
)

// cutFrozen is a resolved Fast Pipeline whose Check is the Go tests,
// with setup when setup is set.
func cutFrozen(t *testing.T, setup string) *pipeline.Frozen {
	t.Helper()
	f, probs := pipeline.Resolve(nil, pipeline.Overrides{Fast: true, Checks: []string{"go test ./..."},
		Tests: []string{"**/*_test.go"}, Outputs: []string{"**/*.go"}}, []string{"claude"})
	if len(probs) > 0 {
		t.Fatalf("resolve: %v", probs)
	}
	f.Setup.Run = setup
	return f
}

// openCut opens a Chat on a small Go module with fakeclaude serving turns.
func openCut(t *testing.T, setup string, turns ...multiturn.Turn) *Chat {
	t.Helper()
	return openChatIn(t.Context(), t, fakeClaude(t, multiturn.Scenario{Turns: turns}), 0, cutFrozen(t, setup),
		map[string]string{"go.mod": cutMod, "add.go": cutAdd, "add_test.go": cutAddTest})
}

func cut(t *testing.T, c *Chat) *Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	res, err := c.Cut(ctx, CutParams{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// runRecords are a Run's records of typ.
func runRecords(t *testing.T, dir, typ string) []map[string]any {
	t.Helper()
	recs, err := ledger.Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, r := range recs {
		if r.Type == typ {
			var m map[string]any
			if err := json.Unmarshal(r.Data, &m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m)
		}
	}
	return out
}

func turnRef(t *testing.T, c *Chat, n int) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", c.repo.Dir, "rev-parse", fmt.Sprintf("refs/oge/chat/turns/%d", n)).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// A turn that changes nothing makes no cut: no Run, and no wait on the
// Preflight.
func TestCutNoChangeMakesNoCut(t *testing.T) {
	c := openCut(t, "", multiturn.Turn{Reply: "hi"})
	drain(t, send(t, c, "yo"), nil)
	if _, err := c.Cut(t.Context(), CutParams{}); !errors.Is(err, ErrNothingToCut) {
		t.Fatalf("cut: %v, want ErrNothingToCut", err)
	}
}

// A Promoted change makes a cut: its own Run directory, holding the Chat,
// the turn range and the Chat repository, whose Candidate is the turn
// commit, verified to a Verdict.
func TestCutPromotedChangeIsARun(t *testing.T) {
	sub := cutAdd + "\nfunc Sub(a, b int) int { return a - b }\n"
	c := openCut(t, "", multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: sub}}})
	drain(t, send(t, c, "add Sub"), nil)
	res := cut(t, c)
	if res.Outcome != Accepted {
		t.Logf("checks %v", runRecords(t, res.Dir, RecCheckEnded))
		t.Logf("pre %v", runRecords(t, res.Dir, RecPreflightObserved))
		t.Fatalf("outcome %s %v", res.Outcome, res.Why)
	}
	if res.Candidate != turnRef(t, c, 1) {
		t.Errorf("Candidate %s, want turn 1's commit", res.Candidate)
	}
	started := runRecords(t, res.Dir, RecRunStarted)[0]
	if started["chat"] != c.ID || started["chat_repo"] != c.repo.Dir || started["follows"] != nil ||
		!reflect.DeepEqual(started["turns"], map[string]any{"from": float64(1), "to": float64(1)}) {
		t.Errorf("RunStarted %v", started)
	}
	hashes, _ := started["task_turns"].([]any)
	if len(hashes) != 1 || hashes[0].(map[string]any)["sha256"] != sha("add Sub") {
		t.Errorf("task_turns %v", started["task_turns"])
	}
	if v := runRecords(t, res.Dir, RecVerdict); len(v) != 1 || v[0]["verdict"] != "pass" {
		t.Errorf("Verdict %v", v)
	}
	// The Run repository holds the turn commit itself, byte for byte.
	repo := &workspace.RunRepo{Dir: filepath.Join(res.Dir, "repo.git")}
	if got, ok, err := repo.Show(res.Candidate, "add.go"); err != nil || !ok || string(got) != sub {
		t.Errorf("the Candidate's add.go: %q %v %v", got, ok, err)
	}
	if _, err := c.Cut(t.Context(), CutParams{}); !errors.Is(err, ErrNothingToCut) {
		t.Errorf("a second cut with no new turn: %v", err)
	}
}

// A second cut covers only the turns since the first: its turn range and
// diff. Its Task is every turn since the last delivery (D7), and none has
// happened, so it holds both.
func TestCutTwoCuts(t *testing.T) {
	sub := cutAdd + "\nfunc Sub(a, b int) int { return a - b }\n"
	c := openCut(t, "",
		multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: sub}}},
		multiturn.Turn{Reply: "and mul", Writes: []multiturn.File{{Path: "mul.go", Content: "package fx\n\nfunc Mul(a, b int) int { return a * b }\n"}}})
	drain(t, send(t, c, "add Sub"), nil)
	first := cut(t, c)
	drain(t, send(t, c, "now Mul"), nil)
	second := cut(t, c)
	if first.Outcome != Accepted || second.Outcome != Accepted {
		t.Fatalf("outcomes %s %s", first.Outcome, second.Outcome)
	}
	started := runRecords(t, second.Dir, RecRunStarted)[0]
	if started["follows"] != first.ID || started["base"] != first.Candidate ||
		!reflect.DeepEqual(started["turns"], map[string]any{"from": float64(2), "to": float64(2)}) {
		t.Errorf("second RunStarted %v", started)
	}
	ended := runRecords(t, second.Dir, RecAttemptEnded)[0]
	if !reflect.DeepEqual(ended["changed"], []any{"mul.go"}) {
		t.Errorf("the second cut's diff %v, want mul.go only", ended["changed"])
	}
	if hashes, _ := started["task_turns"].([]any); len(hashes) != 2 {
		t.Errorf("task_turns %v", started["task_turns"])
	}
}

// A turn's Tamper event reaches the cut's Run, with its Chat id, and its
// Gate: unattended, the Run parks.
func TestCutCarriesTamper(t *testing.T) {
	edited := strings.Replace(cutAddTest, "Add(2, 3) != 5", "false", 1)
	c := openCut(t, "", multiturn.Turn{Reply: "fixed", Writes: []multiturn.File{
		{Path: "add_test.go", Content: edited}, {Path: "add.go", Content: cutAdd + "\n// Add sums.\n"}}})
	drain(t, send(t, c, "fix it"), nil)
	res := cut(t, c)
	tamper := runRecords(t, res.Dir, RecTamperEvent)
	if len(tamper) != 1 || tamper[0]["id"] != "chat#1/tamper-1" || tamper[0]["chat"] != c.ID || tamper[0]["path"] != "add_test.go" {
		t.Errorf("TamperEvent %v", tamper)
	}
	if res.Outcome == Accepted {
		t.Errorf("accepted past an unacknowledged Tamper event")
	}
}

// A cut fails closed: a setup that fails on the Snapshot refuses it, and
// so does one that hasn't finished when the cut gives up waiting.
func TestCutFailsClosedOnPreflight(t *testing.T) {
	w := multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: cutAdd + "\n// Add sums.\n"}}}
	t.Run("failed", func(t *testing.T) {
		c := openCut(t, "exit 3", w)
		drain(t, send(t, c, "doc Add"), nil)
		res := cut(t, c)
		if res.Outcome != Refused || len(runRecords(t, res.Dir, RecVerdict)) != 0 {
			t.Errorf("outcome %s %v", res.Outcome, res.Why)
		}
		// The refusal is kept: a later cut makes no new Run.
		if again := cut(t, c); again.Outcome != Refused || again.ID != res.ID {
			t.Errorf("a later cut %s %s, want the refusal of %s", again.Outcome, again.ID, res.ID)
		}
	})
	t.Run("unfinished", func(t *testing.T) {
		c := openCut(t, "sleep 30", w)
		drain(t, send(t, c, "doc Add"), nil)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		res, err := c.Cut(ctx, CutParams{})
		if err != nil || res.Outcome != Refused || res.ID != "" {
			t.Logf("pre %v", runRecords(t, res.Dir, RecPreflightObserved))
			t.Errorf("cut %+v, %v", res, err)
		}
	})
}

// A cut that parked on a Tamper event leaves it unacknowledged: the next
// cut carries it, and gates on it too.
func TestCutParkedTamperGatesTheNext(t *testing.T) {
	edited := strings.Replace(cutAddTest, "Add(2, 3) != 5", "false", 1)
	c := openCut(t, "",
		multiturn.Turn{Reply: "fixed", Writes: []multiturn.File{{Path: "add_test.go", Content: edited}, {Path: "add.go", Content: cutAdd + "\n// Add sums.\n"}}},
		multiturn.Turn{Reply: "and mul", Writes: []multiturn.File{{Path: "mul.go", Content: "package fx\n\nfunc Mul(a, b int) int { return a * b }\n"}}})
	drain(t, send(t, c, "fix it"), nil)
	if first := cut(t, c); first.Outcome != Parked {
		t.Fatalf("first cut %s %v", first.Outcome, first.Why)
	}
	drain(t, send(t, c, "now Mul"), nil)
	second := cut(t, c)
	tamper := runRecords(t, second.Dir, RecTamperEvent)
	if second.Outcome != Parked || len(tamper) != 1 || tamper[0]["id"] != "chat#1/tamper-1" {
		t.Errorf("second cut %s, TamperEvent %v", second.Outcome, tamper)
	}
}

// A late Tamper event on a turn already cut, caught once the Session
// ends, reaches the next cut's Gate.
func TestCutLateTamperOnACutTurn(t *testing.T) {
	sub := cutAdd + "\nfunc Sub(a, b int) int { return a - b }\n"
	c := openCut(t, "", multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: sub}}})
	drain(t, send(t, c, "add Sub"), nil)
	if first := cut(t, c); first.Outcome != Accepted {
		t.Fatalf("first cut %s %v", first.Outcome, first.Why)
	}
	writeIn(t, c.Workspace, "add_test.go", "package fx\n")
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	_ = sess.Close()
	select {
	case <-c.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the Session never ended")
	}
	if late := recordsOf(t, c, RecTamperEvent); len(late) != 1 || late[0]["late"] != true || late[0]["turn"] != float64(1) {
		t.Fatalf("late TamperEvent %v", late)
	}
	// The reopened Session's turn; the Workspace change stands for its work.
	writeIn(t, c.Workspace, "mul.go", "package fx\n\nfunc Mul(a, b int) int { return a * b }\n")
	drain(t, send(t, c, "now Mul"), nil)
	second := cut(t, c)
	tamper := runRecords(t, second.Dir, RecTamperEvent)
	if second.Outcome != Parked || len(tamper) != 1 || tamper[0]["id"] != "chat#1/tamper-1" || tamper[0]["late"] != true {
		t.Errorf("second cut %s %v, TamperEvent %v", second.Outcome, second.Why, tamper)
	}
}

// A cut whose Check fails is never sent back (#262 does that in the
// Session): it ends Failed with its Verdict, and the next cut follows it.
func TestCutFailingCheckFails(t *testing.T) {
	c := openCut(t, "", multiturn.Turn{Reply: "oops", Writes: []multiturn.File{{Path: "add.go", Content: strings.Replace(cutAdd, "a + b", "a - b", 1)}}})
	drain(t, send(t, c, "break it"), nil)
	res := cut(t, c)
	if res.Outcome != Failed {
		t.Fatalf("outcome %s %v", res.Outcome, res.Why)
	}
	if v := runRecords(t, res.Dir, RecVerdict); len(v) != 1 || v[0]["verdict"] != "fail" {
		t.Errorf("Verdict %v", v)
	}
	if a := runRecords(t, res.Dir, RecAttemptStarting); len(a) != 0 {
		t.Errorf("an implementer Attempt started: %v", a)
	}
	if _, err := c.Cut(t.Context(), CutParams{}); !errors.Is(err, ErrNothingToCut) {
		t.Errorf("after a failed cut: %v", err)
	}
}

// Only the mode may change at a cut: a Pipeline that changes the Chat's
// Oracle or setup is refused, with no Run.
func TestCutRefusesAnotherOracle(t *testing.T) {
	c := openCut(t, "", multiturn.Turn{Reply: "added", Writes: []multiturn.File{{Path: "add.go", Content: cutAdd + "\n// Add sums.\n"}}})
	drain(t, send(t, c, "doc Add"), nil)
	for name, change := range map[string]func(*pipeline.Frozen){
		"test globs": func(f *pipeline.Frozen) { f.Project.TestGlobs = []string{"none/*_test.go"} },
		"setup":      func(f *pipeline.Frozen) { f.Setup.Run = "true" },
	} {
		f := cutFrozen(t, "")
		change(f)
		res, err := c.Cut(t.Context(), CutParams{Frozen: f})
		if err != nil || res.Outcome != Refused || res.ID != "" {
			t.Errorf("%s: cut %+v, %v", name, res, err)
		}
	}
	if res := cut(t, c); res.Outcome != Accepted {
		t.Errorf("the Chat's own Pipeline: %s %v", res.Outcome, res.Why)
	}
}

// Closing the Chat cancels a cut in flight: its Check stops, and neither
// the cut nor Close hangs. The Check holds on the cut's tree (never on
// the Snapshot, so the Snapshot control finishes) and marks that it has
// started, so Close lands mid-Check.
func TestCutClosedInFlight(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "checking")
	f := cutFrozen(t, "")
	f.Checks[0].Run = "if [ -f hold.go ]; then touch '" + mark + "'; while :; do sleep 0.1; done; fi"
	c := openChatIn(t.Context(), t, fakeClaude(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "held", Writes: []multiturn.File{{Path: "hold.go", Content: "package fx\n"}}}}}), 0, f,
		map[string]string{"go.mod": cutMod, "add.go": cutAdd, "add_test.go": cutAddTest})
	drain(t, send(t, c, "hold"), nil)
	type out struct {
		res *Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := c.Cut(t.Context(), CutParams{})
		done <- out{res, err}
	}()
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		select {
		case o := <-done:
			t.Fatalf("the cut ended before its Check held: %+v, %v; %s", o.res, o.err, mustJSON(runRecords(t, o.res.Dir, RecCheckEnded)))
		default:
		}
		if time.Since(start) > time.Minute {
			t.Fatal("the Check never started")
		}
	}
	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	select {
	case o := <-done:
		if o.err == nil && o.res.Outcome == Accepted {
			t.Errorf("a cancelled cut was accepted")
		}
	case <-time.After(40 * time.Second):
		t.Fatal("the cut didn't end when the Chat closed")
	}
	select {
	case <-closed:
	case <-time.After(20 * time.Second):
		t.Fatal("Close hung")
	}
}

// A cancelled cut verified nothing: it doesn't move the last cut, so the
// next cut, after a turn that changed nothing, still covers its changes.
func TestCancelledCutIsCutAgain(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "checking")
	f := cutFrozen(t, "")
	// Only the first Check on hold.go holds; the next one runs through.
	f.Checks[0].Run = "if [ -f hold.go ] && [ ! -f '" + mark + "' ]; then touch '" + mark + "'; while :; do sleep 0.1; done; fi; " + f.Checks[0].Run
	c := openChatIn(t.Context(), t, fakeClaude(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "held", Writes: []multiturn.File{{Path: "hold.go", Content: "package fx\n"}}},
		{Reply: "nothing changed"}}}), 0, f,
		map[string]string{"go.mod": cutMod, "add.go": cutAdd, "add_test.go": cutAddTest})
	t.Cleanup(func() { c.Close() })
	drain(t, send(t, c, "hold"), nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := c.Cut(ctx, CutParams{})
		done <- err
	}()
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		if time.Since(start) > time.Minute {
			t.Fatal("the Check never started")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
		t.Fatal("the cut didn't end when it was cancelled")
	}
	drain(t, send(t, c, "and now?"), nil)
	res, err := c.Cut(t.Context(), CutParams{})
	if errors.Is(err, ErrNothingToCut) {
		t.Fatal("the cancelled cut's changes were never cut again")
	}
	if err != nil {
		t.Fatal(err)
	}
	if res.Candidate == "" {
		t.Errorf("the second cut has no Candidate: %+v", res)
	}
}

// waitFor waits for the file at path, failing if done ends first.
func waitFor[T any](t *testing.T, path string, done <-chan T) {
	t.Helper()
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-done:
			t.Fatal("the cut ended before its Check held")
		default:
		}
		if time.Since(start) > time.Minute {
			t.Fatal("the Check never started")
		}
	}
}

// While a cut's Check is held, the next turn is sent and replies; its cut
// supersedes the held one, which ends with no Verdict and a superseded
// stop in its Ledger, and covers both turns' changes.
func TestCutSupersededByANewerCut(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "checking")
	f := cutFrozen(t, "")
	// Only the first Check on hold.go holds; the next one runs through.
	f.Checks[0].Run = "if [ -f hold.go ] && [ ! -f '" + mark + "' ]; then touch '" + mark + "'; while :; do sleep 0.1; done; fi; " + f.Checks[0].Run
	c := openChatIn(t.Context(), t, fakeClaude(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "held", Writes: []multiturn.File{{Path: "hold.go", Content: "package fx\n"}}},
		{Reply: "and more", Writes: []multiturn.File{{Path: "more.go", Content: "package fx\n"}}}}}), 0, f,
		map[string]string{"go.mod": cutMod, "add.go": cutAdd, "add_test.go": cutAddTest})
	t.Cleanup(func() { c.Close() })
	drain(t, send(t, c, "hold"), nil)
	type out struct {
		res *Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := c.Cut(t.Context(), CutParams{})
		done <- out{res, err}
	}()
	waitFor(t, mark, done)
	evs := drain(t, send(t, c, "more"), nil) // the held Check doesn't hold the turn up
	replied := false
	for _, ev := range evs {
		replied = replied || strings.Contains(ev.Text, "and more")
	}
	if !replied {
		t.Fatalf("the turn sent during the Check didn't reply: %+v", evs)
	}
	newer := cut(t, c)
	var older out
	select {
	case older = <-done:
	case <-time.After(40 * time.Second):
		t.Fatal("the superseded cut didn't end")
	}
	if !errors.Is(older.err, ErrSuperseded) || older.res == nil {
		t.Fatalf("the older cut: %+v, %v; want ErrSuperseded with its Run", older.res, older.err)
	}
	if len(runRecords(t, older.res.Dir, RecVerdict)) != 0 {
		t.Error("a superseded cut has a Verdict")
	}
	if ended := runRecords(t, older.res.Dir, RecRunEnded); len(ended) != 1 || ended[0]["stop"] != "superseded" {
		t.Errorf("the superseded cut's RunEnded %v", ended)
	}
	if newer.Outcome != Accepted {
		t.Fatalf("the newer cut %s %v", newer.Outcome, newer.Why)
	}
	started := runRecords(t, newer.Dir, RecRunStarted)[0]
	if started["follows"] != nil || !reflect.DeepEqual(started["turns"], map[string]any{"from": float64(1), "to": float64(2)}) {
		t.Errorf("the newer cut doesn't cover the superseded one's turns: %v", started)
	}
	if ch := runRecords(t, newer.Dir, RecAttemptEnded)[0]["changed"]; !reflect.DeepEqual(ch, []any{"hold.go", "more.go"}) {
		t.Errorf("the newer cut's changes %v", ch)
	}
}

// A cut of a tree an earlier cut checked, on the same Oracle and
// Pipeline, reuses its Check: no Check runs, and its Ledger names the Run
// whose it is. Another Pipeline checks again.
func TestCutReusesAVerdict(t *testing.T) {
	count := filepath.Join(t.TempDir(), "checks")
	f := cutFrozen(t, "")
	f.Checks[0].Run = "echo x >> '" + count + "'; " + f.Checks[0].Run
	one := multiturn.File{Path: "x.go", Content: "package fx\n\n// one\n"}
	two := multiturn.File{Path: "x.go", Content: "package fx\n\n// two\n"}
	c := openChatIn(t.Context(), t, fakeClaude(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "1", Writes: []multiturn.File{one}}, {Reply: "2", Writes: []multiturn.File{two}},
		{Reply: "3", Writes: []multiturn.File{one}}, {Reply: "4", Writes: []multiturn.File{two}}}}), 0, f,
		map[string]string{"go.mod": cutMod, "add.go": cutAdd, "add_test.go": cutAddTest})
	t.Cleanup(func() { c.Close() })
	checks := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	var res []*Result
	before := 0 // the Snapshot control runs the Check too
	for i := range 3 {
		drain(t, send(t, c, fmt.Sprint("turn ", i)), nil)
		res = append(res, cut(t, c))
		if res[i].Outcome != Accepted {
			t.Fatalf("cut %d: %s %v", i, res[i].Outcome, res[i].Why)
		}
		if i == 0 {
			before = checks() - 1
		}
	}
	if n := checks() - before; n != 2 {
		t.Errorf("%d Checks ran, want 2: the third cut's tree is the first's", n)
	}
	reused := runRecords(t, res[2].Dir, RecVerdictReused)
	if len(reused) != 1 || reused[0]["run"] != res[0].ID {
		t.Errorf("the third cut's VerdictReused %v, want the first cut's Run %s", reused, res[0].ID)
	}
	if len(runRecords(t, res[2].Dir, RecVerdict)) != 1 {
		t.Error("the reusing cut has no Verdict")
	}
	// The second cut's tree again, in another Pipeline: checked again.
	g := *f
	g.Limits.Retries++
	drain(t, send(t, c, "turn 3"), nil)
	r, err := c.Cut(t.Context(), CutParams{Frozen: &g})
	if err != nil || r.Outcome != Accepted {
		t.Fatalf("cut in another Pipeline: %+v, %v", r, err)
	}
	if n := checks() - before; n != 3 || len(runRecords(t, r.Dir, RecVerdictReused)) != 0 {
		t.Errorf("a Verdict was reused across Pipelines: %d Checks", n)
	}
}

// A cut that reuses a Check still takes its turns' Tamper events to their
// Gate: the reuse is of the Check, never of the walk after it.
func TestCutReusedCheckStillGatesTamper(t *testing.T) {
	one := multiturn.File{Path: "x.go", Content: "package fx\n\n// one\n"}
	two := multiturn.File{Path: "x.go", Content: "package fx\n\n// two\n"}
	edited := strings.Replace(cutAddTest, "Add(2, 3) != 5", "false", 1)
	c := openCut(t, "",
		multiturn.Turn{Reply: "1", Writes: []multiturn.File{one}},
		multiturn.Turn{Reply: "2", Writes: []multiturn.File{two}},
		multiturn.Turn{Reply: "3", Writes: []multiturn.File{one, {Path: "add_test.go", Content: edited}}})
	t.Cleanup(func() { c.Close() })
	var first *Result
	for i := range 3 {
		drain(t, send(t, c, fmt.Sprint("turn ", i)), nil)
		res := cut(t, c)
		if i == 0 {
			first = res
		}
		if i < 2 && res.Outcome != Accepted {
			t.Fatalf("cut %d: %s %v", i, res.Outcome, res.Why)
		}
		if i == 2 {
			if reused := runRecords(t, res.Dir, RecVerdictReused); len(reused) != 1 || reused[0]["run"] != first.ID {
				t.Errorf("VerdictReused %v, want the first cut's", reused)
			}
			if res.Outcome != Parked || len(runRecords(t, res.Dir, RecTamperEvent)) != 1 {
				t.Errorf("a reused Check skipped the Tamper Gate: %s %v", res.Outcome, res.Why)
			}
		}
	}
}
