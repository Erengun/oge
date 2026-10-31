package run

import (
	"encoding/json"

	"github.com/erengun/oge/internal/ledger"
)

// CutUsage is what a cut's RunStarted records of the Chat's turns in the
// cut: how many of each kind, and the tokens the agent reported for them.
// A snapshot at cut time, so the Receipt and /runs read the Ledger only.
// Turns are classified from the Chat log's records (TurnStarted's by,
// AdviceSent), never from text. Tokens is what the agent reported per turn
// (input and output together), summed only when every counted turn
// reported it: Reported says how many did, and nothing is estimated.
type CutUsage struct {
	You      int `json:"you"`
	Feedback int `json:"feedback"`
	Advice   int `json:"advice"`
	// Reported is the turns whose AttemptEnded carried a token count.
	Reported int `json:"reported"`
	// Tokens is the sum over the Reported turns.
	Tokens int `json:"tokens"`
}

// Turns is every counted turn.
func (u CutUsage) Turns() int { return u.You + u.Feedback + u.Advice }

// usageIn reads recs, a Chat log, for turns from to to.
func usageIn(recs []ledger.Record, chat string, from, to int) CutUsage {
	kind := map[int]string{} // turn → "you", "feedback" or "advice"
	advice := map[int]bool{}
	tokens := map[int]*int{}
	for _, rec := range recs {
		switch rec.Type {
		case RecTurnStarted, RecAdviceSent, RecAttemptEnded:
		default:
			continue
		}
		var d struct {
			Chat         string `json:"chat"`
			Turn         int    `json:"turn"`
			By           string `json:"by"`
			Consultation string `json:"consultation"`
			Usage        *struct {
				Tokens *int `json:"tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(rec.Data, &d) != nil || d.Turn < from || d.Turn > to || (d.Chat != "" && d.Chat != chat) {
			continue
		}
		switch rec.Type {
		case RecTurnStarted:
			if d.By == "oge" {
				kind[d.Turn] = "feedback"
			} else {
				kind[d.Turn] = "you"
			}
		case RecAdviceSent:
			if d.By == "oge" { // verbatim: Öge's advice turn; an edited one is the user's
				advice[d.Turn] = true
			}
		case RecAttemptEnded:
			// A Consultation's AttemptEnded has no turn: it is not one.
			if d.Consultation == "" && d.Usage != nil && d.Usage.Tokens != nil {
				tokens[d.Turn] = d.Usage.Tokens
			}
		}
	}
	var u CutUsage
	for n, k := range kind {
		if advice[n] {
			k = "advice"
		}
		switch k {
		case "you":
			u.You++
		case "feedback":
			u.Feedback++
		case "advice":
			u.Advice++
		}
		if t := tokens[n]; t != nil {
			u.Reported++
			u.Tokens += *t
		}
	}
	return u
}

// chatUsage is usageIn over the Chat's log now.
func (c *Chat) chatUsage(from, to int) (CutUsage, error) {
	c.logMu.Lock()
	recs, err := ledger.Replay(c.Dir)
	c.logMu.Unlock()
	if err != nil {
		return CutUsage{}, err
	}
	return usageIn(recs, c.ID, from, to), nil
}
