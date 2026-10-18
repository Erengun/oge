package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
)

// The advisor key on an AskUserQuestion (docs/ux/advisor.md, slice 5): `a`
// consults the Advisor on the question shown and highlights the option it
// suggests. The advice is a Claim and only highlights: the human still
// answers, and the answer records advised_by next to answered_by=human.
// Esc cancels the consult while it runs (a second Esc is the ask's, as
// ever). Moving to another ask, answering or withdrawing it discards the
// advice, and a consult that fails or says nothing leaves the ask as it was.

// askAdvice is the Consultation on the question of one ask.
type askAdvice struct {
	ask     *agent.Ask
	q       int // the question's index within the ask
	seq     int // the consult its tick chain belongs to
	cancel  context.CancelFunc
	started time.Time
	running bool
	advice  *run.Advice // landed, non-empty
	suggest int         // the 1-based option it suggests, 0 for none
}

// askAdviceTickMsg redraws the elapsed seconds of the consult on q of ask.
// seq numbers the consult, so a tick of an earlier one (Esc, then the key
// again on the same question) does not start a second chain.
type askAdviceTickMsg struct {
	ask *agent.Ask
	q   int
	seq int
}

func askAdviceTick(a *agent.Ask, q, seq int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return askAdviceTickMsg{a, q, seq} })
}

// askAdviceMsg is the end of the Consultation on question q of ask.
type askAdviceMsg struct {
	ask    *agent.Ask
	q      int
	seq    int // the consult it ends: an earlier one lands after a newer began
	advice *run.Advice
	err    error
}

// askAdviceQuestion is the question the Advisor is asked: the agent's own,
// verbatim, with its options, and how to say which one it suggests.
func askAdviceQuestion(q agent.AskQuestion) string {
	var b strings.Builder
	b.WriteString("The coding agent asks the user this question:\n\n")
	b.WriteString(q.Text)
	b.WriteString("\n\nOptions:\n")
	for i, o := range q.Options {
		fmt.Fprintf(&b, "%d. %s\n", i+1, o)
	}
	b.WriteString("\nBegin your reply with the number of the option you would suggest, if one fits, then give your reasons briefly.")
	return b.String()
}

var leadingNumber = regexp.MustCompile(`(?i)^\W*(?:option\s*)?#?(\d+)\b`)

// suggestedOption is the option an advice's first line names by number, or
// by exactly its label; 0 when it names none.
func suggestedOption(text string, options []string) int {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if m := leadingNumber.FindStringSubmatch(first); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= len(options) {
			return n
		}
		return 0
	}
	f := strings.ToLower(strings.Trim(first, " \t*_`\".:"))
	for i, o := range options {
		if f != "" && f == strings.ToLower(strings.TrimSpace(o)) {
			return i + 1
		}
	}
	return 0
}

// askAdviceKey is `a` on a question: ok is true when the key was its own.
// It is "a" and enter (the caller checks the box), with no Consultation
// of this question yet, so typing "always" is still an answer.
func (h *home) askAdviceKey(a *agent.Ask) (cmd tea.Cmd, ok bool) {
	t := h.turn
	q := len(t.answers)
	if a == nil || a.Questions == nil || q >= len(a.Questions) ||
		(t.askAdv != nil && t.askAdv.q == q) {
		return nil, false
	}
	var c *run.Chat
	if h.chat != nil {
		c = h.chat.get()
	}
	switch {
	case c == nil:
		return nil, false
	case h.adv != nil:
		h.msg = []string{"an advisor is already consulted"}
		return nil, true
	}
	if _, _, err := c.Advisor(); err != nil {
		h.msg = []string{"advisor: " + clean(err.Error())}
		return nil, true
	}
	h.msg = nil
	parent := h.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	h.advSeq++
	seq := h.advSeq
	t.askAdv = &askAdvice{ask: a, q: q, seq: seq, cancel: cancel, started: h.clock(), running: true}
	question := askAdviceQuestion(a.Questions[q])
	consult := func() tea.Msg {
		adv, err := c.ConsultAsk(ctx, question)
		return askAdviceMsg{ask: a, q: q, seq: seq, advice: adv, err: err}
	}
	return tea.Batch(consult, askAdviceTick(a, q, seq)), true
}

// askAdviceLanded takes a Consultation's end. It is discarded when its ask
// is no longer the one shown, or its question no longer the current one.
func (h *home) askAdviceLanded(m askAdviceMsg) tea.Cmd {
	t := h.turn
	if t == nil || t.askAdv == nil || !t.askAdv.running || t.askAdv.ask != m.ask || t.askAdv.q != m.q || t.askAdv.seq != m.seq ||
		h.ask() != m.ask || len(t.answers) != m.q {
		return nil
	}
	aa := t.askAdv
	aa.running = false
	switch {
	case errors.Is(m.err, context.Canceled):
		t.askAdv = nil
	case m.err != nil:
		t.askAdv = nil
		return h.println(h.ogeLine(h.st.dim("advisor: " + clean(m.err.Error()))))
	case m.advice == nil || strings.TrimSpace(m.advice.Text) == "":
		t.askAdv = nil
		return h.println(h.ogeLine(h.st.dim("advisor: no advice")))
	default:
		aa.advice = m.advice
		aa.suggest = suggestedOption(m.advice.Text, m.ask.Questions[m.q].Options)
		t.advisedBy = m.advice.ID
	}
	return nil
}

// dropAskAdvice discards the ask's Consultation: running, it is cancelled;
// landed, its advice goes with the ask it was for.
func (h *home) dropAskAdvice() {
	if t := h.turn; t != nil && t.askAdv != nil {
		if t.askAdv.cancel != nil {
			t.askAdv.cancel()
		}
		t.askAdv = nil
	}
}

// askAdviceEsc is Esc while the ask's Consultation runs: it cancels the
// consult and leaves the ask.
func (h *home) askAdviceEsc() (tea.Cmd, bool) {
	t := h.turn
	if t == nil || t.askAdv == nil || !t.askAdv.running {
		return nil, false
	}
	h.dropAskAdvice()
	return h.println(h.ogeLine(h.st.dim("advising cancelled"))), true
}

// askAdviceLines is what shows under the options of the question q: the
// status while it runs, then the advice as a block.
func (h *home) askAdviceLines(q int) []string {
	aa := h.turn.askAdv
	if aa == nil || aa.q != q {
		return nil
	}
	if aa.running {
		secs := max(int(h.clock().Sub(aa.started)/time.Second), 0)
		return []string{h.st.dim(fmt.Sprintf(" advising · fresh, read-only · %ds   esc cancel", secs))}
	}
	lines := strings.Split(h.adviceBlock(aa.advice), "\n")
	const most = 12
	if len(lines) > most {
		lines = append(lines[:most:most], "  "+h.st.dim("… (the advice is in the Chat log)"))
	}
	return lines
}

// advisedBy is the Consultation id to record on the answer to a, "" when
// none advised it.
func (h *home) advisedBy(a *agent.Ask) string {
	if a == nil || a.Questions == nil {
		return ""
	}
	return h.turn.advisedBy
}
