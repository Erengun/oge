package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/agent/codex"
	"github.com/erengun/oge/internal/run"
)

// Adopting a native session (docs/ux/sessions.md, ADR-0025, S4). Adopting
// resumes that exact session with the settings it had, in its real
// working directory: an in-place Chat. It was never isolated, and Öge
// does not pretend otherwise: no Workspace, no Candidate, no cut or apply,
// and "in place · not isolated" on its rail row and header for as long as
// it is open. When in doubt this refuses, or says less.

// quietFor is how long a session's file must have been left alone before
// Öge resumes it: two writers on one session would interleave its history.
const quietFor = 2 * time.Minute

// adoptPrompt is the one-line question waiting for y.
type adoptPrompt struct{ s agent.NativeSession }

// adoptFoundMsg is the discovery a click on a ◌ row started: the session
// the row stands for, as it is now.
type adoptFoundMsg struct {
	seq   int
	key   string
	found sessionFound
}

// adoptedMsg is the resume finished, or why it failed.
type adoptedMsg struct {
	s    agent.NativeSession
	chat *run.Chat
	err  error
}

// nativeProfile is the profile name a session was found under: "claude@work"
// is "work", plain "claude" the default.
func nativeProfile(s agent.NativeSession) string {
	_, name, _ := strings.Cut(s.Profile, "@")
	return name
}

// ago is a short age for the refusal: seconds under a minute, then whole
// minutes.
func ago(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// askAdopt puts the adopt prompt on c, or says why not on c.note. The
// checks run in this order: provider, working directory, one writer.
func (h *home) askAdopt(c *sessionCatalog, s agent.NativeSession) {
	c.note, c.adopt = "", nil
	switch {
	case h.chat == nil || h.chat.adopt == nil:
		c.note = "this Öge can't adopt a session here"
		return
	case s.Provider != claude.Name && s.Provider != codex.Name:
		c.note = "adopting a " + clean(s.Provider) + " session isn't built yet: nothing was resumed"
		return
	case s.Cwd == "":
		c.note = "that session recorded no working directory: nothing was resumed"
		return
	}
	if v := h.openAdopted(s); v != nil {
		c.note = "already open here as Chat " + fmt.Sprint(v.num)
		return
	}
	written, note := h.quietCheck(s)
	if note != "" {
		c.note = note
		return
	}
	s.Written = written
	c.adopt = &adoptPrompt{s}
}

// quietCheck is the one-writer check: the session file's own write time as
// of now (the catalog's may be minutes old), or why it can't be resumed.
func (h *home) quietCheck(s agent.NativeSession) (written time.Time, note string) {
	written = s.Written
	if s.Path != "" {
		if st, err := os.Stat(s.Path); err == nil && st.ModTime().After(written) {
			written = st.ModTime()
		}
	}
	if written.IsZero() {
		return written, "can't tell whether it is open elsewhere: nothing was resumed"
	}
	if age := h.clock().Sub(written); age < quietFor {
		return written, "written to " + ago(age) + " ago: close it there first"
	}
	return written, ""
}

// openAdopted is the view already holding this session as a live in-place
// Chat, or nil: Öge must not be the second writer.
func (h *home) openAdopted(s agent.NativeSession) *chatView {
	for _, v := range h.views {
		if !v.inPlace || v.chat == nil || v.chat.get() == nil {
			continue
		}
		c := v.chat.get()
		if p, id := c.Native(); p == s.Provider && id == s.ID && c.Err() == nil {
			return v
		}
	}
	return nil
}

// adoptLines is what shows under the box while a prompt, or the reason
// there is none, is up. Enter alone decides nothing (ADR-0022).
func (h *home) adoptLines() []string {
	c, st := h.catalog, h.st
	var out []string
	switch {
	case c.adopt != nil:
		title := clean(c.adopt.s.Title)
		if title == "" {
			title = "(untitled)"
		}
		out = append(out, " "+st.bold("adopt "+title+" in place?")+" not isolated "+st.dim("· y adopt · esc"))
	case c.loading:
		out = append(out, " "+st.dim("looking for the session…"))
	}
	if c.note != "" {
		out = append(out, " "+st.dim(c.note))
	}
	if c.adopt == nil && !c.loading {
		out = append(out, " "+st.dim("esc"))
	}
	return out
}

// adoptKey is a key while the prompt is up: y adopts, anything that is
// not y or esc does nothing, Enter included.
func (h *home) adoptKey(msg tea.KeyPressMsg) tea.Cmd {
	c := h.catalog
	switch msg.String() {
	case "y":
		s := c.adopt.s
		// The prompt may have been up a long time: check again.
		if v := h.openAdopted(s); v != nil {
			c.note, c.adopt = "already open here as Chat "+fmt.Sprint(v.num), nil
			return nil
		}
		if _, note := h.quietCheck(s); note != "" {
			c.note, c.adopt = note, nil
			return nil
		}
		h.chats, h.catalog = nil, nil
		hc := h.chat
		h.note = "adopting " + clean(s.Title) + "…"
		return func() tea.Msg {
			ch, err := hc.adopt(s)
			return adoptedMsg{s: s, chat: ch, err: err}
		}
	case "esc", "ctrl+c", "n":
		if c.promptOnly {
			h.chats, h.catalog = nil, nil
		} else {
			c.adopt = nil
		}
	}
	return nil
}

// sessionRowClick selects a ◌ row, for /close, and looks the session up
// again to ask about it: the row holds only a title and an age.
func (h *home) sessionRowClick(key string) tea.Cmd {
	i := slices.IndexFunc(h.onDesk, func(d deskSession) bool { return d.key() == key })
	if i < 0 {
		return nil
	}
	h.selSess = key
	d := h.onDesk[i]
	c := &sessionCatalog{root: d.repo, loading: true, promptOnly: true}
	h.sessSeq++
	c.seq = h.sessSeq
	h.catalog = c
	l := newSessionList(h)
	h.chats = &l
	find, ctx, seq := h.discover, h.ctx, c.seq
	if find == nil {
		getenv := h.getenv
		if getenv == nil {
			getenv = func(string) string { return "" }
		}
		find = defaultDiscover(getenv)
	}
	if ctx == nil {
		ctx = contextBackground()
	}
	return func() tea.Msg { return adoptFoundMsg{seq, key, find(ctx, []string{d.repo})} }
}

// adoptFound asks about the session a click looked up.
func (h *home) adoptFound(m adoptFoundMsg) {
	c := h.catalog
	if c == nil || c.seq != m.seq || !c.promptOnly {
		return
	}
	c.loading = false
	i := slices.IndexFunc(h.onDesk, func(d deskSession) bool { return d.key() == m.key })
	if i < 0 {
		c.note = "that row is gone"
		return
	}
	d := h.onDesk[i]
	for _, s := range m.found.sessions {
		if s.Provider == d.provider && s.ID == d.id {
			if s.Project == "" {
				s.Project = d.repo
			}
			h.askAdopt(c, s)
			return
		}
	}
	c.note = "that session wasn't found any more: nothing was resumed"
	if len(m.found.problems) > 0 {
		c.note += " · " + clean(m.found.problems[0])
	}
}

// adopted opens the adopted Chat as a view of its own, focused, and says
// what it is: in place, and what is gated.
func (h *home) adopted(m adoptedMsg) tea.Cmd {
	h.note = ""
	if m.err != nil {
		return h.problem("oge: couldn't adopt the session: " + clean(m.err.Error()))
	}
	s := m.s
	repo := s.Project
	if repo == "" {
		repo = h.root
	}
	rc := h.repoCtx
	if repoKey(repo) != repoKey(h.root) && h.repoFor != nil {
		if r, _ := h.repoFor(repo); r != nil {
			rc = r
		}
	}
	h.place(h.repoCtx.root)
	v := h.newView()
	if rc != h.repoCtx {
		v = h.newViewIn(rc)
	}
	if v.chat == nil {
		_ = m.chat.Close()
		return h.problem("oge: couldn't adopt the session: this repo can't hold a Chat")
	}
	title := clean(task0(s.Title))
	v.name, v.inPlace, v.nativeProvider = title, true, s.Provider
	v.chat.c = m.chat
	h.place(v.root)
	h.views = append(h.views, v)
	h.deskRecord().ChatAddedInPlace(m.chat.ID, v.num, title, repo, s.Provider, s.ID)
	h.archiveSession(s.Provider + "/" + s.ID) // it is a Chat now, no longer a ◌ row
	h.switchTo(v)
	st := h.st
	return tea.Sequence(
		h.println(h.ogeLine("adopted "+title+" · in place · not isolated")),
		h.println(h.ogeLine(st.dim("what is gated is read from the session when its first turn starts"))),
		h.println(h.ogeLine(st.dim("Checks: /check runs them on your working tree, when you ask · not isolated"))),
	)
}

// task0 is s for a title, or "(untitled)".
func task0(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(untitled)"
	}
	return s
}

// coverageLine is what Öge can gate in a session, from what the agent
// reported at start and nothing else. Anything it can't read is said so.
func coverageLine(mode, approval, sandbox, reviewer string) string {
	switch {
	case reviewer != "" && reviewer != "user":
		// Approvals go to codex's own reviewer: none reach Öge.
		return "not gated: approvals reviewed by codex (" + reviewer + ")"
	case mode == "bypassPermissions":
		return "not gated: bypassPermissions"
	case mode == "dontAsk":
		return "not gated: dontAsk"
	case approval == "never":
		return "not gated: approval policy never"
	case sandbox == "danger-full-access":
		return "not gated: sandbox danger-full-access"
	case mode == "default":
		return "gated: what your settings would ask · allowed without Öge: your allow rules"
	case mode == "acceptEdits":
		return "gated: what your settings would ask · allowed without Öge: your allow rules and every file edit · mode acceptEdits"
	case mode == "auto":
		return "gated: what your settings would ask · allowed without Öge: what claude's auto mode allows · mode auto"
	case mode != "":
		return "coverage unknown: mode " + mode + " is not one Öge recognises"
	case approval == "" && sandbox != "":
		return "coverage unknown: the session's approval policy wasn't read"
	case approval != "":
		return "gated: what its approval policy would ask · allowed without Öge: what its sandbox allows"
	}
	return "coverage unknown: the session didn't report its permission settings"
}

// coverage prints the coverage line for a session that just opened, and
// records it in the Chat log. The Chat log and the screen carry the same
// words.
func (h *home) coverage(si *agent.SessionInfo) tea.Cmd {
	line := coverageLine(clean(si.PermissionMode), clean(si.ApprovalPolicy), clean(si.Sandbox), clean(si.ApprovalsReviewer))
	if n := len(si.Unsupervised); n > 0 {
		// What codex reported it runs outside its sandbox, named, not hidden.
		names := make([]string, n)
		for i, u := range si.Unsupervised {
			names[i] = clean(u)
		}
		what := "MCP server " + strings.Join(names, ", ")
		if n > 1 {
			what = "MCP servers " + strings.Join(names, ", ")
		}
		line += " · not gated: " + what + " (runs outside its sandbox)"
	}
	if si.MCPUnread {
		line += " · MCP servers: not read, so none are named"
	}
	if n := len(si.Refused); n > 0 {
		refused := make([]string, n)
		for i, r := range si.Refused {
			refused[i] = clean(r)
		}
		line += " · refused by Öge: " + strings.Join(refused, ", ")
	}
	if c := h.chat.get(); c != nil {
		_ = c.RecordCoverage(line, si.PermissionMode, si.Sandbox)
	}
	st := h.st
	if strings.HasPrefix(line, "gated") && len(si.Unsupervised) == 0 && !si.MCPUnread && len(si.Refused) == 0 {
		return h.println(h.ogeLine(st.dim(line)))
	}
	return h.println(h.ogeLine(line))
}

// notCovered is what Öge did not supervise in an in-place Chat, said
// wherever one reports a result.
const notCovered = "not covered: writes on your tree weren't isolated or scope-checked · anything the session's own settings allow ran without a Gate"

// chatChecksMsg is the end of /check on a view's working tree.
type chatChecksMsg struct {
	view *chatView
	res  *run.TreeChecks
	err  error
}

// checkCmd is /check: the Pipeline's Checks on the adopted session's
// working tree, only when asked. Nothing is cut or applied and no Receipt
// is made: the tree was never isolated, and the output says so.
func (h *home) checkCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/check")
	}
	c := h.chat.get()
	switch {
	case !h.inPlace || c == nil:
		return nil, "/check is for an adopted session: other Chats are checked in isolation after each turn"
	case h.turn != nil:
		return nil, "/check: the session is working: ask again when its turn ends"
	case h.checking:
		return nil, "/check: the Checks are already running"
	}
	res, problem := h.resolve(h.sel)
	if problem != nil {
		return nil, strings.Join(problem, " ")
	}
	h.checking, h.turnDuringChecks = true, false
	ctx, v, getenv := h.ctx, h.chatView, h.getenv
	if ctx == nil {
		ctx = contextBackground()
	}
	run1 := func() tea.Msg {
		r, err := c.ChecksOnTree(ctx, run.TreeChecksParams{Frozen: res.frozen, Getenv: getenv})
		return chatChecksMsg{view: v, res: r, err: err}
	}
	return tea.Sequence(h.println(h.ogeLine(h.st.dim("running the Checks on your working tree · not isolated"))), run1), ""
}

// checksEnded prints what /check ran. The words never claim isolation.
func (h *home) checksEnded(m chatChecksMsg) tea.Cmd {
	h.checking = false
	during := h.turnDuringChecks
	h.turnDuringChecks = false
	if m.err != nil {
		return h.problem("oge: couldn't run the Checks: " + clean(m.err.Error()))
	}
	st := h.st
	lines := []string{"ran on your working tree · not isolated"}
	for i, e := range m.res.Commands {
		lines = append(lines, "  "+commandLine(e))
		if i < len(m.res.Tails) {
			for _, l := range m.res.Tails[i] {
				lines = append(lines, "    "+st.dim(clean(l)))
			}
		}
	}
	verdict := st.ok("✓") + " the Checks passed on your tree"
	if !m.res.Pass {
		verdict = st.bad("✗") + " the Checks failed on your tree"
	}
	lines = append(lines, verdict+st.dim(" · no cut, nothing applied, no Receipt"))
	if during {
		lines = append(lines, st.warn("!")+" the session worked while the Checks ran: the result may not match your tree now")
	}
	lines = append(lines, st.dim(notCovered))
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = h.ogeLine(l)
	}
	return h.println(strings.Join(out, "\n"))
}
