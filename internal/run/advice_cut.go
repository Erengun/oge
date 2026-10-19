package run

import (
	"encoding/json"

	"github.com/erengun/oge/internal/ledger"
)

// CutAdvice is what a cut's RunStarted records of the Chat's Consultations
// (docs/ux/advisor.md): the advice that reached the agent in the Task's
// turns, and the Consultations asked in them that ended without it. It is
// a snapshot at cut time, so the Receipt reads the Ledger only, and a
// later Consultation never changes an earlier cut's Receipt. The advice
// itself is a Claim: only its blob id is kept, never its text.
type CutAdvice struct {
	Sent      []AdviceSent `json:"sent,omitempty"`
	Dismissed []Dismissed  `json:"dismissed,omitempty"`
	// Shown are ask Consultations whose advice reached the human, not the
	// agent: no AdviceSent, so neither Sent nor Dismissed.
	Shown []Shown `json:"shown,omitempty"`
}

// Shown is an ask Consultation (the agent's question, advice shown to the
// human) that landed with advice. A Claim: the human may have followed it.
type Shown struct {
	Consultation string `json:"consultation"`
	Turn         int    `json:"turn"`
	Agent        string `json:"agent,omitempty"`
	Model        string `json:"model,omitempty"`
	Advice       string `json:"advice,omitempty"` // the advice blob's id
	// Before: asked after the turn was cut (not in a turn of this cut), so
	// Turn is the cut's first, and the Receipt says "before" it.
	Before bool `json:"before,omitempty"`
}

// AdviceSent is a Consultation's advice that became a turn.
type AdviceSent struct {
	Consultation string `json:"consultation"`
	Turn         int    `json:"turn"`
	// How is "verbatim" (an Öge turn) or "edited" (the user's own turn).
	How    string `json:"how"`
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Advice string `json:"advice,omitempty"` // the advice blob's id
	// Trigger is why it ran ("user", "feedback_budget", "ask"); the Receipt
	// says an automatic one was.
	Trigger string `json:"trigger,omitempty"`
}

// Dismissed is a Consultation that ended without its advice being sent.
type Dismissed struct {
	Consultation string `json:"consultation"`
	Turn         int    `json:"turn"` // the last turn started when it was asked
	Agent        string `json:"agent,omitempty"`
	Model        string `json:"model,omitempty"`
	// Outcome is the Consultation's: advised (and not sent), cancelled or failed.
	Outcome string `json:"outcome"`
	Trigger string `json:"trigger,omitempty"`
	Before  bool   `json:"before,omitempty"` // as Shown.Before
}

// resolvedIDs are the Consultations this snapshot counted as Dismissed or
// Shown: a Sent one is counted by its AdviceSent's turn, never twice.
func (a CutAdvice) resolvedIDs() []string {
	var ids []string
	for _, d := range a.Dismissed {
		ids = append(ids, d.Consultation)
	}
	for _, h := range a.Shown {
		ids = append(ids, h.Consultation)
	}
	return ids
}

func (a CutAdvice) empty() bool {
	return len(a.Sent) == 0 && len(a.Dismissed) == 0 && len(a.Shown) == 0
}

// consultationsIn reads recs, a Chat log, for turns from to to. A
// Consultation still running has no outcome yet, so it is neither. One
// that no delivered cut's snapshot counted (a Delivered record lists the
// ids it resolved) and whose turn is at or before the delivered one was in
// flight at that cut, or asked after it: it belongs to the next cut's
// window (turn+1, Before), or no cut would ever show it.
func consultationsIn(recs []ledger.Record, chat string, from, to int) CutAdvice {
	type consult struct {
		turn            int
		before          bool
		agent, model    string
		trigger         string
		outcome, advice string
		sent            bool
		dismissed       bool // the user dismissed its advice (AdviceDismissed)
		shown           bool // the TUI displayed its ask advice (AdviceShown)
	}
	byID := map[string]*consult{}
	var order []string
	var sends []AdviceSent
	delivered := 0
	counted := map[string]bool{} // resolved by a delivered cut's snapshot
	for _, rec := range recs {
		if rec.Type == RecDelivered {
			var d struct {
				Turn     int
				Resolved []string
			}
			if json.Unmarshal(rec.Data, &d) == nil {
				delivered = max(delivered, d.Turn)
				for _, id := range d.Resolved {
					counted[id] = true
				}
			}
		}
	}
	for _, rec := range recs {
		switch rec.Type {
		case RecConsultationStarted, RecConsultationEnded, RecAdviceSent, RecAdviceDismissed, RecAdviceShown:
		default:
			continue
		}
		var d struct {
			Chat         string `json:"chat"`
			Consultation string `json:"consultation"`
			Turn         int    `json:"turn"`
			How          string `json:"how"`
			Outcome      string `json:"outcome"`
			Advice       string `json:"advice"`
			Trigger      string `json:"trigger"`
			Advisor      struct{ Agent, Model string }
		}
		if json.Unmarshal(rec.Data, &d) != nil || d.Consultation == "" || (d.Chat != "" && d.Chat != chat) {
			continue
		}
		switch rec.Type {
		case RecConsultationStarted:
			if byID[d.Consultation] == nil {
				order = append(order, d.Consultation)
			}
			turn, before := d.Turn, false
			if turn > 0 && turn <= delivered && !counted[d.Consultation] {
				turn, before = delivered+1, true
			}
			byID[d.Consultation] = &consult{turn: turn, before: before, agent: d.Advisor.Agent, model: d.Advisor.Model, trigger: d.Trigger}
		case RecConsultationEnded:
			if c := byID[d.Consultation]; c != nil {
				c.outcome, c.advice = d.Outcome, d.Advice
			}
		case RecAdviceDismissed:
			if c := byID[d.Consultation]; c != nil {
				c.dismissed = true
			}
		case RecAdviceShown:
			if c := byID[d.Consultation]; c != nil {
				c.shown = true
			}
		case RecAdviceSent:
			if d.Turn >= from && d.Turn <= to {
				sends = append(sends, AdviceSent{Consultation: d.Consultation, Turn: d.Turn, How: d.How})
			}
			if c := byID[d.Consultation]; c != nil {
				c.sent = true
			}
		}
	}
	var out CutAdvice
	for _, s := range sends {
		if c := byID[s.Consultation]; c != nil {
			s.Agent, s.Model, s.Advice, s.Trigger = c.agent, c.model, c.advice, c.trigger
		}
		out.Sent = append(out.Sent, s)
	}
	for _, id := range order {
		c := byID[id]
		if c.sent || c.outcome == "" || c.turn < from || c.turn > to {
			continue
		}
		// A /advise Consultation advised but neither sent nor dismissed still
		// waits for the user: not this cut's to count, or a later cut would
		// count it again as Sent or Dismissed.
		if c.trigger == "user" && c.outcome == ConsultAdvised && !c.dismissed {
			continue
		}
		if c.trigger == TriggerAsk && c.outcome == ConsultAdvised && !c.shown {
			out.Dismissed = append(out.Dismissed, Dismissed{Consultation: id, Turn: c.turn, Agent: c.agent, Model: c.model, Outcome: OutcomeNotShown, Trigger: c.trigger, Before: c.before})
			continue
		}
		if c.trigger == TriggerAsk && c.outcome == ConsultAdvised {
			out.Shown = append(out.Shown, Shown{Consultation: id, Turn: c.turn, Agent: c.agent, Model: c.model, Advice: c.advice, Before: c.before})
			continue
		}
		out.Dismissed = append(out.Dismissed, Dismissed{Consultation: id, Turn: c.turn, Agent: c.agent, Model: c.model, Outcome: c.outcome, Trigger: c.trigger, Before: c.before})
	}
	return out
}

// OutcomeNotShown is a Dismissed outcome: an ask Consultation advised, but
// the TUI never displayed its advice (no AdviceShown).
const OutcomeNotShown = "not shown"

// chatAdvice is consultationsIn over the Chat's log now.
func (c *Chat) chatAdvice(from, to int) (CutAdvice, error) {
	c.logMu.Lock()
	recs, err := ledger.Replay(c.Dir)
	c.logMu.Unlock()
	if err != nil {
		return CutAdvice{}, err
	}
	return consultationsIn(recs, c.ID, from, to), nil
}
