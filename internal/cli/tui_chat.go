package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/delivery"
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
// Ambiguous changes is cut into a Run after queued follow-ups settle (D4),
// verified in the background while the composer stays live (D3), then summed up on one
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

// homeChat is one of the session's Chats (one per chatView), opened by its
// first message.
type homeChat struct {
	// open opens the Chat on the config the first message resolved.
	open func(res *resolved) (*run.Chat, error)
	// cut cuts the Chat's turns since the last cut into a Run, in the
	// Pipeline f resolved for the turn (its Shift+Tab mode); observe gets
	// its progress events.
	cut func(ctx context.Context, c *run.Chat, f *pipeline.Frozen, gates gate.Port, observe func(run.Event)) (*run.Result, error)
	// verdict is the cut's Verdict line, and what can take its Run.
	verdict func(res *run.Result, changed int) (string, *lastRun)
	agents  map[string]agent.Adapter
	// shown logs that ask advice was displayed (Chat.AdviceShown); a test
	// seam, nil in the product.
	shown func(c *run.Chat, consultation string)

	// mu is held only to read or swap c, never across opening one: /new
	// and /close run while a first message opens its Chat. gen moves on
	// each reset or close, so a Chat opened meanwhile is closed, unused.
	mu  sync.Mutex
	c   *run.Chat
	gen int
	// closing counts the Chats being opened, and the ones /new and /close
	// are closing: quitting waits for them.
	closing sync.WaitGroup
}

// errChatGone is a message's Chat closed while it opened: the message is
// for a Chat the home no longer has.
var errChatGone = errors.New("the Chat was closed while it opened")

// current is the open Chat, nil when none, and its generation.
func (hc *homeChat) current() (*run.Chat, int) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	return hc.c, hc.gen
}

// opened keeps c as the Chat, unless hc was reset or closed since gen.
func (hc *homeChat) opened(c *run.Chat, gen int) bool {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.gen != gen {
		return false
	}
	hc.c = c
	return true
}

// take empties hc, returning the Chat that was open.
func (hc *homeChat) take() *run.Chat {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	c := hc.c
	hc.c = nil
	hc.gen++
	return c
}

// fits reports whether a message on res goes to the Chat: one is open, or
// res's implementer can run one (agent.AutoMode: claude, codex, agy). The
// fake runs each message as a one-shot Run under its Launch profile.
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

// clone is a homeChat like hc with no Chat open yet: another Chat of the
// session's (/new). nil clones to nil.
func (hc *homeChat) clone() *homeChat {
	if hc == nil {
		return nil
	}
	return &homeChat{open: hc.open, cut: hc.cut, verdict: hc.verdict, agents: hc.agents, shown: hc.shown}
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
	if c := hc.take(); c != nil {
		_ = c.Close()
	}
	hc.closing.Wait()
}

// stop ends the Chat for Öge going away (run.Chat.Stop): its Workspace
// is kept, and a turn or cut in flight is logged as interrupted, why.
func (hc *homeChat) stop(why string) {
	if hc == nil {
		return
	}
	hc.closing.Wait() // a Chat still opening is closed by its opener
	if c := hc.take(); c != nil {
		_ = c.Stop(why)
	}
}

// reset closes the Chat in the background, so the next message opens a
// new one (/new): closing waits for a cut in flight to unwind. The
// command's message is v's chatClosedMsg; nil when no Chat is open.
func (hc *homeChat) reset(v *chatView) tea.Cmd {
	if hc == nil {
		return nil
	}
	c := hc.take()
	if c == nil {
		return nil
	}
	hc.closing.Add(1)
	return func() tea.Msg {
		defer hc.closing.Done()
		_ = c.Close()
		return chatClosedMsg{view: v}
	}
}

// chatTurn is the Chat turn in flight.
type chatTurn struct {
	text    string // the message, back in the composer if the Chat can't open
	frozen  *pipeline.Frozen
	events  <-chan agent.Event
	asks    []*agent.Ask // the prompts waiting, one shown at a time
	answers []string     // the shown question ask's answers so far
	// askAdv is the advisor key's Consultation on the question shown, and
	// advisedBy the id of the advice that landed for this ask.
	askAdv    *askAdvice
	advisedBy string
	changed   int // Promoted and Ambiguous paths the turn changed
	// stopped: Esc or Ctrl+C interrupted the turn; a second Ctrl+C then
	// quits.
	stopped bool
	queued  []string // messages typed and sent during the turn
	// oge: the turn is Öge's, a feedback turn (D5) or, with advice, an
	// advice turn (docs/ux/advisor.md).
	oge    bool
	advice bool
	// edited: the advice the text was edited from, kept for a retry.
	edited *adviceEditState
	// feedback is a failing cut's feedback turn, held until this turn
	// ends: sent then, unless this turn cuts anew or was interrupted.
	feedback string
	fbFrozen *pipeline.Frozen
}

// chatVerify is a cut verifying in the background.
type chatVerify struct {
	seq     int
	changed int
	frozen  *pipeline.Frozen
	cancel  context.CancelFunc
	stopped bool // Ctrl+C cancelled it
	// since is when the cut started; step is the cut's current phase named
	// for the status line (empty: the bare verifying).
	since time.Time
	step  string
	// gate is the Gate its Run waits at, if any: shown only at delivery.
	gate *chatGateMsg
}

type (
	// chatSentMsg is a message's turn started, or why it couldn't.
	chatSentMsg struct {
		view   *chatView // the Chat it is for; nil is the focused one
		events <-chan agent.Event
		err    error
	}
	// chatEvMsg is the turn's next event; ok is false once they end.
	chatEvMsg struct {
		view *chatView
		ev   agent.Event
		ok   bool
	}
	// chatClosedMsg is a Chat /new closed, closed.
	chatClosedMsg struct{ view *chatView }
	// chatCutMsg is the end of the cut seq.
	chatCutMsg struct {
		view *chatView
		seq  int
		res  *run.Result
		err  error
	}
	// chatGateMsg is a Gate the cut seq's Run reached: the decision goes
	// to reply, and next waits on the cut again.
	chatGateMsg struct {
		view  *chatView
		seq   int
		req   gate.Request
		reply chan<- gate.Decision
		next  tea.Cmd
	}
	// chatStepMsg is the cut seq's next progress event; next waits on the
	// cut again.
	chatStepMsg struct {
		view *chatView
		seq  int
		ev   run.Event
		next tea.Cmd
	}
	// verifyTickMsg redraws the verify line's elapsed seconds for seq.
	verifyTickMsg struct {
		view *chatView
		seq  int
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
	// The glyph carries the status; its colour only backs it up.
	if h.full {
		// A Verdict line takes its trust colour, bold, and its actions go dim.
		paint := map[string]func(string) string{"✓": h.st.ok, "✗": h.st.bad, "■": h.st.warn}
		for glyph, p := range paint {
			if strings.HasPrefix(text, glyph) {
				head, rest, _ := strings.Cut(text, " · ")
				return "  " + h.st.dim("oge ›") + " " + p(h.st.bold(head)) + optional(h.st.dim(" · "), h.st.dim(rest), rest != "")
			}
		}
	}
	switch {
	case strings.HasPrefix(text, "✓"):
		text = h.st.ok("✓") + strings.TrimPrefix(text, "✓")
	case strings.HasPrefix(text, "✗"):
		text = h.st.bad("✗") + strings.TrimPrefix(text, "✗")
	case strings.HasPrefix(text, "■"):
		text = h.st.warn("■") + strings.TrimPrefix(text, "■")
	}
	if h.full {
		return "  " + h.st.dim("oge ›") + " " + text // a notice, quieter than the words, in the reply's gutter
	}
	return h.st.accent("oge ›") + " " + text
}

// userBar is the human's echoed message as a bar across the width: a
// subtle background where there is colour, bold where there is none.
func (h *home) userBar(text string) string {
	if h.st.th != nil && h.width > 0 {
		text += strings.Repeat(" ", max(h.width-1-ansi.StringWidth(text), 0))
	}
	return h.st.user(text)
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
	h.pendingFB = nil // a new turn of the human's: the old failure is theirs no more
	h.turn = &chatTurn{text: text, frozen: res.frozen, queued: queued}
	h.side.since, h.side.step, h.side.took = h.clock(), "", 0
	h.comp = h.composer()
	edited := h.adviceEdit
	h.adviceEdit = nil
	h.turn.edited = edited
	var dismissed tea.Cmd
	if h.adv != nil && h.adv.shown {
		// The user's own turn: the advice shown no longer takes ↵ or e.
		h.dismissAdvice(h.adv)
		h.adv = nil
		dismissed = h.println(h.ogeLine(h.st.dim("advice dismissed: nothing was sent to the agent")))
	}
	hc, ctx, v := h.chat, h.ctx, h.chatView
	c, gen := hc.current()
	if c == nil {
		hc.closing.Add(1) // quitting waits for it to open, and closes it
	}
	send := func() tea.Msg {
		if c == nil {
			defer hc.closing.Done()
			opened, err := hc.open(res)
			if err != nil {
				return chatSentMsg{view: v, err: err}
			}
			if !hc.opened(opened, gen) {
				_ = opened.Close() // /close or quitting got here first
				return chatSentMsg{view: v, err: errChatGone}
			}
			c = opened
		}
		if edited != nil && edited.chat == c {
			ch, err := c.SendEdited(ctx, edited.id, text) // the user's turn, from an advisor's text
			return chatSentMsg{view: v, events: ch, err: err}
		}
		ch, err := c.Send(ctx, text)
		return chatSentMsg{view: v, events: ch, err: err}
	}
	if h.full {
		h.tr.latest() // sending brings the view back to the bottom
		h.tr.freshReply = true
	}
	var echo tea.Cmd
	if h.full {
		echo = h.userEntry(task.Parse(text).Title)
	} else {
		echo = h.println(h.userBar("> " + task.Parse(text).Title))
	}
	return h, tea.Sequence(echo, dismissed, send)
}

// nextEvent waits for the turn's next event.
func nextEvent(v *chatView, ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return chatEvMsg{view: v, ev: ev, ok: ok}
	}
}

// chatUpdate is update while a turn or its cut is in flight.
func (h *home) chatUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case chatSentMsg:
		if msg.err != nil && h.turn.oge {
			// The composer holds the human's draft, never the feedback.
			line := h.ogeLine("✗ sending the feedback turn: " + clean(msg.err.Error()))
			if h.turn.advice {
				line = h.ogeLine("✗ sending the advice: " + clean(msg.err.Error()))
				// The Chat logs the advice dismissed itself.
			}
			if errors.Is(msg.err, run.ErrChainEnded) {
				line = h.ogeLine(h.st.dim("the advice wasn't sent: you've sent a turn since"))
			}
			if errors.Is(msg.err, run.ErrFeedbackBudget) {
				// Instead of stopping at the user's call, Öge consults once
				// (D-A3), unless [advisor] auto is off.
				var consult tea.Cmd
				ok := false
				if h.feedbackMode() == pipeline.ChatFeedbackOn {
					consult, ok = h.autoConsult()
				}
				if ok {
					line = h.ogeLine("✗ still failing after " + tries(h.feedbackBudget()) + "; asking the advisor")
				} else {
					line = h.ogeLine("✗ still failing after " + tries(h.feedbackBudget()) + "; your call")
				}
				m, cmd := h.nextTurn(h.println(line))
				return m, tea.Batch(cmd, consult)
			}
			return h.nextTurn(h.println(line))
		}
		if msg.err != nil {
			// The composer is this Chat's own; msg is the terminal's, so
			// problem says it in the transcript of a Chat in the
			// background.
			text, held, ed := h.turn.text, h.turn.feedback != "", h.turn.edited
			queued := h.endTurn()
			defer func() { h.adviceEdit = ed }() // kept for the retry
			// Kept, to send again, with what was queued and typed since.
			h.comp.set(strings.TrimSpace(strings.Join(append(append([]string{text}, queued...), h.comp.text()), "\n\n")))
			said := h.problem("oge: " + clean(msg.err.Error()))
			if held {
				// The feedback turn waited on this one: say it goes unsent.
				return h, tea.Sequence(said, h.println(h.ogeLine(h.st.dim("the failing cut's feedback turn wasn't sent"))))
			}
			return h, said
		}
		h.turn.events = msg.events
		if h.turn.oge {
			// Shown once it is sent: a spent budget prints no such line.
			return h, tea.Sequence(h.println(h.ogeAdviceLine(h.turn.text)), nextEvent(h.chatView, msg.events))
		}
		return h, nextEvent(h.chatView, msg.events)
	case chatEvMsg:
		if !msg.ok {
			return h.turnEnded()
		}
		return h, tea.Sequence(h.chatEvent(msg.ev), nextEvent(h.chatView, h.turn.events))
	case tea.KeyPressMsg:
		return h.chatKey(msg)
	case tea.PasteMsg:
		if h.chats != nil {
			break // the /chats picker has the screen; a paste never lands behind it
		}
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
	h.dropAskAdvice()
	h.turn.advisedBy = ""
	if a := h.ask(); a != nil && a.Questions != nil {
		// The composer is the Chat's own, in the background too.
		if text := strings.TrimSpace(h.comp.expanded()); text != "" {
			h.enqueue(text)
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
			if t := cleanLines(strings.ReplaceAll(ev.Text, "\t", "    ")); t != "" {
				h.noteReply(ev.Text)
				if h.full {
					return h.replyEntry(t)
				}
				return h.println(agentLines(t))
			}
			return nil
		}
		if ev.Tool == "TodoWrite" {
			return nil
		}
		if s, ok := activityStep(ev); ok {
			h.side.step = s
			h.side.steps++
			if h.full {
				return h.toolEntry(clean(ev.Tool), clean(ev.Target), ev.ToolID, false)
			}
			return h.println("  " + h.st.agent("⏺") + " " + h.st.dim(s))
		}
	case agent.ToolResult:
		h.toolResult(ev)
		return nil
	case agent.Usage:
		if ev.Context != nil {
			h.side.ctx = ev.Context
		}
		if ev.Tokens != nil {
			h.side.tokens = ev.Tokens
		}
		if ev.CostUSD != nil {
			h.side.cost = ev.CostUSD
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
			if h.full {
				return h.printBlock("  ● "+s, func(w int, _ bool) string {
					return ansi.Truncate("  "+h.st.bad("●")+" "+h.st.dim(s), max(w-1, 8), "…")
				})
			}
			return h.println("  " + h.st.bad("⏺") + " " + h.st.dim(s))
		}
	case agent.TurnSettled:
		h.turn.asks = nil
		h.dropAskAdvice() // a consult for an ask that is gone costs money for nothing
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
	if fb := h.turn.feedback; fb != "" && h.turn.changed == 0 && !h.unchecked && !h.turn.stopped {
		// The feedback turn waited for this one, which cut nothing newer:
		// it goes first, before the messages queued meanwhile.
		f := h.turn.fbFrozen
		queued := h.endTurn()
		return h, h.feedbackSubmit(fb, f, queued)
	}
	// Messages already queued will immediately continue the implementer's
	// work. Avoid launching a verifier for the intermediate tree. Commands
	// are different: they may request delivery and do not send a turn.
	followUp := len(h.turn.queued) > 0
	for _, q := range h.turn.queued {
		followUp = followUp && !queuedCommand(q)
	}
	if (h.turn.changed == 0 && !h.unchecked) || h.turn.stopped || followUp {
		// Deferred and interrupted changes wait for the next settled turn.
		h.unchecked = h.unchecked || h.turn.changed > 0
		h.uncheckedChanged = max(h.uncheckedChanged, h.turn.changed)
		return h.nextTurn(nil)
	}
	if h.unchecked {
		h.turn.changed = max(h.turn.changed, h.uncheckedChanged)
	}
	cut := h.startCut()
	m, next := h.nextTurn(nil)
	return m, tea.Batch(cut, next)
}

// startCut starts the turn's cut, in the Pipeline it resolved for the
// turn, and makes it the one verifying.
func (h *home) startCut() tea.Cmd {
	if v := h.verify; v != nil {
		// Its turns are this cut's: so are its changes, at least.
		h.turn.changed = max(h.turn.changed, v.changed)
	}
	return h.cutWith(h.turn.frozen, h.turn.changed)
}

// cutWith starts a cut in f's Pipeline, covering changed files at least.
// A delivery waiting on the cut it supersedes goes with it.
func (h *home) cutWith(f *pipeline.Frozen, changed int) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	h.cuts++
	h.unchecked = false
	h.uncheckedChanged = 0
	h.cutGate, h.gateFor = nil, nil
	h.cutFrozen = f
	h.verify = &chatVerify{seq: h.cuts, changed: changed, frozen: f, cancel: cancel, since: h.clock()}
	hc, seq, view := h.chat, h.cuts, h.chatView
	c := hc.get()
	reqs, done := make(chan chatGateMsg), make(chan chatCutMsg, 1)
	steps := make(chan chatStepMsg, 16)
	go func() {
		res, err := hc.cut(ctx, c, f, cutGates{reqs}, func(ev run.Event) {
			// Only the events a step text consumes reach the pipe, so an
			// agent's event burst can't fill it and drop a boundary one.
			if ev.Kind != run.EvStage && ev.Kind != run.EvAttempt {
				return
			}
			// Progress never stalls the cut: a full pipe drops the event.
			select {
			case steps <- chatStepMsg{view: view, seq: seq, ev: ev}:
			default:
			}
		})
		done <- chatCutMsg{view: view, seq: seq, res: res, err: err}
	}()
	var wait tea.Cmd
	wait = func() tea.Msg {
		select {
		case m := <-done:
			return m
		case g := <-reqs:
			g.view, g.seq, g.next = view, seq, wait
			return g
		case s := <-steps:
			s.next = wait
			return s
		}
	}
	return tea.Batch(wait, verifyTick(view, seq))
}

// verifyTick repaints the verify line's elapsed seconds, once a second
// while the cut seq verifies.
func verifyTick(v *chatView, seq int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return verifyTickMsg{view: v, seq: seq} })
}

// cutStepped names the step a cut's progress event puts on the status
// line. A superseded cut's events are dropped with it.
func (h *home) cutStepped(m chatStepMsg) tea.Cmd {
	v := h.verify
	if v == nil || v.seq != m.seq {
		return m.next
	}
	v.step = verifyStepText(m.ev, v.step)
	return m.next
}

// verifyStepText names a cut event's step for the status line; cur is
// kept for events that name none. A verifier Attempt's end returns to the
// bare verifying until the Check's own step starts.
func verifyStepText(ev run.Event, cur string) string {
	switch {
	case ev.Kind == run.EvStage && ev.Step == "preparing":
		return "preparing the baseline"
	case ev.Kind == run.EvStage && ev.Step == "verify":
		return "QA writing held-out tests"
	case ev.Kind == run.EvStage && ev.Step == "checks":
		return "running the Checks"
	case ev.Kind == run.EvAttempt && ev.Attempt != nil && ev.Attempt.Role == "verifier":
		return ""
	}
	return cur
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
	if h.gateFor != nil && h.gateFor.seq == g.seq && h.turn == nil {
		// The delivery's Gate was decided and the same cut reached the
		// next one: it opens at once, for the same delivery.
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
	h.gateFor = &staleAsk{key: key, arg: arg, seq: g.seq}
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
		if g.recording {
			// Decided: the cut goes on in the background, as any does.
			h.cutGate, h.verify.gate = nil, nil
			switch g.choice.Word {
			case "reject", "quit":
				h.gateFor = nil // the delivery ends here
			}
			return h.println(h.ogeLine(h.st.dim(g.choice.Word + " recorded · verifying again…")))
		}
	default:
		if k.Text != "" {
			g.input += clean(k.Text)
		}
	}
	return nil
}

// endTurn opens the composer again, keeping what was typed during the
// turn and not yet sent; it returns the messages queued meanwhile.
//
// The composer is the Chat's own; for a Chat in the background, reopen
// leaves the rest of the terminal alone.
func (h *home) endTurn() []string {
	text, queued := h.comp.expanded(), h.turn.queued
	h.turn = nil
	h.side.turns++
	if !h.side.since.IsZero() {
		h.side.took = h.clock().Sub(h.side.since)
	}
	h.side.step = ""
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

// queuedCommand reports whether a queued line is a /command or a !shell
// line: it runs on its own and is never the agent's message.
func queuedCommand(q string) bool {
	if _, _, _, ok := parseSlash(q); ok {
		return true
	}
	if _, escaped := unescapeBang(q); escaped {
		return false
	}
	_, shell := shellLine(q)
	return shell
}

// submitQueued runs the queued commands in order. With none queued, the
// messages go as one turn, joined by a blank line. With any, they are back
// in the composer, unsent: a command's prompt or viewer is on screen, and
// a turn started under it would hide it.
//
// A Chat in the background runs none: a command's output, prompt or
// viewer is the terminal's, and would land on the focused Chat. They wait
// in this Chat's own box, with the messages, until it has the focus.
func (h *home) submitQueued(queued []string) (tea.Model, tea.Cmd) {
	draft := h.comp.expanded()
	var cmds []tea.Cmd
	var msgs []string
	ran := false // a command was queued: the messages wait
	for _, q := range queued {
		if queuedCommand(q) {
			ran = true
			if h.job != nil || h.background {
				msgs = append(msgs, q) // after a running shell job: waits too
				continue
			}
			var cmd tea.Cmd
			if _, _, _, ok := parseSlash(q); ok {
				cmd, _ = h.slash(q)
			} else {
				line, _ := shellLine(q)
				cmd = h.shellSubmit(line)
			}
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			continue
		}
		msgs = append(msgs, q)
	}
	keep := strings.TrimSpace(strings.Join(append(msgs, draft), "\n\n"))
	if ran || len(msgs) == 0 {
		h.comp.set(keep)
		return h, tea.Sequence(cmds...)
	}
	h.comp.set(draft)
	res, problem := h.resolve(h.sel)
	if problem != nil {
		h.comp.set(keep)
		return h, h.problem(problem...)
	}
	text := strings.Join(msgs, "\n\n")
	h.hist.add(text)
	_, send := h.chatSubmit(text, res, nil)
	return h, send
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
	h.pendingFB = nil // a newer cut supersedes the one that failed
	res := msg.res
	h.cutGate = nil
	deliver := h.gateFor
	h.gateFor = nil
	verdict := msg.err == nil && res != nil && res.Check != nil &&
		(res.Outcome == run.Accepted || res.Outcome == run.Failed || res.Outcome == run.Parked)
	var line string
	var diff tea.Cmd
	switch {
	case errors.Is(msg.err, run.ErrNothingToCut) && v.changed > 0:
		// What changed has nothing to cut against (a delivery took it
		// meanwhile, say): it is not checked, and the next cut will.
		h.unchecked = true
		h.side.verdict, h.side.outcome, h.side.changes = "", "", nil
		return h.println(h.ogeLine(h.st.dim(files(v.changed) + " changed, not checked yet · the next turn's cut checks them")))
	case errors.Is(msg.err, run.ErrNothingToCut), errors.Is(msg.err, run.ErrSuperseded):
		return nil
	case v.stopped && !verdict:
		h.last = nil // as on an error: the older cut isn't what was just changed
		h.side.verdict, h.side.outcome, h.side.changes = "", "", nil
		line = h.ogeLine("■ verification cancelled: nothing was verified")
	case msg.err != nil:
		h.last = nil // never an older cut's: it isn't what was just changed
		h.side.verdict, h.side.outcome, h.side.changes = "", "", nil
		line = h.ogeLine("✗ verifying the change: " + clean(msg.err.Error()))
	default:
		var l string
		l, h.last = h.chat.verdict(res, v.changed)
		line = h.ogeLine(l)
		h.side.verdict, h.side.outcome, h.side.changes = sideVerdict(l), res.Outcome, nil
		if res.Outcome == run.Accepted || res.Outcome == run.Failed {
			diff = h.inlineDiff()
		}
		diff = tea.Batch(diff, h.sideChanges())
	}
	// The action row acts on h.last: never on a cut the human hasn't
	// seen yet.
	h.actions = false
	if deliver != nil && deliver.seq == msg.seq && h.last != nil && res != nil && res.Outcome == run.Accepted {
		// Through its Gate: the delivery /apply or /branch asked for, of
		// this very cut, and never under a turn.
		if h.turn != nil {
			return h.println(line + "\n" + h.ogeLine(h.st.dim("ready to "+deliver.verb()+" · /"+deliver.verb())))
		}
		return tea.Sequence(h.println(line), diff, h.actNow(deliver.key, deliver.arg))
	}
	// The setup's saved apply preference authorizes delivery, not Gate
	// decisions or stale edits. Use the same checked-Candidate path as
	// /apply, including conflict and late-Tamper refusals. Cancelled cuts
	// and an active turn never deliver on their own.
	if h.sel != nil && h.sel.apply && verdict && res.Outcome == run.Accepted &&
		res.Check.Pass && !v.stopped && h.last != nil && h.last.apply != nil &&
		h.turn == nil && !h.unchecked && h.job == nil && h.stale == nil {
		return tea.Sequence(h.println(line), diff, h.actNow("a", ""))
	}
	// After Ctrl+C on the verify, the human has taken over: no feedback.
	if verdict && res.Feedback != "" && !v.stopped {
		switch h.feedbackMode() {
		case pipeline.ChatFeedbackOff:
			return tea.Sequence(h.println(line), diff)
		case pipeline.ChatFeedbackAsk:
			// Öge doesn't prompt the agent for the human: the turn waits
			// for s or /feedback, and is never sent on its own.
			h.pendingFB = &pendingFeedback{text: res.Feedback, frozen: v.frozen}
			return tea.Sequence(h.println(line), diff)
		}
		if h.turn != nil {
			// Nothing enters a running turn: it waits, and a newer cut of
			// that turn drops it (D5).
			h.turn.feedback, h.turn.fbFrozen = res.Feedback, v.frozen
			return tea.Sequence(h.println(line), diff)
		}
		return tea.Sequence(h.println(line), diff, h.feedbackSubmit(res.Feedback, v.frozen, nil))
	}
	return tea.Sequence(h.println(line), diff)
}

// pendingFeedback is a failing cut's feedback turn held for the human.
type pendingFeedback struct {
	text   string
	frozen *pipeline.Frozen
}

// feedbackMode is [chat] feedback, "ask" without a Chat.
func (h *home) feedbackMode() string {
	if h.chat != nil {
		if c := h.chat.get(); c != nil {
			return c.FeedbackMode()
		}
	}
	return pipeline.ChatFeedbackAsk
}

// feedbackBudget is the Chat's feedback budget.
func (h *home) feedbackBudget() int {
	if h.chat != nil {
		if c := h.chat.get(); c != nil {
			return c.FeedbackBudget()
		}
	}
	return run.FeedbackBudget
}

// sendBack sends the held feedback turn (s on the action row, /feedback):
// the human's explicit act, never decided by Enter alone.
func (h *home) sendBack() (tea.Cmd, string) {
	p := h.pendingFB
	switch {
	case p == nil:
		return nil, "/feedback: no failed Check to send back"
	case h.turn != nil:
		return nil, "/feedback: a turn is running"
	case h.verify != nil:
		return nil, "/feedback: a newer cut is verifying"
	}
	h.pendingFB = nil
	return h.feedbackSubmit(p.text, p.frozen, nil), ""
}

// feedbackCmd is /feedback.
func (h *home) feedbackCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/feedback")
	}
	return h.sendBack()
}

// feedbackSubmit sends a failing cut's feedback turn into the Chat's
// Session (D5), shown dim by its first line, the failing count and
// criterion ids. The composer keeps the human's draft; its changes cut
// as any turn's do.
func (h *home) feedbackSubmit(text string, f *pipeline.Frozen, queued []string) tea.Cmd {
	c := h.chat.get()
	if c == nil {
		if len(queued) > 0 {
			_, send := h.submitQueued(queued) // never lost with the feedback
			return send
		}
		return nil
	}
	first, _, _ := strings.Cut(text, "\n")
	h.turn = &chatTurn{text: first, frozen: f, queued: queued, oge: true}
	h.side.since, h.side.step, h.side.took = h.clock(), "", 0
	ctx, v := h.ctx, h.chatView
	return func() tea.Msg {
		ch, err := c.SendFeedback(ctx, text)
		return chatSentMsg{view: v, events: ch, err: err}
	}
}

// staleAsk is /apply or /branch while newer edits than the verified cut
// are unchecked: a cut still verifying, or an interrupted turn's changes.
type staleAsk struct {
	key, arg string // the action's key, and a branch's name
	// last is the verified cut when it was asked: [a] delivers that one.
	last *lastRun
	seq  int // a Gate's delivery: the cut whose Gate was decided
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
		if h.last == nil || h.last != s.last {
			// /new, or a newer Verdict came in meanwhile: ask again.
			return h.println(h.ogeLine(h.st.dim("the verified cut changed; nothing delivered")))
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

// inlineDiff prints h.last's diff under its Verdict line, dim, indented
// and cut to the width, when it is inlineMax lines or fewer; nothing
// otherwise. Like the Verdict's file count, it is the Candidate's change
// since the Chat's Snapshot. It is the agent's content: cleaned, every
// line. The diff is read off the update loop.
func (h *home) inlineDiff() tea.Cmd {
	if h.last == nil || h.last.patch == nil {
		return nil
	}
	patch, width := h.last.patch, h.width
	if width <= 0 {
		width = 80
	}
	full, view := h.full, h.chatView
	return func() tea.Msg {
		b, err := patch()
		text := strings.TrimRight(string(b), "\n")
		if full && err == nil && text != "" {
			return diffEntryMsg{view, text}
		}
		if err != nil || text == "" || strings.Count(text, "\n")+1 > inlineMax {
			return nil
		}
		lines := strings.Split(cleanLines(text), "\n")
		for i, l := range lines {
			lines[i] = ansi.Truncate("    "+l, max(width-1, 8), "…")
		}
		return chatDiffMsg{view, lines}
	}
}

// diffEntryMsg is a Verdict's patch for the full-screen transcript, which
// draws it as hunks; view is its Chat.
type diffEntryMsg struct {
	view *chatView
	text string
}

// chatDiffMsg is a Verdict's inline diff, ready to print in view's Chat.
type chatDiffMsg struct {
	view  *chatView
	lines []string
}

// chatKey takes a key during a turn. Esc interrupts the turn; so does
// Ctrl+C, and a second Ctrl+C quits. y or n answers a
// permission prompt; a question takes the composer's text or an option's
// number. Otherwise the composer stays live, and Enter queues a message
// for when the turn has ended.
func (h *home) chatKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := h.turn
	h.msg = nil
	switch k.String() {
	case "ctrl+c":
		// A key is the focused Chat's: it stops that turn only.
		if t.stopped {
			if !h.leave() {
				return h, nil
			}
			h.quit = true
			return h, tea.Quit
		}
		h.stop()
		return h, nil
	case "esc":
		if cmd, ok := h.askAdviceEsc(); ok {
			return h, cmd
		}
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
		if text == "a" {
			// "a" and enter is the advisor key; typing "a" is still text.
			if cmd, ok := h.askAdviceKey(a); ok {
				if h.msg == nil {
					h.comp = h.composer()
				}
				return h, cmd
			}
		}
		full := strings.TrimSpace(h.comp.expanded()) // a paste, whole
		h.comp = h.composer()
		if word, _, _, ok := parseSlash(full); ok && slashDuringTurn(word) {
			// It runs alongside the turn, and is never a question's
			// answer: the point of asking now, or of opening another
			// Chat while this one works.
			cmd, _ := h.slash(full)
			if h.msg != nil {
				h.comp.set(full) // the text stays in the box
			}
			return h, cmd
		}
		if a != nil {
			// One question at a time, each with its own numbers.
			q := a.Questions[len(h.turn.answers)]
			if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(q.Options) {
				text = q.Options[n-1]
			}
			h.turn.answers = append(h.turn.answers, text)
			h.dropAskAdvice()
			if len(h.turn.answers) < len(a.Questions) {
				return h, h.println(h.ogeLine(h.st.dim("answered: " + clean(text))))
			}
			return h, h.answer(a, agent.Answer{Allow: true, Answers: h.turn.answers})
		}
		h.enqueue(full)
		return h, nil
	}
	if k.String() == "up" && a == nil && h.comp.empty() && len(t.queued) > 0 {
		// The last queued message comes back to the box, to edit.
		last := len(t.queued) - 1
		h.comp.set(t.queued[last])
		t.queued = t.queued[:last]
		return h, nil
	}
	h.comp.key(k)
	return h, nil
}

// enqueue holds text, typed during the turn, for when it has ended: a
// message goes with the others as one turn, a /command or !shell line runs
// and never reaches the agent (submitQueued).
func (h *home) enqueue(text string) {
	h.turn.queued = append(h.turn.queued, text)
}

// stop interrupts the turn.
func (h *home) stop() {
	t := h.turn
	if t.stopped {
		return
	}
	t.stopped, t.asks = true, nil
	h.dropAskAdvice()
	if c := h.chat.get(); c != nil {
		_ = c.Interrupt()
	}
}

// answer answers the prompt shown and prints what was answered.
func (h *home) answer(a *agent.Ask, ans agent.Answer) tea.Cmd {
	if ans.Allow && len(ans.Answers) > 0 {
		ans.AdvisedBy = h.advisedBy(a) // a Claim the human saw; By stays human
	}
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
	width := h.width
	if width <= 0 {
		width = 80
	}
	var lines []string
	a := h.ask()
	switch {
	case a != nil && a.Questions != nil:
		// The question is the agent's text: indented, never after the
		// marker.
		q := a.Questions[len(t.answers)]
		lines = append(lines, agentLines(cleanLines(q.Text)))
		suggest := 0
		if t.askAdv != nil && t.askAdv.q == len(t.answers) {
			suggest = t.askAdv.suggest
		}
		for i, o := range q.Options {
			row := fmt.Sprintf("    %d. %s", i+1, clean(o))
			if i+1 == suggest {
				row = h.st.accent(row + "   ← advisor suggests")
			}
			lines = append(lines, row)
		}
		hint := "type an answer or an option's number · enter"
		if t.askAdv == nil && h.adv == nil {
			hint += " · a↵ ask the advisor"
		}
		lines = append(lines, h.ogeLine(h.st.dim(hint)))
		lines = append(lines, h.askAdviceLines(len(t.answers))...)
	case a != nil:
		lines = append(lines, h.ogeLine(h.st.ask("Allow "+clean(a.Tool)+"?")))
		if !h.askFits(a) {
			lines = append(lines, "    "+h.st.warn("too long to show at this width; [n] to deny or widen the terminal"))
			break
		}
		for _, r := range h.askRows(a) {
			lines = append(lines, "    "+r)
		}
		lines = append(lines, "    "+h.st.ask("[y] yes  [n] no"))
	case t.stopped:
		lines = append(lines, h.ogeLine(h.st.dim("interrupting… · ctrl+c again quits")))
	default:
		lines = append(lines, h.ogeLine(h.st.dim("working… · esc to interrupt")))
	}
	if s := h.adviseStatus(); s != "" {
		lines = append(lines, s)
	}
	if a == nil || a.Questions != nil {
		for _, q := range t.queued {
			lines = append(lines, h.st.dim(ansi.Truncate(" queued › "+clean(strings.ReplaceAll(q, "\n", " ⏎ ")), width, "…")))
		}
		blank := h.st.dim("Type to queue a message")
		if a != nil {
			blank = "" // a question's box is its answer's
		}
		box, _, _ := h.boxLines(width, blank)
		lines = append(lines, box...)
		for _, l := range h.msg {
			lines = append(lines, " "+h.st.bad(l))
		}
	}
	return strings.Join(lines, "\n")
}

// chatDelivered is what /branch of the cut res shows: once it is
// delivered the Chat marks it, so the next cut's Task starts after its
// turns (D7). A refusal shows as the command printed it.
func (h *home) chatDelivered(c *run.Chat, res *run.Result, how, out string, code int) string {
	out = strings.TrimSuffix(out, "\n")
	if code != ExitOK || c == nil {
		return out
	}
	if err := c.Delivered(res.ID, how, res.Candidate); err != nil {
		out += "\n" + h.ogeLine("✗ marking the delivery: "+clean(err.Error()))
	}
	return out
}

// chatDeliverable refuses delivering the cut res when a Tamper event on
// its turns was recorded after it was verified, and asks for the cut
// again, whose Gate shows it.
func (h *home) chatDeliverable(c *run.Chat, res *run.Result) (string, bool) {
	if c == nil {
		return "", true
	}
	err := c.Deliverable(res.ID, res.Dir)
	if err == nil {
		return "", true
	}
	if errors.Is(err, run.ErrTamperAfterCut) {
		h.recut = true
	}
	return h.ogeLine("✗ not delivered: " + clean(err.Error())), false
}

// chatApply is /apply of the cut res: summed up in three lines (the
// outcome, the files written, the checks) with the Receipt behind
// /receipt, and marked delivered (D7). After the Chat's first apply it
// merges on the commit last applied, not the Snapshot: the working tree
// holds that one already (#261). A cut whose turns have a Tamper event
// it never carried is refused, and cut again. The Chat goes on.
func (h *home) chatApply(env Env, root string, c *run.Chat, res *run.Result) string {
	r, err := delivery.Load(res.Dir)
	if err != nil {
		return "oge: " + clean(err.Error())
	}
	var base delivery.Base
	if c != nil {
		if msg, ok := h.chatDeliverable(c, res); !ok {
			return msg
		}
		base.Rev, base.From = c.Applied()
	}
	var b strings.Builder
	q := env
	q.Stdout, q.Stderr = &b, &b
	code, a := applyOnto(q, r, root, delivery.Choice{}, base, true)
	if code != ExitOK || a == nil {
		return strings.TrimSuffix(b.String(), "\n")
	}
	p := a.Plan
	var first, note string
	switch {
	case p.Writes() == 0 && p.Already == 0:
		return h.ogeLine("✗ nothing applied: everything "+clean(res.ID)+" changes is held back") + heldLines(a.Held)
	case p.Writes() == 0:
		first = "✓ " + clean(res.ID) + " is already in your working tree; nothing to change"
	case p.Merged > 0:
		first = fmt.Sprintf("✓ Applied %s, merged with your edits in %s · nothing committed or staged · /receipt", clean(res.ID), plural(p.Merged, "file"))
	default:
		first = "✓ Applied " + clean(res.ID) + " to your working tree · nothing committed or staged · /receipt"
	}
	if c != nil {
		if err := c.Delivered(res.ID, "apply", a.Held.Rev); err != nil {
			note = "\n" + h.ogeLine("✗ marking the delivery: "+clean(err.Error()))
		}
	}
	// D10: the Snapshot is retaken on what was delivered. A repo that
	// changed outside Öge is never merged in: one line says so.
	if c != nil {
		switch r, err := c.RefreshSnapshot(); {
		case err != nil:
			note += "\n" + h.ogeLine("✗ retaking the Snapshot: "+clean(err.Error()))
		case r == run.RefreshOutside:
			note += "\n" + h.ogeLine(h.st.dim(run.OutsideNote))
		}
	}
	files := p.Paths()
	what := "no files written"
	if len(files) > 0 {
		shown := files[:min(len(files), 5)]
		what = plural(len(files), "file") + ": " + clean(strings.Join(shown, ", "))
		if len(files) > len(shown) {
			what += fmt.Sprintf(", +%d more", len(files)-len(shown))
		}
	}
	sum := []string{h.ogeLine(first), "  " + what, "  checks: " + checkSummary(res)}
	// What a hold-back left out is never dropped from the summary.
	return strings.Join(sum, "\n") + heldLines(a.Held) + note
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

// sideVerdict is a Verdict line for the sidebar: the line without the
// commands it ends with.
func sideVerdict(line string) string {
	parts := strings.Split(clean(line), " · ")
	for len(parts) > 1 && strings.HasPrefix(parts[len(parts)-1], "/") {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, " · ")
}

// tries is n feedback tries in words: "1 try", "3 tries".
func tries(n int) string {
	if n == 1 {
		return "1 try"
	}
	return fmt.Sprintf("%d tries", n)
}
