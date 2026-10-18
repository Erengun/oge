//go:build unix

package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// "a" and enter on an AskUserQuestion consults the advisor and highlights
// its option; the human still answers, the Consultation is recorded as an
// ask's, and the answer's advised_by is durable in the turn's events.
func TestHomeChatAskAdvisorKey(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first", Ask: &multiturn.Ask{Tool: "AskUserQuestion", Target: "Ship it?"}},
	}}, "echo '2. no, not yet'\n")
	s.composer()
	s.type_("ask me\r")
	s.expect("a↵ ask the advisor")
	s.type_("a\r")
	s.expect("2. no   ← advisor suggests")
	s.expect("a Claim, not checked")
	s.type_("1\r")
	s.expect("answered: yes")
	s.composer()
	s.quitChat(f)
	started := chatRecords(t, f, run.RecConsultationStarted)
	if len(started) != 1 || started[0]["trigger"] != "ask" || started[0]["requested_by"] != "agent" {
		t.Fatalf("ConsultationStarted %v", started)
	}
	id, _ := started[0]["consultation"].(string)
	logs, _ := filepath.Glob(filepath.Join(f.state, "private", "chats", "*"))
	blobs, err := ledger.OpenBlobs(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range chatRecords(t, f, run.RecAttemptEnded) {
		ref, _ := r["events"].(string)
		b, err := blobs.Get(ref)
		if err != nil {
			continue
		}
		found = found || (strings.Contains(string(b), `"AdvisedBy":"`+id+`"`) && strings.Contains(string(b), `"By":"human"`))
	}
	if !found {
		t.Errorf("no human answer advised by %s in the turn's events", id)
	}
}

// Typing "always" at a question is an answer, never the advisor key.
func TestHomeChatAskTypingAIsNotTheAdvisorKey(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first", Ask: &multiturn.Ask{Tool: "AskUserQuestion", Target: "Ship it?"}},
	}}, "echo '2. no'\n")
	s.composer()
	s.type_("ask me\r")
	s.expect("a↵ ask the advisor")
	s.type_("always\r")
	s.expect("answered: always")
	s.composer()
	s.quitChat(f)
	if started := chatRecords(t, f, run.RecConsultationStarted); len(started) != 0 {
		t.Errorf("a Consultation started: %v", started)
	}
}
