//go:build unix

package cli

import (
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/run"
)

// adviceThen shows /advise advice, then types keys and waits as wait does.
// The advice is never sent.
func adviceThen(t *testing.T, keys string, wait func(s *homeSession)) (*homeSession, *runFixture) {
	t.Helper()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "first reply"}, {Reply: "second reply"}}}, adviceScript)
	s.composer()
	s.type_("yo\r")
	s.expect("  first reply")
	s.composer()
	s.type_("/advise how?\r")
	s.expect("send to agent")
	s.type_(keys)
	wait(s)
	return s, f
}

func assertAdviceDismissedOnce(t *testing.T, f *runFixture) {
	t.Helper()
	ended := chatRecords(t, f, run.RecConsultationEnded)
	dis := chatRecords(t, f, run.RecAdviceDismissed)
	if len(ended) != 1 || len(dis) != 1 || dis[0]["consultation"] != ended[0]["consultation"] {
		t.Errorf("AdviceDismissed %v for %v", dis, ended)
	}
	if n := len(chatRecords(t, f, run.RecAdviceSent)); n != 0 {
		t.Errorf("%d AdviceSent", n)
	}
}

// The user's own turn while the advice is shown leaves it unsent: that is
// a dismissal, logged.
func TestHomeChatAdviceDismissedByOwnTurnIsLogged(t *testing.T) {
	t.Parallel()
	s, f := adviceThen(t, "my own\r", func(s *homeSession) {
		s.expect("  second reply")
		s.composer()
	})
	s.quitChat(f)
	assertAdviceDismissedOnce(t, f)
}

// e then abandoning the edit (here, clearing the box) is a dismissal.
func TestHomeChatAdviceEditAbandonedIsLogged(t *testing.T) {
	t.Parallel()
	s, f := adviceThen(t, "e", func(s *homeSession) {
		s.expect("Inject a Clock.")
		s.type_("\x03")
		s.composer()
	})
	s.quitChat(f)
	assertAdviceDismissedOnce(t, f)
}

// /clear drops the advice shown: a dismissal.
func TestHomeChatAdviceDismissedByClearIsLogged(t *testing.T) {
	t.Parallel()
	s, f := adviceThen(t, "/clear\r", func(s *homeSession) {
		s.composer()
	})
	s.quitChat(f)
	assertAdviceDismissedOnce(t, f)
}
