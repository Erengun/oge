package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		if res := cut(t, c); res.Outcome != Refused || len(runRecords(t, res.Dir, RecVerdict)) != 0 {
			t.Errorf("outcome %s %v", res.Outcome, res.Why)
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
