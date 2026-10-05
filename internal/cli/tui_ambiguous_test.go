package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/run"
)

func ambiguousRequest() gate.Request {
	files := []string{"docs/debug.md", "internal/foo/helper.go", "tmp/result.json"}
	content := map[string]string{
		"docs/debug.md":          "# debug\n\x1b]0;pwned\x07notes\n",
		"internal/foo/helper.go": "package foo\n",
		"tmp/result.json":        "{\"ok\": true}\n",
	}
	promote, drop := gate.Choices["promote"], gate.Choices["drop"]
	promote.Says = "selected/all join the Candidate: fresh QA, then the final Check"
	drop.Says = "selected/all leave the Candidate, then the final Check"
	reject, quit := gate.Choices["reject"], gate.Choices["quit"]
	reject.Says, quit.Says = "end the Run Rejected (type the word and a reason)", "end the Run Cancelled"
	return gate.Request{
		Name: "Ambiguous-file", What: "3 new files were not covered by the declared output/test globs:",
		Need: "3 new files need a decision", Files: files,
		Pins:    gate.Pins{Gate: "gate.ambiguous_file", Attempt: "implement#1", Candidate: "6d1231d9f00d", Oracle: 1, Verdicts: []int{1}, Files: files},
		Choices: []gate.Choice{promote, drop, reject, quit},
		Inspect: func(p string) ([]byte, error) { return []byte(content[p]), nil },
	}
}

// The Ambiguous-file review in the live view: it appears only when needed,
// with its attention line; Enter alone does nothing; inspect shows a file
// cleaned and decides nothing; promote and drop are batch actions on a
// selection or on every file.
func TestTUIAmbiguousReview(t *testing.T) {
	h := newStandardHarness(t)
	h.working()
	h.implemented("")
	v := h.reviewing(1)
	h.at(9000 * time.Millisecond)
	h.qaDone(v, 1)
	h.checked(true)
	if got := h.m.render(); strings.Contains(got, "Ambiguous") || !strings.Contains(got, "Nothing needs you.") {
		t.Errorf("before the review:\n%s", got)
	}
	r := ambiguousRequest()
	reply := h.openGate(r)
	frame := h.m.render()
	golden(t, "ambiguous-review", frame)
	if !strings.Contains(frame, "ATTENTION NEEDED: 3 new files need a decision") || strings.Contains(frame, "Nothing needs you.") {
		t.Errorf("attention line:\n%s", frame)
	}

	h.typeLine("")
	if got := h.m.render(); got != frame {
		t.Errorf("Enter alone changed the screen:\n%s", got)
	}
	h.typeLine("i 1")
	got := h.m.render()
	golden(t, "ambiguous-inspect", got)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("raw control bytes in the inspect view: %q", got)
	}
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := h.m.render(); got != frame {
		t.Errorf("esc didn't return to the Gate:\n%s", got)
	}
	noDecision(t, reply)

	h.typeLine("p 7")
	if got := h.m.render(); !strings.Contains(got, `"7" isn't one of the files shown`) {
		t.Errorf("a bad selection:\n%s", got)
	}
	noDecision(t, reply)
	h.typeLine("p 2")
	select {
	case d := <-reply:
		if !reflect.DeepEqual(d, gate.Decision{Choice: "promote", Files: []string{"internal/foo/helper.go"}}) {
			t.Fatalf("decided %+v", d)
		}
	default:
		t.Fatal("no decision")
	}
	if got := h.m.render(); !strings.Contains(got, "promote · recording…") {
		t.Errorf("before GateDecided:\n%s", got)
	}
	h.decided(r, gate.Decision{Choice: "promote", Files: []string{"internal/foo/helper.go"}})

	// The rest, all at once.
	r2 := ambiguousRequest()
	r2.What, r2.Need = "2 new files were not covered by the declared output/test globs:", "2 new files need a decision"
	r2.Files = []string{"docs/debug.md", "tmp/result.json"}
	reply = h.openGate(r2)
	h.typeLine("d")
	select {
	case d := <-reply:
		if !reflect.DeepEqual(d, gate.Decision{Choice: "drop", Files: []string{"docs/debug.md", "tmp/result.json"}}) {
			t.Fatalf("decided %+v", d)
		}
	default:
		t.Fatal("no decision")
	}
	h.decided(r2, gate.Decision{Choice: "drop", Files: r2.Files})
	h.res.Candidate = "9f00d6d1231d"
	h.res.Resolutions = []run.Resolution{{Choice: "promote", Files: []string{"internal/foo/helper.go"}}, {Choice: "drop", Files: r2.Files}}
	h.at(11 * time.Second)
	h.send(run.Event{Kind: run.EvResolved, Result: h.res, Next: "verify"})
	golden(t, "ambiguous-resolved", h.m.render())
}
