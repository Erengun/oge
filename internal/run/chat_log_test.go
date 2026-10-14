package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
)

// recordsOf is the data of c's Chat log records of type typ, in order.
func recordsOf(t *testing.T, c *Chat, typ string) []map[string]any {
	t.Helper()
	recs, err := ledger.Replay(c.Dir)
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

func typesOf(t *testing.T, c *Chat) []string {
	t.Helper()
	recs, err := ledger.Replay(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.Type)
	}
	return out
}

// The Chat log is written ahead: the Chat and its Snapshot first, then
// per turn its start and manifest before the message, and the scope plan
// before its reverts, then the turn's end with its events, reply and Usage.
func TestChatLogThreeTurns(t *testing.T) {
	c := openChat(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "one"},
		{Reply: "two", Writes: []multiturn.File{{Path: "b.go", Content: "package fx\n"}}, Usage: &multiturn.Usage{CostUSD: 0.5, Tokens: 7}},
		{Reply: "three"},
	}}, 0)
	for _, m := range []string{"msg one", "msg two", "msg three"} {
		settledOf(t, drain(t, send(t, c, m), nil))
	}
	want := []string{RecChatStarted, RecSnapshotTaken, RecOracleVersion, RecProcessStarted}
	for range 3 {
		want = append(want, RecTurnStarted, RecBriefingManifest, RecScopeObserved, RecScopeReverted, RecAttemptEnded)
	}
	if got := typesOf(t, c); !reflect.DeepEqual(got, want) {
		t.Fatalf("records\n got %v\nwant %v", got, want)
	}
	started, ended := recordsOf(t, c, RecTurnStarted), recordsOf(t, c, RecAttemptEnded)
	for i, m := range []string{"msg one", "msg two", "msg three"} {
		if started[i]["message"] != sha(m) || started[i]["turn"] != float64(i+1) {
			t.Errorf("TurnStarted %d: %v", i+1, started[i])
		}
	}
	e := ended[1]
	if e["turn"] != float64(2) || !strings.HasPrefix(e["reply"].(string), "two") || e["failure"] != "" {
		t.Errorf("AttemptEnded 2: %v", e)
	}
	if u, _ := e["usage"].(map[string]any); u["cost_usd"] != 0.5 || u["tokens"] != float64(7) {
		t.Errorf("AttemptEnded 2 usage: %v", e["usage"])
	}
	// No output globs here: a new file is Ambiguous.
	if ch, _ := e["changes"].(map[string]any); !reflect.DeepEqual(ch["ambiguous"], []any{"b.go"}) {
		t.Errorf("AttemptEnded 2 changes: %v", e["changes"])
	}
	blobs, err := ledger.OpenBlobs(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	events, err := blobs.Get(e["events"].(string))
	if err != nil || !strings.Contains(string(events), `"two"`) {
		t.Errorf("turn 2's events blob %q: %v", events, err)
	}
	if ch, _ := ended[2]["changes"].(map[string]any); !reflect.DeepEqual(ch["ambiguous"], []any{}) {
		t.Errorf("turn 3 changed nothing, yet: %v", ended[2]["changes"])
	}
	if err := c.HeldOutViewed("AC-1"); err != nil {
		t.Fatal(err)
	}
	if r := recordsOf(t, c, RecHeldOutViewed); len(r) != 1 || r[0]["turn"] != float64(3) {
		t.Errorf("HeldOutViewed %v", r)
	}
}

func scopedFrozen() *pipeline.Frozen {
	f := chatFrozen("implementer")
	f.Project.TestGlobs = []string{"*_test.go"}
	f.Project.OutputGlobs = []string{"*.go"}
	return f
}

// openScoped opens a Chat whose Snapshot holds an Oracle test.
func openScoped(t *testing.T, turns ...multiturn.Turn) *Chat {
	t.Helper()
	return openChatIn(t.Context(), t, fakeClaude(t, multiturn.Scenario{Turns: turns}), 0, scopedFrozen(),
		map[string]string{"add.go": "package fx\n\nfunc Add(a, b int) int { return a - b }\n", "add_test.go": additiveBase})
}

// A turn's edit to an Oracle test is reverted silently and recorded as a
// Tamper event with the turn; the plan is in the log before the revert.
func TestChatTurnTamperReverted(t *testing.T) {
	edited := strings.Replace(additiveBase, "Add(2, 3) != 5", "false", 1)
	c := openScoped(t, multiturn.Turn{Reply: "fixed", Writes: []multiturn.File{
		{Path: "add_test.go", Content: edited}, {Path: "add.go", Content: "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}})
	s := settledOf(t, drain(t, send(t, c, "fix it"), nil))
	if s.Failure != "" || s.Scope == nil {
		t.Fatalf("settled %+v", s)
	}
	if !reflect.DeepEqual(s.Scope.Tamper, []string{"add_test.go"}) || !reflect.DeepEqual(s.Scope.Promoted, []string{"add.go"}) {
		t.Errorf("scope %+v", s.Scope)
	}
	got, err := os.ReadFile(filepath.Join(c.Workspace, "add_test.go"))
	if err != nil || string(got) != additiveBase {
		t.Errorf("the Oracle test wasn't put back: %q, %v", got, err)
	}
	tamper := recordsOf(t, c, RecTamperEvent)
	if len(tamper) != 1 || tamper[0]["turn"] != float64(1) || tamper[0]["path"] != "add_test.go" || tamper[0]["reverted"] != true ||
		tamper[0]["id"] != "chat#1/tamper-1" {
		t.Errorf("TamperEvent %v", tamper)
	}
	types := strings.Join(typesOf(t, c), " ")
	if !strings.Contains(types, RecScopeObserved+" "+RecScopeReverted+" "+RecTamperEvent+" "+RecAttemptEnded) {
		t.Errorf("not written ahead: %s", types)
	}
	if obs := recordsOf(t, c, RecScopeObserved); obs[0]["state"] != "planned" || obs[0]["tamper"] != float64(1) {
		t.Errorf("ScopeObserved %v", obs)
	}
}

// An edit that only adds a test to an Oracle test file is kept, and is
// no Tamper event (#117).
func TestChatTurnAdditiveTestKept(t *testing.T) {
	c := openScoped(t, multiturn.Turn{Writes: []multiturn.File{{Path: "add_test.go", Content: additiveBase + testZero}}})
	s := settledOf(t, drain(t, send(t, c, "add a test"), nil))
	if s.Scope == nil || len(s.Scope.Tamper) != 0 || !reflect.DeepEqual(s.Scope.Promoted, []string{"add_test.go"}) {
		t.Errorf("scope %+v", s.Scope)
	}
	if got, _ := os.ReadFile(filepath.Join(c.Workspace, "add_test.go")); string(got) != additiveBase+testZero {
		t.Errorf("the addition wasn't kept: %q", got)
	}
	if r := recordsOf(t, c, RecTamperEvent); len(r) != 0 {
		t.Errorf("TamperEvent %v", r)
	}
}

// A turn that only touches agent instructions changed nothing a judged
// role sees: Excluded only, so no cut. A new file no glob matches is
// Ambiguous.
func TestChatTurnExcludedAndAmbiguous(t *testing.T) {
	c := openScoped(t,
		multiturn.Turn{Writes: []multiturn.File{{Path: "CLAUDE.md", Content: "be brief\n"}}},
		multiturn.Turn{Writes: []multiturn.File{{Path: "notes.txt", Content: "n\n"}}})
	s := settledOf(t, drain(t, send(t, c, "remember"), nil)).Scope
	if s == nil || !reflect.DeepEqual(s.Excluded, []string{"CLAUDE.md"}) || len(s.Promoted)+len(s.Ambiguous)+len(s.Tamper) != 0 {
		t.Errorf("turn 1 scope %+v", s)
	}
	s = settledOf(t, drain(t, send(t, c, "note"), nil)).Scope
	if s == nil || !reflect.DeepEqual(s.Ambiguous, []string{"notes.txt"}) || len(s.Promoted)+len(s.Excluded) != 0 {
		t.Errorf("turn 2 scope %+v", s)
	}
}
