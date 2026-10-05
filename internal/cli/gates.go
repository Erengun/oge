package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/run"
)

// The Gate texts below are shared by both views (ADR-0022).

func sendBackText(ev run.Event) string {
	return fmt.Sprintf("%d of %d · the Candidate goes back to the implementer with the failure output", ev.SendBack, ev.SendBacks)
}

// decidedText echoes a decision once it is recorded (ADR-0015).
func decidedText(ev run.Event) string {
	s := fmt.Sprintf("%s · recorded at the %s Gate", ev.Decision.Choice, ev.Gate.Name)
	if ev.Decision.Reason != "" {
		s += " · reason: " + ev.Decision.Reason
	}
	if ev.Decision.Note != "" {
		s += " · note: " + ev.Decision.Note
	}
	return clean(s)
}

// gateScreen is an open Gate: what happened, what is pinned, what is
// needed and the choices. There is no default.
func gateScreen(r gate.Request) []string {
	lines := []string{fmt.Sprintf("%s Gate", r.Name), clean(r.What)}
	if r.Check != nil {
		for _, l := range checkLines(r.Check) {
			lines = append(lines, "  "+l)
		}
	}
	lines = append(lines, pinsLine(r.Pins), "", clean(r.Need))
	for _, c := range r.Choices {
		key := c.Word
		if c.Key != "" {
			key = c.Key + "  " + c.Word
		}
		lines = append(lines, fmt.Sprintf("  %-13s %s", key, c.Says))
	}
	return lines
}

func pinsLine(p gate.Pins) string {
	var v []string
	for _, n := range p.Verdicts {
		v = append(v, fmt.Sprintf("#%d", n))
	}
	cand := p.Candidate
	if len(cand) > 7 {
		cand = cand[:7]
	}
	return fmt.Sprintf("Candidate %s · Attempt %s · Oracle v%d · Verdicts %s", cand, p.Attempt, p.Oracle, strings.Join(v, " "))
}

// gateHint is the line under the choices.
const gateHint = "There is no default: type a choice and press Enter."

// parkedSummary is a parked Run's end.
func (r *renderer) parkedSummary(res *run.Result) {
	r.p("")
	r.p("%-10s at the %s Gate · Candidate %s · Oracle v%d · %s", "PARKED", gateLabel(res), short(res.Candidate), res.Oracle, res.Duration.Round(100*time.Millisecond))
	for _, w := range res.Why {
		r.p("  %s", clean(w))
	}
	r.p("  Unattended Runs never decide a Gate; a human must (exit 10).")
	r.p("Nothing was written to your repository.")
}

func gateLabel(res *run.Result) string {
	if res.Gate == "" {
		return "?"
	}
	return strings.ReplaceAll(strings.TrimPrefix(res.Gate, "gate."), "_", "-")
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}
