package cli

import (
	"errors"
	"testing"

	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/run"
)

func TestChatApplyOnAcceptRequiresSafeCheckedCut(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*home, *run.Result, *chatCutMsg)
		want   int
	}{
		{"opted in", func(*home, *run.Result, *chatCutMsg) {}, 1},
		{"default off", func(h *home, _ *run.Result, _ *chatCutMsg) { h.sel.apply = false }, 0},
		{"no setup", func(h *home, _ *run.Result, _ *chatCutMsg) { h.sel = nil }, 0},
		{"failed", func(_ *home, r *run.Result, _ *chatCutMsg) { r.Outcome = run.Failed }, 0},
		{"parked gate", func(_ *home, r *run.Result, _ *chatCutMsg) { r.Outcome = run.Parked }, 0},
		{"unchecked", func(h *home, _ *run.Result, _ *chatCutMsg) { h.unchecked = true }, 0},
		{"active turn", func(h *home, _ *run.Result, _ *chatCutMsg) { h.turn = &chatTurn{} }, 0},
		{"cancelled", func(h *home, _ *run.Result, _ *chatCutMsg) { h.verify.stopped = true }, 0},
		{"superseded", func(_ *home, _ *run.Result, m *chatCutMsg) { m.seq = 0 }, 0},
		{"error", func(_ *home, _ *run.Result, m *chatCutMsg) { m.err = errors.New("verification failed") }, 0},
		{"check failed", func(_ *home, r *run.Result, _ *chatCutMsg) { r.Check.Pass = false }, 0},
		{"no check", func(_ *home, r *run.Result, _ *chatCutMsg) { r.Check = nil }, 0},
		{"shell active", func(h *home, _ *run.Result, _ *chatCutMsg) { h.job = &shellJob{} }, 0},
		{"stale decision", func(h *home, _ *run.Result, _ *chatCutMsg) { h.stale = &staleAsk{} }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applied := 0
			checked := &lastRun{id: "checked", apply: func() string { applied++; return "applied checked" }}
			h := &home{st: newStyles(false), comp: newComposer(), sel: &setupState{apply: true},
				verify: &chatVerify{seq: 1, cancel: func() {}},
				chat:   &homeChat{verdict: func(*run.Result, int) (string, *lastRun) { return "✓ Accepted", checked }},
			}
			r := &run.Result{Outcome: run.Accepted, Check: &oracle.Result{Pass: true}}
			m := chatCutMsg{seq: 1, res: r}
			tc.change(h, r, &m)
			h.cutEnded(m)
			if applied != tc.want {
				t.Fatalf("applied %d times, want %d", applied, tc.want)
			}
			// Replayed completion cannot retry a refused/conflicting delivery.
			h.cutEnded(m)
			if applied != tc.want {
				t.Fatalf("replayed completion retried delivery: %d", applied)
			}
		})
	}
}
