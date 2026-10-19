package run

import (
	"testing"
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
