package receipt

import (
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/run"
)

// AdviceSent is a Consultation's advice that reached the agent inside a
// cut's turns (docs/ux/advisor.md). The advice itself is another model's
// text: a Claim, never Evidence and never part of the Task, so the
// Receipt names that it was sent and by whom, and not what it said.
type AdviceSent = run.AdviceSent

// Dismissed is a Consultation that ended without its advice being sent.
// It changed no work, so only the full Receipt lists it.
type Dismissed = run.Dismissed

// Shown is an ask Consultation whose advice reached you on the agent's
// question, not the agent. Still a Claim; the compact Receipt lists it
// because advice you saw before answering is worth one line.
type Shown = run.Shown

// cutAdvice reads the Consultations a cut's RunStarted recorded at cut
// time, cleaned for display.
func cutAdvice(a run.CutAdvice) (sent []AdviceSent, dismissed []Dismissed, shown []Shown) {
	for _, s := range a.Sent {
		s.Consultation, s.How, s.Agent, s.Model = clean(s.Consultation), clean(s.How), clean(s.Agent), clean(s.Model)
		sent = append(sent, s)
	}
	for _, d := range a.Dismissed {
		d.Consultation, d.Agent, d.Model, d.Outcome = clean(d.Consultation), clean(d.Agent), clean(d.Model), clean(d.Outcome)
		dismissed = append(dismissed, d)
	}
	for _, h := range a.Shown {
		h.Consultation, h.Agent, h.Model = clean(h.Consultation), clean(h.Agent), clean(h.Model)
		shown = append(shown, h)
	}
	return sent, dismissed, shown
}

// advisorName is the advisor's agent and model as one value.
func advisorName(agent, model string) string {
	if who := strings.TrimSpace(agent + " " + model); who != "" {
		return val(who)
	}
	return "advisor"
}

// adviceLines is the Receipt's disclosure of advice that reached the
// agent. Verbatim advice is a Claim, so it says so; edited advice is the
// user's own turn (D-A4), so it says that.
func (c *ChatCut) adviceLines() []Line {
	if len(c.Advice)+len(c.Shown) == 0 {
		return nil
	}
	tail := func(a AdviceSent) string {
		if a.How == "edited" {
			return " · edited by you, sent as your turn"
		}
		return " · a Claim, not checked"
	}
	one := func(a AdviceSent) string {
		fresh := "fresh"
		if a.Trigger == run.TriggerFeedbackBudget {
			fresh += ", automatic"
		}
		return fmt.Sprintf("sent at turn %d (%s, %s)%s", a.Turn, advisorName(a.Agent, a.Model), fresh, tail(a))
	}
	var items []string
	for _, a := range c.Advice {
		items = append(items, one(a))
	}
	for _, h := range c.Shown {
		items = append(items, fmt.Sprintf("on the agent's question %s (%s): shown to you · a Claim, not checked", askedAt(h.Turn, h.Before), advisorName(h.Agent, h.Model)))
	}
	if len(items) == 1 {
		return []Line{{"Advice", "1 consultation " + items[0], Plain}}
	}
	ls := []Line{{"Advice", fmt.Sprintf("%d consultations", len(items)), Plain}}
	for _, it := range items {
		ls = append(ls, Line{"", it, Plain})
	}
	return ls
}

// dismissedLines are the Consultations asked in this cut's turns that
// ended without their advice being sent: for the full Receipt only.
func (c *ChatCut) dismissedLines() []Line {
	var ls []Line
	for i, d := range c.Dismissed {
		label := ""
		if i == 0 {
			label = "Dismissed"
		}
		out := map[string]string{"advised": "not sent by this cut", "cancelled": "cancelled", "failed": "failed", run.OutcomeNotShown: "advice never shown to you"}[d.Outcome]
		if out == "" {
			out = val(d.Outcome)
		}
		who := advisorName(d.Agent, d.Model)
		if d.Trigger == run.TriggerFeedbackBudget {
			who += ", automatic"
		}
		ls = append(ls, Line{label, fmt.Sprintf("consultation asked %s (%s): %s", askedAt(d.Turn, d.Before), who, out), Plain})
	}
	return ls
}

// FullText is Text with the lines only the full Receipt shows: the
// dismissed Consultations.
func (r *Receipt) FullText(p Paint) string {
	f := *r
	f.full = true
	return f.Text(p)
}

// askedAt is when a Consultation was asked: a Before one was asked after
// its turn was cut, so it says it came before the cut's first turn.
func askedAt(turn int, before bool) string {
	if before {
		return fmt.Sprintf("before turn %d", turn)
	}
	return fmt.Sprintf("at turn %d", turn)
}
