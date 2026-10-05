package pipeline

import (
	"strings"
	"testing"
)

// facts is a walk's state at a node: the conditions that hold and the
// bounds used up.
type facts struct {
	holds     []string
	exhausted []string
}

func (f facts) route(g Graph, from string) (Edge, bool) {
	in := func(list []string) func(string) bool {
		return func(s string) bool {
			for _, x := range list {
				if x == s {
					return true
				}
			}
			return false
		}
	}
	return g.Route(from, in(f.holds), in(f.exhausted))
}

// The check node's edges overlap; the first that holds, in precedence
// order, wins (ADR-0007, ADR-0019).
func TestCheckVerdictEdgePrecedence(t *testing.T) {
	for _, c := range []struct {
		name       string
		resultGate bool
		f          facts
		to         string
	}{
		{"fail sends back", false, facts{holds: []string{"verdict:fail"}}, "implement"},
		{"fail with the limit used up", false, facts{holds: []string{"verdict:fail"}, exhausted: []string{"send_backs"}}, "gate.bound_exhaustion"},
		{"a fail outranks own tests", false, facts{holds: []string{"verdict:fail", "own_tests:fail"}, exhausted: []string{"send_backs"}}, "gate.bound_exhaustion"},
		{"own tests fail on a pass", false, facts{holds: []string{"verdict:pass", "own_tests:fail", "tamper_event"}}, "implement"},
		{"own tests with the limit used up", false, facts{holds: []string{"verdict:pass", "own_tests:fail"}, exhausted: []string{"send_backs"}}, "gate.own_test_failure"},
		{"tamper outranks ambiguous files", true, facts{holds: []string{"verdict:pass", "tamper_event", "ambiguous_files"}}, "gate.tamper"},
		{"ambiguous files outrank the Result gate", true, facts{holds: []string{"verdict:pass", "ambiguous_files"}}, "gate.ambiguous_file"},
		{"a clean pass with --confirm", true, facts{holds: []string{"verdict:pass"}}, "gate.result"},
		{"a clean pass", false, facts{holds: []string{"verdict:pass"}}, "end"},
	} {
		e, ok := c.f.route(Compile(Fast, c.resultGate), "check")
		if !ok || e.To != c.to {
			t.Errorf("%s: took %v (%v), want %s", c.name, e, ok, c.to)
		}
	}
}

func TestEveryGateHasChoiceEdges(t *testing.T) {
	for _, m := range []Mode{Fast, Standard, Blind} {
		g := Compile(m, true)
		for _, n := range g.Nodes {
			if n.Kind != KindGate {
				continue
			}
			words := strings.Join(g.Choices(n.ID), ",")
			if !strings.Contains(words, "reject") || !strings.Contains(words, "quit") {
				t.Errorf("%s %s: choices %q lack reject and quit", m, n.ID, words)
			}
		}
	}
}

func TestGateChoicesAndTheirOutcomes(t *testing.T) {
	g := Compile(Fast, true)
	for _, c := range []struct {
		gate, choice, to, outcome string
	}{
		{"gate.result", "take", "end", "Accepted"},
		{"gate.result", "send back", "implement", ""},
		{"gate.result", "reject", "end", "Rejected"},
		{"gate.result", "quit", "end", "Cancelled"},
		{"gate.bound_exhaustion", "send back", "implement", ""},
		{"gate.bound_exhaustion", "reject", "end", "Rejected"},
		{"gate.bound_exhaustion", "quit", "end", "Cancelled"},
		{"gate.own_test_failure", "override", "end", "Overridden"},
		{"gate.ambiguous_file", "promote", "check", ""},
		{"gate.infeasible", "infeasible", "end", "Infeasible"},
		// The Verdict routes again once the Tamper events are acknowledged.
		{"gate.tamper", "acknowledge", "check", ""},
	} {
		e, ok := g.Choice(c.gate, c.choice)
		if !ok || e.To != c.to || e.Outcome != c.outcome {
			t.Errorf("%s %s: %v (%v), want → %s %s", c.gate, c.choice, e, ok, c.to, c.outcome)
		}
	}
	if got := strings.Join(g.Choices("gate.result"), ","); got != "take,send back,reject,quit" {
		t.Errorf("Result gate choices %q", got)
	}
	if got := strings.Join(g.Choices("gate.bound_exhaustion"), ","); got != "send back,reject,quit" {
		t.Errorf("bound-exhaustion choices %q", got)
	}
	// The Result gate's send back is bounded; past the limit it isn't offered.
	if e, _ := g.Choice("gate.result", "send back"); e.Bound != "send_backs" {
		t.Errorf("Result gate send back is unbounded: %v", e)
	}
	// Promoting a withheld file forces a fresh verifier Attempt (ADR-0013).
	if e, _ := Compile(Standard, false).Choice("gate.ambiguous_file", "promote"); e.To != "verify" {
		t.Errorf("Standard promote goes to %s", e.To)
	}
}

// Unattended Runs have no optional Gates (ADR-0008), so the frozen graph
// a park is pinned to never holds the Result gate.
func TestUnattendedRemovesTheResultGate(t *testing.T) {
	cfg, probs := Load([]byte("schema = 1\n[project]\ntest_globs = [\"**/*_test.go\"]\n[[check.commands]]\nrun = \"go test ./...\"\n[pipelines.default.gates]\nresult = true\n"))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	for _, c := range []struct {
		o    Overrides
		want bool
	}{
		{Overrides{Fast: true}, true},
		{Overrides{Fast: true, Unattended: true}, false},
		{Overrides{Fast: true, Confirm: true, Unattended: true}, false},
	} {
		f, probs := Resolve(cfg, c.o, []string{"claude"})
		if len(probs) > 0 {
			t.Fatal(probs)
		}
		_, has := f.Graph.Choice("gate.result", "take")
		if f.ResultGate != c.want || has != c.want {
			t.Errorf("%+v: ResultGate %v, graph has it %v", c.o, f.ResultGate, has)
		}
	}
}
