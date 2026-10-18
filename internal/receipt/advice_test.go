package receipt_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/receipt"
	"github.com/erengun/oge/internal/run"
)

// A cut's Receipt discloses the advice that reached the agent in its
// turns, read from the Chat's log. The advice is a Claim: the Receipt
// never carries its text, and Dismissed Consultations stay out of the
// compact lines but are in the full text and the JSON.
func TestBuildDisclosesAdviceFromTheChatLog(t *testing.T) {
	chatDir := t.TempDir()
	cl, err := ledger.Create(chatDir)
	if err != nil {
		t.Fatal(err)
	}
	adv := map[string]string{"agent": "claude", "model": "opus"}
	for _, r := range []struct {
		typ string
		d   map[string]any
	}{
		{run.RecConsultationStarted, map[string]any{"consultation": "k-1", "turn": 2, "advisor": adv}}, // before the cut
		{run.RecConsultationEnded, map[string]any{"consultation": "k-1", "outcome": "advised"}},
		{run.RecAdviceSent, map[string]any{"consultation": "k-1", "turn": 3, "how": "edited", "by": "user"}}, // before the cut
		{run.RecConsultationStarted, map[string]any{"consultation": "k-2", "turn": 4, "advisor": adv}},
		{run.RecConsultationEnded, map[string]any{"consultation": "k-2", "outcome": "advised", "advice": "sha256:cc"}},
		{run.RecAdviceSent, map[string]any{"consultation": "k-2", "turn": 5, "how": "verbatim", "by": "oge"}},
		{run.RecConsultationStarted, map[string]any{"consultation": "k-3", "turn": 5, "advisor": adv}},
		{run.RecConsultationEnded, map[string]any{"consultation": "k-3", "outcome": "cancelled"}},
		{run.RecConsultationStarted, map[string]any{"consultation": "k-4", "turn": 5, "advisor": adv}}, // never ended
	} {
		r.d["chat"] = "c-1"
		if err := cl.Append(r.typ, r.d); err != nil {
			t.Fatal(err)
		}
	}
	cl.Close()
	runDir := t.TempDir()
	l, err := ledger.Create(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range scenario(t, "chat-cut-advice").Records() {
		data := r.Data
		if r.Type == run.RecRunStarted {
			var d map[string]any
			_ = json.Unmarshal(data, &d)
			d["chat_dir"], d["turns"] = chatDir, map[string]int{"from": 4, "to": 5}
			data, _ = json.Marshal(d)
		}
		if err := l.Append(r.Type, data); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	r, err := receipt.Build(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Chat.Advice; len(got) != 1 || got[0].Consultation != "k-2" || got[0].Turn != 5 || got[0].How != "verbatim" {
		t.Errorf("advice sent in the cut: %+v", got)
	}
	var out []string
	for _, d := range r.Chat.Dismissed {
		out = append(out, d.Consultation+":"+d.Outcome)
	}
	if strings.Join(out, " ") != "k-3:cancelled k-4:unfinished" {
		t.Errorf("dismissed: %v", out)
	}
	compact, full := r.Text(receipt.Paint{}), r.FullText(receipt.Paint{})
	if !strings.Contains(compact, "1 consultation sent at turn 5 (claude opus, fresh) · a Claim, not checked") {
		t.Errorf("the compact Receipt lacks the disclosure:\n%s", compact)
	}
	if strings.Contains(compact, "Dismissed") || !strings.Contains(full, "Dismissed     consultation asked at turn 5 (claude opus): cancelled") {
		t.Errorf("dismissed Consultations belong in the full Receipt only:\n%s\n---\n%s", compact, full)
	}
	if js, _ := r.JSON(); !strings.Contains(string(js), `"dismissed_consultations"`) {
		t.Errorf("JSON lacks the dismissed Consultations: %s", js)
	}

	// A Chat log that can't be read is said, not silently left out.
	if err := os.Remove(filepath.Join(chatDir, ledger.LedgerFile)); err != nil {
		t.Fatal(err)
	}
	r, err = receipt.Build(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.NotCovered, "\n"), "Chat's log is unreadable") {
		t.Errorf("an unreadable Chat log wasn't disclosed: %v", r.NotCovered)
	}
}

// Advice the user edited is the user's own turn: the Receipt says so.
func TestReceiptSaysEditedAdviceWasEdited(t *testing.T) {
	r := scenario(t, "chat-cut-advice").Receipt()
	r.WithAdvice([]receipt.AdviceSent{{Consultation: "k-1", Turn: 4, How: "edited", Agent: "claude", Model: "opus"}}, nil)
	if got := r.Text(receipt.Paint{}); !strings.Contains(got, "(claude opus, fresh, edited by you)") {
		t.Errorf("edited advice undisclosed:\n%s", got)
	}
}
