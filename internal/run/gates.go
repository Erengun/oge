package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/briefing"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/workspace"
)

// Outcomes and statuses a Gate can end a Run with.
const (
	Cancelled  Outcome = "Cancelled"
	Overridden Outcome = "Overridden"
	Infeasible Outcome = "Infeasible"
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
		"gate.tamper": {
			what: "Öge's Check passed, but an Attempt wrote to a protected file. Öge reverted it and recorded a Tamper event, which must be acknowledged before the Run can be Accepted.",
			need: "a protected file change was reverted",
			says: map[string]string{
				"acknowledge": "accept that the change was reverted; the passing Check then decides (type the word and a reason)",
				"reject":      "end the Run Rejected (type the word and a reason)",
				"quit":        "end the Run Cancelled",
			},
			// TODO(#49): dismiss, inspect views and batching with the
			// Ambiguous-file review.
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
		// What and need are worded for its files (ambiguousRequest).
		gateAmbiguous: {
			says: map[string]string{
				"promote": "selected/all join the Candidate: fresh QA, then the final Check",
				"drop":    "selected/all leave the Candidate, then the final Check",
				"reject":  "end the Run Rejected (type the word and a reason)",
				"quit":    "end the Run Cancelled",
			},
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
	attempts  int           // implementer Attempts so far
	tamper    []tamperEvent // Tamper events so far
	acked     int           // how many of them are acknowledged
	check     int           // the latest Check: the Verdict a Gate shows

	// The Ambiguous-file review's state (ambiguous.go).
	repo      *workspace.RunRepo
	snap      string
	promoted  map[string]bool      // new files a human promoted this Run
	ambiguous []workspace.Withheld // the latest Candidate's Ambiguous files
	resolved  []Resolution         // every promote and drop so far
}

// tamperEvent is one Tamper event as a Gate shows it.
type tamperEvent struct{ id, path, change string }

// addTamper keeps a's Tamper events, with the ids recordTamper gave them.
func (w *walk) addTamper(a *Attempt) {
	n := 0
	for _, r := range a.Reverted {
		if r.Tamper {
			n++
			w.tamper = append(w.tamper, tamperEvent{fmt.Sprintf("%s/tamper-%d", a.ID, n), r.Path, r.Change})
		}
	}
}

// unacknowledged are the Tamper events no decision has acknowledged.
func (w *walk) unacknowledged() []tamperEvent { return w.tamper[w.acked:] }

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
	if node == gateAmbiguous {
		w.ambiguousRequest(&r, a.Candidate)
		if e, _ := w.g.Choice(node, "promote"); e.To == "check" {
			// No verifier: a promoted file goes straight to the final Check.
			spec.says["promote"] = "selected/all join the Candidate, then the final Check"
		}
	}
	if node == "gate.tamper" {
		for _, t := range w.unacknowledged() {
			r.Detail = append(r.Detail, fmt.Sprintf("reverted: %s (%s)", t.path, t.change))
			r.Pins.Tamper = append(r.Pins.Tamper, t.id)
		}
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
	// candidate is the Candidate an Ambiguous-file review left: the walk
	// goes on from it.
	candidate string
}

// follow takes e through every Gate it reaches until it comes to the end
// of the Run or back to the implementer.
// holds is the check node's conditions for the latest Verdict.
func (w *walk) follow(ctx context.Context, e pipeline.Edge, a *Attempt, oracleVersion int, cr *oracle.Result, holds func(string) bool) (step, error) {
	var s step
	for e.To != "end" && e.To != "implement" {
		if e.From == "gate.tamper" && e.To == "check" {
			// Acknowledged: the same Verdict, on the same Candidate, takes
			// the check node's edges again.
			next, ok := w.g.Route("check", holds, w.exhausted)
			if !ok {
				return s, fmt.Errorf("the frozen graph has no edge for this Verdict")
			}
			e = next
			continue
		}
		if e.To == gateAmbiguous {
			// The review returns the promote or drop edge to the walk,
			// which goes on from the resolved Candidate.
			return w.review(ctx, a, oracleVersion, cr)
		}
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
		d, stop, err := w.decide(ctx, e.To, r)
		if err != nil {
			return s, err
		}
		if stop != nil {
			return *stop, nil
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

// decide opens r at node and waits for its decision. With nobody to
// decide, it returns the step the Run stops at: parked, or abandoned.
func (w *walk) decide(ctx context.Context, node string, r gate.Request) (gate.Decision, *step, error) {
	d, err := w.open(ctx, r)
	switch {
	case errors.Is(err, errParked):
		why := r.What
		if len(r.Files) > 0 {
			why = r.Need + ": " + strings.Join(r.Files, ", ")
		}
		return d, &step{gate: node, stop: Parked, why: []string{why}}, nil
	case ctx.Err() != nil, errors.Is(err, gate.ErrNoDecision):
		why := interrupted
		if ctx.Err() == nil {
			why = fmt.Sprintf("the %s Gate got no decision: the terminal closed", r.Name)
		}
		if err := w.l.Append(RecGateAbandoned, map[string]any{"pins": r.Pins, "why": why}); err != nil {
			return d, nil, err
		}
		return d, &step{gate: node, stop: InfrastructureStop, why: []string{why}}, nil
	}
	return d, nil, err
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
	if err := checkSelection(r, d); err != nil {
		return gate.Decision{}, err
	}
	// Pinned and durable before it takes effect; the actor is a kind,
	// never an identity (ADR-0006).
	rec := map[string]any{"pins": r.Pins, "actor": "human", "choice": d.Choice, "reason": d.Reason, "note": d.Note}
	if len(d.Files) > 0 {
		rec["files"] = d.Files
	}
	if d.Choice == "acknowledge" {
		// Written before it takes effect: only then do the events count
		// as acknowledged.
		rec["tamper_ids"] = r.Pins.Tamper
		defer func() { w.acked = len(w.tamper) }()
	}
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

// sendBackTurn is what a send-back adds to the implementer's Briefing:
// why the Candidate came back. Visible failures go back in full; held-out
// ones only as a count plus criterion ids (ADR-0009).
// TODO(#44): Öge's Briefing builder takes this over.
func sendBackTurn(cr *oracle.Result, blobs *ledger.Blobs, d *gate.Decision, m *oracle.Manifest) string {
	return sendBackTurnFrom(cr, blobs, d, m, nil)
}

// sendBackTurnFrom is sendBackTurn where candidate lists the Candidate's
// Go sources: a held-out helper whose name they (or the visible Oracle)
// also use is a common name, and doesn't hide visible output.
func sendBackTurnFrom(cr *oracle.Result, blobs *ledger.Blobs, d *gate.Decision, m *oracle.Manifest, candidate func() []string) string {
	var b strings.Builder
	b.WriteString("\n\n---\nÖge sent your Candidate back.")
	if d != nil {
		text := d.Reason + d.Note
		if text != "" {
			fmt.Fprintf(&b, " A human said: %s", strings.TrimSpace(text))
		}
	}
	if cr != nil && !cr.Pass {
		b.WriteString("\nÖge's Check failed on it:\n")
		b.WriteString(failureOutput(cr, blobs, newHeldOutFilter(m, cr, blobs, candidate)))
	}
	return b.String()
}

// heldOutFilter keeps held-out tests out of what the implementer is told:
// their names, files, source, assertions and output (ADR-0009).
type heldOutFilter struct {
	names   map[string]bool // held-out tests' names
	words   *regexp.Regexp  // names and file base names a line must not mention
	failing [][]string      // each failing held-out test's criterion ids
	// conflicts are packages holding held-out tests that didn't build.
	conflicts []string
	dropped   bool // some output was withheld
}

// newHeldOutFilter is nil when the Oracle has no held-out tests, so a
// Fast-mode send-back is exactly what it was.
func newHeldOutFilter(m *oracle.Manifest, cr *oracle.Result, blobs *ledger.Blobs, candidate func() []string) *heldOutFilter {
	if m == nil || len(m.HeldOut) == 0 {
		return nil
	}
	f := &heldOutFilter{names: map[string]bool{}}
	files := map[string]bool{}
	for _, h := range m.HeldOut {
		f.names[h.Test.Name] = true
		files[path.Base(h.File)] = true
	}
	var alts []string
	// What the implementer can already see: the visible Oracle and the
	// Candidate. A held-out helper named like anything there is a common
	// name, and filtering it would hide visible failures.
	var seen []string
	for _, t := range m.Tests {
		if !t.HeldOut {
			if src, err := blobs.Get(t.Blob); err == nil {
				seen = append(seen, string(src))
			}
		}
	}
	if candidate != nil {
		seen = append(seen, candidate()...)
	}
	common := func(name string) bool {
		re := regexp.MustCompile(`(^|[^\pL\pN_])` + regexp.QuoteMeta(name) + `($|[^\pL\pN_])`)
		for _, s := range seen {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}
	// Every held-out-only function a held-out file declares: its name in
	// a stack trace names held-out source too.
	for _, t := range m.Tests {
		if !t.HeldOut {
			continue
		}
		files[path.Base(t.Path)] = true
		if src, err := blobs.Get(t.Blob); err == nil {
			for _, n := range oracle.FuncNames(src) {
				if f.names[n] || !common(n) {
					alts = append(alts, regexp.QuoteMeta(n))
				}
			}
		}
	}
	for n := range f.names {
		alts = append(alts, regexp.QuoteMeta(n))
	}
	for n := range files {
		alts = append(alts, regexp.QuoteMeta(n))
	}
	sort.Strings(alts)
	// Whole words: a held-out TestNeg never hides a visible TestNegate.
	f.words = regexp.MustCompile(`(^|[^\pL\pN_])(` + strings.Join(alts, "|") + `)($|[^\pL\pN_])`)
	for _, h := range m.HeldOutFailures(cr) {
		f.failing = append(f.failing, h.Criteria)
	}
	f.conflicts = m.HeldOutBuildConflicts(cr)
	return f
}

// mentions reports whether s names a held-out test or file.
func (f *heldOutFilter) mentions(s string) bool {
	return f.words.MatchString(s)
}

// text drops the lines of s that name a held-out test or file.
func (f *heldOutFilter) text(s string) string {
	var keep []string
	for _, line := range strings.SplitAfter(s, "\n") {
		if f.mentions(line) {
			f.dropped = true
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "")
}

// goTest is the visible tests' and the package's own output text: every
// line a held-out test printed, or that names one, is left out.
func (f *heldOutFilter) goTest(raw []byte) []byte {
	var out bytes.Buffer
	for _, ol := range oracle.SplitGoTestOutput(raw) {
		if f.names[ol.Test.Name] || f.mentions(ol.Text) {
			if ol.Test.Name == "" {
				f.dropped = true
			}
			continue
		}
		out.WriteString(ol.Text)
	}
	return out.Bytes()
}

// failureTail bounds each command's output in a send-back turn.
const failureTail = 4 << 10

// failureOutput is what failed in a Check: each failing command, its
// failed tests and the tail of its output. With held-out tests in the
// Oracle, hf keeps them out of it and adds their count and criteria.
func failureOutput(cr *oracle.Result, blobs *ledger.Blobs, hf *heldOutFilter) string {
	var b strings.Builder
	if cr.Setup != nil && !cr.Setup.Pass {
		fmt.Fprintf(&b, "- setup %q failed (%s)\n", cr.Setup.Run, cr.Setup.Why)
	}
	why := cr.Why
	if i := strings.Index(why, "held-out: "); hf != nil && i >= 0 {
		why = strings.TrimSuffix(strings.TrimSpace(why[:i]), ";")
	}
	switch {
	case why == "":
	case hf != nil && len(cr.Missing) > 0:
		// The Oracle tests that never passed, minus the held-out ones.
		var visible []string
		for _, id := range cr.Missing {
			if !hf.mentions(id) {
				visible = append(visible, id)
			}
		}
		if len(visible) > 0 {
			fmt.Fprintf(&b, "- Oracle tests that never passed (%d): %s\n", len(visible), strings.Join(visible, ", "))
		}
	case hf != nil && hf.mentions(why):
		b.WriteString("- the Check failed on a held-out test path\n")
	default:
		fmt.Fprintf(&b, "- %s\n", why)
	}
	for _, e := range cr.Commands {
		if e.Pass || e.Part == oracle.PartHeldOut {
			// The held-out execution's output never goes back, only the
			// count and criterion ids below (ADR-0009).
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", e.Run, e.Why)
		if e.Report != nil && len(e.Report.FailedTests) > 0 {
			failed := e.Report.FailedTests
			if hf != nil {
				failed = nil
				for _, t := range e.Report.FailedTests {
					if top, _, _ := strings.Cut(t, "/"); !hf.names[top] {
						failed = append(failed, t)
					}
				}
			}
			if len(failed) > 0 {
				fmt.Fprintf(&b, "  failed tests: %s\n", strings.Join(failed, ", "))
			}
		}
		for _, o := range []oracle.Output{e.Stdout, e.Stderr} {
			if o.Blob == "" {
				continue
			}
			raw, err := blobs.Get(o.Blob)
			if err != nil {
				continue
			}
			switch {
			case hf != nil && e.Report != nil && e.Report.Format == oracle.ReportGoTestJSON && o == e.Stdout:
				raw = hf.goTest(raw)
			case e.Report != nil && e.Report.Format == oracle.ReportGoTestJSON:
				raw = goTestOutput(raw)
			}
			if hf != nil {
				raw = []byte(hf.text(string(raw)))
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
	if hf != nil && len(hf.failing) > 0 {
		b.WriteString("- " + briefing.HeldOutFeedback(hf.failing) + "\n")
	}
	if hf != nil {
		// Neither side is to blame without the other's source: say so,
		// neutrally (#46).
		for _, p := range hf.conflicts {
			b.WriteString("- Your change conflicts with a held-out test in package " + p + " (names withheld).\n")
		}
	}
	if hf != nil && hf.dropped {
		b.WriteString("- Some output names held-out tests, so Öge left it out.\n")
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
		// build-output carries the compiler's errors (go test -json, go1.24+).
		if json.Unmarshal(sc.Bytes(), &ev) == nil && (ev.Action == "output" || ev.Action == "build-output") {
			out.WriteString(ev.Output)
		}
	}
	return out.Bytes()
}

// goSources lists commit's Go files' contents, read when first needed.
func goSources(repo *workspace.RunRepo, commit string) func() []string {
	return func() []string {
		files, err := repo.Files(commit)
		if err != nil {
			return nil
		}
		var out []string
		for _, p := range files {
			if strings.HasSuffix(p, ".go") {
				if b, ok, err := repo.Show(commit, p); err == nil && ok {
					out = append(out, string(b))
				}
			}
		}
		return out
	}
}
