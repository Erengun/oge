package gate

import (
	"context"
	"testing"
)

func resultGate() Request {
	return Request{Name: "Result gate", Choices: []Choice{Choices["take"], Choices["send back"], Choices["reject"], Choices["quit"]}}
}

// The entry rules (ADR-0015): no default, ordinary choices by letter or
// word, trust-weakening or Run-ending ones only in full.
func TestEntryRules(t *testing.T) {
	r := resultGate()
	for _, c := range []struct {
		line, choice, rest string
	}{
		{"", "", ""},
		{"   ", "", ""},
		{"t", "take", ""},
		{"take", "take", ""},
		{" q ", "quit", ""},
		{"s", "send back", ""},
		{"send back keep it short", "send back", "keep it short"},
		{"r", "", ""},
		{"rej", "", ""},
		{"reject", "reject", ""},
		{"reject  not what I asked ", "reject", "not what I asked"},
		{"rejected", "", ""},
		{"T", "", ""},
		{"yes", "", ""},
	} {
		got, rest, ok := r.Match(c.line)
		if ok != (c.choice != "") || got.Word != c.choice || rest != c.rest {
			t.Errorf("%q: matched %q %q (%v), want %q %q", c.line, got.Word, rest, ok, c.choice, c.rest)
		}
	}
}

func TestAReasonIsRequired(t *testing.T) {
	if _, err := Choices["reject"].Decide("  "); err == nil {
		t.Error("reject took an empty reason")
	}
	d, err := Choices["reject"].Decide(" wrong fix ")
	if err != nil || d != (Decision{Choice: "reject", Reason: "wrong fix"}) {
		t.Errorf("%+v %v", d, err)
	}
	d, err = Choices["send back"].Decide("")
	if err != nil || d != (Decision{Choice: "send back"}) {
		t.Errorf("a send back's note is optional: %+v %v", d, err)
	}
	for _, w := range []string{"reject", "override", "infeasible"} {
		if c := Choices[w]; c.Key != "" || !c.Reason {
			t.Errorf("%s can be chosen by letter or without a reason", w)
		}
	}
}

func TestScriptedPortRefusesWhatTheGateDoesntOffer(t *testing.T) {
	s := &Scripted{Decisions: []Decision{{Choice: "override", Reason: "x"}, {Choice: "reject"}, {Choice: "take"}}}
	ctx := context.Background()
	if _, err := s.Decide(ctx, resultGate()); err == nil {
		t.Error("the Result gate took override")
	}
	if _, err := s.Decide(ctx, resultGate()); err == nil {
		t.Error("reject went through without a reason")
	}
	if d, err := s.Decide(ctx, resultGate()); err != nil || d.Choice != "take" {
		t.Errorf("%+v %v", d, err)
	}
	if _, err := s.Decide(ctx, resultGate()); err != ErrNoDecision {
		t.Errorf("an empty script decided: %v", err)
	}
}

// The live view's attention line comes from the Gate state (ADR-0022).
func TestAttention(t *testing.T) {
	if got := Attention(nil); got != "Nothing needs you." {
		t.Errorf("no Gate open: %q", got)
	}
	r := resultGate()
	r.Need = "Decide whether to take it."
	if got := Attention(&r); got != "ATTENTION NEEDED: Decide whether to take it." {
		t.Errorf("Result gate open: %q", got)
	}
}
