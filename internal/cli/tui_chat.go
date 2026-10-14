package cli

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

// The bare oge home talks to one Chat for the session (docs/ux/chat-first.md,
// slice 7): Enter sends the message as a turn, whose reply and dim tool
// one-liners are printed as they come. A turn that changes nothing ends
// there: no stages, no Receipt, no action row. A turn with Promoted or
// Ambiguous changes is cut into a Run at once (D4), and the cut is waited
// for (background verify is #260), then summed up on one Verdict line;
// /apply, /diff and /receipt act on its Run.

// homeChat is the session's Chat, opened by the first message.
type homeChat struct {
	// open opens the Chat on the config the first message resolved.
	open func(res *resolved) (*run.Chat, error)
	// cut cuts the Chat's turns since the last cut into a Run.
	cut func(c *run.Chat) (*run.Result, error)
	// verdict is the cut's Verdict line, and what can take its Run.
	verdict func(res *run.Result, changed int) (string, *lastRun)

	agents map[string]agent.Adapter

	mu sync.Mutex
	c  *run.Chat
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
}

// chatTurn is the Chat turn in flight, then its cut.
type chatTurn struct {
	events  <-chan agent.Event
	ask     *agent.Ask // the permission prompt waiting for y or n
	changed int        // Promoted and Ambiguous paths the turn changed
	cutting bool
	stopped bool // Esc or Ctrl+C interrupted it
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
	// chatCutMsg is the cut's end.
	chatCutMsg struct {
		res *run.Result
		err error
	}
)

// chatSubmit sends text as the next turn, opening the Chat first.
func (h *home) chatSubmit(text string, res *resolved) (tea.Model, tea.Cmd) {
	h.turn = &chatTurn{}
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
			h.turn = nil
			h.reopen()
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
	case chatCutMsg:
		return h.cutEnded(msg)
	case tea.KeyPressMsg:
		return h, h.chatKey(msg)
	case ctxDoneMsg:
		h.quit = true
		return h, tea.Quit
	}
	return h, nil
}

// chatEvent prints what a turn's event shows: the reply's words, a tool's
// dim one-liner, a denial; an Ask waits in the live area for y or n.
func (h *home) chatEvent(ev agent.Event) tea.Cmd {
	switch ev.Kind {
	case agent.Claim:
		if ev.Tool == "" {
			if t := cleanLines(ev.Text); t != "" {
				return h.println(t)
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
		if ev.Ask != nil {
			h.turn.ask = ev.Ask
			return nil
		}
		if ev.Host != nil && ev.Host.By == "human" {
			return nil // the answer is printed where it was given
		}
		if s, ok := activityStep(ev); ok {
			return h.println(h.st.dim("  " + s))
		}
	case agent.TurnSettled:
		h.turn.ask = nil
		var tamper tea.Cmd
		if sc := ev.Scope; sc != nil {
			h.turn.changed = len(sc.Promoted) + len(sc.Ambiguous)
			if len(sc.Tamper) > 0 {
				// A protected path the turn wrote was put back (ADR-0009).
				tamper = h.println(h.st.dim("  reverted " + clean(strings.Join(sc.Tamper, ", ")) + ": protected"))
			}
		}
		switch {
		case h.turn.stopped:
			return tea.Sequence(tamper, h.println(h.st.dim("■ interrupted")))
		case ev.Stop != "":
			return tea.Sequence(tamper, h.println("■ "+clean(ev.Stop)))
		case ev.Failure != "":
			return tea.Sequence(tamper, h.println("✗ "+clean(ev.Failure)))
		}
		return tamper
	}
	return nil
}

// turnEnded cuts a turn that changed something, or opens the composer.
func (h *home) turnEnded() (tea.Model, tea.Cmd) {
	if h.turn.changed == 0 {
		h.turn = nil
		h.reopen()
		return h, nil
	}
	h.turn.cutting = true
	hc := h.chat
	c := hc.get()
	return h, func() tea.Msg {
		res, err := hc.cut(c)
		return chatCutMsg{res, err}
	}
}

// cutEnded prints the cut's Verdict line and opens the composer.
func (h *home) cutEnded(msg chatCutMsg) (tea.Model, tea.Cmd) {
	changed := h.turn.changed
	h.turn = nil
	var line string
	switch {
	case errors.Is(msg.err, run.ErrNothingToCut):
	case msg.err != nil:
		line = "oge: verifying the change: " + clean(msg.err.Error())
	default:
		var last *lastRun
		line, last = h.chat.verdict(msg.res, changed)
		if last != nil {
			h.last = last
		}
	}
	h.reopen()
	if line == "" {
		return h, nil
	}
	return h, h.println(line)
}

// chatKey takes a key during a turn: y or n answers an Ask, Esc and
// Ctrl+C interrupt the turn. Nothing is typed ahead.
func (h *home) chatKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c":
		if h.turn.cutting || h.turn.stopped {
			return nil
		}
		h.turn.stopped = true
		if c := h.chat.get(); c != nil {
			_ = c.Interrupt()
		}
	case "y", "n":
		a := h.turn.ask
		if a == nil {
			return nil
		}
		h.turn.ask = nil
		allow := k.String() == "y"
		_ = a.Reply(allow)
		what := "denied"
		if allow {
			what = "allowed"
		}
		return h.println(h.st.dim(fmt.Sprintf("  %s: %s", what, clean(strings.TrimSpace(a.Tool+" "+a.Target)))))
	}
	return nil
}

// turnView is the live area during a turn: its Ask, or what it is doing.
func (h *home) turnView() string {
	t := h.turn
	switch {
	case t.ask != nil:
		return " ? " + clean(strings.TrimSpace("Allow "+t.ask.Tool+" "+t.ask.Target)) + "  " + h.st.bold("[y] yes  [n] no")
	case t.cutting:
		return " " + h.st.dim(files(t.changed)+" · verifying…")
	case t.stopped:
		return " " + h.st.dim("interrupting…")
	}
	return " " + h.st.dim("working… · esc to interrupt")
}

// verdictLine sums a cut's Run up in one line.
func verdictLine(res *run.Result, changed int) string {
	if n := res.Attempt; n != nil && len(n.Changed) > 0 {
		changed = len(n.Changed)
	}
	what := files(changed)
	switch res.Outcome {
	case run.Accepted:
		return "✓ Accepted · " + what + " · Check passed · /apply /diff"
	case run.Failed:
		return "✗ Failed · " + what + " · the Check failed · /diff /receipt"
	case run.Parked:
		at := ""
		if res.Gate != "" {
			at = " at " + res.Gate
		}
		return "✗ Parked" + at + " · " + what + " · /receipt"
	case run.Refused:
		why := ""
		if len(res.Why) > 0 {
			why = ": " + clean(res.Why[0])
		}
		return "✗ Refused" + why
	}
	return "✗ " + clean(string(res.Outcome)) + " · " + what + " · /receipt"
}
