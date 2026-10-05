package run

import (
	"context"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
)

func testWalk(t *testing.T, g pipeline.Graph, port gate.Port) *walk {
	t.Helper()
	l, err := ledger.Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return &walk{p: Params{Gates: port, Observe: func(Event) {}}, l: l, g: g, limits: pipeline.DefaultLimits}
}

var testAttempt = &Attempt{ID: "implement#1", Candidate: "6d1231d9f00d"}

// Edges the graph holds but the walk can't take yet stop the Run as an
// Infrastructure stop with no Attempt, never an internal error.
func TestUnbuiltPathsStopTheRun(t *testing.T) {
	// A Gate with no wording yet (e.g. Ambiguous-file, #48).
	w := testWalk(t, pipeline.Compile(pipeline.Fast, false), &gate.Scripted{})
	s, err := w.follow(context.Background(), pipeline.Edge{From: "check", To: "gate.ambiguous_file"}, testAttempt, 0, &oracle.Result{Pass: true})
	if err != nil || s.stop != InfrastructureStop || !strings.Contains(strings.Join(s.why, " "), "isn't built yet") {
		t.Errorf("unwritten Gate: %+v %v", s, err)
	}

	// A choice whose edge leads to a node the walk can't enter yet
	// (promote → check or verify).
	g := pipeline.Graph{
		Nodes: []pipeline.Node{{ID: "check", Kind: pipeline.KindCheck}, {ID: "gate.result", Kind: pipeline.KindGate, Label: "Result gate"}},
		Edges: []pipeline.Edge{{From: "gate.result", To: "check", On: pipeline.ChoicePrefix + "take"}},
	}
	w = testWalk(t, g, &gate.Scripted{Decisions: []gate.Decision{{Choice: "take"}}})
	s, err = w.follow(context.Background(), pipeline.Edge{From: "check", To: "gate.result"}, testAttempt, 0, &oracle.Result{Pass: true})
	if err != nil || s.stop != InfrastructureStop || !strings.Contains(strings.Join(s.why, " "), "isn't built yet") {
		t.Errorf("unbuilt edge: %+v %v", s, err)
	}
}
