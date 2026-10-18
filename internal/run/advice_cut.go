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
}

// Dismissed is a Consultation that ended without its advice being sent.
type Dismissed struct {
	Consultation string `json:"consultation"`
	Turn         int    `json:"turn"` // the last turn started when it was asked
	Agent        string `json:"agent,omitempty"`
	Model        string `json:"model,omitempty"`
	// Outcome is the Consultation's: advised (and not sent), cancelled or failed.
	Outcome string `json:"outcome"`
}

func (a CutAdvice) empty() bool { return len(a.Sent) == 0 && len(a.Dismissed) == 0 }

// consultationsIn reads recs, a Chat log, for turns from to to. A
// Consultation still running has no outcome yet, so it is neither.
func consultationsIn(recs []ledger.Record, chat string, from, to int) CutAdvice {
	type consult struct {
		turn            int
		agent, model    string
		outcome, advice string
		sent            bool
	}
	byID := map[string]*consult{}
	var order []string
	var sends []AdviceSent
	for _, rec := range recs {
		switch rec.Type {
		case RecConsultationStarted, RecConsultationEnded, RecAdviceSent:
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
			byID[d.Consultation] = &consult{turn: d.Turn, agent: d.Advisor.Agent, model: d.Advisor.Model}
		case RecConsultationEnded:
			if c := byID[d.Consultation]; c != nil {
				c.outcome, c.advice = d.Outcome, d.Advice
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
			s.Agent, s.Model, s.Advice = c.agent, c.model, c.advice
		}
		out.Sent = append(out.Sent, s)
	}
	for _, id := range order {
		if c := byID[id]; !c.sent && c.outcome != "" && c.turn >= from && c.turn <= to {
			out.Dismissed = append(out.Dismissed, Dismissed{Consultation: id, Turn: c.turn, Agent: c.agent, Model: c.model, Outcome: c.outcome})
		}
	}
	return out
}

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
