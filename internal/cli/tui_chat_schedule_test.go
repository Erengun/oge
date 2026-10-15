package cli

import (
	"context"
	"testing"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

func TestQueuedFollowUpDefersVerificationUntilSettled(t *testing.T) {
	h := turnQueueHome()
	h.turn.changed = 2
	h.turn.queued = []string{"also handle empty input"}
	h.turnEnded()
	if h.verify != nil || !h.unchecked || h.turn == nil {
		t.Fatalf("intermediate tree verified or lost: verify=%v unchecked=%v turn=%v", h.verify, h.unchecked, h.turn)
	}
	done := make(chan struct{})
	h.chat.cut = func(context.Context, *run.Chat, *pipeline.Frozen, gate.Port) (*run.Result, error) {
		close(done)
		return nil, run.ErrNothingToCut
	}
	// The follow-up may only explain the earlier change. It must still cut
	// the edits deferred from the first turn.
	h.turn.changed = 0
	h.turnEnded()
	if h.verify == nil || h.unchecked || h.verify.changed != 2 {
		t.Fatalf("final tree not scheduled: verify=%v unchecked=%v", h.verify, h.unchecked)
	}
	<-done
	h.verify.cancel()
}

func TestNewChatClearsDeferredChanges(t *testing.T) {
	h := turnQueueHome()
	h.unchecked, h.uncheckedChanged = true, 2
	h.newCmd(nil)
	if h.unchecked || h.uncheckedChanged != 0 {
		t.Fatal("fresh conversation inherited the previous conversation's pending changes")
	}
}

func TestQueuedCommandDoesNotDeferVerification(t *testing.T) {
	h := turnQueueHome()
	h.turn.changed = 1
	h.turn.queued = []string{"/diff"}
	done := make(chan struct{})
	h.chat.cut = func(context.Context, *run.Chat, *pipeline.Frozen, gate.Port) (*run.Result, error) {
		close(done)
		return nil, run.ErrNothingToCut
	}
	h.turnEnded()
	if h.verify == nil {
		t.Fatal("queued command suppressed verification")
	}
	<-done
	h.verify.cancel()
}

func TestDeferredChangesKeepExistingVerificationTracked(t *testing.T) {
	h := turnQueueHome()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous := &chatVerify{seq: 1, cancel: cancel, changed: 3}
	h.verify = previous
	h.turn.changed = 2
	h.turn.queued = []string{"also handle empty input"}
	h.turnEnded()
	if h.verify != previous || ctx.Err() != nil || !h.unchecked {
		t.Fatal("deferral discarded existing verification or unchecked edits")
	}
	done := make(chan struct{})
	h.chat.cut = func(context.Context, *run.Chat, *pipeline.Frozen, gate.Port) (*run.Result, error) {
		close(done)
		return nil, run.ErrNothingToCut
	}
	h.turnEnded()
	if h.verify == previous || h.verify.changed != 3 || h.unchecked {
		t.Fatal("settled follow-up failed to carry pending and existing changes")
	}
	<-done
	h.verify.cancel()
}
