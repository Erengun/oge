package cli

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// The sidebar answers ADR-0022's three questions at a glance: what is
// happening (SESSION), does Öge need me (ATTENTION) and can I trust it
// (TRUST, CHANGES). It only reads what Öge already knows: the turn, the
// cut's Verdict and Candidate, the Launch profile's limitations. It never
// computes a verdict of its own. The section style is after pitago (MIT, see
// THIRD_PARTY_NOTICES).

// sideState is what the sidebar keeps of the session between frames.
type sideState struct {
	frozen  *pipeline.Frozen // what the next turn will use
	step    string           // the turn's latest tool step
	since   time.Time        // when the turn began
	took    time.Duration    // how long the last turn took
	verdict string           // the last cut's Verdict line, plain
	outcome run.Outcome
	changes []diffFile // the last cut's files with their +/- counts
	tokens  *int       // what the agent reported, never estimated
	cost    *float64
	turns   int               // turns this session has settled
	steps   int               // tool steps it has taken
	ctx     *agent.ContextUse // how full the context is, when reported
}

// setSetup stores the resolved config: the setup bar's lines and what the
// sidebar names as the agent and model.
func (h *home) setSetup(f *pipeline.Frozen) {
	h.setup = setupLines(f)
	h.side.frozen = f
}

// sideChangesMsg is a cut's files with their counts; id is the cut's Run, so
// a read still in flight when /new or a newer cut came is dropped.
type sideChangesMsg struct {
	id    string
	files []diffFile
}

// sideChanges reads the Candidate's patch for the sidebar's CHANGES.
func (h *home) sideChanges() tea.Cmd {
	if !h.full || h.last == nil || h.last.patch == nil {
		return nil
	}
	patch, id := h.last.patch, h.last.id
	return func() tea.Msg {
		b, err := patch()
		if err != nil {
			return nil
		}
		return sideChangesMsg{id, splitPatch(cleanLines(string(b)))}
	}
}

// binding is the implementer's agent and model of the config, "" when unknown.
func (s *sideState) binding() (agent, model string) {
	if s.frozen == nil {
		return "", ""
	}
	for _, st := range s.frozen.Stages {
		if st.Role == "implementer" {
			return st.Binding(), st.Model
		}
	}
	return "", ""
}

// attention is the line that says whether Öge needs the human, and why.
func (h *home) attention() (text string, needs bool) {
	switch {
	case h.turn != nil && h.ask() != nil:
		a := h.ask()
		if a.Questions != nil {
			return "a question is waiting for your answer", true
		}
		return "Allow " + clean(a.Tool) + "? answer y or n below", true
	case h.stale != nil:
		return "newer edits, unchecked: a to " + h.stale.verb() + " verified or w to wait", true
	case h.cutGate != nil, h.verify != nil && h.verify.gate != nil:
		return "a Gate is waiting for you · /apply", true
	case h.hidden != nil:
		return "hidden characters in your message: confirm below", true
	case h.last != nil && h.side.outcome == run.Accepted && !h.cutDelivered():
		return "the cut is Accepted and not applied · /apply or /diff", true
	case h.last != nil && h.side.outcome == run.Failed:
		return "the Check failed · /diff /receipt", true
	}
	return "nothing needs you", false
}

// cutDelivered reports whether the Chat marked the last cut delivered, by
// /apply, /branch or apply on accept: the Ledger's word, not the view's.
func (h *home) cutDelivered() bool {
	if h.chat == nil || h.last == nil {
		return false
	}
	c := h.chat.get()
	return c != nil && c.IsDelivered(h.last.id)
}

// sidebar draws the pane at width and height: a rounded box of sections.
func (h *home) sidebar(width, height int) []string {
	st := h.st
	inner := max(width-4, 8)
	var body []string
	rule := func() { body = append(body, st.border(strings.Repeat("─", inner))) }
	head := func(s string) { body = append(body, st.accent(st.bold(strings.ToUpper(s)))) }
	kv := func(k, v string) {
		k = fmt.Sprintf("%-6s", k)
		v = ansi.Truncate(v, max(inner-7, 1), "…")
		body = append(body, st.dim(k)+" "+v)
	}
	wrap := func(s string, paint func(string) string, max int) {
		lines := strings.Split(ansi.Wrap(s, inner, ""), "\n")
		for i, l := range lines {
			if i == max {
				body = append(body, st.dim("…"))
				break
			}
			body = append(body, paint(l))
		}
	}
	plain := func(s string) string { return s }

	// SESSION
	head("session")
	state, dot := "ready", st.ok("●")
	switch {
	case h.turn != nil && h.ask() != nil:
		state, dot = "waiting for you", st.warn("●")
	case h.turn != nil:
		state, dot = "working", st.accent("●")
		if tool, _, _ := strings.Cut(h.side.step, " "); tool != "" {
			state += " · " + tool
		}
	case h.verify != nil:
		state, dot = "verifying", st.accent("●")
	case h.run != nil:
		state, dot = "running", st.accent("●")
	}
	body = append(body, ansi.Truncate(dot+" "+st.bold(state), inner, "…"))
	agentName, model := h.side.binding()
	if agentName != "" {
		kv("agent", clean(agentName))
	}
	if model != "" {
		kv("model", clean(model))
	}
	if h.side.frozen != nil {
		kv("mode", fmt.Sprint(h.side.frozen.Mode))
	}
	if h.turn != nil && h.side.step != "" {
		kv("step", h.side.step)
	}
	switch {
	case h.turn != nil && !h.side.since.IsZero():
		kv("time", elapsed(h.clock().Sub(h.side.since)))
	case h.verify != nil:
		kv("time", elapsed(h.clock().Sub(h.verify.since)))
	case h.side.took > 0:
		kv("last", elapsed(h.side.took))
	}

	// STATS: counted from the events Öge saw; tokens, cost and context only
	// when the agent reported them, never estimated.
	if s := h.side; s.turns > 0 || s.steps > 0 || s.tokens != nil || s.cost != nil || s.ctx != nil {
		rule()
		head("stats")
		kv("turns", fmt.Sprint(s.turns))
		kv("steps", fmt.Sprint(s.steps))
		if s.tokens != nil {
			kv("tokens", fmt.Sprint(*s.tokens))
		}
		if s.cost != nil {
			kv("cost", fmt.Sprintf("$%.3f", *s.cost))
		}
		if c := s.ctx; c != nil && c.Window > 0 {
			pct := c.Percent()
			n := inner - 5
			full := min(pct*n/100, n)
			paint := st.ok
			switch {
			case pct >= 85:
				paint = st.bad
			case pct >= 60:
				paint = st.warn
			}
			body = append(body, st.dim("ctx   ")+paint(strings.Repeat("█", full))+st.dim(strings.Repeat("░", n-full))+" "+st.dim(fmt.Sprintf("%d%%", pct)))
		}
	}

	// ATTENTION
	rule()
	head("attention")
	text, needs := h.attention()
	if needs {
		wrap(text, st.warn, 3)
	} else {
		wrap(text, st.dim, 2)
	}

	// TRUST
	rule()
	head("trust")
	switch {
	case h.verify != nil && h.verify.stopped:
		body = append(body, st.dim("cancelling verification…"))
	case h.verify != nil && h.verify.gate != nil:
		body = append(body, st.warn("waiting at its Gate"))
	case h.verify != nil:
		step := h.verify.step
		if step == "" {
			step = "verifying…"
		}
		wrap(step, st.accent, 2)
	}
	if v := h.side.verdict; v != "" {
		paint := plain
		switch h.side.outcome {
		case run.Accepted:
			paint = st.ok
		case run.Failed, run.Refused:
			paint = st.bad
		case run.Parked:
			paint = st.warn
		}
		body = append(body, st.dim("verdict"))
		wrap(v, paint, 4)
	} else if h.verify == nil {
		body = append(body, st.dim("no cut yet"))
	}
	if f := h.side.frozen; f != nil && len(f.Limitations) > 0 {
		// One line: the words are in /receipt, not worth five rows here.
		body = append(body, st.warn("⚠")+" "+st.dim(plural(len(f.Limitations), "Agent limitation")+" · /receipt"))
	} else if f != nil {
		body = append(body, st.dim("no Agent limitations"))
	}

	// WORKSPACE: at the bottom, with the room the sections above leave.
	top := body
	body = nil
	rule()
	title := "workspace"
	if parts := strings.Split(h.title, " · "); len(parts) > 2 {
		title += " · " + clean(parts[len(parts)-1])
	}
	head(title)
	room := max(height-2-len(top)-len(body), 1)
	switch {
	case h.turn != nil && h.turn.changed > 0:
		body = append(body, st.dim(files(h.turn.changed)+", not checked yet"))
	case h.verify != nil:
		body = append(body, st.dim(files(h.verify.changed)))
	case len(h.side.changes) > 0:
		shown := h.side.changes
		held := 0
		for _, f := range shown {
			if heldStatus(f.status) {
				held++
			}
		}
		limit := max(room-1, 1)
		if len(shown) > limit {
			shown = shown[:max(limit-1, 1)]
		}
		for _, f := range shown {
			cs := counts(st, f.add, f.del)
			cw := ansi.StringWidth(cs)
			if cw > 0 {
				cw++
				cs = " " + cs
			}
			path := clean(f.path)
			if w := ansi.StringWidth(path); w > inner-cw {
				path = "…" + ansi.TruncateLeft(path, w-(inner-cw)+1, "")
			}
			pad := strings.Repeat(" ", max(inner-ansi.StringWidth(path)-cw, 0))
			body = append(body, path+pad+cs)
		}
		if len(shown) < len(h.side.changes) {
			body = append(body, st.dim(fmt.Sprintf("… +%d more", len(h.side.changes)-len(shown))))
		}
		if held > 0 {
			body = append(body, st.warn("⚠")+" "+st.dim(plural(held, "file")+" held back"))
		}
	default:
		body = append(body, st.dim("no changes"))
	}
	// A gap keeps the workspace on the pane's last rows.
	for len(top)+len(body) < height-2 {
		top = append(top, "")
	}
	body = append(top, body...)

	// The box: its rows are height tall.
	out := make([]string, 0, height)
	b := st.border
	out = append(out, b("╭"+strings.Repeat("─", width-2)+"╮"))
	for i := 0; i < height-2; i++ {
		l := ""
		if i < len(body) {
			l = ansi.Truncate(body[i], inner, "…")
		}
		out = append(out, b("│")+" "+l+strings.Repeat(" ", max(inner-ansi.StringWidth(l), 0))+" "+b("│"))
	}
	out = append(out, b("╰"+strings.Repeat("─", width-2)+"╯"))
	return out[:height]
}
