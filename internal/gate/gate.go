// Package gate is the Gate port: where a Run stops for a human decision
// (ADR-0008, ADR-0015). It is its own port, never merged with the one an
// agent's Host requests will use (#45).
//
// A Gate offers a closed set of choices and has no default. The entry
// rules live here so every view applies the same ones: ordinary choices
// are a single letter (or the word), and choices that weaken trust or end
// the Run are typed in full with a non-empty reason.
package gate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/oracle"
)

// Port is where a Gate waits for a decision. Decide blocks until a human
// decides or ctx ends. Whatever implements it never offers a default.
type Port interface {
	Decide(ctx context.Context, r Request) (Decision, error)
}

// ErrNoDecision means the terminal went away before anyone decided.
var ErrNoDecision = errors.New("no decision was made: the terminal closed")

// Choice is one choice a Gate offers.
type Choice struct {
	Word string // as the graph names it and as typed in full
	// Key is the single letter for an ordinary choice, and empty for one
	// that must be typed in full.
	Key    string
	Reason bool   // a non-empty reason is required
	Note   bool   // an optional note goes with it (send back)
	Says   string // what it does, for the screen
}

// Choices are the entry rules for each choice word (ADR-0015). A Gate's
// own wording of what a choice does goes in Says.
var Choices = map[string]Choice{
	"take":       {Word: "take", Key: "t"},
	"send back":  {Word: "send back", Key: "s", Note: true},
	"quit":       {Word: "quit", Key: "q"},
	"promote":    {Word: "promote", Key: "p"},
	"drop":       {Word: "drop", Key: "d"},
	"reject":     {Word: "reject", Reason: true},
	"override":   {Word: "override", Reason: true},
	"infeasible": {Word: "infeasible", Reason: true},
}

// Pins are what a decision was shown, and so what it is pinned to
// (ADR-0008).
type Pins struct {
	Gate      string   `json:"gate"`
	Attempt   string   `json:"attempt"`
	Candidate string   `json:"candidate"`
	Oracle    int      `json:"oracle_version"`
	Verdicts  []int    `json:"verdicts"` // the Checks whose Verdicts were shown
	Tamper    []string `json:"tamper,omitempty"`
}

// Request is an open Gate.
type Request struct {
	Name    string // its glossary name, e.g. "bound-exhaustion"
	What    string // what happened
	Need    string // what is needed from the human
	Check   *oracle.Result
	Pins    Pins
	Choices []Choice
}

// Decision is what the human chose.
type Decision struct {
	Choice string `json:"choice"`
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Match reads one line typed at the Gate. An empty line matches nothing,
// so Enter alone does nothing. A choice typed as its word may carry its
// reason or note inline after it ("reject the tests are wrong").
func (r Request) Match(line string) (c Choice, rest string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Choice{}, "", false
	}
	for _, c := range r.Choices {
		if c.Key != "" && line == c.Key {
			return c, "", true
		}
		if line == c.Word {
			return c, "", true
		}
		if after, found := strings.CutPrefix(line, c.Word+" "); found {
			return c, strings.TrimSpace(after), true
		}
	}
	return Choice{}, "", false
}

// Decide makes the decision for c with text as its reason or note. A
// choice that needs a reason refuses an empty one.
func (c Choice) Decide(text string) (Decision, error) {
	text = strings.TrimSpace(text)
	d := Decision{Choice: c.Word}
	switch {
	case c.Reason && text == "":
		return d, fmt.Errorf("%s needs a reason", c.Word)
	case c.Reason:
		d.Reason = text
	case c.Note:
		d.Note = text
	}
	return d, nil
}

// Scripted is the Gate port for tests and scripts: it answers each Gate
// with the next decision, and refuses one the Gate doesn't offer.
type Scripted struct {
	Decisions []Decision
	Seen      []Request
}

func (s *Scripted) Decide(_ context.Context, r Request) (Decision, error) {
	s.Seen = append(s.Seen, r)
	if len(s.Decisions) == 0 {
		return Decision{}, ErrNoDecision
	}
	d := s.Decisions[0]
	s.Decisions = s.Decisions[1:]
	for _, c := range r.Choices {
		if c.Word == d.Choice {
			if c.Reason && strings.TrimSpace(d.Reason) == "" {
				return Decision{}, fmt.Errorf("%s needs a reason", c.Word)
			}
			return d, nil
		}
	}
	return Decision{}, fmt.Errorf("the %s Gate doesn't offer %q", r.Name, d.Choice)
}

// Attention is the line that says whether the Run needs the human: with
// no Gate open, nothing does (ADR-0022).
// TODO(#45): an open Host request needs the human too.
func Attention(open *Request) string {
	if open == nil {
		return "Nothing needs you."
	}
	return "ATTENTION NEEDED: " + open.Need
}
