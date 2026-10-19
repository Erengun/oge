package run

import (
	"errors"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

// An advice turn whose Send fails never reached the agent: no AdviceSent,
// exactly one AdviceDismissed, and a cut discloses it as Dismissed only.
func TestSendAdviceFailedSendIsDismissedNotSent(t *testing.T) {
	st := newStub(nil)
	c := openChatWith(t.Context(), t, stubAdapter{st}, 0)
	first := send(t, c, "one")
	st.events <- agent.Event{Kind: agent.TurnSettled}
	drain(t, first, nil)
	st.sendErr = errors.New("broken pipe")
	if _, err := c.SendAdvice(t.Context(), "k-1", "Inject a Clock."); err == nil {
		t.Fatal("SendAdvice succeeded on a dead process")
	}
	if s := recordsOf(t, c, RecAdviceSent); len(s) != 0 {
		t.Errorf("AdviceSent %v", s)
	}
	if d := recordsOf(t, c, RecAdviceDismissed); len(d) != 1 || d[0]["consultation"] != "k-1" {
		t.Errorf("AdviceDismissed %v", d)
	}
	// The real advice records, under the helper's chat id.
	log := adviceLog("k-1", 2, "user")
	for _, typ := range []string{RecAdviceSent, RecAdviceDismissed} {
		for _, d := range recordsOf(t, c, typ) {
			log = append(log, chatRec(typ, d))
		}
	}
	cut := consultationsIn(log, "c-1", 1, 2)
	if len(cut.Sent) != 0 || len(cut.Dismissed) != 1 {
		t.Errorf("the cut: %+v", cut)
	}
}

// A queued advice turn that never starts (the process dies first) was
// never sent: dismissed once, never Sent.
func TestQueuedAdviceDroppedIsDismissedNotSent(t *testing.T) {
	st := newStub(nil)
	c := openChatWith(t.Context(), t, stubAdapter{st}, 0)
	first := send(t, c, "one")
	queued, err := c.SendAdvice(t.Context(), "k-1", "Inject a Clock.")
	if err != nil {
		t.Fatal(err)
	}
	st.die()
	drain(t, first, nil)
	if s := settledOf(t, drain(t, queued, nil)); s.Failure == "" {
		t.Errorf("queued advice settled %+v", s)
	}
	<-c.Done()
	if s := recordsOf(t, c, RecAdviceSent); len(s) != 0 {
		t.Errorf("AdviceSent %v", s)
	}
	if d := recordsOf(t, c, RecAdviceDismissed); len(d) != 1 || d[0]["consultation"] != "k-1" {
		t.Errorf("AdviceDismissed %v", d)
	}
}
