package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// heldFor is how long these tests hold a request. The real hold is far
// longer (docs/research/held-requests.md records a 90 s hold against the
// real claude, past the 60 s auto-continue in claude-code #73125); the
// adapter has no timer on a pending Ask, so a short hold exercises the same
// path and the test stays quick.
const heldFor = 1500 * time.Millisecond

// A pending Ask that Öge holds is answered later and the agent proceeds as
// if it had been answered at once: nothing is denied, withdrawn or settled
// while it waits (docs/ux/away.md, held requests).
func TestHeldAskStaysAnswerable(t *testing.T) {
	for _, c := range []struct{ name, tool, target string }{
		{"can_use_tool", "Bash", "make deploy"},
		{"AskUserQuestion", "AskUserQuestion", "Ship it?"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, ws := openAuto(t, multiturn.Scenario{Turns: []multiturn.Turn{
				{Ask: &multiturn.Ask{Tool: c.tool, Target: c.target}, Writes: []multiturn.File{{Path: "a.txt", Content: "a"}}},
			}})
			if err := s.Send(agent.Turn{Text: "go"}); err != nil {
				t.Fatal(err)
			}
			var ask *agent.Ask
			for ask == nil {
				select {
				case ev := <-s.Events():
					ask = ev.Ask
				case <-time.After(10 * time.Second):
					t.Fatal("the ask never arrived")
				}
			}
			// Hold it. Any event now would be Öge or the adapter deciding
			// for the human.
			select {
			case ev := <-s.Events():
				t.Fatalf("an event while the ask was held: %+v host=%+v", ev, ev.Host)
			case <-time.After(heldFor):
			}
			if _, err := os.Stat(filepath.Join(ws, "a.txt")); err == nil {
				t.Fatal("the agent proceeded before the answer")
			}
			ans := agent.Answer{Allow: true}
			if c.tool == "AskUserQuestion" {
				ans.Answers = []string{"yes"}
			}
			if err := ask.Answer(ans); err != nil {
				t.Fatalf("answering after the hold: %v", err)
			}
			evs := turnEvents(t, s, nil)
			last := evs[len(evs)-1]
			if last.Failure != "" {
				t.Errorf("the turn settled %+v", last)
			}
			for _, ev := range evs {
				if h := ev.Host; h != nil && h.Decision != "allow" {
					t.Errorf("a %s decision after the hold: %+v", h.Decision, h)
				}
			}
			if _, err := os.Stat(filepath.Join(ws, "a.txt")); err != nil {
				t.Errorf("the allowed call didn't run: %v", err)
			}
		})
	}
}

// A held Ask answered with a deny is still a human deny, not a timeout:
// the reason names the user, and the turn carries on.
func TestHeldAskDeniedByTheHuman(t *testing.T) {
	s, ws := openAuto(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Ask: &multiturn.Ask{Tool: "Bash", Target: "make deploy"}, Writes: []multiturn.File{{Path: "a.txt", Content: "a"}}},
	}})
	if err := s.Send(agent.Turn{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	var ask *agent.Ask
	for ask == nil {
		select {
		case ev := <-s.Events():
			ask = ev.Ask
		case <-time.After(10 * time.Second):
			t.Fatal("the ask never arrived")
		}
	}
	time.Sleep(heldFor)
	if err := ask.Reply(false); err != nil {
		t.Fatal(err)
	}
	var denied bool
	for _, ev := range turnEvents(t, s, nil) {
		if h := ev.Host; h != nil && h.Decision == "deny" {
			denied = h.By == "human" && h.Rule == ruleHumanAnswer
		}
	}
	if !denied {
		t.Error("no human deny after the hold")
	}
	if _, err := os.Stat(filepath.Join(ws, "a.txt")); err == nil {
		t.Error("the denied call ran")
	}
}
