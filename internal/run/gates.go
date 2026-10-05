package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
)

// Outcomes and statuses a Gate can end a Run with.
const (
	Cancelled  Outcome = "Cancelled"
	Overridden Outcome = "Overridden"
	Infeasible Outcome = "Infeasible"
	// Parked is a status: an unattended Run waits at a mandatory Gate.
	Parked Outcome = "Parked"
)

// Gate records. A decision is written before it takes effect (ADR-0008).
const (
	RecGateOpened  = "GateOpened"
	RecGateDecided = "GateDecided"
	// RecRunParked ends the Ledger of a parked Run, in place of RunEnded:
	// the Run hasn't finished.
	RecRunParked = "RunParked"
	// RecGateAbandoned closes a Gate nobody decided: the Run was cancelled
	// or the terminal closed. Öge never decides in the human's place.
	RecGateAbandoned = "GateAbandoned"
)

// gateSpec is how the Run words one Gate.
type gateSpec struct {
	what, need string
	// says is what each choice does at this Gate.
	says map[string]string
	// extends names the limit a choice extends by one here. An extension
	// is typed in full with a reason, and recorded as one (ADR-0008).
	extends map[string]string
}

// gateSpecs words each Gate the Run can open. A new Gate adds its entry
// here and its choice edges in pipeline.Compile; its trigger is a
// condition on the check node's edges.
func gateSpecs(l pipeline.Limits) map[string]gateSpec {
	return map[string]gateSpec{
		"gate.result": {
			what: "Öge's Check passed on the Candidate.",
			need: "Decide whether to take it.",
			says: map[string]string{
				"take":      "end the Run Accepted",
				"send back": "return it to the implementer, with an optional note",
				"reject":    "end the Run Rejected (type the word and a reason)",
				"quit":      "end the Run Cancelled",
			},
		},
		"gate.bound_exhaustion": {
			what: fmt.Sprintf("The Check failed and the send-back limit (%d) is used up.", l.SendBacks),
			need: "Decide what happens to the Candidate.",
			says: map[string]string{
				"send back": "one more send-back past the limit, with the failure output (type it in full, with a reason)",
				"reject":    "end the Run Rejected (type the word and a reason)",
				"quit":      "end the Run Cancelled",
			},
			extends: map[string]string{"send back": "send_backs"},
		},
	}
}

// walk is the state the Run's walk of the graph keeps.
type walk struct {
	p         Params
	l         *ledger.Ledger
	g         pipeline.Graph
	limits    pipeline.Limits
	sendBacks int
	attempts  int // implementer Attempts so far
	check     int // the latest Check: the Verdict a Gate shows
}

// capped reports whether the Run's Attempt cap is reached. The cap is
// hard: nothing extends it yet.
// TODO(#51): `extend attempts +N` with a reason, at the Gate.
func (w *walk) capped() bool { return w.attempts >= w.limits.Attempts }

// exhausted reports whether bound is used up. A send-back also needs an
// Attempt under the cap.
func (w *walk) exhausted(bound string) bool {
	switch bound {
	case "send_backs":
		return w.sendBacks >= w.limits.SendBacks || w.capped()
	}
	return false
}

// request builds the open Gate from the graph: only the choices it
// offers, and a bounded one only while its bound lasts.
func (w *walk) request(node string, a *Attempt, oracleVersion int, cr *oracle.Result) (gate.Request, error) {
	spec, ok := gateSpecs(w.limits)[node]
	if !ok {
		return gate.Request{}, errNotBuilt{node}
	}
	if node == "gate.bound_exhaustion" && w.capped() {
		spec.what = fmt.Sprintf("The Check failed and the Attempt cap (%d per Run) is reached.", w.limits.Attempts)
	}
	r := gate.Request{
		Name: gateName(w.g, node), What: spec.what, Need: spec.need, Check: cr,
		Pins: gate.Pins{Gate: node, Attempt: a.ID, Candidate: a.Candidate, Oracle: oracleVersion, Verdicts: []int{w.check}},
	}
	for _, word := range w.g.Choices(node) {
		e, _ := w.g.Choice(node, word)
		if e.Bound != "" && w.exhausted(e.Bound) {
			continue
		}
		if e.To == "implement" && w.capped() {
			continue // no Attempt is left to send it back to
		}
		c, ok := gate.Choices[word]
		if !ok {
			return gate.Request{}, fmt.Errorf("choice %q has no entry rule", word)
		}
		c.Says = spec.says[word]
		if limit := spec.extends[word]; limit != "" {
			c.Key, c.Reason, c.Note, c.Extends = "", true, false, limit
		}
		r.Choices = append(r.Choices, c)
	}
	return r, nil
}

// errNotBuilt means the walk reached a node it can't enter yet.
type errNotBuilt struct{ node string }

func (e errNotBuilt) Error() string {
	return fmt.Sprintf("the walk reached %s, which isn't built yet", e.node)
}

// step is where following an edge through Gates got to: a send-back, the
// end of the Run, or a stop.
type step struct {
	edge     pipeline.Edge
	gate     string // the last Gate it passed or stopped at
	decision *gate.Decision
	stop     Outcome // set when the Run stops here
	why      []string
}

// follow takes e through every Gate it reaches until it comes to the end
// of the Run or back to the implementer.
func (w *walk) follow(ctx context.Context, e pipeline.Edge, a *Attempt, oracleVersion int, cr *oracle.Result) (step, error) {
	var s step
	for e.To != "end" && e.To != "implement" {
		r, err := w.request(e.To, a, oracleVersion, cr)
		var nb errNotBuilt
		if errors.As(err, &nb) {
			// TODO(#48): the Ambiguous-file Gate, and the walk into check
			// or verify after a promote or drop.
			return step{stop: InfrastructureStop, why: []string{nb.Error()}}, nil
		}
		if err != nil {
			return s, err
		}
		s.gate = e.To
		d, err := w.open(ctx, r)
		switch {
		case errors.Is(err, errParked):
			return step{gate: e.To, stop: Parked, why: []string{r.What}}, nil
		case ctx.Err() != nil, errors.Is(err, gate.ErrNoDecision):
			why := interrupted
			if ctx.Err() == nil {
				why = fmt.Sprintf("the %s Gate got no decision: the terminal closed", r.Name)
			}
			if err := w.l.Append(RecGateAbandoned, map[string]any{"pins": r.Pins, "why": why}); err != nil {
				return s, err
			}
			return step{gate: e.To, stop: InfrastructureStop, why: []string{why}}, nil
		case err != nil:
			return s, err
		}
		s.decision = &d
		next, ok := w.g.Choice(e.To, d.Choice)
		if !ok {
			return s, fmt.Errorf("the %s Gate has no edge for %q", r.Name, d.Choice)
		}
		e = next
	}
	s.edge = e
	return s, nil
}

func gateName(g pipeline.Graph, node string) string {
	for _, n := range g.Nodes {
		if n.ID == node {
			return n.Label
		}
	}
	return node
}

// errParked means an unattended Run reached a mandatory Gate.
var errParked = errors.New("parked")

// open records the Gate as open, then waits for its decision and records
// that before returning it. Unattended, there is nobody to decide: the
// Run parks (ADR-0008).
func (w *walk) open(ctx context.Context, r gate.Request) (gate.Decision, error) {
	var words []string
	for _, c := range r.Choices {
		words = append(words, c.Word)
	}
	if err := w.l.Append(RecGateOpened, map[string]any{"pins": r.Pins, "choices": words}); err != nil {
		return gate.Decision{}, err
	}
	if w.p.Gates == nil {
		return gate.Decision{}, errParked
	}
	d, err := w.p.Gates.Decide(ctx, r)
	if err != nil {
		return d, err
	}
	// Pinned and durable before it takes effect; the actor is a kind,
	// never an identity (ADR-0006).
	rec := map[string]any{"pins": r.Pins, "actor": "human", "choice": d.Choice, "reason": d.Reason, "note": d.Note}
	for _, c := range r.Choices {
		if c.Word == d.Choice && c.Extends != "" {
			d.Extends = c.Extends
			rec["extension"] = map[string]any{"limit": c.Extends, "by": 1}
		}
	}
	if err := w.l.Append(RecGateDecided, rec); err != nil {
		return d, err
	}
	w.p.Observe(Event{Kind: EvDecided, Gate: &r, Decision: &d})
	return d, nil
}

// sendBackTurn is the implementer's turn after a send-back: the Task, then
// why the Candidate came back.
// TODO(#44): Öge's Briefing builder takes this over. In Fast mode the
// whole Oracle is visible, so its failure output may reach the
// implementer; held-out output never may.
func sendBackTurn(task string, cr *oracle.Result, blobs *ledger.Blobs, d *gate.Decision) string {
	var b strings.Builder
	b.WriteString(task)
	b.WriteString("\n\n---\nÖge sent your Candidate back.")
	if d != nil {
		text := d.Reason + d.Note
		if text != "" {
			fmt.Fprintf(&b, " A human said: %s", strings.TrimSpace(text))
		}
	}
	if cr != nil && !cr.Pass {
		b.WriteString("\nÖge's Check failed on it:\n")
		b.WriteString(failureOutput(cr, blobs))
	}
	return b.String()
}

// failureTail bounds each command's output in a send-back turn.
const failureTail = 4 << 10

// failureOutput is what failed in a Check: each failing command, its
// failed tests and the tail of its output.
func failureOutput(cr *oracle.Result, blobs *ledger.Blobs) string {
	var b strings.Builder
	if cr.Setup != nil && !cr.Setup.Pass {
		fmt.Fprintf(&b, "- setup %q failed (%s)\n", cr.Setup.Run, cr.Setup.Why)
	}
	if cr.Why != "" {
		fmt.Fprintf(&b, "- %s\n", cr.Why)
	}
	for _, e := range cr.Commands {
		if e.Pass {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", e.Run, e.Why)
		if e.Report != nil && len(e.Report.FailedTests) > 0 {
			fmt.Fprintf(&b, "  failed tests: %s\n", strings.Join(e.Report.FailedTests, ", "))
		}
		for _, o := range []oracle.Output{e.Stdout, e.Stderr} {
			if o.Blob == "" {
				continue
			}
			raw, err := blobs.Get(o.Blob)
			if err != nil {
				continue
			}
			if e.Report != nil && e.Report.Format == oracle.ReportGoTestJSON {
				raw = goTestOutput(raw)
			}
			if len(raw) > failureTail {
				raw = raw[len(raw)-failureTail:]
			}
			if s := strings.TrimSpace(string(raw)); s != "" {
				b.WriteString(s)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// goTestOutput is the text of `go test -json` output events.
func goTestOutput(raw []byte) []byte {
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev struct{ Action, Output string }
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Action == "output" {
			out.WriteString(ev.Output)
		}
	}
	return out.Bytes()
}
