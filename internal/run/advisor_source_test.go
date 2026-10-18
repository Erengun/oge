package run

import (
	"strings"
	"testing"
)

// The Briefing manifest names where the question came from: the agent's
// own AskUserQuestion is agent text, so it is labelled a Claim.
func TestAdviceQuestionSourceByTrigger(t *testing.T) {
	for trigger, want := range map[string]string{
		"user":                "the user's question",
		"ask":                 "a Claim",
		TriggerFeedbackBudget: "Öge's question",
	} {
		c := &Chat{}
		_, items, _, _ := c.adviceBriefing("q?", trigger)
		src, _ := items[0]["source"].(string)
		if items[0]["item"] != "question" || !strings.Contains(src, want) {
			t.Errorf("%s: source %q, want it to name %q", trigger, src, want)
		}
	}
}
