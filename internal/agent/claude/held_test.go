package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// heldFor is how long these tests hold a request, 1.5 s by default. That
// guards only against a timer shorter than 1.5 s in the adapter or the
// fake; a minute-scale timer would pass it. Set OGE_HELD_FOR (a Go
// duration, e.g. 5m) to run the long hold. The real claude's behaviour over
// a long hold is in testdata/held_ask.ndjson (TestRecordedHeldAsk).
var heldFor = func() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("OGE_HELD_FOR")); err == nil && d > 0 {
		return d
	}
	return 1500 * time.Millisecond
}()

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

// held_ask.ndjson (an Edit, 300 s) and held_ask_question.ndjson
// (AskUserQuestion, 200 s): a real claude 2.1.316 left a can_use_tool
// unanswered for the recorded hold. It sent nothing in between, and the
// turn ended in success once allowed. Frames are stamped when a reader
// thread received them, so a frame during the hold would show. The
// recordings are default mode with no hook, not Chat's auto mode with the
// PreToolUse hook: no auto-mode ask could be provoked (see
// docs/research/held-requests.md).
func TestRecordedHeldAsk(t *testing.T) {
	for _, c := range []struct {
		file string
		min  float64
	}{{"held_ask.ndjson", 280}, {"held_ask_question.ndjson", 180}} {
		t.Run(c.file, func(t *testing.T) { recordedHeldAsk(t, c.file, c.min) })
	}
}

func recordedHeldAsk(t *testing.T, file string, minHold float64) {
	t.Helper()
	type frame struct {
		T   float64        `json:"t"`
		Dir string         `json:"dir"`
		Msg map[string]any `json:"msg"`
	}
	var fs []frame
	for _, l := range fixture(t, file) {
		var f frame
		if err := json.Unmarshal([]byte(l), &f); err != nil {
			t.Fatal(err)
		}
		if f.Dir == "in" || f.Dir == "out" {
			fs = append(fs, f)
		}
	}
	var asked, answered, resultAt float64
	var askID string
	var during []string
	var toolErr bool
	var result string
	for _, f := range fs {
		typ, _ := f.Msg["type"].(string)
		req, _ := f.Msg["request"].(map[string]any)
		switch {
		case f.Dir == "out" && typ == "control_request" && req["subtype"] == "can_use_tool" && asked == 0:
			asked, askID = f.T, f.Msg["request_id"].(string)
			continue
		case f.Dir == "in" && typ == "control_response" && answered == 0 && asked != 0:
			if resp, _ := f.Msg["response"].(map[string]any); resp["request_id"] == askID {
				answered = f.T
				continue
			}
		}
		// claude's rate_limit_event comes in the same instant as the ask.
		if asked != 0 && answered == 0 && !(typ == "rate_limit_event" && f.T-asked < 2) {
			during = append(during, f.Dir+" "+typ)
		}
		if answered != 0 && f.Dir == "out" {
			if typ == "user" {
				b, _ := json.Marshal(f.Msg)
				toolErr = toolErr || strings.Contains(string(b), `"is_error": true`) || strings.Contains(string(b), `"is_error":true`)
			}
			if typ == "result" {
				resultAt, result = f.T, f.Msg["subtype"].(string)
			}
		}
	}
	if asked == 0 || answered-asked < minHold {
		t.Fatalf("the recording held the ask %.0f s, want at least %.0f", answered-asked, minHold)
	}
	if len(during) != 0 {
		t.Errorf("frames during the hold: %v", during)
	}
	if toolErr {
		t.Error("a tool result after the answer was an error")
	}
	if result != "success" || resultAt < answered {
		t.Errorf("result %q at %.1f, answer at %.1f", result, resultAt, answered)
	}
}
