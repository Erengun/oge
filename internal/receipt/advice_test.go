package receipt_test

import (
	"strings"
	"testing"

	"github.com/erengun/oge/internal/receipt"
)

// A cut's Receipt discloses the advice the cut's RunStarted recorded: a
// Claim for verbatim advice, the user's own turn for edited advice.
// Dismissed Consultations stay out of the compact lines and are in the
// full text, the Markdown's evidence block and the JSON.
func TestReceiptDisclosesAdviceFromTheCut(t *testing.T) {
	r := scenario(t, "chat-cut-advice").Receipt()
	compact, full := r.Text(receipt.Paint{}), r.FullText(receipt.Paint{})
	for _, want := range []string{
		"3 consultations",
		"on the agent's question at turn 4 (claude opus): shown to you · a Claim, not checked",
		"sent at turn 3 (claude opus, fresh) · edited by you, sent as your turn",
		"sent at turn 4 (claude opus, fresh, automatic) · a Claim, not checked",
	} {
		if !strings.Contains(compact, want) {
			t.Errorf("the compact Receipt lacks %q:\n%s", want, compact)
		}
	}
	if strings.Contains(compact, "Dismissed") || !strings.Contains(full, "Dismissed     consultation asked at turn 4 (claude opus): cancelled") {
		t.Errorf("dismissed Consultations belong in the full Receipt only:\n%s\n---\n%s", compact, full)
	}
	if md := r.Markdown(); !strings.Contains(md, "Dismissed: consultation asked at turn 4") || strings.Contains(md, "` `") {
		t.Errorf("Markdown: dismissed missing, or the advisor split into code spans:\n%s", md)
	}
	if js, _ := r.JSON(); !strings.Contains(string(js), `"dismissed_consultations"`) {
		t.Errorf("JSON lacks the dismissed Consultations: %s", js)
	}
	// The Receipt is of the Ledger alone: built again, it is the same.
	if again := scenario(t, "chat-cut-advice").Receipt().FullText(receipt.Paint{}); again != full {
		t.Errorf("a Receipt built twice differs:\n%s\n---\n%s", full, again)
	}
}
