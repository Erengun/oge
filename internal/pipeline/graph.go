package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// GraphFormat versions the compiled graph's encoding, and so its hash.
const GraphFormat = 1

// Node kinds.
const (
	KindStage = "stage"
	KindCheck = "check"
	KindGate  = "gate"
	KindEnd   = "end"
)

// Graph is the compiled, static graph a Run freezes and walks. Agent output
// can never add a node or edge or raise a bound (ADR-0007).
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Node is a Stage, Check, Gate or the end of the Run.
type Node struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Role      string `json:"role,omitempty"`      // Stage: its Role kind
	Label     string `json:"label,omitempty"`     // Gate: its glossary name
	Mandatory bool   `json:"mandatory,omitempty"` // Gate
	Trigger   string `json:"trigger,omitempty"`   // Gate: when it fires
}

// Edge is labelled from a closed set (an Exit, a Verdict, or a limit being
// exhausted) and may carry the name of the limit that bounds it.
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	On    string `json:"on"`
	Bound string `json:"bound,omitempty"`
}

// Compile builds the fixed built-in graph for a mode, inserting the
// mandatory Gates where their triggers can fire (ADR-0013, ADR-0016,
// ADR-0019). Gate choice edges are added with the Gates themselves.
func Compile(mode Mode, resultGate bool) Graph {
	var g Graph
	node := func(n Node) { g.Nodes = append(g.Nodes, n) }
	edge := func(from, to, on, bound string) { g.Edges = append(g.Edges, Edge{from, to, on, bound}) }

	hasVerifier := mode != Fast

	switch mode {
	case Fast:
		node(Node{ID: "implement", Kind: KindStage, Role: "implementer"})
	case Blind:
		node(Node{ID: "verify", Kind: KindStage, Role: "verifier"})
		node(Node{ID: "implement", Kind: KindStage, Role: "implementer"})
	default:
		node(Node{ID: "implement", Kind: KindStage, Role: "implementer"})
		node(Node{ID: "verify", Kind: KindStage, Role: "verifier"})
	}
	node(Node{ID: "check", Kind: KindCheck})

	// Mandatory Gates. Tamper, Own-test-failure and Ambiguous-file share the
	// end-of-run review (ADR-0019).
	node(Node{ID: "gate.tamper", Kind: KindGate, Label: "tamper", Mandatory: true, Trigger: "a Tamper event"})
	node(Node{ID: "gate.infeasible", Kind: KindGate, Label: "infeasible", Mandatory: true, Trigger: "an infeasible or conflicts_with_oracle Exit"})
	node(Node{ID: "gate.bound_exhaustion", Kind: KindGate, Label: "bound-exhaustion", Mandatory: true, Trigger: "a budget, send-back limit or the Attempt cap exhausted"})
	if hasVerifier {
		node(Node{ID: "gate.oracle_growth", Kind: KindGate, Label: "Oracle-growth", Mandatory: true, Trigger: "an Oracle-growth limit hit"})
	}
	node(Node{ID: "gate.own_test_failure", Kind: KindGate, Label: "Own-test-failure", Mandatory: true, Trigger: "Implementer-authored tests fail after the repair budget"})
	node(Node{ID: "gate.ambiguous_file", Kind: KindGate, Label: "Ambiguous-file", Mandatory: true, Trigger: "Ambiguous files remain after a pass"})
	if resultGate {
		node(Node{ID: "gate.result", Kind: KindGate, Label: "Result gate", Trigger: "the final Check passes"})
	}
	node(Node{ID: "end", Kind: KindEnd})

	// Agent-side failures retry the same Stage on its own budget.
	for _, n := range g.Nodes {
		if n.Kind == KindStage {
			edge(n.ID, n.ID, "attempt_failure", "retries")
		}
	}
	afterImplement, afterVerify := "check", "check"
	switch mode {
	case Standard:
		afterImplement = "verify"
	case Blind:
		afterVerify = "implement"
	}
	edge("implement", afterImplement, "exit:done", "")
	edge("implement", "gate.infeasible", "exit:infeasible", "")
	if hasVerifier {
		edge("verify", afterVerify, "exit:extended", "")
		edge("verify", afterVerify, "exit:no_additions", "")
		edge("verify", "gate.infeasible", "exit:conflicts_with_oracle", "")
		edge("verify", "gate.oracle_growth", "limit:oracle_growth", "")
	}
	edge("check", "implement", "verdict:fail", "send_backs")
	edge("check", "gate.bound_exhaustion", "limit:send_backs", "")
	// Failing Implementer-authored tests go back for repair on the same
	// budget; the Gate fires only when it is exhausted (ADR-0019).
	edge("check", "implement", "own_tests:fail", "send_backs")
	edge("check", "gate.own_test_failure", "limit:send_backs+own_tests:fail", "")
	// The end-of-run review, before a pass can end the Run.
	edge("check", "gate.tamper", "verdict:pass+tamper_event", "")
	edge("check", "gate.ambiguous_file", "verdict:pass+ambiguous_files", "")
	if resultGate {
		edge("check", "gate.result", "verdict:pass", "")
	} else {
		edge("check", "end", "verdict:pass", "")
	}
	return g
}

// Hash is the compiled graph's identity together with the limits that bound
// it: a hex SHA-256 of a deterministic encoding.
func (g Graph) Hash(l Limits) string {
	b, err := json.Marshal(struct {
		Format int    `json:"format"`
		Graph  Graph  `json:"graph"`
		Limits Limits `json:"limits"`
	}{GraphFormat, g, l})
	if err != nil {
		panic(err) // plain structs; cannot fail
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Shape is the one-line picture of the main path, e.g.
// "implement ─▶ verify (after) ─▶ Check".
func (g Graph) Shape(verify string) string {
	var parts []string
	for _, n := range g.Nodes {
		switch {
		case n.Kind == KindStage && n.Role == "verifier":
			parts = append(parts, "verify ("+verify+")")
		case n.Kind == KindStage:
			parts = append(parts, n.ID)
		case n.Kind == KindCheck:
			parts = append(parts, "Check")
		case n.ID == "gate.result":
			parts = append(parts, "[Result gate]")
		}
	}
	return strings.Join(parts, " ─▶ ")
}

// MandatoryGates lists the mandatory Gates' names in graph order.
func (g Graph) MandatoryGates() []string {
	var names []string
	for _, n := range g.Nodes {
		if n.Kind == KindGate && n.Mandatory {
			names = append(names, n.Label)
		}
	}
	return names
}
