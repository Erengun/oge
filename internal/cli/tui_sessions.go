package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/agent/codex"
	"github.com/erengun/oge/internal/run"
)

// Native sessions on the Desk (docs/ux/sessions.md, S3). /sessions lists
// what Claude and Codex themselves store for a project, `a` puts one on
// the Desk as a ◌ row under its project, /close takes the selected row off
// again. A row is never ▲ ● ✓ ✗: Öge does not drive it, so it only knows
// when the session was last active. Nothing here is a transcript.

// deskSession is a native session on the Desk: what its rail row shows.
type deskSession struct {
	provider, id, profile string
	repo                  string // the project's git root
	title                 string
	last                  time.Time // last activity, as of the last /sessions
}

func (s deskSession) key() string { return s.provider + "/" + s.id }

// sessionFound is what discovery brings back: the sessions in the asked
// projects, and one line per provider that could not be read.
type sessionFound struct {
	sessions []agent.NativeSession
	problems []string
}

// sessionDiscover finds the native sessions in the project roots. It runs
// off the UI goroutine and starts nothing on a timer.
type sessionDiscover func(ctx context.Context, roots []string) sessionFound

// defaultDiscover reads Claude's session files and asks a short-lived
// Codex app-server, then keeps the sessions inside roots.
func defaultDiscover(getenv func(string) string) sessionDiscover {
	return func(ctx context.Context, roots []string) sessionFound {
		var f sessionFound
		all := claude.DiscoverSessions(claude.Profiles(getenv("HOME"), getenv, nil))
		cs, why := (&codex.Adapter{}).ListSessions(ctx, roots)
		all = append(all, cs...)
		if why != "" {
			f.problems = append(f.problems, why)
		}
		f.sessions = sessionsInProjects(all, roots)
		return f
	}
}

// sessionsMsg is a finished discovery for the catalog opened as seq.
type sessionsMsg struct {
	seq   int
	found sessionFound
}

// sessionCatalog is the open /sessions.
type sessionCatalog struct {
	seq      int
	all      bool
	root     string // the focused project, when not all
	loading  bool
	problems []string
	note     string // the last key's one dim line
	found    []agent.NativeSession
}

type sessionItem struct {
	s   agent.NativeSession
	tag string // the project, with /sessions all
	on  bool
}

func (i sessionItem) FilterValue() string { return i.s.Title }

type sessionDelegate struct{ h *home }

func (sessionDelegate) Height() int                         { return 1 }
func (sessionDelegate) Spacing() int                        { return 0 }
func (sessionDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d sessionDelegate) Render(w io.Writer, m list.Model, i int, it list.Item) {
	x := it.(sessionItem)
	st := d.h.st
	who := x.s.Profile
	if who == "" {
		who = x.s.Provider
	}
	title := clean(x.s.Title)
	if title == "" {
		title = "(untitled)"
	}
	tail := " · last active " + sessionAge(d.h.clock(), x.s.LastActive)
	if x.tag != "" {
		tail += " · " + clean(x.tag)
	}
	if x.on {
		tail += " · on the Desk"
	}
	head := clean(who) + " · "
	room := max(d.h.width-6-ansi.StringWidth("◌ "+head+tail), 8)
	title = ansi.Truncate(title, room, "…")
	line := st.dim("◌") + " " + head + title + st.dim(tail)
	if i == m.Index() {
		fmt.Fprint(w, " "+st.accent("> ")+line)
		return
	}
	fmt.Fprint(w, "   "+line)
}

// sessionAge is how long ago t was, coarse; a minute or less is "now".
func sessionAge(now, t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	if a := railAge(now, t); a != "" {
		return a
	}
	return "now"
}

// sessionsCmd is /sessions [all]: the catalog for the focused project, or
// for every project on the Desk. It opens at once and fills when the
// discovery, run in the background, comes back.
func (h *home) sessionsCmd(args []string) (tea.Cmd, string) {
	if len(args) > 1 || len(args) == 1 && args[0] != "all" {
		return nil, usageOf("/sessions")
	}
	c := &sessionCatalog{all: len(args) == 1, root: h.root, loading: true}
	roots := []string{h.root}
	if c.all {
		_, roots = h.railGroups()
		if !slices.ContainsFunc(roots, func(r string) bool { return repoKey(r) == repoKey(h.root) }) {
			roots = append(roots, h.root)
		}
	}
	h.sessSeq++
	c.seq = h.sessSeq
	h.catalog = c
	l := list.New(nil, sessionDelegate{h}, 100, 4)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
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
		ctx = context.Background()
	}
	return func() tea.Msg { return sessionsMsg{seq, find(ctx, roots)} }, ""
}

// sessionsFound fills the catalog when its discovery returns, and brings
// the age of the rows already on the Desk up to date: the one time it
// changes besides /sessions being opened again.
func (h *home) sessionsFound(m sessionsMsg) {
	for _, s := range m.found.sessions {
		for i, d := range h.onDesk {
			if d.provider == s.Provider && d.id == s.ID && !s.LastActive.IsZero() {
				h.onDesk[i].last = s.LastActive
				if s.Title != "" {
					h.onDesk[i].title = s.Title
				}
			}
		}
	}
	c := h.catalog
	if c == nil || c.seq != m.seq || h.chats == nil {
		return
	}
	c.loading, c.problems, c.found = false, m.found.problems, m.found.sessions
	sort.SliceStable(c.found, func(i, j int) bool { return c.found[i].LastActive.After(c.found[j].LastActive) })
	h.catalogItems()
}

// catalogItems rebuilds the list from the found sessions, keeping the
// selection where it was.
func (h *home) catalogItems() {
	c := h.catalog
	var roots []string
	if c.all {
		_, roots = h.railGroups()
	}
	labels := projectLabels(roots)
	var items []list.Item
	for _, s := range c.found {
		if !c.all && repoKey(s.Project) != repoKey(c.root) {
			continue
		}
		tag := ""
		if c.all {
			if i := slices.IndexFunc(roots, func(r string) bool { return repoKey(r) == repoKey(s.Project) }); i >= 0 {
				tag = labels[i]
			} else {
				tag = filepath.Base(s.Project)
			}
		}
		items = append(items, sessionItem{s, tag, h.sessionOn(s) >= 0})
	}
	sel := h.chats.Index()
	h.chats.SetItems(items)
	h.chats.SetHeight(min(len(items), max(h.height-8, 3)) + 4)
	h.chats.Select(min(sel, max(len(items)-1, 0)))
}

// sessionOn is where s sits in the On-the-Desk rows, -1 when it is not.
func (h *home) sessionOn(s agent.NativeSession) int {
	return slices.IndexFunc(h.onDesk, func(d deskSession) bool { return d.provider == s.Provider && d.id == s.ID })
}

// deskRecord is the Desk record the home writes to; nil records nothing.
func (h *home) deskRecord() *run.Desk {
	if h.chat == nil {
		return nil
	}
	return h.chat.desk
}

// addSession puts s on the Desk, after the rows already there.
func (h *home) addSession(s agent.NativeSession) {
	d := deskSession{provider: s.Provider, id: s.ID, profile: s.Profile, repo: s.Project, title: s.Title, last: s.LastActive}
	h.onDesk = append(h.onDesk, d)
	h.place(d.repo) // its project keeps its slot in the rail
	h.deskRecord().SessionAdded(run.SessionAdded{Provider: d.provider, ID: d.id, Profile: d.profile, Repo: d.repo, Title: d.title, Last: d.last})
}

// archiveSession takes the row off the Desk. The native session is left
// as it is.
func (h *home) archiveSession(key string) {
	i := slices.IndexFunc(h.onDesk, func(d deskSession) bool { return d.key() == key })
	if i < 0 {
		return
	}
	d := h.onDesk[i]
	h.onDesk = slices.Delete(h.onDesk, i, i+1)
	h.deskRecord().SessionArchived(d.provider, d.id)
	if h.selSess == key {
		h.selSess = ""
	}
}

// sessionsIn is the Desk's session rows in the project at root, in the
// order they were added.
func (h *home) sessionsIn(root string) []deskSession {
	k := repoKey(root)
	var out []deskSession
	for _, d := range h.onDesk {
		if repoKey(d.repo) == k {
			out = append(out, d)
		}
	}
	return out
}

// restoreSessions is the sessions desk.log says are on the Desk.
func restoreSessions(private string) []deskSession {
	recs, _ := run.ReadDesk(private)
	var out []deskSession
	for _, s := range run.SessionsOnDesk(recs) {
		out = append(out, deskSession{s.Provider, s.ID, s.Profile, s.Repo, s.Title, s.Last})
	}
	return out
}

// catalogKey is a key while /sessions is open: a adds the selected
// session to the Desk, Enter says adopting is not built yet, Esc closes.
func (h *home) catalogKey(msg tea.KeyPressMsg) tea.Cmd {
	c := h.catalog
	switch msg.String() {
	case "esc", "ctrl+c":
		h.chats, h.catalog = nil, nil
	case "a":
		it, ok := h.chats.SelectedItem().(sessionItem)
		switch {
		case !ok:
		case it.on:
			c.note = "already on the Desk"
		default:
			h.addSession(it.s)
			c.note = "added to the Desk"
			h.catalogItems()
		}
	case "enter":
		if _, ok := h.chats.SelectedItem().(sessionItem); ok {
			c.note = "adopting a session comes in #385; for now it only sits on the Desk"
		}
	default:
		l, _ := h.chats.Update(msg)
		h.chats = &l
	}
	return nil
}

func (h *home) catalogTitle() string {
	if h.catalog.all {
		return "Sessions · all projects"
	}
	return "Sessions · " + clean(filepath.Base(h.catalog.root))
}

// catalogLines draws /sessions under the box.
func (h *home) catalogLines() []string {
	c, st := h.catalog, h.st
	var out []string
	switch items := h.chats.Items(); {
	case c.loading:
		out = append(out, " "+st.dim("looking for sessions…"))
	case len(items) == 0:
		out = append(out, " "+st.dim("no sessions found here"))
	default:
		for _, l := range strings.Split(h.chats.View(), "\n") {
			if strings.TrimSpace(l) != "" {
				out = append(out, ansi.Truncate(strings.TrimRight(l, " "), max(h.width, 20), "…"))
			}
		}
	}
	for _, p := range c.problems {
		out = append(out, " "+st.dim(ansi.Truncate(clean(p), max(h.width-2, 18), "…")))
	}
	if c.note != "" {
		out = append(out, " "+st.dim(c.note))
	}
	return append(out, " "+st.dim("a adds to the Desk · ⏎ adopt (#385) · esc"))
}

// sessionRowClick selects the row and says adopting is not built yet.
func (h *home) sessionRowClick(key string) {
	i := slices.IndexFunc(h.onDesk, func(d deskSession) bool { return d.key() == key })
	if i < 0 {
		return
	}
	h.selSess = key
	h.sessNote = "adopting a session comes in #385 · /close takes it off the Desk"
}

// closeSession is /close with a session row selected: it archives that
// row and leaves every Chat alone.
func (h *home) closeSession() bool {
	if h.selSess == "" {
		return false
	}
	key := h.selSess
	h.selSess, h.sessNote = "", ""
	if slices.ContainsFunc(h.onDesk, func(d deskSession) bool { return d.key() == key }) {
		h.archiveSession(key)
		h.note = "taken off the Desk · the session itself is untouched"
		return true
	}
	return false
}
