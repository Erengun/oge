package receipt

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// AdviceSent is a Consultation's advice that reached the agent inside a
// cut's turns (docs/ux/advisor.md). The advice itself is another model's
// text: a Claim, never Evidence and never part of the Task, so the
// Receipt names that it was sent and by whom, and not what it said.
type AdviceSent struct {
	Consultation string `json:"consultation"`
	Turn         int    `json:"turn"`
	// How is "verbatim" (an Öge turn) or "edited" (the user's own turn).
	How    string `json:"how"`
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Advice string `json:"advice,omitempty"` // the advice blob's id, to find it by
}

// Dismissed is a Consultation that ended without its advice being sent.
// It changed no work, so only the full Receipt lists it.
type Dismissed struct {
	Consultation string `json:"consultation"`
	Turn         int    `json:"turn"` // the last turn started when it was asked
	Agent        string `json:"agent,omitempty"`
	Model        string `json:"model,omitempty"`
	// Outcome is the Consultation's: advised (and not sent), cancelled,
	// failed, or unfinished when its log has no end for it.
	Outcome string `json:"outcome"`
}

// chatRange is the Chat and turn range a cut's RunStarted names.
type chatRange struct {
	Dir, Chat string
	From, To  int
}

// chatRangeOf reads the Chat directory and turns of the cut's RunStarted.
func chatRangeOf(recs []ledger.Record) (chatRange, bool) {
	for _, rec := range recs {
		if rec.Type != run.RecRunStarted {
			continue
		}
		var d struct {
			Chat  string `json:"chat"`
			Dir   string `json:"chat_dir"`
			Turns struct{ From, To int }
		}
		if json.Unmarshal(rec.Data, &d) != nil || d.Chat == "" || d.Dir == "" {
			return chatRange{}, false
		}
		return chatRange{Dir: d.Dir, Chat: d.Chat, From: d.Turns.From, To: d.Turns.To}, true
	}
	return chatRange{}, false
}

// readChatAdvice reads the Chat log in rg.Dir. Records before a torn last
// line stand; a log that can't be read is an error.
func readChatAdvice(rg chatRange) ([]AdviceSent, []Dismissed, error) {
	recs, _, err := ledger.ReplayHead(rg.Dir)
	if err != nil && (len(recs) == 0 || errors.Is(err, ledger.ErrBrokenChain)) {
		return nil, nil, err
	}
	sent, dismissed := ChatAdvice(recs, rg.Chat, rg.From, rg.To)
	return sent, dismissed, nil
}

// ChatAdvice reads a Chat log's Consultations for the turns from to to:
// the advice sent in them, and the Consultations asked in them that ended
// without it.
func ChatAdvice(recs []ledger.Record, chat string, from, to int) (sent []AdviceSent, dismissed []Dismissed) {
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
		case run.RecConsultationStarted, run.RecConsultationEnded, run.RecAdviceSent:
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
		case run.RecConsultationStarted:
			if byID[d.Consultation] == nil {
				order = append(order, d.Consultation)
			}
			byID[d.Consultation] = &consult{turn: d.Turn, agent: d.Advisor.Agent, model: d.Advisor.Model, outcome: "unfinished"}
		case run.RecConsultationEnded:
			if c := byID[d.Consultation]; c != nil {
				c.outcome, c.advice = d.Outcome, d.Advice
			}
		case run.RecAdviceSent:
			if d.Turn >= from && d.Turn <= to {
				sends = append(sends, AdviceSent{Consultation: d.Consultation, Turn: d.Turn, How: d.How})
			}
			if c := byID[d.Consultation]; c != nil {
				c.sent = true
			}
		}
	}
	for _, s := range sends {
		if c := byID[s.Consultation]; c != nil {
			s.Agent, s.Model, s.Advice = clean(c.agent), clean(c.model), c.advice
		}
		s.Consultation, s.How = clean(s.Consultation), clean(s.How)
		sent = append(sent, s)
	}
	for _, id := range order {
		if c := byID[id]; !c.sent && c.turn >= from && c.turn <= to {
			dismissed = append(dismissed, Dismissed{Consultation: clean(id), Turn: c.turn, Agent: clean(c.agent),
				Model: clean(c.model), Outcome: clean(c.outcome)})
		}
	}
	return sent, dismissed
}

// WithAdvice adds a Chat cut's advice to r; it does nothing for a Run
// that isn't a Chat's cut.
func (r *Receipt) WithAdvice(sent []AdviceSent, dismissed []Dismissed) {
	if r.Chat != nil {
		r.Chat.Advice, r.Chat.Dismissed = sent, dismissed
	}
}

func advisorName(agent, model string) string {
	if who := strings.TrimSpace(val(agent) + " " + val(model)); who != "" {
		return who
	}
	return "advisor"
}

// adviceLines is the Receipt's disclosure of advice that reached the
// agent: the advice is a Claim, so it says so.
func (c *ChatCut) adviceLines() []Line {
	if len(c.Advice) == 0 {
		return nil
	}
	how := func(a AdviceSent) string {
		s := advisorName(a.Agent, a.Model) + ", fresh"
		if a.How == "edited" {
			s += ", edited by you"
		}
		return s
	}
	const claim = " · a Claim, not checked"
	if len(c.Advice) == 1 {
		a := c.Advice[0]
		return []Line{{"Advice", fmt.Sprintf("1 consultation sent at turn %d (%s)%s", a.Turn, how(a), claim), Plain}}
	}
	ls := []Line{{"Advice", fmt.Sprintf("%d consultations sent%s", len(c.Advice), claim), Plain}}
	for _, a := range c.Advice {
		ls = append(ls, Line{"", fmt.Sprintf("at turn %d (%s)", a.Turn, how(a)), Plain})
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
		out := map[string]string{"advised": "advice not sent", "cancelled": "cancelled", "failed": "failed", "unfinished": "never ended"}[d.Outcome]
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
