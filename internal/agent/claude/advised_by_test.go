package claude

import (
	"testing"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// An answer records the Consultation that advised it next to By=human: the
// advice never replaces who answered.
func TestAnswerRecordsAdvisedBy(t *testing.T) {
	s, _ := openAuto(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Ask: &multiturn.Ask{Tool: "AskUserQuestion", Target: "Ship it?"}},
	}})
	if err := s.Send(agent.Turn{Text: "ask"}); err != nil {
		t.Fatal(err)
	}
	evs := turnEvents(t, s, func(ev agent.Event) {
		if err := ev.Ask.Answer(agent.Answer{Allow: true, Answers: []string{"no"}, AdvisedBy: "c-1"}); err != nil {
			t.Error(err)
		}
	})
	for _, ev := range evs {
		if h := ev.Host; h != nil && h.By == "human" {
			if h.AdvisedBy != "c-1" || h.Decision != "allow" {
				t.Errorf("answer %+v", h)
			}
			return
		}
	}
	t.Errorf("no human answer in %+v", evs)
}
