package receipt

import (
	"fmt"

	"github.com/erengun/oge/internal/run"
)

// ChatUsage is a Chat cut's turns by kind (from the Chat log's records,
// never the text) and the tokens the agent reported for them (docs/ux/desk.md:
// whether stopping auto-prompting saved anything). Never estimated.
type ChatUsage struct {
	Turns    int `json:"turns"`
	You      int `json:"you"`
	Feedback int `json:"feedback"`
	Advice   int `json:"advice"`
	// Tokens is what the agent reported (input and output together),
	// summed only when every turn reported it; nil otherwise.
	Tokens *int `json:"tokens,omitempty"`
	// ReportedTurns is how many turns reported tokens, set only when some
	// but not all did: Tokens is then withheld, never a partial sum.
	ReportedTurns int `json:"reported_turns,omitempty"`
}

func chatUsage(u *run.CutUsage) *ChatUsage {
	if u == nil || u.Turns() == 0 {
		return nil
	}
	c := &ChatUsage{Turns: u.Turns(), You: u.You, Feedback: u.Feedback, Advice: u.Advice}
	switch {
	case u.Reported == c.Turns:
		t := u.Tokens
		c.Tokens = &t
	case u.Reported > 0:
		c.ReportedTurns = u.Reported
	}
	return c
}

// line is the Receipt's one line: "4 turns (you 2, feedback 1, advice 1) ·
// 12345 tokens". The tokens are the agent's report, input and output.
func (u *ChatUsage) line() string {
	if u == nil {
		return ""
	}
	s := fmt.Sprintf("%s (you %d, feedback %d, advice %d)", plural(u.Turns, "turn", "turns"), u.You, u.Feedback, u.Advice)
	switch {
	case u.Tokens != nil:
		s += fmt.Sprintf(" · %d tokens (summed from the agent's per-turn reports)", *u.Tokens)
	case u.ReportedTurns > 0:
		s += fmt.Sprintf(" · tokens: partial (%d of %d turns reported)", u.ReportedTurns, u.Turns)
	}
	return s
}
