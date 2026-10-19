package run

import (
	"errors"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// An automatic Consultation under auto "ask" waits for the user like a
// /advise one: pending at a delivered cut, then Dismissed once, never twice.
func TestConsultationsInPendingAutoAskAdviceIsDisclosedOnce(t *testing.T) {
	log := adviceLog("k-1", 2, TriggerFeedbackBudget)
	cut1 := consultationsIn(log, "c-1", 1, 2)
	if !cut1.empty() || len(cut1.resolvedIDs()) != 0 {
		t.Fatalf("pending in cut 1: %+v", cut1)
	}
	log = append(log,
		chatRec(RecDelivered, map[string]any{"run": "r-1", "turn": 2, "resolved": cut1.resolvedIDs()}),
		chatRec(RecAdviceDismissed, map[string]any{"consultation": "k-1"}))
	cut2 := consultationsIn(log, "c-1", 3, 4)
	if len(cut2.Dismissed) != 1 || cut2.Dismissed[0].Consultation != "k-1" || !cut2.Dismissed[0].Before {
		t.Fatalf("cut 2: %+v", cut2)
	}
}

// Auto advice (auto "on") is shown, then dropped because the user's own
// turn started a new chain: exactly one AdviceDismissed, no AdviceSent.
func TestSendAutoAdviceDroppedByUserTurnIsDismissedOnce(t *testing.T) {
	var turns []multiturn.Turn
	for range FeedbackBudget + 4 {
		turns = append(turns, multiturn.Turn{Reply: "ok"})
	}
	c := openCut(t, "", turns...)
	drain(t, send(t, c, "yo"), nil)
	for range FeedbackBudget {
		ch, err := c.SendFeedback(t.Context(), "Öge's Check failed")
		if err != nil {
			t.Fatal(err)
		}
		drain(t, ch, nil)
	}
	withAdvisor(c, advisorScript(t, "echo 'Inject a Clock.'\n"))
	chain, _ := c.AutoState()
	adv, err := c.ConsultAuto(t.Context(), chain)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, send(t, c, "my own turn"), nil) // meanwhile
	if _, err := c.SendAutoAdvice(t.Context(), adv); !errors.Is(err, ErrChainEnded) {
		t.Fatalf("stale automatic advice: %v", err)
	}
	if d := recordsOf(t, c, RecAdviceDismissed); len(d) != 1 || d[0]["consultation"] != adv.ID {
		t.Errorf("AdviceDismissed %v", d)
	}
	if s := recordsOf(t, c, RecAdviceSent); len(s) != 0 {
		t.Errorf("AdviceSent %v", s)
	}
}
