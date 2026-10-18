//go:build unix

package cli

import (
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/run"
)

// a on an AskUserQuestion consults the advisor and highlights its option;
// the human still answers, and the Consultation is recorded as an ask's.
func TestHomeChatAskAdvisorKey(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first", Ask: &multiturn.Ask{Tool: "AskUserQuestion", Target: "Ship it?"}},
	}}, "echo '2. no, not yet'\n")
	s.composer()
	s.type_("ask me\r")
	s.expect("a ask the advisor")
	s.type_("a")
	s.expect("2. no   ← advisor suggests")
	s.expect("a Claim, not checked")
	s.type_("1\r")
	s.expect("answered: yes")
	s.composer()
	s.quitChat(f)
	started := chatRecords(t, f, run.RecConsultationStarted)
	if len(started) != 1 || started[0]["trigger"] != "ask" || started[0]["requested_by"] != "agent" {
		t.Errorf("ConsultationStarted %v", started)
	}
}
