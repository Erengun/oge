package gate

import (
	"context"
	"reflect"
	"strings"
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
	if err != nil || !reflect.DeepEqual(d, Decision{Choice: "reject", Reason: "wrong fix"}) {
		t.Errorf("%+v %v", d, err)
	}
	d, err = Choices["send back"].Decide("")
	if err != nil || !reflect.DeepEqual(d, Decision{Choice: "send back"}) {
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

func ambiguousGate() Request {
	return Request{Name: "Ambiguous-file", Files: []string{"docs/debug.md", "internal/foo/helper.go", "tmp/result.json"},
		Choices: []Choice{Choices["promote"], Choices["drop"], Choices["reject"], Choices["quit"]},
		Inspect: func(string) ([]byte, error) { return nil, nil }}
}

// Promote and drop are batch choices: alone they cover every file, and a
// selection names files by number or path. Enter alone still does nothing.
func TestBatchSelection(t *testing.T) {
	r := ambiguousGate()
	for _, c := range []struct {
		line, choice, files string
		bad                 bool
	}{
		{line: ""},
		{line: "p", choice: "promote", files: "docs/debug.md internal/foo/helper.go tmp/result.json"},
		{line: "d", choice: "drop", files: "docs/debug.md internal/foo/helper.go tmp/result.json"},
		{line: "p 1 3", choice: "promote", files: "docs/debug.md tmp/result.json"},
		{line: "promote 3,1", choice: "promote", files: "docs/debug.md tmp/result.json"},
		{line: "d tmp/result.json", choice: "drop", files: "tmp/result.json"},
		{line: "d 4", choice: "drop", bad: true},
		{line: "d 0", choice: "drop", bad: true},
		{line: "p 1x", choice: "promote", bad: true},
		{line: "p nope.md", choice: "promote", bad: true},
		{line: "r 1"},
		{line: "q 1"},
	} {
		got, rest, ok := r.Match(c.line)
		if ok != (c.choice != "") || got.Word != c.choice {
			t.Errorf("%q: matched %q (%v), want %q", c.line, got.Word, ok, c.choice)
			continue
		}
		if !ok {
			continue
		}
		files, err := r.Select(rest)
		if (err != nil) != c.bad || strings.Join(files, " ") != c.files {
			t.Errorf("%q: selected %v %v", c.line, files, err)
		}
	}
	// Elsewhere a key never takes an argument.
	if _, _, ok := resultGate().Match("s a note"); ok {
		t.Error(`"s a note" matched at the Result gate`)
	}
}

func TestInspectTarget(t *testing.T) {
	r := ambiguousGate()
	for _, c := range []struct {
		line, path string
		ok, bad    bool
	}{
		{line: "i 2", path: "internal/foo/helper.go", ok: true},
		{line: "inspect tmp/result.json", path: "tmp/result.json", ok: true},
		{line: "i", ok: true, bad: true},
		{line: "i 9", ok: true, bad: true},
		{line: "p 1"},
		{line: "ii"},
	} {
		p, ok, err := r.InspectTarget(c.line)
		if p != c.path || ok != c.ok || (err != nil) != c.bad {
			t.Errorf("%q: %q %v %v", c.line, p, ok, err)
		}
	}
	one := ambiguousGate()
	one.Files = one.Files[:1]
	if p, ok, err := one.InspectTarget("i"); p != "docs/debug.md" || !ok || err != nil {
		t.Errorf("one file: %q %v %v", p, ok, err)
	}
	none := resultGate()
	if _, ok, _ := none.InspectTarget("i 1"); ok {
		t.Error("a Gate with no inspect view took i")
	}
}

func TestScriptedBatchNeedsFiles(t *testing.T) {
	s := &Scripted{Decisions: []Decision{{Choice: "drop"}}}
	if _, err := s.Decide(context.Background(), ambiguousGate()); err == nil {
		t.Error("a drop naming no files went through")
	}
}
