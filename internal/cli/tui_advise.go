package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// /advise (docs/ux/advisor.md): a Consultation runs in the background
// while the composer stays live and the implementer keeps working. Its
// advice prints as one block once no turn is streaming, and waits for one
// key: ↵ sends it to the agent as Öge's dim turn, e opens it in the
// composer (what is sent from there is the user's turn), esc dismisses it.
//
// Esc rule: while a turn runs, Esc interrupts the turn and nothing else
// (the Consultation goes on). Once no turn runs, Esc cancels a
// Consultation still advising, else dismisses the advice shown. The keys
// ↵ and e act on the advice only while the composer is empty, so they are
// never taken from a message being typed.

// adviseState is the session's one Consultation: advising, then its
// advice, until it is sent, edited or dismissed.
type adviseState struct {
	seq     int
	cancel  context.CancelFunc
	started time.Time
	agent   string    // "claude opus"
	chat    *run.Chat // the Chat it advises: another Chat's advice is never sent
	running bool
	advice  *run.Advice // landed
	shown   bool        // its block is printed: ↵ e esc act on it
	// auto: the chain's automatic Consultation (feedback budget spent,
	// D-A3). With mode "on" its advice is sent when it lands; with "ask"
	// it waits for ↵ like any other.
	auto bool
	mode string
}

type (
	// adviceMsg is the end of the Consultation seq.
	adviceMsg struct {
		seq    int
		advice *run.Advice
		err    error
	}
	// adviceTickMsg redraws the elapsed seconds of the Consultation seq.
	adviceTickMsg struct{ seq int }
)

func adviceTick(seq int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return adviceTickMsg{seq} })
}

// adviseCmd is /advise <question>.
func (h *home) adviseCmd(args []string) (tea.Cmd, string) {
	question := strings.Join(args, " ")
	if question == "" {
		return nil, usageOf("/advise")
	}
	var c *run.Chat
	if h.chat != nil {
		c = h.chat.get()
	}
	switch {
	case c == nil:
		return nil, "/advise: no Chat yet; send a task first"
	case h.adv != nil && h.adv.running, h.turn != nil && h.turn.askAdv != nil && h.turn.askAdv.running:
		return nil, "/advise: already advising"
	case h.adv != nil && h.turn != nil:
		return nil, "/advise: an advice is already waiting"
	case h.adv != nil:
		return nil, "/advise: the advice above waits for ↵, e or esc"
	}
	agentName, model, err := c.Advisor()
	if err != nil {
		return nil, "/advise: " + clean(err.Error())
	}
	parent := h.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	h.advSeq++
	seq := h.advSeq
	h.adv = &adviseState{chat: c, seq: seq, cancel: cancel, started: h.clock(), running: true,
		agent: strings.TrimSpace(agentName + " " + model)}
	consult := func() tea.Msg {
		a, err := c.Consult(ctx, question)
		return adviceMsg{seq: seq, advice: a, err: err}
	}
	echo := h.println(h.userBar("> /advise " + clean(question)))
	return tea.Batch(tea.Sequence(echo), consult, adviceTick(seq)), ""
}

// adviseStatus is the line shown while a Consultation runs. Its Esc hint
// is shown only when Esc would cancel it, which is not during a turn.
func (h *home) adviseStatus() string {
	a := h.adv
	if a == nil || !a.running {
		return ""
	}
	secs := max(int(h.clock().Sub(a.started)/time.Second), 0)
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	line := fmt.Sprintf(" %s advising · %s · fresh, read-only · %ds", frames[secs%len(frames)], clean(a.agent), secs)
	if h.turn == nil {
		line += "   esc cancel"
	}
	return h.st.dim(line)
}

// adviceKeys is the line under a printed advice block.
func (h *home) adviceKeys() string {
	if h.adv == nil || !h.adv.shown {
		return ""
	}
	return " " + h.st.accent("↵") + h.st.dim(" send to agent   ") + h.st.accent("e") + h.st.dim(" edit first   ") +
		h.st.accent("esc") + h.st.dim(" dismiss")
}

// adviceLanded takes a Consultation's end. The advice itself prints once
// no turn is streaming (flushAdvice).
func (h *home) adviceLanded(m adviceMsg) tea.Cmd {
	a := h.adv
	if a == nil || a.seq != m.seq || !a.running {
		return nil // cancelled, or a newer one
	}
	a.running = false
	switch {
	case errors.Is(m.err, context.Canceled):
		h.adv = nil
		return nil
	case errors.Is(m.err, run.ErrChainEnded):
		h.adv = nil // the user has moved on: nothing to say
		return nil
	case m.err != nil && a.auto:
		// A cut that fails stops the chain as it did before: nothing sent.
		h.adv = nil
		return h.println(h.ogeLine("✗ still failing · your call (" + clean(m.err.Error()) + ")"))
	case m.err != nil:
		h.adv = nil
		return h.println(h.ogeLine("✗ " + clean(m.err.Error())))
	}
	a.advice = m.advice
	return nil
}

// flushAdvice prints a landed advice block once no turn is in flight, so
// the transcript stays in order.
func (h *home) flushAdvice() tea.Cmd {
	a := h.adv
	if a == nil || a.advice == nil || a.shown || h.turn != nil || h.quit {
		return nil
	}
	a.shown = true
	block := h.println(h.adviceBlock(a.advice))
	if a.auto && a.mode == pipeline.AdvisorAutoOn {
		// Automatic (D-A3): printed, then sent as Öge's advice turn.
		return tea.Sequence(block, h.sendAdvice())
	}
	return block
}

// autoConsult runs the chain's one automatic Consultation after the
// feedback budget is spent (D-A3, D-A5a), per [advisor] auto: "off" runs
// nothing. It reports whether one started, and its Cmd.
func (h *home) autoConsult() (tea.Cmd, bool) {
	var c *run.Chat
	if h.chat != nil {
		c = h.chat.get()
	}
	if c == nil || h.adv != nil || c.AdvisorAuto() == pipeline.AdvisorAutoOff {
		return nil, false
	}
	if h.turn != nil && len(h.turn.queued) > 0 {
		return nil, false // the user's next turn supersedes it: a new chain
	}
	chain, used := c.AutoState()
	if used {
		return nil, false // the chain's one is spent: the user's call
	}
	agentName, model, err := c.Advisor()
	if err != nil {
		return nil, false
	}
	parent := h.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	h.advSeq++
	seq := h.advSeq
	h.adv = &adviseState{chat: c, seq: seq, cancel: cancel, started: h.clock(), running: true, auto: true,
		mode: c.AdvisorAuto(), agent: strings.TrimSpace(agentName + " " + model)}
	consult := func() tea.Msg {
		a, err := c.ConsultAuto(ctx, chain)
		return adviceMsg{seq: seq, advice: a, err: err}
	}
	return tea.Batch(consult, adviceTick(seq)), true
}

// adviceBlock is the advice as a block: a header with who, how long and
// what it cost, then its text behind a bar. It is the advisor's text, so
// it is cleaned like any model text, and it is a Claim.
func (h *home) adviceBlock(a *run.Advice) string {
	width := h.width
	if width <= 0 {
		width = 80
	}
	who := strings.TrimSpace(a.Agent + " " + a.Model)
	head := "Advisor · " + clean(who) + " · fresh · " + a.Duration.Round(time.Second).String()
	if a.CostUSD != nil {
		head += fmt.Sprintf(" · $%.2f", *a.CostUSD)
	}
	const claim = "a Claim, not checked"
	pad := max(width-2-ansi.StringWidth(head)-len(claim), 2)
	lines := []string{"  " + h.st.accent(head) + strings.Repeat(" ", pad) + h.st.dim(claim)}
	bar := "  " + h.st.border("│") + " "
	for _, l := range strings.Split(cleanLines(a.Text), "\n") {
		for _, w := range strings.Split(ansi.Wordwrap(l, max(width-4, 10), ""), "\n") {
			lines = append(lines, bar+w)
		}
	}
	return strings.Join(lines, "\n")
}

// adviceKey takes a key at the idle composer while a Consultation runs or
// its advice waits: ok is true when the key was its own.
func (h *home) adviceKey(k tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	a := h.adv
	if a == nil {
		return nil, false
	}
	key := k.String()
	if key == "esc" && h.vim && !h.normal && !h.comp.empty() {
		return nil, false // vim's INSERT Esc, with text in the box
	}
	switch {
	case key == "esc" && a.running:
		a.cancel()
		h.adv = nil
		return h.println(h.ogeLine(h.st.dim("advising cancelled"))), true
	case key == "esc" && a.shown:
		h.dismissAdvice(a)
		h.adv = nil
		return h.println(h.ogeLine(h.st.dim("advice dismissed: nothing was sent to the agent"))), true
	case !a.shown || !h.comp.empty():
		return nil, false
	case h.kb.resolve(ctxComposer, key) == actSubmit:
		return h.sendAdvice(), true
	case key == "e":
		h.comp.set(a.advice.Text)
		h.adviceEdit = &adviceEditState{id: a.advice.ID, text: a.advice.Text, chat: a.chat}
		h.adv = nil
		return nil, true
	}
	return nil, false
}

// dismissAdvice logs that the user let a Consultation's advice go unsent
// (Esc, their own turn, /new), so a cut counts it once, as Dismissed. A
// Consultation still running has no advice to log.
func (h *home) dismissAdvice(a *adviseState) {
	if a == nil || a.advice == nil || h.chat == nil {
		return
	}
	if c := h.chat.get(); c != nil && c == a.chat {
		_ = c.DismissAdvice(a.advice.ID)
	}
}

// dropAdviceEdit abandons the advice put in the box with e: it was never
// sent, so it is logged dismissed. A submit that sends it logs AdviceSent
// instead and clears the edit itself.
func (h *home) dropAdviceEdit() {
	e := h.adviceEdit
	h.adviceEdit = nil
	if e == nil || e.chat == nil {
		return
	}
	if h.chat != nil && h.chat.get() == e.chat {
		_ = e.chat.DismissAdvice(e.id)
	}
}

// sendAdvice sends the advice shown to the agent, verbatim, as Öge's turn
// (Chat.SendAdvice): dim, outside the Task and the feedback budget. The
// turn is cut like any turn that changed something.
func (h *home) sendAdvice() tea.Cmd {
	a := h.adv
	if h.chat == nil || h.chat.get() != a.chat {
		h.adv = nil // a Consultation of a Chat that has gone
		h.msg = []string{"oge: that advice was for a Chat that has ended; nothing was sent"}
		return nil
	}
	h.adv = nil
	return h.sendAdviceTurn(a.chat, a.advice, a.auto)
}

// sendAdviceTurn sends adv as the advice turn of its Consultation. An
// automatic one is refused if the user has turned since (a new chain).
func (h *home) sendAdviceTurn(c *run.Chat, adv *run.Advice, auto bool) tea.Cmd {
	res, problem := h.resolve(h.sel)
	switch {
	case problem != nil:
		h.msg = problem
		return nil
	}
	if !auto {
		// An automatic send keeps what the user is typing meanwhile.
		h.comp = h.composer()
	}
	first, _, _ := strings.Cut(strings.TrimSpace(adv.Text), "\n")
	h.turn = &chatTurn{text: run.AdvicePrefix + first, frozen: res.frozen, oge: true, advice: true, adviceID: adv.ID, adviceChat: c}
	ctx := h.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		if auto {
			ch, err := c.SendAutoAdvice(ctx, adv)
			return chatSentMsg{events: ch, err: err}
		}
		ch, err := c.SendAdvice(ctx, adv.ID, adv.Text)
		return chatSentMsg{events: ch, err: err}
	}
}

// adviceEditState is the advice the composer holds after e: a send of
// exactly this text is the advice turn, verbatim; any other is the
// user's, edited. It is dropped whenever the box is emptied or replaced
// by anything but typing.
type adviceEditState struct {
	id, text string
	chat     *run.Chat
}

// ogeAdviceLine is the dim line an advice turn shows when it is sent,
// cut to one row.
func (h *home) ogeAdviceLine(text string) string {
	line := "Öge → agent: " + clean(text)
	if h.width > 0 {
		line = ansi.Truncate(line, h.width-1, "…")
	}
	return h.st.dim(line)
}
