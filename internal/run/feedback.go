package run

import (
	"fmt"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
)

// FeedbackBudget is how many feedback turns a chain of cuts gets (D5).
// A chain starts with the user's own turn: one resets it.
// TODO(#262-decision): the chain resets on the user's turn, not at
// delivery, which is simpler and needs nothing from the delivery path.
const FeedbackBudget = 3

// feedbackTurn is the turn Öge sends a Chat's Session when a cut's Check
// fails (D5): verdictFailures, its first line the failing count and
// criterion ids, then what failed, through the held-out filter a
// send-back uses (ADR-0009): never a held-out test's name, file, source
// or output.
func feedbackTurn(failures string) string {
	return failures + "Fix it and carry on with the task."
}

// verdictFailures is what a failing Check failed on, through the
// held-out filter: a feedback turn's body, and what an Advisor is told of
// the newest Verdict (docs/ux/advisor.md).
func verdictFailures(cr *oracle.Result, blobs *ledger.Blobs, m *oracle.Manifest, candidate func() []string) string {
	hf := newHeldOutFilter(m, cr, blobs, candidate)
	var b strings.Builder
	b.WriteString(feedbackSummary(cr, hf) + "\n")
	b.WriteString(failureOutput(cr, blobs, hf))
	return b.String()
}

// feedbackSummary is a feedback turn's first line: how many Oracle tests
// didn't pass, and the acceptance criteria the failing held-out ones
// name. Only a count and ids: the tests' names include held-out ones.
func feedbackSummary(cr *oracle.Result, hf *heldOutFilter) string {
	s := "Öge's Check failed on your last change"
	n := len(cr.Missing)
	if len(cr.Tests) > 0 {
		n = 0 // the attested tests, as the Receipt counts them
		for _, t := range cr.Tests {
			if t.Attested != "pass" && t.Attested != "skip" && t.Excluded == "" {
				n++
			}
		}
	}
	if n > 0 {
		s += fmt.Sprintf(": %d failing", n)
	}
	seen := map[string]bool{}
	var ids []string
	if hf != nil {
		for _, cs := range hf.failing {
			for _, c := range cs {
				if !seen[c] {
					seen[c] = true
					ids = append(ids, c)
				}
			}
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		s += " (criteria " + strings.Join(ids, ", ") + ")"
	}
	return s + "."
}
