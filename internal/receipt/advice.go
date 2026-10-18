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

// cutAdvice reads the Consultations a cut's RunStarted recorded at cut
// time, cleaned for display.
func cutAdvice(a run.CutAdvice) (sent []AdviceSent, dismissed []Dismissed) {
	for _, s := range a.Sent {
		s.Consultation, s.How, s.Agent, s.Model = clean(s.Consultation), clean(s.How), clean(s.Agent), clean(s.Model)
		sent = append(sent, s)
	}
	for _, d := range a.Dismissed {
		d.Consultation, d.Agent, d.Model, d.Outcome = clean(d.Consultation), clean(d.Agent), clean(d.Model), clean(d.Outcome)
		dismissed = append(dismissed, d)
	}
	return sent, dismissed
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
	if len(c.Advice) == 0 {
		return nil
	}
	tail := func(a AdviceSent) string {
		if a.How == "edited" {
			return " · edited by you, sent as your turn"
		}
		return " · a Claim, not checked"
	}
	one := func(a AdviceSent) string {
		return fmt.Sprintf("sent at turn %d (%s, fresh)%s", a.Turn, advisorName(a.Agent, a.Model), tail(a))
	}
	if len(c.Advice) == 1 {
		return []Line{{"Advice", "1 consultation " + one(c.Advice[0]), Plain}}
	}
	ls := []Line{{"Advice", fmt.Sprintf("%d consultations", len(c.Advice)), Plain}}
	for _, a := range c.Advice {
		ls = append(ls, Line{"", one(a), Plain})
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
		out := map[string]string{"advised": "not sent by this cut", "cancelled": "cancelled", "failed": "failed"}[d.Outcome]
		if out == "" {
			out = val(d.Outcome)
		}
		ls = append(ls, Line{label, fmt.Sprintf("consultation asked at turn %d (%s): %s", d.Turn, advisorName(d.Agent, d.Model), out), Plain})
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
