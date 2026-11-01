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
	case s.Provider != claude.Name:
		// TODO(#385-decision): Codex resumes with thread/resume and its own
		// approval policy and sandbox; its adapter has no such path yet.
		c.note = "adopting a " + clean(s.Provider) + " session isn't built yet: nothing was resumed"
		return
	case s.Cwd == "":
		c.note = "that session recorded no working directory: nothing was resumed"
		return
	}
	// The file's own write time, as of now: the catalog's may be minutes old.
	written := s.Written
	if s.Path != "" {
		if st, err := os.Stat(s.Path); err == nil && st.ModTime().After(written) {
			written = st.ModTime()
		}
	}
	if written.IsZero() {
		c.note = "can't tell whether it is open elsewhere: nothing was resumed"
		return
	}
	if age := h.clock().Sub(written); age < quietFor {
		c.note = "written to " + ago(age) + " ago: close it there first"
		return
	}
	s.Written = written
	c.adopt = &adoptPrompt{s}
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
	v.name, v.inPlace = title, true
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
		h.println(h.ogeLine(st.dim("Checks: not run on in-place Chats yet"))),
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
func coverageLine(mode, approval, sandbox string) string {
	switch {
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
	case mode != "":
		return "gated: what your settings would ask · allowed without Öge: your allow rules · mode " + mode
	case approval != "" || sandbox != "":
		return "gated: what its approval policy would ask · allowed without Öge: what its sandbox allows"
	}
	return "coverage unknown: the session didn't report its permission settings"
}

// coverage prints the coverage line for a session that just opened, and
// records it in the Chat log. The Chat log and the screen carry the same
// words.
func (h *home) coverage(si *agent.SessionInfo) tea.Cmd {
	line := coverageLine(clean(si.PermissionMode), clean(si.ApprovalPolicy), clean(si.Sandbox))
	if c := h.chat.get(); c != nil {
		_ = c.RecordCoverage(line, si.PermissionMode, si.Sandbox)
	}
	st := h.st
	if strings.HasPrefix(line, "gated") {
		return h.println(h.ogeLine(st.dim(line)))
	}
	return h.println(h.ogeLine(line))
}
