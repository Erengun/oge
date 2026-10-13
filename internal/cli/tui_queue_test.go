package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/task"
)

// queueHome is a home with a Run in progress, a counter of Runs started and
// an end that opens the composer again.
func queueHome(t *testing.T) (*home, *int) {
	t.Helper()
	started := new(int)
	h := testHome(70)
	h.now = func() time.Time { return time.Unix(0, 0) }
	t0 := time.Unix(0, 0)
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		*started++
		return newModel(tk, homeFrozen(), newStyles(false), func() time.Time { return t0 }), nil
	}
	h.after = func(ended) (string, *lastRun, bool) { return "summary", nil, false }
	h.comp.insert("fix Add")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.run == nil || *started != 1 {
		t.Fatal("no Run started")
	}
	return h, started
}

func typeInto(h *home, s string) {
	for _, r := range s {
		h.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestQueueTypingDoesNotReachTheRun(t *testing.T) {
	h, started := queueHome(t)
	h.run.expanded = false
	typeInto(h, "add tests")
	if h.next == nil || h.next.comp.text() != "add tests" || h.next.kept {
		t.Fatalf("draft: %+v", h.next)
	}
	if h.run.cancelling || h.run.expanded {
		t.Fatal("a key reached the Run")
	}
	got := h.View().Content
	if !strings.Contains(got, "next › add tests") || !strings.Contains(got, "queued: starts after this Run, Enter to keep") {
		t.Errorf("draft view:\n%s", got)
	}
	golden(t, "run-queue-draft", got)
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !h.next.kept || *started != 1 {
		t.Fatal("Enter should only keep it")
	}
	got = h.View().Content
	if !strings.Contains(got, "queued › add tests") || !strings.Contains(got, "ctrl+x to cancel") {
		t.Errorf("queued view:\n%s", got)
	}
	golden(t, "run-queue-kept", got)
	h.Update(ctrlKey('x'))
	if h.next != nil || strings.Contains(h.View().Content, "add tests") {
		t.Fatal("Ctrl+X did not cancel the queue")
	}
}

func TestQueueNothingUntilTyped(t *testing.T) {
	h, _ := queueHome(t)
	if strings.Contains(h.View().Content, "next ›") {
		t.Fatal("an empty queue is shown")
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.next != nil && h.next.kept {
		t.Fatal("Enter on nothing queued something")
	}
}

func TestQueueGateOwnsItsKeys(t *testing.T) {
	h, _ := queueHome(t)
	typeInto(h, "later")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	reply := make(chan gate.Decision, 1)
	r := boundExhaustionRequest()
	h.Update(batchMsg{gateMsg{req: r, reply: reply}})
	if h.run.gate == nil {
		t.Fatal("no Gate")
	}
	if strings.Contains(h.View().Content, "later") {
		t.Error("the queue shows over a Gate")
	}
	typeInto(h, "xyz")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.next == nil || h.next.comp.text() != "later" || !h.next.kept {
		t.Fatalf("a Gate key reached the queue: %+v", h.next)
	}
	if h.run.gate == nil || len(reply) != 0 {
		t.Fatal("the Gate should still be open and undecided")
	}
	// Ctrl+C at the Gate is the Gate's: the Run is cancelled, the queue stays.
	h.Update(ctrlKey('c'))
	if !h.run.cancelling || h.next == nil || h.next.comp.text() != "later" {
		t.Fatalf("Ctrl+C at a Gate: cancelling=%v next=%+v", h.run.cancelling, h.next)
	}
}

func TestQueueCtrlCClearsTheTextFirst(t *testing.T) {
	h, _ := queueHome(t)
	cancelled := 0
	h.run.interrupt = func() { cancelled++ }
	typeInto(h, "later")
	h.Update(ctrlKey('c'))
	if h.next != nil || cancelled != 0 || h.run.cancelling {
		t.Fatalf("first Ctrl+C: next=%v cancelled=%d", h.next, cancelled)
	}
	h.Update(ctrlKey('c'))
	if cancelled != 1 || !h.run.cancelling {
		t.Fatal("second Ctrl+C should cancel the Run")
	}
}

func TestQueueMovesIntoTheComposerAtTheEnd(t *testing.T) {
	for _, kept := range []bool{true, false} {
		h, started := queueHome(t)
		typeInto(h, "add tests")
		if kept {
			h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		h.Update(runEndedMsg{})
		if h.run != nil || *started != 1 {
			t.Fatalf("kept=%v: the Run ended, but a new one started (%d)", kept, *started)
		}
		if h.comp.text() != "add tests" || h.next != nil || h.note != queueReadyMsg {
			t.Fatalf("kept=%v: composer %q note %q", kept, h.comp.text(), h.note)
		}
		if h.comp.col != len("add tests") {
			t.Fatalf("cursor at %d", h.comp.col)
		}
		// An Enter already on its way is dropped; a later one starts it.
		h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if h.run != nil {
			t.Fatal("a typed-ahead Enter started the queued Task")
		}
		h.now = func() time.Time { return time.Now().Add(time.Hour) }
		h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if h.run == nil || *started != 2 {
			t.Fatal("Enter did not start the queued Task")
		}
	}
}

func TestQueueKeepsTheActionRow(t *testing.T) {
	h, _ := queueHome(t)
	h.last = &lastRun{}
	typeInto(h, "add tests")
	h.Update(runEndedMsg{})
	h.after = nil
	h.last = &lastRun{}
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !h.actions {
		t.Fatal("↓ no longer reaches the action row with the queued Task in the composer")
	}
}

func TestQueueIsNotRunByTheRunsKeys(t *testing.T) {
	h, _ := queueHome(t)
	h.run.expanded = true
	typeInto(h, "n")
	if h.next != nil {
		t.Fatal("expanded view keys went to the queue")
	}
}
