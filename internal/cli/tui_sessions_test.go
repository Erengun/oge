package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
)

// feed runs cmd and hands its messages back to the home, as the program
// would: a Batch is flattened.
func feed(h *home, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch m := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range m {
			feed(h, c)
		}
	case sessionsMsg:
		h.Update(m)
	}
}

func slash(h *home, s string) {
	typeText(h, s)
	_, cmd := h.Update(enter())
	feed(h, cmd)
}

// sessionsHome is reposHome with discovery faked: Claude and Codex
// sessions in alpha, one in beta, and Codex's reason when asked.
func sessionsHome(t *testing.T, codexWhy string) (*home, map[string]string) {
	h, roots := reposHome(t, "alpha", "beta")
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	h.discover = func(_ context.Context, rs []string) sessionFound {
		all := []agent.NativeSession{
			{Provider: "claude", Profile: "claude", ID: "c1", Title: "fix the parser", LastActive: ago(4), Project: roots["alpha"]},
			{Provider: "codex", ID: "x1", Title: "", LastActive: ago(1), Project: roots["alpha"]},
			{Provider: "claude", Profile: "claude@work", ID: "c2", Title: "a very long title about nothing in particular that must be cut to fit", LastActive: ago(90), Project: roots["alpha"]},
			{Provider: "claude", Profile: "claude", ID: "b1", Title: "beta thing", LastActive: ago(600), Project: roots["beta"]},
		}
		f := sessionFound{}
		for _, s := range all {
			for _, r := range rs {
				if repoKey(r) == repoKey(s.Project) {
					f.sessions = append(f.sessions, s)
				}
			}
		}
		if codexWhy != "" {
			f.problems = []string{codexWhy}
		}
		return f
	}
	return h, roots
}

func screenText(h *home) string { return ansi.Strip(h.View().Content) }

func TestSessionsCatalogListsNewestFirstAt80Columns(t *testing.T) {
	h, _ := sessionsHome(t, "")
	h.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.termW = 80
	slash(h, "/sessions")
	got := screenText(h)
	for _, l := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("%d columns: %q", w, l)
		}
	}
	i1, i2, i3 := strings.Index(got, "◌ codex · (untitled) · last active 1m"), strings.Index(got, "◌ claude · fix the parser · last active 4m"), strings.Index(got, "claude@work")
	if i1 < 0 || i2 < i1 || i3 < i2 {
		t.Errorf("not newest first, or a row is wrong:\n%s", got)
	}
	if strings.Contains(got, "beta thing") {
		t.Errorf("another project's session is in this catalog:\n%s", got)
	}
	if !strings.Contains(got, "last active 1h") {
		t.Errorf("no age for the 90 minute row:\n%s", got)
	}
	_, cat, _ := strings.Cut(got, "Sessions ·") // under the header, which has its own ●
	for _, g := range []string{"▲", "●", "✓", "✗"} {
		if strings.Contains(cat, g) {
			t.Errorf("catalog shows %s:\n%s", g, got)
		}
	}
	if strings.Contains(h.View().Content, "\x1b[") {
		t.Errorf("a plain style drew escape codes")
	}
}

func TestSessionsAllSpansProjectsWithATag(t *testing.T) {
	h, roots := sessionsHome(t, "")
	pin(h, []string{roots["beta"]})
	slash(h, "/sessions all")
	got := screenText(h)
	if !strings.Contains(got, "beta thing · last active 10h · beta") || !strings.Contains(h.catalogTitle(), "all projects") {
		t.Errorf("/sessions all:\n%s", got)
	}
}

func TestSessionsShowsAProviderReason(t *testing.T) {
	h, _ := sessionsHome(t, "codex isn't installed, so its sessions aren't listed")
	slash(h, "/sessions")
	got := screenText(h)
	if !strings.Contains(got, "codex isn't installed") || !strings.Contains(got, "fix the parser") {
		t.Errorf("reason or Claude rows missing:\n%s", got)
	}
}

func TestSessionsLoadsOffTheUIGoroutine(t *testing.T) {
	h, _ := sessionsHome(t, "")
	typeText(h, "/sessions")
	_, cmd := h.Update(enter())
	if cmd == nil || !strings.Contains(screenText(h), "looking for sessions") {
		t.Fatalf("the catalog should open at once and wait:\n%s", screenText(h))
	}
	feed(h, cmd)
	if strings.Contains(screenText(h), "looking for sessions") {
		t.Error("still waiting after the discovery came back")
	}
}

func TestSessionAddEnterEscAndRail(t *testing.T) {
	h, roots := sessionsHome(t, "")
	slash(h, "/sessions")
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := screenText(h); !strings.Contains(got, "#385") {
		t.Errorf("Enter does not say adoption is later:\n%s", got)
	}
	if len(h.onDesk) != 0 {
		t.Fatal("Enter added a row")
	}
	typeText(h, "a") // the newest: the Codex one
	press(h, tea.KeyPressMsg{Code: tea.KeyDown})
	typeText(h, "a") // fix the parser
	typeText(h, "a") // again: already there
	if got := screenText(h); !strings.Contains(got, "already on the Desk") {
		t.Errorf("a second a:\n%s", got)
	}
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.chats != nil || h.catalog != nil {
		t.Fatal("esc left the catalog open")
	}
	if len(h.onDesk) != 2 || h.onDesk[0].provider != "codex" || h.onDesk[1].id != "c1" || h.onDesk[1].repo != roots["alpha"] {
		t.Fatalf("on the Desk: %+v", h.onDesk)
	}
	rail := railText(h)
	if !strings.Contains(rail, "◌ (untit") || !strings.Contains(rail, "◌ fix th") {
		t.Errorf("rail:\n%s", rail)
	}
	for _, g := range []string{"▲", "●", "✓", "✗"} {
		for _, l := range strings.Split(rail, "\n") {
			if strings.Contains(l, "◌") && strings.Contains(l, g) {
				t.Errorf("a session row shows %s: %q", g, l)
			}
		}
	}
	if strings.Contains(rail, "claude") || strings.Contains(rail, "beta thing") {
		t.Errorf("something entered the rail without a:\n%s", rail)
	}
}

func rowY(t *testing.T, h *home, want string) int {
	t.Helper()
	for y, l := range railCol(h) {
		if strings.Contains(l, want) {
			return y
		}
	}
	t.Fatalf("no rail row %q:\n%s", want, railText(h))
	return 0
}

func TestSessionRowsNeverReorderClickAndArchive(t *testing.T) {
	h, _ := sessionsHome(t, "")
	slash(h, "/sessions")
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyDown})
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyDown})
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	order := func() []string {
		var out []string
		for _, l := range railCol(h) {
			if strings.Contains(l, "◌") {
				out = append(out, strings.TrimSpace(strings.Trim(l, "│ ")))
			}
		}
		return out
	}
	before := order()
	if len(before) != 3 {
		t.Fatalf("rows: %q", before)
	}
	// Time passes and a rerun refreshes ages: the order does not move.
	slash(h, "/sessions")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := order(); strings.Join(got, "|") != strings.Join(before, "|") {
		// only the ages may change, never the order
		for i := range got {
			if strings.Fields(got[i])[1] != strings.Fields(before[i])[1] {
				t.Errorf("reordered: %q -> %q", before, got)
			}
		}
	}
	// A click selects, says adopting is later, and /close archives that row.
	y := rowY(t, h, "◌ fix th")
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: y})
	if got := screenText(h); !strings.Contains(got, "#385") {
		t.Errorf("a click says nothing:\n%s", got)
	}
	chats := len(h.views)
	slash(h, "/close")
	if len(h.views) != chats {
		t.Error("/close closed a Chat, not the row")
	}
	if len(h.onDesk) != 2 || strings.Contains(railText(h), "◌ fix th") {
		t.Errorf("not archived: %+v\n%s", h.onDesk, railText(h))
	}
	// A row that stays keeps its place, and the archived one is back in the catalog.
	if got := order(); len(got) != 2 {
		t.Errorf("rows: %q", got)
	}
	slash(h, "/sessions")
	if got := screenText(h); !strings.Contains(got, "fix the parser · last active 4m") || strings.Contains(got, "fix the parser · last active 4m · on the Desk") {
		t.Errorf("archived session not back as plain catalog row:\n%s", got)
	}
}

func TestCloseWithNoSelectedRowStillClosesTheChat(t *testing.T) {
	h, _ := sessionsHome(t, "")
	slash(h, "/sessions")
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	h.views = append(h.views, h.newView())
	n := len(h.views)
	slash(h, "/close")
	if len(h.onDesk) != 1 || len(h.views) != n-1 {
		t.Errorf("sessions %d, Chats %d -> %d", len(h.onDesk), n, len(h.views))
	}
}

func TestSessionRowsSurviveARestart(t *testing.T) {
	private := t.TempDir()
	d, err := run.OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	h, roots := sessionsHome(t, "")
	h.chat.desk = d
	slash(h, "/sessions")
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyDown})
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyDown})
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	y := rowY(t, h, "◌ fix th")
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: y})
	slash(h, "/close") // archived: not restored
	d.Close(true)

	got := restoreSessions(private)
	if len(got) != 2 || got[0].id != "x1" || got[1].id != "c2" || got[1].repo != roots["alpha"] || got[1].profile != "claude@work" {
		t.Fatalf("restored %+v", got)
	}
	h2, _ := sessionsHome(t, "")
	h2.onDesk = got
	rail := railText(h2)
	if !strings.Contains(rail, "◌ (untit") || !strings.Contains(rail, "◌ a very") || strings.Contains(rail, "fix th") {
		t.Errorf("restored rail:\n%s", rail)
	}
	if !strings.HasSuffix(filepath.Base(roots["alpha"]), "alpha") {
		t.Fatal("setup")
	}
}

func TestSessionAgeRefreshesOnlyWhenSessionsRerun(t *testing.T) {
	h, _ := sessionsHome(t, "")
	slash(h, "/sessions")
	typeText(h, "a") // codex, 1m ago: "now"
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	later := h.clock().Add(30 * time.Minute)
	h.now = func() time.Time { return later }
	if rail := railText(h); !strings.Contains(rail, "30m") && !strings.Contains(rail, "31m") {
		t.Errorf("age is measured from the last activity:\n%s", rail)
	}
	if h.onDesk[0].last.Equal(later) {
		t.Error("the last activity moved without a rerun")
	}
}

func TestStaleRowSelectionDoesNotTakeOverClose(t *testing.T) {
	h, _ := sessionsHome(t, "")
	slash(h, "/sessions")
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	h.views = append(h.views, h.newView())
	y := rowY(t, h, "◌ ")
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: y})
	if h.selSess == "" {
		t.Fatal("row not selected")
	}
	// Another command, then /close: the Chat closes, the row stays.
	slash(h, "/help")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	n, rows := len(h.views), len(h.onDesk)
	slash(h, "/close")
	if len(h.views) != n-1 || len(h.onDesk) != rows {
		t.Errorf("Chats %d -> %d, rows %d -> %d", n, len(h.views), rows, len(h.onDesk))
	}
}

func TestSessionRefreshSurvivesARestart(t *testing.T) {
	private := t.TempDir()
	d, err := run.OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	h, _ := sessionsHome(t, "")
	h.chat.desk = d
	slash(h, "/sessions")
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	h.onDesk[0].last = h.onDesk[0].last.Add(-72 * time.Hour) // as if added 3d ago
	want := h.onDesk[0].last.Add(72 * time.Hour)
	slash(h, "/sessions")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	d.Close(true)
	got := restoreSessions(private)
	if len(got) != 1 || !got[0].last.Equal(want) {
		t.Errorf("restored %+v, want last %v", got, want)
	}
}
