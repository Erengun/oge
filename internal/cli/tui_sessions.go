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
	"github.com/erengun/oge/internal/workspace"
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
	// viewOnly says, for a project (keyed by repoKey) the Desk can't hold,
	// why: its sessions are listed but not added or adopted.
	viewOnly map[string]string
}

// sessionDiscover finds the native sessions in the project roots, or, when
// all is set, every session on the machine with Project set to the main
// repo of its cwd. It runs off the UI goroutine and starts nothing on a
// timer.
type sessionDiscover func(ctx context.Context, roots []string, all bool) sessionFound

// defaultDiscover reads Claude's session files and asks a short-lived
// Codex app-server, then keeps the sessions inside roots, or groups every
// one by project when all is set (codex is always asked without a cwd filter).
func defaultDiscover(getenv func(string) string) sessionDiscover {
	return discoverWith(getenv, (&codex.Adapter{}).ListAllSessions)
}

// discoverWith is defaultDiscover with codex's listing given. Codex's cwd
// filter matches a directory exactly, so a thread started in a worktree or
// a subdirectory would never come back from it: codex is always listed
// whole, and the folding into projects is done here, on the client.
func discoverWith(getenv func(string) string, listAll func(context.Context) ([]agent.NativeSession, string)) sessionDiscover {
	return func(ctx context.Context, roots []string, all bool) sessionFound {
		var f sessionFound
		found := claude.DiscoverSessions(claude.Profiles(getenv("HOME"), getenv, nil))
		cs, why := listAll(ctx)
		found = append(found, cs...)
		if why != "" {
			f.problems = append(f.problems, why)
		}
		if all {
			f.sessions, f.viewOnly = sessionsByProject(found)
		} else {
			f.sessions = sessionsInProjects(found, roots)
		}
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
	viewOnly map[string]string
	// adopt is the prompt waiting for y (tui_adopt.go); promptOnly is a
	// catalog opened by a click on a ◌ row: it has no list, only the
	// prompt or why there is none.
	adopt      *adoptPrompt
	promptOnly bool
}

type sessionItem struct {
	s        agent.NativeSession
	on       bool
	viewOnly string // why the Desk can't hold this session's project, "" when it can
}

func (i sessionItem) FilterValue() string { return i.s.Title }

// sessionHeader is a project's line in /sessions all. It is never selected.
type sessionHeader struct {
	label    string
	onDesk   bool
	viewOnly string
}

func (i sessionHeader) FilterValue() string { return i.label }

type sessionDelegate struct{ h *home }

func (sessionDelegate) Height() int                         { return 1 }
func (sessionDelegate) Spacing() int                        { return 0 }
func (sessionDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d sessionDelegate) Render(w io.Writer, m list.Model, i int, it list.Item) {
	st := d.h.st
	if hd, ok := it.(sessionHeader); ok {
		line := st.bold(clean(hd.label))
		switch {
		case hd.onDesk:
			line += st.dim(" · on the Desk")
		case hd.viewOnly != "":
			line += st.dim(" · view only")
		}
		fmt.Fprint(w, "  "+ansi.Truncate(line, max(d.h.width-4, 8), "…"))
		return
	}
	x := it.(sessionItem)
	who := x.s.Profile
	if who == "" {
		who = x.s.Provider
	}
	title := clean(x.s.Title)
	if title == "" {
		title = "(untitled)"
	}
	tail := " · last active " + sessionAge(d.h.clock(), x.s.LastActive)
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
		ctx = context.Background()
	}
	return func() tea.Msg { return sessionsMsg{seq, find(ctx, roots, c.all)} }, ""
}

// sessionsFound fills the catalog when its discovery returns, and brings
// the age of the rows already on the Desk up to date: the one time it
// changes besides /sessions being opened again.
func (h *home) sessionsFound(m sessionsMsg) {
	for _, s := range m.found.sessions {
		for i, d := range h.onDesk {
			if d.provider == s.Provider && d.id == s.ID && !s.LastActive.IsZero() {
				title := h.onDesk[i].title
				if s.Title != "" {
					title = s.Title
				}
				if !s.LastActive.Equal(d.last) || title != d.title {
					h.onDesk[i].last, h.onDesk[i].title = s.LastActive, title
					h.deskRecord().SessionSeen(run.SessionAdded{Provider: d.provider, ID: d.id, Title: title, Last: s.LastActive})
				}
			}
		}
	}
	c := h.catalog
	if c == nil || c.seq != m.seq || h.chats == nil {
		return
	}
	c.loading, c.problems, c.found, c.viewOnly = false, m.found.problems, m.found.sessions, m.found.viewOnly
	sort.SliceStable(c.found, func(i, j int) bool { return c.found[i].LastActive.After(c.found[j].LastActive) })
	h.catalogItems()
}

// catalogItems rebuilds the list from the found sessions, keeping the
// selection where it was. With /sessions all the rows sit under a header
// per project, projects ordered by their newest session.
func (h *home) catalogItems() {
	c := h.catalog
	var items []list.Item
	if !c.all {
		for _, s := range c.found {
			if repoKey(s.Project) == repoKey(c.root) {
				items = append(items, sessionItem{s: s, on: h.sessionOn(s) >= 0})
			}
		}
	} else {
		_, desk := h.railGroups()
		var keys, roots []string // projects in order of their newest session
		byKey := map[string][]agent.NativeSession{}
		for _, s := range c.found { // found is newest first
			k := repoKey(s.Project)
			if _, ok := byKey[k]; !ok {
				keys, roots = append(keys, k), append(roots, s.Project)
			}
			byKey[k] = append(byKey[k], s)
		}
		labels := projectLabels(roots)
		for i, k := range keys {
			why := c.viewOnly[k]
			if roots[i] == "" {
				labels[i] = "(no working directory)"
			}
			items = append(items, sessionHeader{
				label:    labels[i],
				onDesk:   slices.ContainsFunc(desk, func(r string) bool { return mainKey(r) == k }),
				viewOnly: why,
			})
			for _, s := range byKey[k] {
				items = append(items, sessionItem{s: s, on: h.sessionOn(s) >= 0, viewOnly: why})
			}
		}
	}
	sel := h.chats.Index()
	h.chats.SetItems(items)
	h.chats.SetHeight(min(len(items), max(h.height-8, 3)) + 4)
	h.chats.Select(min(sel, max(len(items)-1, 0)))
	h.skipHeader(1)
}

// skipHeader moves the selection off a project header, in dir (1 down,
// -1 up), turning round at the end of the list.
func (h *home) skipHeader(dir int) {
	l := h.chats
	items := l.Items()
	i := l.Index()
	for _, d := range []int{dir, -dir} {
		for j := i; j >= 0 && j < len(items); j += d {
			if _, hd := items[j].(sessionHeader); !hd {
				l.Select(j)
				return
			}
		}
	}
}

// sessionOn is where s sits in the On-the-Desk rows, -1 when it is not.
func (h *home) sessionOn(s agent.NativeSession) int {
	return slices.IndexFunc(h.onDesk, func(d deskSession) bool { return d.provider == s.Provider && d.id == s.ID })
}

// mainKey is repoKey of the main repo root belongs to, so a worktree and
// its repo compare equal; repoKey of root itself when it is not a repo.
func mainKey(root string) string {
	if m, err := workspace.MainRoot(root); err == nil {
		return repoKey(m)
	}
	return repoKey(root)
}

// deskRootFor is the root the Desk already holds (a Chat's, or a pinned
// one) for the project at main, when that is a linked worktree of it or
// the repo itself; "" when the Desk holds none.
func (h *home) deskRootFor(main string) string {
	if main == "" {
		return ""
	}
	k := mainKey(main)
	_, desk := h.railGroups()
	for _, r := range desk {
		if mainKey(r) == k {
			return r
		}
	}
	return ""
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
	repo := s.Project
	if r := h.deskRootFor(repo); r != "" {
		repo = r // the project already has a slot, under a linked worktree's root
	}
	d := deskSession{provider: s.Provider, id: s.ID, profile: s.Profile, repo: repo, title: s.Title, last: s.LastActive}
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
	if c.adopt != nil {
		return h.adoptKey(msg)
	}
	if c.promptOnly {
		// Only the reason there is no prompt: esc closes it.
		if k := msg.String(); k == "esc" || k == "ctrl+c" {
			h.chats, h.catalog = nil, nil
		}
		return nil
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		h.chats, h.catalog = nil, nil
	case "a":
		it, ok := h.chats.SelectedItem().(sessionItem)
		switch {
		case !ok:
		case it.viewOnly != "":
			c.note = it.viewOnly
		case it.on:
			c.note = "already on the Desk"
		default:
			h.addSession(it.s)
			c.note = "added to the Desk"
			h.catalogItems()
		}
	case "enter":
		if it, ok := h.chats.SelectedItem().(sessionItem); ok {
			if it.viewOnly != "" {
				c.note, c.adopt = it.viewOnly, nil
				break
			}
			h.askAdopt(c, it.s)
		}
	default:
		before := h.chats.Index()
		l, _ := h.chats.Update(msg)
		h.chats = &l
		dir := 1
		if h.chats.Index() < before {
			dir = -1
		}
		h.skipHeader(dir)
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
	if c.promptOnly || c.adopt != nil {
		return h.adoptLines()
	}
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
	return append(out, " "+st.dim("a adds to the Desk · ⏎ adopt · esc"))
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

// newSessionList is the empty list the catalog fills.
func newSessionList(h *home) list.Model {
	l := list.New(nil, sessionDelegate{h}, 100, 4)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	return l
}

func contextBackground() context.Context { return context.Background() }
