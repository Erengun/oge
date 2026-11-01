package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// A turn's cut runs in the Pipeline the turn was sent in, so a Shift+Tab
// to Standard after the Chat opened in Fast gets QA (B3).
func TestChatCutUsesTheTurnsPipeline(t *testing.T) {
	standard := &pipeline.Frozen{Mode: pipeline.Standard}
	var got *pipeline.Frozen
	h := &home{chatView: &chatView{comp: newComposer(), chat: &homeChat{cut: func(_ context.Context, _ *run.Chat, f *pipeline.Frozen, _ gate.Port, _ func(run.Event)) (*run.Result, error) {
		got = f
		return nil, run.ErrNothingToCut
	}},
		turn: &chatTurn{frozen: standard, changed: 1}},
	}
	batch, _ := h.startCut()().(tea.BatchMsg)
	if len(batch) == 0 {
		t.Fatal("the cut returned no command")
	}
	if _, ok := batch[0]().(chatCutMsg); !ok || got != standard {
		t.Errorf("the cut ran in %v, not the turn's Pipeline", got)
	}
}

// A cut's progress events name the status line's step; a stale seq's are
// dropped with it (#296).
func TestChatVerifyNamesAStep(t *testing.T) {
	ready := make(chan func(run.Event), 1)
	h := &home{now: func() time.Time { return time.Unix(0, 0) },
		chatView: &chatView{comp: newComposer(), chat: &homeChat{cut: func(ctx context.Context, _ *run.Chat, _ *pipeline.Frozen, _ gate.Port, o func(run.Event)) (*run.Result, error) {
			ready <- o
			<-ctx.Done() // the cut holds until the test ends it
			return nil, ctx.Err()
		}},
			turn: &chatTurn{frozen: &pipeline.Frozen{}, changed: 1}},
	}
	batch, _ := h.startCut()().(tea.BatchMsg)
	if len(batch) == 0 {
		t.Fatal("the cut returned no command")
	}
	wait := batch[0]
	observe := <-ready
	observe(run.Event{Kind: run.EvStage, Step: "preparing"})
	m, ok := wait().(chatStepMsg)
	if !ok {
		t.Fatalf("wait returned %T, want chatStepMsg", m)
	}
	h.cutStepped(m)
	if h.verify.step != "preparing the baseline" {
		t.Errorf("step %q, want the baseline named", h.verify.step)
	}
	h.cutStepped(chatStepMsg{seq: h.verify.seq, ev: run.Event{Kind: run.EvStage, Step: "verify", Attempt: &run.Attempt{Role: "verifier"}}})
	if h.verify.step != "QA writing held-out tests" {
		t.Errorf("step %q, want QA named", h.verify.step)
	}
	// The verifier Attempt's end is back to the bare verifying until the
	// Check's own step starts.
	h.cutStepped(chatStepMsg{seq: h.verify.seq, ev: run.Event{Kind: run.EvAttempt, Attempt: &run.Attempt{Role: "verifier"}}})
	if h.verify.step != "" {
		t.Errorf("step %q after the verifier Attempt, want the bare verifying", h.verify.step)
	}
	h.cutStepped(chatStepMsg{seq: h.verify.seq, ev: run.Event{Kind: run.EvStage, Step: "checks"}})
	if h.verify.step != "running the Checks" {
		t.Errorf("step %q, want the Checks named", h.verify.step)
	}
	h.cutStepped(chatStepMsg{seq: h.verify.seq + 9, ev: run.Event{Kind: run.EvStage, Step: "preparing"}})
	if h.verify.step != "running the Checks" {
		t.Errorf("a stale seq's step landed: %q", h.verify.step)
	}
	h.verify.cancel()
}

// A cut refused for setup drift says so, and how to go on.
func TestVerdictLineSetupDrift(t *testing.T) {
	res := &run.Result{Outcome: run.Refused, Why: []string{run.WhySetupChanged}}
	if got, want := verdictLine(res, 1), "✗ Refused: setup changed since this Chat opened; /new to start a new Chat"; got != want {
		t.Errorf("verdictLine = %q, want %q", got, want)
	}
}

// An ask's rows are wrapped by display width: no row is wider than the
// frame, with wide characters too. When a resize leaves the prompt too
// tall to show, y does nothing and the prompt says why.
func TestAskRowsFitTheFrame(t *testing.T) {
	var got []agent.Answer
	a := agent.NewAsk("Bash", "echo", "echo "+strings.Repeat("漢字😀", 12), func(ans agent.Answer) error {
		got = append(got, ans)
		return nil
	})
	h := &home{width: 30, height: 24, st: newStyles(false), chatView: &chatView{comp: newComposer(), turn: &chatTurn{asks: []*agent.Ask{a}}}}
	for _, r := range h.askRows(a) {
		if w := ansi.StringWidth("    " + r); w > h.width {
			t.Errorf("row %q is %d wide in a %d-wide frame", r, w, h.width)
		}
	}
	if !h.askFits(a) {
		t.Fatal("the ask doesn't fit at 30 columns")
	}
	h.width = 12 // more than 8 rows now
	if h.askFits(a) {
		t.Fatal("the ask fits at 12 columns")
	}
	if v := h.turnView(); !strings.Contains(v, "too long to show at this width") {
		t.Errorf("the prompt doesn't say why y is off:\n%s", v)
	}
	h.chatKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(got) != 0 || h.ask() != a {
		t.Errorf("y approved what wasn't shown: %+v", got)
	}
}

// AskUserQuestion's questions are asked one at a time, each with its own
// numbers, and answered together; what was typed before is queued.
func TestQuestionsOneAtATime(t *testing.T) {
	var got []agent.Answer
	a := agent.NewAsk("AskUserQuestion", "a question", "Which? Name?", func(ans agent.Answer) error {
		got = append(got, ans)
		return nil
	})
	a.Questions = []agent.AskQuestion{{Text: "Which?", Options: []string{"red", "blue"}}, {Text: "Name?", Options: []string{"Ada"}}}
	h := &home{width: 80, height: 24, st: newStyles(false), chatView: &chatView{comp: newComposer(), turn: &chatTurn{}}}
	h.comp.set("a draft")
	h.turn.asks = []*agent.Ask{a}
	h.showAsk()
	if h.comp.text() != "" || len(h.turn.queued) != 1 || h.turn.queued[0] != "a draft" {
		t.Fatalf("the draft wasn't queued: %q, %q", h.comp.text(), h.turn.queued)
	}
	if v := h.turnView(); !strings.Contains(v, "  Which?") || strings.Contains(v, "Name?") || strings.Contains(v, "oge › Which?") {
		t.Errorf("first question view:\n%s", v)
	}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	h.comp.set("2")
	h.chatKey(enter)
	if v := h.turnView(); !strings.Contains(v, "Name?") {
		t.Errorf("second question not shown:\n%s", v)
	}
	h.comp.set("Grace")
	h.chatKey(enter)
	if len(got) != 1 || !got[0].Allow || strings.Join(got[0].Answers, ",") != "blue,Grace" {
		t.Errorf("answers %+v", got)
	}
}

// A cut's end moves h.last only with a Verdict, decided from its Run,
// not from Ctrl+C: one that reached its Verdict first shows it. Either
// way the action row closes, so [a] never takes a cut not yet seen.
func TestCutEndedDecidesFromTheRun(t *testing.T) {
	older, newer := &lastRun{id: "older"}, &lastRun{id: "newer"}
	for _, tc := range []struct {
		name    string
		res     *run.Result
		want    *lastRun
		printed string
	}{
		{"verdict after ctrl+c", &run.Result{Outcome: run.Accepted, Check: &oracle.Result{Pass: true}}, newer, "✓ shown"},
		{"cancelled", &run.Result{Outcome: run.InfrastructureStop}, nil, "verification cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &home{st: newStyles(false),
				chatView: &chatView{comp: newComposer(), last: older,
					actions: true,
					chat:    &homeChat{verdict: func(*run.Result, int) (string, *lastRun) { return "✓ shown", newer }},
					verify:  &chatVerify{seq: 1, cancel: func() {}, stopped: true}},
			}
			h.cutEnded(chatCutMsg{seq: 1, res: tc.res})
			if h.last != tc.want || h.actions || h.verify != nil {
				t.Errorf("last %v, actions %v, verify %v", h.last, h.actions, h.verify)
			}
			if got := strings.Join(h.scroll, "\n"); !strings.Contains(got, tc.printed) {
				t.Errorf("printed %q, want %q", got, tc.printed)
			}
		})
	}
	// A superseded cut, or one /new let go, ends unshown.
	h := &home{st: newStyles(false), chatView: &chatView{comp: newComposer(), last: older, verify: &chatVerify{seq: 2, cancel: func() {}}}}
	h.cutEnded(chatCutMsg{seq: 1, res: &run.Result{Outcome: run.Accepted, Check: &oracle.Result{Pass: true}}})
	h.cutEnded(chatCutMsg{seq: 2, err: run.ErrSuperseded})
	if h.last != older || len(h.scroll) != 0 {
		t.Errorf("a superseded cut showed: last %v, %q", h.last, h.scroll)
	}
}

// A command in the composer when a question takes it is never queued for
// the agent: it is back in the composer when the turn ends.
func TestShowAskKeepsASlashCommandFromTheAgent(t *testing.T) {
	a := agent.NewAsk("AskUserQuestion", "", "", func(agent.Answer) error { return nil })
	a.Questions = []agent.AskQuestion{{Text: "Which?"}}
	h := &home{st: newStyles(false),
		resolve:  func(*setupState) (*resolved, []string) { return nil, nil },
		chatView: &chatView{comp: newComposer(), turn: &chatTurn{asks: []*agent.Ask{a}}},
	}
	h.comp = h.composer()
	h.comp.set("/apply")
	h.showAsk()
	if len(h.turn.queued) != 1 || h.comp.text() != "" {
		t.Fatalf("queued %q, composer %q", h.turn.queued, h.comp.text())
	}
	if queued := h.endTurn(); len(queued) != 1 || queued[0] != "/apply" {
		t.Errorf("after the turn: queued %q", queued)
	}
}

// Öge's feedback turn shows as busy as a user turn: the live area says
// it is working, and Esc interrupts it.
func TestFeedbackTurnShowsWorking(t *testing.T) {
	h := &home{width: 80, height: 24, st: newStyles(false), chatView: &chatView{comp: newComposer(), turn: &chatTurn{oge: true, text: "Öge's Check failed on your last change: 1 failing."}}}
	h.comp = h.composer()
	if v := h.view().Content; !strings.Contains(v, "working… · esc to interrupt") || strings.Contains(v, "[ctrl+e] editor") {
		t.Errorf("the feedback turn's live area:\n%s", v)
	}
}

func turnQueueHome() *home {
	h := &home{width: 80,
		height:  24,
		st:      newStyles(false),
		hist:    &history{},
		resolve: func(*setupState) (*resolved, []string) { return &resolved{frozen: &pipeline.Frozen{}}, nil },
		chatView: &chatView{comp: newComposer(), turn: &chatTurn{},
			chat: &homeChat{}},
	}
	h.comp = h.composer()
	return h
}

// During a turn the box stays, with each queued message dim above it, and
// Up on an empty box takes the last one back to edit.
func TestQueuedMessagesStayVisible(t *testing.T) {
	h := turnQueueHome()
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	for _, m := range []string{"first", "/diff", "second"} {
		h.comp.set(m)
		h.chatKey(enter)
	}
	v := h.turnView()
	for _, want := range []string{"working…", "queued › first", "queued › /diff", "queued › second", "╭─", "Type to queue a message"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	h.chatKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if h.comp.text() != "second" || len(h.turn.queued) != 2 {
		t.Fatalf("up: composer %q, queued %q", h.comp.text(), h.turn.queued)
	}
	h.chatKey(tea.KeyPressMsg{Code: tea.KeyUp}) // the box isn't empty: no pull
	if len(h.turn.queued) != 2 {
		t.Errorf("up on a filled box pulled: %q", h.turn.queued)
	}
}

// Everything queued is one turn, in order with a blank line between.
func TestQueuedMessagesAreOneTurn(t *testing.T) {
	h := turnQueueHome()
	h.turn.queued = []string{"first", "second"}
	queued := h.endTurn()
	h.submitQueued(queued)
	if h.turn == nil || h.turn.text != "first\n\nsecond" {
		t.Fatalf("turn %+v", h.turn)
	}
}

// A paste over the limit, queued during a turn, reaches the turn whole.
func TestQueuedPasteIsKeptWhole(t *testing.T) {
	h := turnQueueHome()
	big := strings.Repeat("line of the paste\n", pasteLines+5)
	h.comp.insertPaste(big)
	h.chatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(h.turn.queued) != 1 || h.turn.queued[0] != strings.TrimSpace(big) {
		t.Fatalf("queued %q", h.turn.queued)
	}
	h.submitQueued(h.endTurn())
	if h.turn == nil || h.turn.text != strings.TrimSpace(big) {
		t.Fatalf("turn text %v", h.turn)
	}
}

// With a command queued, it runs and the messages wait, unsent, in the
// composer with the draft: no turn starts under the command's prompt.
func TestQueuedCommandHoldsTheMessages(t *testing.T) {
	h := turnQueueHome()
	h.turn.queued = []string{"first", "/nosuch", "!echo hi", "second"}
	h.comp.set("draft")
	h.submitQueued(h.endTurn())
	if !strings.Contains(strings.Join(h.msg, "\n"), "not available") { // the shell line ran last
		t.Errorf("the commands didn't run: %q", h.msg)
	}
	if h.turn != nil {
		t.Fatalf("a turn started: %+v", h.turn)
	}
	if got, want := h.comp.text(), "first\n\nsecond\n\ndraft"; got != want {
		t.Errorf("composer %q, want %q", got, want)
	}
}

// Esc in vim INSERT with text in the box is vim's, never the advice's.
func TestAdviceEscInVimInsertIsVims(t *testing.T) {
	h := &home{st: newStyles(false),
		vim:      true,
		chatView: &chatView{comp: newComposer(), adv: &adviseState{shown: true, cancel: func() {}, advice: &run.Advice{ID: "c1", Text: "x"}}},
	}
	h.comp.set("typing")
	if _, ok := h.adviceKey(tea.KeyPressMsg{Code: tea.KeyEscape}); ok || h.adv == nil {
		t.Errorf("Esc dismissed the advice from vim INSERT")
	}
	h.comp.set("")
	if _, ok := h.adviceKey(tea.KeyPressMsg{Code: tea.KeyEscape}); !ok || h.adv != nil {
		t.Errorf("Esc on an empty box didn't dismiss it")
	}
}

// Advice for a Chat that has gone is never sent.
func TestAdviceSendAfterTheChatClosed(t *testing.T) {
	h := &home{st: newStyles(false),
		chatView: &chatView{comp: newComposer(), chat: &homeChat{},
			adv: &adviseState{shown: true, chat: &run.Chat{}, advice: &run.Advice{ID: "c1", Text: "x"}}},
	}
	if cmd := h.sendAdvice(true); cmd != nil || h.adv != nil || h.turn != nil || len(h.msg) == 0 {
		t.Errorf("cmd %v adv %v turn %v msg %v", cmd != nil, h.adv, h.turn, h.msg)
	}
}

// A cut with nothing to cut against, after changes, says they are not
// checked, and the next cut covers them.
func TestCutNothingToCutAfterChangesIsUnchecked(t *testing.T) {
	h := &home{st: newStyles(false), chatView: &chatView{comp: newComposer(), verify: &chatVerify{seq: 1, changed: 2, cancel: func() {}}}}
	h.cutEnded(chatCutMsg{seq: 1, err: run.ErrNothingToCut})
	if !h.unchecked || !strings.Contains(strings.Join(h.scroll, "\n"), "not checked") {
		t.Errorf("unchecked %v, printed %q", h.unchecked, h.scroll)
	}
	h = &home{st: newStyles(false), chatView: &chatView{comp: newComposer(), verify: &chatVerify{seq: 1, cancel: func() {}}}}
	h.cutEnded(chatCutMsg{seq: 1, err: run.ErrNothingToCut})
	if h.unchecked || len(h.scroll) != 0 {
		t.Errorf("a cut of nothing said %q", h.scroll)
	}
}

// /advise typed while a question waits runs as a command: it is never
// the question's answer, and its refusal leaves the text in the box.
func TestAdviseDuringAQuestionIsNotTheAnswer(t *testing.T) {
	var got []agent.Answer
	a := agent.NewAsk("AskUserQuestion", "q", "Which?", func(ans agent.Answer) error {
		got = append(got, ans)
		return nil
	})
	a.Questions = []agent.AskQuestion{{Text: "Which?", Options: []string{"red"}}}
	h := &home{width: 80, height: 24, st: newStyles(false), chatView: &chatView{comp: newComposer(), turn: &chatTurn{}}}
	h.turn.asks = []*agent.Ask{a}
	h.showAsk()
	h.comp.set("/advise should I?")
	h.chatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(got) != 0 || len(h.turn.answers) != 0 || h.ask() != a {
		t.Errorf("the command answered the question: %+v %v", got, h.turn.answers)
	}
	if len(h.msg) == 0 || h.comp.text() != "/advise should I?" {
		t.Errorf("msg %v, box %q", h.msg, h.comp.text())
	}
}
