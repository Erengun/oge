package cli

import (
	"context"
	"testing"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// A turn's cut runs in the Pipeline the turn was sent in, so a Shift+Tab
// to Standard after the Chat opened in Fast gets QA (B3).
func TestChatCutUsesTheTurnsPipeline(t *testing.T) {
	standard := &pipeline.Frozen{Mode: pipeline.Standard}
	var got *pipeline.Frozen
	h := &home{chat: &homeChat{cut: func(_ context.Context, _ *run.Chat, f *pipeline.Frozen) (*run.Result, error) {
		got = f
		return nil, run.ErrNothingToCut
	}}, turn: &chatTurn{frozen: standard, changed: 1}}
	_, cmd := h.turnEnded()
	if cmd == nil {
		t.Fatal("a turn with changes wasn't cut")
	}
	if _, ok := cmd().(chatCutMsg); !ok || got != standard {
		t.Errorf("the cut ran in %v, not the turn's Pipeline", got)
	}
}

// A cut refused for setup drift says so, and how to go on.
func TestVerdictLineSetupDrift(t *testing.T) {
	res := &run.Result{Outcome: run.Refused, Why: []string{run.WhySetupChanged}}
	if got, want := verdictLine(res, 1), "✗ Refused: setup changed since this Chat opened; /new to start a new Chat"; got != want {
		t.Errorf("verdictLine = %q, want %q", got, want)
	}
}
