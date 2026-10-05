package run

import (
	"context"
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/workspace"
)

// The Ambiguous-file review (#97, ADR-0013, ADR-0019 #2): new files no
// output or test glob covers stay in the Candidate until a human promotes
// or drops each one, in one batch review at the end of the Run. A Run with
// one unresolved can't be Accepted, and an unattended one parks.

const gateAmbiguous = "gate.ambiguous_file"

// RecAmbiguousResolved records what one promote or drop did: the files,
// the Candidate it applied to and the new Candidate it made. It follows
// the GateDecided record that decided it.
const RecAmbiguousResolved = "AmbiguousResolved"

// Resolution is one promote or drop at the Ambiguous-file review.
type Resolution struct {
	Choice string // promote | drop
	Files  []string
	// From is the Candidate it applied to; Candidate the new one.
	From, Candidate string
}

// promotedNew reports whether a new file reaches judged roles: it matches
// an output or test glob, or a human promoted it this Run. No location or
// extension heuristic promotes anything (ADR-0019). A promotion holds by
// path for the rest of the Run (GLOSSARY: Ambiguous file). A new file
// matching the test-config globs never gets here: the implementer's scope
// check reverts it.
func (w *walk) promotedNew(p string) bool {
	pr := w.p.Frozen.Project
	return oracle.MatchAny(pr.OutputGlobs, p) || oracle.MatchAny(pr.TestGlobs, p) || w.promoted[p]
}

// classify finds cand's Ambiguous files: the review's to resolve.
func (w *walk) classify(cand string) error {
	_, withheld, err := w.repo.Classify(w.snap, cand, w.promotedNew)
	if err != nil {
		return err
	}
	w.ambiguous = w.ambiguous[:0]
	for _, f := range withheld {
		if f.Class == workspace.ClassAmbiguous {
			w.ambiguous = append(w.ambiguous, f)
		}
	}
	return nil
}

func (w *walk) ambiguousPaths() []string {
	var out []string
	for _, f := range w.ambiguous {
		out = append(out, f.Path)
	}
	return out
}

// ambiguousRequest words the review for the files still unresolved, and
// lets the views inspect them in cand.
func (w *walk) ambiguousRequest(r *gate.Request, cand string) {
	files := w.ambiguousPaths()
	n := len(files)
	r.What = fmt.Sprintf("%s not covered by the declared output/test globs:", pluralFiles(n, "was", "were"))
	r.Need = fmt.Sprintf("%s a decision", pluralFiles(n, "needs", "need"))
	r.Files, r.Pins.Files = files, files
	blobs := map[string]string{}
	for _, f := range w.ambiguous {
		blobs[f.Path] = f.Hash
	}
	r.Inspect = func(p string) ([]byte, error) {
		oid, ok := blobs[p]
		if !ok {
			return nil, fmt.Errorf("%s isn't one of the files shown", p)
		}
		// Sized before it is read: a huge file is named, never loaded.
		n, err := w.repo.BlobSize(oid)
		if err != nil {
			return nil, err
		}
		if n > InspectMaxBytes {
			return nil, fmt.Errorf("%d bytes, too large to show here; oge diff shows the Candidate once the Run ends", n)
		}
		return w.repo.Blob(oid)
	}
}

// InspectMaxBytes bounds the file an inspect view reads.
const InspectMaxBytes = 1 << 20

func pluralFiles(n int, one, many string) string {
	if n == 1 {
		return "1 new file " + one
	}
	return fmt.Sprintf("%d new files %s", n, many)
}

// review is the Ambiguous-file review: it stays open while any file is
// unresolved, each promote or drop written ahead as its own decision and
// making a new Candidate. Once none is left the walk goes on, from the
// resolved Candidate, to a fresh QA pass if anything was promoted (when
// the Pipeline has a verifier), and the final Check.
// It routes once, after every file is resolved, so a mixed selection costs
// one QA pass and one Check (GLOSSARY: Ambiguous-file review).
func (w *walk) review(ctx context.Context, a *Attempt, oracleVersion int, cr *oracle.Result) (step, error) {
	// How often the review fires, for the evaluation: if it is common,
	// fix the globs `oge init` proposes rather than the Gate.
	if err := w.l.Append(RecObservation, map[string]any{"kind": "ambiguous_review", "count": len(w.ambiguous),
		"files": w.ambiguousPaths(), "candidate": a.Candidate, "check": w.check}); err != nil {
		return step{}, err
	}
	cur := *a
	promoted := false
	s := step{gate: gateAmbiguous}
	for len(w.ambiguous) > 0 {
		r, err := w.request(gateAmbiguous, &cur, oracleVersion, cr)
		if err != nil {
			return step{}, err
		}
		if cur.Candidate != a.Candidate {
			// The Verdicts shown were reached on the checked Candidate;
			// this round decides on a later one.
			r.Pins.Checked = a.Candidate
		}
		d, stop, err := w.decide(ctx, gateAmbiguous, r)
		if err != nil {
			return step{}, err
		}
		if stop != nil {
			stop.candidate = cur.Candidate
			return *stop, nil
		}
		s.decision = &d
		if d.Choice != "promote" && d.Choice != "drop" {
			e, ok := w.g.Choice(gateAmbiguous, d.Choice)
			if !ok {
				return step{}, fmt.Errorf("the %s Gate has no edge for %q", r.Name, d.Choice)
			}
			s.edge, s.candidate = e, cur.Candidate
			return s, nil
		}
		var drop []string
		if d.Choice == "drop" {
			drop = d.Files
		} else {
			promoted = true
			for _, f := range d.Files {
				w.promoted[f] = true
			}
		}
		next, err := w.repo.Resolve(cur.Candidate, drop, fmt.Sprintf("%s %s", d.Choice, strings.Join(d.Files, " ")))
		if err != nil {
			return step{}, err
		}
		res := Resolution{Choice: d.Choice, Files: d.Files, From: cur.Candidate, Candidate: next}
		if err := w.l.Append(RecAmbiguousResolved, map[string]any{"choice": d.Choice, "files": d.Files,
			"from": cur.Candidate, "candidate": next, "attempt": cur.ID}); err != nil {
			return step{}, err
		}
		w.resolved = append(w.resolved, res)
		cur.Candidate = next
		if err := w.classify(next); err != nil {
			return step{}, err
		}
	}
	word := "drop"
	if promoted {
		word = "promote"
	}
	e, ok := w.g.Choice(gateAmbiguous, word)
	if !ok {
		return step{}, fmt.Errorf("the frozen graph has no %s edge from the Ambiguous-file Gate", word)
	}
	s.edge, s.candidate = e, cur.Candidate
	return s, nil
}

// checkSelection refuses a batch decision naming no file, or one the Gate
// didn't show: Öge never resolves a file nobody chose.
func checkSelection(r gate.Request, d gate.Decision) error {
	for _, c := range r.Choices {
		if c.Word != d.Choice || !c.Selects {
			continue
		}
		if len(d.Files) == 0 {
			return fmt.Errorf("%s at the %s Gate names no files", d.Choice, r.Name)
		}
		shown := map[string]bool{}
		for _, f := range r.Files {
			shown[f] = true
		}
		for _, f := range d.Files {
			if !shown[f] {
				return fmt.Errorf("%s at the %s Gate names %q, which it didn't show", d.Choice, r.Name, f)
			}
			delete(shown, f) // and never twice
		}
	}
	return nil
}
