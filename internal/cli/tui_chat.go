package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/receipt"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

// The bare oge home talks to one Chat for the session (docs/ux/chat-first.md,
// slice 7): Enter sends the message as a turn, whose reply and dim tool
// one-liners are printed as they come. A turn that changes nothing ends
// there: no stages, no Receipt, no action row. A turn with Promoted or
// Ambiguous changes is cut into a Run at once (D4), verified in the
// background while the composer stays live (D3), then summed up on one
// Verdict line, with its diff when that is small; a newer cut supersedes
// it. /apply, /diff and /receipt act on the newest cut whose Verdict is
// in (slice 9): with newer edits unchecked, /apply and /branch ask
// first, and a cut's Gates wait for /apply or /branch, never a turn.
//
// The agent's words are always indented; Öge's own lines start at the
// margin with the oge › marker, so a reply can't pass for a Verdict.
//
// TODO(#259-decision): HeldOutViewed (D6) has no caller in the home yet:
// no home view shows held-out tests. The view that does records it.

// homeChat is the session's Chat, opened by the first message.
type homeChat struct {
	// open opens the Chat on the config the first message resolved.
	open func(res *resolved) (*run.Chat, error)
	// cut cuts the Chat's turns since the last cut into a Run, in the
	// Pipeline f resolved for the turn (its Shift+Tab mode).
	cut func(ctx context.Context, c *run.Chat, f *pipeline.Frozen, gates gate.Port) (*run.Result, error)
	// verdict is the cut's Verdict line, and what can take its Run.
	verdict func(res *run.Result, changed int) (string, *lastRun)
	agents  map[string]agent.Adapter

	mu sync.Mutex
	c  *run.Chat
	// closing counts the Chats /new is closing: quitting waits for them.
	closing sync.WaitGroup
}

// fits reports whether a message on res goes to the Chat: one is open, or
// res's implementer has an auto mode to open one in. An agent without one
// (the fake, Codex later) runs each message as a one-shot Run under its
// Launch profile.
func (hc *homeChat) fits(res *resolved) bool {
	if hc == nil {
		return false
	}
	if hc.get() != nil {
		return true
	}
	for _, s := range res.frozen.Stages {
		if s.Role == "implementer" && agent.HasAutoMode(hc.agents[s.Agent]) {
			return true
		}
	}
	return false
}

func (hc *homeChat) get() *run.Chat {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	return hc.c
}

// close closes the Chat, if one was opened: its Session ends and its
// Workspace goes.
func (hc *homeChat) close() {
	if hc == nil {
		return
	}
	if c := hc.get(); c != nil {
		_ = c.Close()
	}
	hc.closing.Wait()
}

// reset closes the Chat in the background, so the next message opens a
// new one (/new): closing waits for a cut in flight to unwind. The
// command's message is chatClosedMsg; nil when no Chat is open.
func (hc *homeChat) reset() tea.Cmd {
	if hc == nil {
		return nil
	}
	hc.mu.Lock()
	c := hc.c
	hc.c = nil
	hc.mu.Unlock()
	if c == nil {
		return nil
	}
	hc.closing.Add(1)
	return func() tea.Msg {
		defer hc.closing.Done()
		_ = c.Close()
		return chatClosedMsg{}
	}
}

// chatTurn is the Chat turn in flight.
type chatTurn struct {
	text    string // the message, back in the composer if the Chat can't open
	frozen  *pipeline.Frozen
	events  <-chan agent.Event
	asks    []*agent.Ask // the prompts waiting, one shown at a time
	answers []string     // the shown question ask's answers so far
	changed int          // Promoted and Ambiguous paths the turn changed
	// stopped: Esc or Ctrl+C interrupted the turn; a second Ctrl+C then
	// quits.
	stopped bool
	queued  []string // messages typed and sent during the turn
}

// chatVerify is a cut verifying in the background.
type chatVerify struct {
	seq     int
	changed int
	cancel  context.CancelFunc
	stopped bool // Ctrl+C cancelled it
	// gate is the Gate its Run waits at, if any: shown only at delivery.
	gate *chatGateMsg
}

type (
	// chatSentMsg is a message's turn started, or why it couldn't.
	chatSentMsg struct {
		events <-chan agent.Event
		err    error
	}
	// chatEvMsg is the turn's next event; ok is false once they end.
	chatEvMsg struct {
		ev agent.Event
		ok bool
	}
	// chatClosedMsg is a Chat /new closed, closed.
	chatClosedMsg struct{}
	// chatCutMsg is the end of the cut seq.
	chatCutMsg struct {
		seq int
		res *run.Result
		err error
	}
	// chatGateMsg is a Gate the cut seq's Run reached: the decision goes
	// to reply, and next waits on the cut again.
	chatGateMsg struct {
		seq   int
		req   gate.Request
		reply chan<- gate.Decision
		next  tea.Cmd
	}
)

// cutGates is a cut's Gate port: a Gate waits, never shown mid-turn,
// until /apply or /branch opens it (Gates at delivery). A newer cut, or
// the Chat closing, cancels the wait.
type cutGates struct {
	reqs chan<- chatGateMsg
}

func (p cutGates) Decide(ctx context.Context, r gate.Request) (gate.Decision, error) {
	reply := make(chan gate.Decision, 1)
	select {
	case p.reqs <- chatGateMsg{req: r, reply: reply}:
	case <-ctx.Done():
		return gate.Decision{}, ctx.Err()
	}
	select {
	case d := <-reply:
		return d, nil
	case <-ctx.Done():
		return gate.Decision{}, ctx.Err()
	}
}

// ogeLine is one of Öge's own lines: at the margin, after the marker.
func (h *home) ogeLine(text string) string {
	return h.st.accent("oge ›") + " " + text
}

// agentLines indents the agent's words, every line.
func agentLines(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// chatSubmit sends text as the next turn, opening the Chat first.
func (h *home) chatSubmit(text string, res *resolved, queued []string) (tea.Model, tea.Cmd) {
	h.turn = &chatTurn{text: text, frozen: res.frozen, queued: queued}
	h.comp = h.composer()
	hc, ctx := h.chat, h.ctx
	send := func() tea.Msg {
		hc.mu.Lock()
		if hc.c == nil {
			c, err := hc.open(res)
			if err != nil {
				hc.mu.Unlock()
				return chatSentMsg{err: err}
			}
			hc.c = c
		}
		c := hc.c
		hc.mu.Unlock()
		ch, err := c.Send(ctx, text)
		return chatSentMsg{events: ch, err: err}
	}
	echo := h.println(h.st.bold("> " + task.Parse(text).Title))
	return h, tea.Sequence(echo, send)
}

// nextEvent waits for the turn's next event.
func nextEvent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return chatEvMsg{ev, ok}
	}
}

// chatUpdate is update while a turn or its cut is in flight.
func (h *home) chatUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case chatSentMsg:
		if msg.err != nil {
			text := h.turn.text
			h.endTurn()
			h.comp.set(text) // kept, to send again
			h.msg = append(h.msg, "oge: "+clean(msg.err.Error()))
			return h, nil
		}
		h.turn.events = msg.events
		return h, nextEvent(msg.events)
	case chatEvMsg:
		if !msg.ok {
			return h.turnEnded()
		}
		return h, tea.Sequence(h.chatEvent(msg.ev), nextEvent(h.turn.events))
	case tea.KeyPressMsg:
		return h.chatKey(msg)
	case tea.PasteMsg:
		if a := h.ask(); a == nil || a.Questions != nil {
			h.comp.insertPaste(msg.Content)
		}
	case ctxDoneMsg:
		h.quit = true
		return h, tea.Quit
	}
	return h, nil
}

// ask is the prompt shown, nil when none waits.
func (h *home) ask() *agent.Ask {
	if len(h.turn.asks) == 0 {
		return nil
	}
	return h.turn.asks[0]
}

// askRows is a's Detail wrapped to the frame's width by display width,
// every row of it. Detail is shown as it is: it is already visible
// (agent.Visible) and is never cleaned, which would redact it.
func (h *home) askRows(a *agent.Ask) []string {
	width := h.width
	if width <= 0 {
		width = 80
	}
	width = max(width-4, 4)
	var rows []string
	for _, l := range strings.Split(a.Detail, "\n") {
		rows = append(rows, strings.Split(ansi.Hardwrap(l, width, true), "\n")...)
	}
	return rows
}

// askFits reports whether a's whole Detail shows in the frame now: at
// most agent.AskMaxLines rows, with the prompt's two other lines.
func (h *home) askFits(a *agent.Ask) bool {
	n := len(h.askRows(a))
	return n <= agent.AskMaxLines && (h.height <= 0 || n+3 <= h.height)
}

// showAsk readies the prompt now shown: a question takes the composer, so
// what was typed there before it appeared is queued, never sent as its
// answer.
func (h *home) showAsk() {
	h.turn.answers = nil
	if a := h.ask(); a != nil && a.Questions != nil {
		if text := strings.TrimSpace(h.comp.text()); text != "" {
			h.turn.queued = append(h.turn.queued, text)
			h.comp = h.composer()
		}
	}
}

// chatEvent prints what a turn's event shows: the reply's words, a tool's
// dim one-liner, a denial; an Ask waits in the live area for its answer.
func (h *home) chatEvent(ev agent.Event) tea.Cmd {
	switch ev.Kind {
	case agent.Claim:
		if ev.Tool == "" {
			if t := cleanLines(ev.Text); t != "" {
				return h.println(agentLines(t))
			}
			return nil
		}
		if ev.Tool == "TodoWrite" {
			return nil
		}
		if s, ok := activityStep(ev); ok {
			return h.println(h.st.dim("  " + s))
		}
	case agent.HostRequest:
		if hd := ev.Host; hd != nil && hd.Decision == "cancel" && hd.By == "agent" {
			// The agent withdrew that ask: only it goes.
			for i, a := range h.turn.asks {
				if a == ev.Ask {
					h.turn.asks = append(h.turn.asks[:i:i], h.turn.asks[i+1:]...)
					if i == 0 {
						h.showAsk()
					}
					break
				}
			}
			return nil
		}
		if a := ev.Ask; a != nil {
			if a.Questions == nil && !h.askFits(a) {
				// Consent to what the prompt can't show isn't consent.
				_ = a.Answer(agent.Answer{Reason: fmt.Sprintf("Öge denied this: it is too long to show the user for approval (more than %d lines). Run it as shorter commands.", agent.AskMaxLines)})
				return h.println(h.ogeLine(h.st.dim("denied " + clean(a.Tool) + ": too long to show for approval")))
			}
			h.turn.asks = append(h.turn.asks, a)
			if len(h.turn.asks) == 1 {
				h.showAsk()
			}
			return nil
		}
		if ev.Host != nil && ev.Host.By == "human" {
			return nil // the answer is printed where it was given
		}
		if s, ok := activityStep(ev); ok {
			return h.println(h.st.dim("  " + s))
		}
	case agent.TurnSettled:
		h.turn.asks = nil
		var tamper tea.Cmd
		if sc := ev.Scope; sc != nil {
			h.turn.changed = len(sc.Promoted) + len(sc.Ambiguous)
			if len(sc.Tamper) > 0 {
				// A protected path the turn wrote was put back (ADR-0009).
				tamper = h.println(h.ogeLine(h.st.dim("reverted " + clean(strings.Join(sc.Tamper, ", ")) + ": protected")))
			}
		}
		switch {
		case h.turn.stopped:
			return tea.Sequence(tamper, h.println(h.ogeLine(h.st.dim("■ interrupted"))))
		case ev.Stop != "":
			return tea.Sequence(tamper, h.println(h.ogeLine("■ "+clean(ev.Stop))))
		case ev.Failure != "":
			return tea.Sequence(tamper, h.println(h.ogeLine("✗ "+clean(ev.Failure))))
		}
		return tamper
	}
	return nil
}

// turnEnded opens the composer, and cuts a turn that changed something in
// the background: the cut supersedes one still verifying, and covers its
// turns.
func (h *home) turnEnded() (tea.Model, tea.Cmd) {
	if h.turn.changed == 0 || h.turn.stopped {
		// An interrupted turn's changes wait for the next turn's cut.
		h.unchecked = h.unchecked || h.turn.changed > 0
		return h.nextTurn(nil)
	}
	cut := h.startCut()
	m, next := h.nextTurn(nil)
	return m, tea.Batch(cut, next)
}

// startCut starts the turn's cut, in the Pipeline it resolved for the
// turn, and makes it the one verifying.
func (h *home) startCut() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	if v := h.verify; v != nil {
		// Its turns are this cut's: so are its changes, at least.
		h.turn.changed = max(h.turn.changed, v.changed)
	}
	h.cuts++
	h.unchecked = false
	h.verify = &chatVerify{seq: h.cuts, changed: h.turn.changed, cancel: cancel}
	hc, f, seq := h.chat, h.turn.frozen, h.cuts
	c := hc.get()
	reqs, done := make(chan chatGateMsg), make(chan chatCutMsg, 1)
	go func() {
		res, err := hc.cut(ctx, c, f, cutGates{reqs})
		done <- chatCutMsg{seq, res, err}
	}()
	var wait tea.Cmd
	wait = func() tea.Msg {
		select {
		case m := <-done:
			return m
		case g := <-reqs:
			g.seq, g.next = seq, wait
			return g
		}
	}
	return wait
}

// cutGated takes a Gate the cut's Run reached. At a delivery it opens
// there and then; otherwise the cut is summed up as waiting for you, and
// its Gate waits for /apply or /branch.
func (h *home) cutGated(g chatGateMsg) tea.Cmd {
	v := h.verify
	if v == nil || v.seq != g.seq {
		return g.next // superseded: its wait is cancelled
	}
	v.gate = &g
	if h.cutGate != nil {
		// The delivery's Gate decided: the next one opens in its place.
		h.cutGate = &gateState{req: g.req, reply: g.reply}
		return g.next
	}
	what := ""
	if len(g.req.Files) > 0 {
		what = " · " + plural(len(g.req.Files), "file")
	}
	h.actions = false
	return tea.Batch(h.println(h.ogeLine("… Waiting for you: the "+clean(gateTitle(g.req.Name))+what+" · /apply")), g.next)
}

// openCutGate opens the newest cut's Gate for the delivery key (and a
// branch's name): the delivery is made once its Run is Accepted.
func (h *home) openCutGate(key, arg string) tea.Cmd {
	g := h.verify.gate
	h.cutGate = &gateState{req: g.req, reply: g.reply}
	h.gateFor = &staleAsk{key: key, arg: arg}
	return nil
}

// cutGateKey takes a key at the cut's Gate, as the live view's Gate does.
// Esc or Ctrl+C at the choices closes it: the cut goes on waiting, for
// the next /apply.
func (h *home) cutGateKey(k tea.KeyPressMsg) tea.Cmd {
	g := h.cutGate
	if g.recording {
		return nil
	}
	if g.inspect != nil {
		switch k.String() {
		case "esc", "enter", "q":
			g.inspect = nil
		}
		return nil
	}
	switch k.String() {
	case "ctrl+c", "esc":
		if g.choice != nil {
			g.choice, g.input, g.msg = nil, "", ""
			return nil
		}
		h.cutGate, h.gateFor = nil, nil
		return h.println(h.ogeLine(h.st.dim("not delivered; the Gate waits for /apply")))
	case "backspace":
		if r := []rune(g.input); len(r) > 0 {
			g.input = string(r[:len(r)-1])
		}
	case "enter":
		g.enter(h.height)
	default:
		if k.Text != "" {
			g.input += clean(k.Text)
		}
	}
	return nil
}

// endTurn opens the composer again, keeping what was typed during the
// turn and not yet sent; it returns the messages queued meanwhile.
func (h *home) endTurn() []string {
	text, queued := h.comp.text(), h.turn.queued
	h.turn = nil
	h.reopen()
	if strings.TrimSpace(text) != "" {
		h.comp.set(text)
	}
	return queued
}

// nextTurn ends the turn, printing print first, and sends the next message
// typed during it, if any.
func (h *home) nextTurn(print tea.Cmd) (tea.Model, tea.Cmd) {
	stopped := h.turn.stopped
	queued := h.endTurn()
	if len(queued) == 0 {
		return h, print
	}
	if stopped {
		// After Esc nothing is sent on its own: what was queued is back in
		// the composer, for the human to send or change.
		h.comp.set(strings.TrimSpace(strings.Join(append(queued, h.comp.text()), "\n")))
		return h, print
	}
	draft := h.comp.text()
	_, send := h.submitQueued(queued)
	if h.turn != nil {
		h.comp.set(draft)
	}
	return h, tea.Sequence(print, send)
}

// submitQueued sends the first queued message as a turn; the rest stay
// queued behind it.
func (h *home) submitQueued(queued []string) (tea.Model, tea.Cmd) {
	res, problem := h.resolve(h.sel)
	if problem != nil {
		h.msg = problem
		h.comp.set(strings.Join(queued, "\n"))
		return h, nil
	}
	h.hist.add(queued[0])
	return h.chatSubmit(queued[0], res, queued[1:])
}

// cutEnded prints the newest cut's Verdict line; h.last moves to it only
// when it has one. A superseded cut, or one the Chat no longer has, ends
// unshown. Whether it was cancelled is the Run's to say: one that
// reached its Verdict before Ctrl+C took effect shows it.
func (h *home) cutEnded(msg chatCutMsg) tea.Cmd {
	v := h.verify
	if v == nil || v.seq != msg.seq {
		return nil
	}
	h.verify = nil
	v.cancel()
	res := msg.res
	h.cutGate = nil
	deliver := h.gateFor
	h.gateFor = nil
	verdict := msg.err == nil && res != nil && res.Check != nil &&
		(res.Outcome == run.Accepted || res.Outcome == run.Failed || res.Outcome == run.Parked)
	var line string
	switch {
	case errors.Is(msg.err, run.ErrNothingToCut), errors.Is(msg.err, run.ErrSuperseded):
		return nil
	case v.stopped && !verdict:
		h.last = nil // as on an error: the older cut isn't what was just changed
		line = h.ogeLine("■ verification cancelled: nothing was verified")
	case msg.err != nil:
		h.last = nil // never an older cut's: it isn't what was just changed
		line = h.ogeLine("✗ verifying the change: " + clean(msg.err.Error()))
	default:
		var l string
		l, h.last = h.chat.verdict(res, v.changed)
		line = h.ogeLine(l)
		if res.Outcome == run.Accepted || res.Outcome == run.Failed {
			line += h.inlineDiff()
		}
	}
	// The action row acts on h.last: never on a cut the human hasn't
	// seen yet.
	h.actions = false
	if deliver != nil && h.last != nil && res != nil && res.Outcome == run.Accepted {
		// Through its Gate: the delivery /apply or /branch asked for.
		return tea.Sequence(h.println(line), h.actNow(deliver.key, deliver.arg))
	}
	return h.println(line)
}

// staleAsk is /apply or /branch while newer edits than the verified cut
// are unchecked: a cut still verifying, or an interrupted turn's changes.
type staleAsk struct {
	key, arg string // the action's key, and a branch's name
}

func (s *staleAsk) verb() string {
	if s.key == "b" {
		return "branch"
	}
	return "apply"
}

// staleKey takes the stale-edit choice: a delivers the verified cut; w
// (or Esc) waits, doing nothing, while the newest cut finishes.
func (h *home) staleKey(k tea.KeyPressMsg) tea.Cmd {
	s := h.stale
	switch k.String() {
	case "a":
		h.stale = nil
		if h.last == nil {
			return nil // the verified cut is gone meanwhile (/new)
		}
		return h.actNow(s.key, s.arg)
	case "w", "esc", "ctrl+c":
		h.stale = nil
		return h.println(h.ogeLine(h.st.dim("waiting: nothing delivered; the newest cut goes on")))
	}
	return nil
}

// inlineMax is the most diff lines the Chat shows under a Verdict.
const inlineMax = 20

// inlineDiff is h.last's diff under its Verdict line, dim and indented,
// when it is inlineMax lines or fewer; "" otherwise. It is the agent's
// content: cleaned, every line.
func (h *home) inlineDiff() string {
	if h.last == nil || h.last.patch == nil {
		return ""
	}
	patch, err := h.last.patch()
	text := strings.TrimRight(string(patch), "\n")
	if err != nil || text == "" || strings.Count(text, "\n")+1 > inlineMax {
		return ""
	}
	var b strings.Builder
	for _, l := range strings.Split(cleanLines(text), "\n") {
		b.WriteString("\n" + h.st.dim("    "+l))
	}
	return b.String()
}

// chatKey takes a key during a turn. Esc interrupts the turn; so does
// Ctrl+C, and a second Ctrl+C quits. y or n answers a
// permission prompt; a question takes the composer's text or an option's
// number. Otherwise the composer stays live, and Enter queues a message
// for when the turn has ended.
func (h *home) chatKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := h.turn
	switch k.String() {
	case "ctrl+c":
		if t.stopped {
			h.quit = true
			return h, tea.Quit
		}
		h.stop()
		return h, nil
	case "esc":
		h.stop()
		return h, nil
	}
	a := h.ask()
	if a != nil && a.Questions == nil {
		switch k.String() {
		case "y":
			if !h.askFits(a) {
				return h, nil // what isn't shown can't be approved
			}
			return h, h.answer(a, agent.Answer{Allow: true})
		case "n":
			return h, h.answer(a, agent.Answer{})
		}
		return h, nil
	}
	if k.String() == "enter" {
		text := strings.TrimSpace(h.comp.text())
		if text == "" {
			return h, nil
		}
		h.comp = h.composer()
		if a != nil {
			// One question at a time, each with its own numbers.
			q := a.Questions[len(h.turn.answers)]
			if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(q.Options) {
				text = q.Options[n-1]
			}
			h.turn.answers = append(h.turn.answers, text)
			if len(h.turn.answers) < len(a.Questions) {
				return h, h.println(h.ogeLine(h.st.dim("answered: " + clean(text))))
			}
			return h, h.answer(a, agent.Answer{Allow: true, Answers: h.turn.answers})
		}
		t.queued = append(t.queued, text)
		return h, nil
	}
	h.comp.key(k)
	return h, nil
}

// stop interrupts the turn.
func (h *home) stop() {
	t := h.turn
	if t.stopped {
		return
	}
	t.stopped, t.asks = true, nil
	if c := h.chat.get(); c != nil {
		_ = c.Interrupt()
	}
}

// answer answers the prompt shown and prints what was answered.
func (h *home) answer(a *agent.Ask, ans agent.Answer) tea.Cmd {
	h.turn.asks = h.turn.asks[1:]
	h.showAsk()
	// What is printed is redacted and short (Target), never the Detail.
	what := clean(strings.TrimSpace(a.Tool + " " + a.Target))
	err := a.Answer(ans)
	switch {
	case errors.Is(err, claude.ErrAskClosed):
		return h.println(h.ogeLine(h.st.dim("that question was withdrawn")))
	case err != nil:
		return h.println(h.ogeLine("✗ answering: " + clean(err.Error())))
	case len(ans.Answers) > 0:
		return h.println(h.ogeLine(h.st.dim("answered: " + clean(ans.Answers[len(ans.Answers)-1]))))
	case ans.Allow:
		return h.println(h.ogeLine(h.st.dim("allowed: " + what)))
	}
	return h.println(h.ogeLine(h.st.dim("denied: " + what)))
}

// turnView is the live area during a turn: its prompt or what it is
// doing, then the composer's text.
func (h *home) turnView() string {
	t := h.turn
	var lines []string
	a := h.ask()
	switch {
	case a != nil && a.Questions != nil:
		// The question is the agent's text: indented, never after the
		// marker.
		q := a.Questions[len(t.answers)]
		lines = append(lines, agentLines(cleanLines(q.Text)))
		for i, o := range q.Options {
			lines = append(lines, fmt.Sprintf("    %d. %s", i+1, clean(o)))
		}
		lines = append(lines, h.ogeLine(h.st.dim("type an answer or an option's number · enter")))
	case a != nil:
		lines = append(lines, h.ogeLine("Allow "+clean(a.Tool)+"?"))
		if !h.askFits(a) {
			lines = append(lines, "    "+h.st.bold("too long to show at this width; [n] to deny or widen the terminal"))
			break
		}
		for _, r := range h.askRows(a) {
			lines = append(lines, "    "+r)
		}
		lines = append(lines, "    "+h.st.bold("[y] yes  [n] no"))
	case t.stopped:
		lines = append(lines, h.ogeLine(h.st.dim("interrupting… · ctrl+c again quits")))
	default:
		lines = append(lines, h.ogeLine(h.st.dim("working… · esc to interrupt")))
	}
	if a == nil || a.Questions != nil {
		box := " › " + strings.ReplaceAll(h.comp.text(), "\n", " ⏎ ")
		if n := len(t.queued); n > 0 {
			box += h.st.dim(fmt.Sprintf("   (%s queued)", plural(n, "message")))
		}
		lines = append(lines, box)
	}
	return strings.Join(lines, "\n")
}

// chatDelivered is what /apply or /branch of the cut res shows: once it
// is delivered the Chat marks it, so the next cut's Task starts after its
// turns (D7), and an apply is summed up in three lines (the outcome, the
// files, the checks) with the Receipt behind /receipt. A refusal shows
// as the command printed it; the Chat goes on either way.
func (h *home) chatDelivered(c *run.Chat, res *run.Result, changed int, how, out string, code int) string {
	if code != ExitOK || c == nil {
		return out
	}
	var note string
	if err := c.Delivered(res.ID, how); err != nil {
		note = "\n" + h.ogeLine("✗ marking the delivery: "+clean(err.Error()))
	}
	if how != "apply" {
		return strings.TrimSuffix(out, "\n") + note
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	files := []string{}
	if a := res.Attempt; a != nil {
		files = a.Changed
	}
	shown := files
	if len(shown) > 5 {
		shown = shown[:5]
	}
	what := plural(max(len(files), changed), "file")
	if len(shown) > 0 {
		what += ": " + clean(strings.Join(shown, ", "))
		if len(files) > len(shown) {
			what += fmt.Sprintf(", +%d more", len(files)-len(shown))
		}
	}
	sum := []string{
		h.ogeLine("✓ Applied " + clean(res.ID) + " to your working tree · nothing committed or staged · /receipt"),
		"  " + what,
		"  checks: " + checkSummary(res),
	}
	// What a hold-back left out is never dropped from the summary.
	sum = append(sum, lines[1:]...)
	return strings.Join(sum, "\n") + note
}

// checkSummary is a cut's Check in a few words: how many tests passed.
func checkSummary(res *run.Result) string {
	check := "Check passed"
	if c := res.Check; c != nil {
		passed := 0
		for _, t := range c.Tests {
			if t.Attested == "pass" {
				passed++
			}
		}
		if passed > 0 {
			check = fmt.Sprintf("%d passed", passed)
		}
	}
	return check
}

// verdictLine sums a cut's Run up in one line.
func verdictLine(res *run.Result, changed int) string {
	if n := res.Attempt; n != nil && len(n.Changed) > 0 {
		changed = len(n.Changed)
	}
	what := plural(changed, "file")
	switch res.Outcome {
	case run.Accepted:
		return "✓ Accepted · " + what + " · " + checkSummary(res) + " · /apply /diff"
	case run.Failed:
		return "✗ Failed · " + what + " · the Check failed · /diff /receipt"
	case run.Parked:
		at := ""
		if res.Gate != "" {
			at = ": the " + receipt.GateTitle(res.Gate)
		}
		return "… Waiting for you" + at + " · " + what + " · /receipt"
	case run.Refused:
		if len(res.Why) > 0 && res.Why[0] == run.WhySetupChanged {
			return "✗ Refused: setup changed since this Chat opened; /new to start a new Chat"
		}
		why := ""
		if len(res.Why) > 0 {
			why = ": " + clean(res.Why[0])
		}
		return "✗ Refused" + why
	}
	return "✗ " + clean(string(res.Outcome)) + " · " + what + " · /receipt"
}
