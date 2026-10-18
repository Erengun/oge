package run

import (
	"encoding/json"
	"testing"

	"github.com/erengun/oge/internal/ledger"
)

func chatRec(typ string, d map[string]any) ledger.Record {
	d["chat"] = "c-1"
	raw, _ := json.Marshal(d)
	return ledger.Record{Type: typ, Data: raw}
}

// A cut's Consultations are those of its turns as of the cut: one asked
// after it, or one still running, is not in it, and a later AdviceSent
// can't change what an earlier read said.
func TestConsultationsInIsASnapshotAtCutTime(t *testing.T) {
	adv := map[string]string{"agent": "claude", "model": "opus"}
	log := []ledger.Record{
		chatRec(RecConsultationStarted, map[string]any{"consultation": "k-1", "turn": 2, "advisor": adv}),
		chatRec(RecConsultationEnded, map[string]any{"consultation": "k-1", "outcome": "advised", "advice": "sha256:aa"}),
		chatRec(RecConsultationStarted, map[string]any{"consultation": "k-2", "turn": 2, "advisor": adv}),
		chatRec(RecConsultationEnded, map[string]any{"consultation": "k-2", "outcome": "cancelled"}),
		chatRec(RecConsultationStarted, map[string]any{"consultation": "k-3", "turn": 2, "advisor": adv}), // running at the cut
	}
	at := consultationsIn(log, "c-1", 1, 2)
	if len(at.Sent) != 0 || len(at.Dismissed) != 2 || at.Dismissed[0].Consultation != "k-1" || at.Dismissed[1].Outcome != ConsultCancelled {
		t.Fatalf("at the cut: %+v", at)
	}

	// After the cut: k-1's advice is sent at turn 3, and k-4 is asked.
	log = append(log,
		chatRec(RecAdviceSent, map[string]any{"consultation": "k-1", "turn": 3, "how": "verbatim"}),
		chatRec(RecConsultationStarted, map[string]any{"consultation": "k-4", "turn": 3, "advisor": adv}),
		chatRec(RecConsultationEnded, map[string]any{"consultation": "k-4", "outcome": "advised"}))
	if again := consultationsIn(log[:5], "c-1", 1, 2); len(again.Dismissed) != 2 {
		t.Errorf("the cut's own read changed: %+v", again)
	}
	// The recorded snapshot is what the cut keeps: nothing of turn 3's.
	late := consultationsIn(log, "c-1", 3, 3)
	if len(late.Sent) != 1 || late.Sent[0].Agent != "claude" || late.Sent[0].Advice != "sha256:aa" || len(late.Dismissed) != 1 || late.Dismissed[0].Consultation != "k-4" {
		t.Errorf("the next cut: %+v", late)
	}
}

// An ask Consultation's advice goes to the human, never the agent: landed,
// it is Shown (not Dismissed); cancelled or failed it keeps its outcome.
func TestConsultationsInShowsALandedAskConsultation(t *testing.T) {
	adv := map[string]string{"agent": "claude", "model": "opus"}
	ask := func(id, outcome string) []ledger.Record {
		return []ledger.Record{
			chatRec(RecConsultationStarted, map[string]any{"consultation": id, "turn": 2, "advisor": adv, "trigger": TriggerAsk}),
			chatRec(RecConsultationEnded, map[string]any{"consultation": id, "outcome": outcome, "advice": "sha256:aa"}),
		}
	}
	log := append(append(ask("k-1", ConsultAdvised), ask("k-2", ConsultCancelled)...), ask("k-3", ConsultFailed)...)
	got := consultationsIn(log, "c-1", 1, 2)
	if len(got.Shown) != 1 || got.Shown[0].Consultation != "k-1" || got.Shown[0].Advice != "sha256:aa" || got.Shown[0].Model != "opus" || len(got.Sent) != 0 {
		t.Fatalf("shown: %+v", got)
	}
	if len(got.Dismissed) != 2 || got.Dismissed[0].Outcome != ConsultCancelled || got.Dismissed[1].Outcome != ConsultFailed {
		t.Fatalf("dismissed: %+v", got.Dismissed)
	}
}

// A Consultation asked after a delivery, before the next turn, records the
// delivered turn: it belongs to the next cut. One from before the delivery
// stays out of it.
func TestConsultationsInAfterADeliveryBelongsToTheNextCut(t *testing.T) {
	adv := map[string]string{"agent": "claude", "model": "opus"}
	log := []ledger.Record{
		chatRec(RecConsultationStarted, map[string]any{"consultation": "k-1", "turn": 2, "advisor": adv}),
		chatRec(RecConsultationEnded, map[string]any{"consultation": "k-1", "outcome": "advised"}),
		chatRec(RecDelivered, map[string]any{"run": "r-1", "turn": 2}),
		chatRec(RecConsultationStarted, map[string]any{"consultation": "k-2", "turn": 2, "advisor": adv}),
		chatRec(RecConsultationEnded, map[string]any{"consultation": "k-2", "outcome": "advised"}),
	}
	next := consultationsIn(log, "c-1", 3, 4)
	if len(next.Dismissed) != 1 || next.Dismissed[0].Consultation != "k-2" || next.Dismissed[0].Turn != 3 {
		t.Fatalf("next cut: %+v", next)
	}
	if prev := consultationsIn(log, "c-1", 1, 2); len(prev.Dismissed) != 1 || prev.Dismissed[0].Consultation != "k-1" {
		t.Fatalf("the delivered cut: %+v", prev)
	}
}
