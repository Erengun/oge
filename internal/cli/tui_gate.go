package cli

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// gateMsg opens a Gate in the live view; the decision goes to reply.
type gateMsg struct {
	req   gate.Request
	reply chan<- gate.Decision
}

// gateState is an open Gate in the live view.
type gateState struct {
	req   gate.Request
	reply chan<- gate.Decision
	input string
	// choice, when set, is the choice whose reason or note is being typed.
	choice *gate.Choice
	msg    string
	// recording: the decision went to the Run, which hasn't recorded it
	// yet. The Gate stays until it has (ADR-0015).
	recording bool
}

// gate opens r in the live view and waits for the human, or for ctx.
// If the live view has stopped, the Gate is asked in plain lines.
func (u *tui) gate(ctx context.Context, r gate.Request) (gate.Decision, error) {
	select {
	case <-u.gone:
		return u.plain.gate(ctx, r)
	default:
	}
	reply := make(chan gate.Decision, 1)
	u.m.queue.push(gateMsg{req: r, reply: reply})
	select {
	case d := <-reply:
		return d, nil
	case <-u.gone:
		return u.plain.gate(ctx, r)
	case <-ctx.Done():
		return gate.Decision{}, ctx.Err()
	}
}

// gateKey takes a key while a Gate is open. ok is false for keys the
// view's usual handling keeps (Ctrl-C at the choice, ctrl+o).
func (m *model) gateKey(k tea.KeyPressMsg) (ok bool) {
	g := m.gate
	switch k.String() {
	case "ctrl+o":
		return false
	}
	if g.recording {
		return k.String() != "ctrl+c"
	}
	switch k.String() {
	case "ctrl+c", "esc":
		if g.choice != nil {
			// Back to the Gate; the Run goes on waiting.
			g.choice, g.input, g.msg = nil, "", ""
			return true
		}
		return k.String() == "esc"
	case "backspace":
		if r := []rune(g.input); len(r) > 0 {
			g.input = string(r[:len(r)-1])
		}
		return true
	case "enter":
		m.gateEnter()
		return true
	}
	if k.Text != "" {
		g.input += clean(k.Text)
	}
	return true
}

func (m *model) gateEnter() {
	g := m.gate
	text := g.input
	if g.choice == nil {
		c, rest, ok := g.req.Match(text)
		if !ok {
			// Enter alone does nothing.
			g.msg = notAChoice(g.req, text)
			if g.msg != "" {
				g.input = ""
			}
			return
		}
		if rest == "" && (c.Reason || c.Note) {
			g.choice, g.input, g.msg = &c, "", ""
			return
		}
		g.choice, text = &c, rest
	}
	if strings.TrimSpace(text) == "e" {
		// TODO(#43-decision): $EDITOR from the live view needs the view
		// to hand over the terminal; plain mode has it.
		g.msg = "the editor isn't available in the live view yet; type the " + label(g.choice)
		g.input = ""
		return
	}
	d, err := g.choice.Decide(text)
	if err != nil {
		g.msg = "a reason is required for " + g.choice.Word
		return
	}
	// The view shows it as taken only when the Run has recorded it.
	g.reply <- d
	g.recording, g.input, g.msg = true, "", ""
}

// gateLines draw the open Gate under the stages: calm, with no default.
func (m *model) gateLines() []string {
	g := m.gate
	if g == nil {
		return nil
	}
	st := m.st
	// What is needed is the attention line, right above the choices
	// (ADR-0022).
	attn := st.bold(clean(gate.Attention(&g.req)))
	screen := gateLinesWith(g.req, attn)
	out := []string{"", "  " + st.bold(screen[0])}
	for _, l := range screen[1:] {
		switch {
		case l == "":
			out = append(out, "")
		case l == attn:
			out = append(out, l)
		default:
			out = append(out, "  "+l)
		}
	}
	out = append(out, "")
	if g.recording {
		out = append(out, st.dim("  "+g.choice.Word+" · recording…"))
		return out
	}
	if g.choice != nil {
		l := label(g.choice)
		if !g.choice.Reason {
			l += ", optional"
		}
		out = append(out, "  "+g.choice.Word+" · "+l+": "+g.input+st.accent("▏"))
		out = append(out, st.dim("  Enter to record it · ctrl+c goes back to the Gate"))
	} else {
		out = append(out, "  > "+g.input+st.accent("▏"))
		out = append(out, st.dim("  "+strings.TrimSuffix(gateHint, ".")))
	}
	if g.msg != "" {
		out = append(out, "  "+st.bad(g.msg))
	}
	return out
}

// note adds a line for a send-back or a recorded decision under the
// stages so far.
func (m *model) note(msg progressMsg) {
	mark := "•"
	if msg.kind == run.EvSendBack {
		mark = "↻"
	}
	m.stages = append(m.stages, stage{name: msg.stage, text: msg.text, state: passed, timed: true, mark: mark,
		start: msg.at, end: msg.at})
	m.cur = len(m.stages)
}

// again lists the implementer and the Check once more after a send-back,
// with the implementer running.
func (m *model) again() {
	at := m.now()
	for _, s := range m.frozen.Stages {
		if s.Role == "implementer" || (s.Role == "verifier" && m.frozen.Mode == pipeline.Standard) {
			m.stages = append(m.stages, m.stageFor(s))
		}
	}
	m.stages = append(m.stages, stage{name: "check", text: m.checkText()})
	m.cur = len(m.stages)
	for j := range m.stages {
		if m.stages[j].state == pending {
			m.begin(j, at)
			break
		}
	}
}

func label(c *gate.Choice) string {
	if c.Reason {
		return "reason"
	}
	return "note"
}
