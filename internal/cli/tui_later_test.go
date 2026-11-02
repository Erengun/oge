package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

func altL() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'l', Mod: tea.ModAlt} }

// laterHome is a Desk home with a real desk.log under a temp tree, so
// what is kept can be read back as a restart would.
func laterHome(t *testing.T) (*home, string) {
	t.Helper()
	private := t.TempDir()
	d, err := run.OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Release)
	h := deskHome()
	h.hist, h.sel = &history{}, &setupState{}
	h.desk, h.absence.private = d, private
	h.root = "/work/oge"
	h.repoCtx.root = h.root
	return h, private
}

func stored(t *testing.T, private string) []run.LaterItem {
	t.Helper()
	recs, err := run.ReadDesk(private)
	if err != nil {
		t.Fatal(err)
	}
	return run.LaterItems(recs)
}

// /later <text>, the composer key and the Inbox key each keep without a
// prompt; the repo is recorded silently; nothing starts.
func TestLaterCaptureRoutes(t *testing.T) {
	h, private := laterHome(t)
	submit(h, "/later look at the flaky test")
	if h.sessNote != "◇ kept for later" || len(h.later) != 1 || h.turn != nil || len(h.views) != 1 {
		t.Fatalf("/later: note %q, kept %d, views %d", h.sessNote, len(h.later), len(h.views))
	}
	typeText(h, "rename the receipt verb")
	if l, _ := h.statusParts(); !strings.Contains(strings.Join(l, " "), "alt+l later") {
		t.Error("the hint isn't shown for a draft")
	}
	h.Update(altL())
	if !h.comp.empty() || len(h.later) != 2 || h.sessNote != "◇ kept for later" {
		t.Fatalf("alt+l: box %q, kept %d, note %q", h.comp.text(), len(h.later), h.sessNote)
	}
	if l, _ := h.statusParts(); strings.Contains(strings.Join(l, " "), "alt+l later") {
		t.Error("the hint stays on an empty box")
	}
	h.Update(ctrlSpace())
	typeText(h, "try the other parser")
	h.Update(altL())
	if h.inbox == nil || !h.inbox.comp.empty() || h.inbox.note != "◇ kept for later" || len(h.views) != 1 {
		t.Fatalf("Inbox alt+l: inbox %v, views %d", h.inbox != nil, len(h.views))
	}
	items := stored(t, private)
	if len(items) != 3 || items[2].Text != "try the other parser" || items[0].Repo != "/work/oge" {
		t.Fatalf("stored %+v", items)
	}
	// A blank draft keeps nothing.
	h.inbox = nil
	h.Update(altL())
	if len(h.later) != 3 {
		t.Errorf("an empty box kept something: %d", len(h.later))
	}
}

// Items survive a restart: a new home reads them back, oldest first.
func TestLaterSurvivesARestart(t *testing.T) {
	h, private := laterHome(t)
	submit(h, "/later one")
	submit(h, "/later two")
	h.removeLater(h.later[0].ID, run.LaterDeleted)
	h.desk.Release()
	got := stored(t, private)
	if len(got) != 1 || got[0].Text != "two" || got[0].At.IsZero() {
		t.Fatalf("after a restart: %+v", got)
	}
	fi, err := osStat(filepath.Join(private, run.DeskDir, run.DeskLog))
	if err != nil || fi != 0o600 {
		t.Errorf("desk.log mode %o, %v", fi, err)
	}
}

// The rail's fold says how many; with none it says nothing; NO_COLOR
// leaves the glyph and the words alone.
func TestLaterRailCount(t *testing.T) {
	h, _ := railHome(120, 3)
	if strings.Contains(strings.Join(railCol(h), "\n"), "later") {
		t.Fatal("the fold says later with nothing kept")
	}
	h.later = []run.LaterItem{{ID: "a", Text: "x"}, {ID: "b", Text: "y"}}
	if !strings.Contains(strings.Join(railCol(h), "\n"), "◇ 2 later") {
		t.Errorf("rail:\n%s", strings.Join(railCol(h), "\n"))
	}
	if got := h.View().Content; strings.Contains(got, "\x1b") {
		t.Errorf("colour without a colour theme: %q", got)
	}
	if len(h.inboxItems()) != 0 {
		t.Error("Later made an Inbox card")
	}
}

// /later lists oldest first; d deletes; esc closes; Enter promotes
// through /new's path, and the item is gone.
func TestLaterListPromoteDelete(t *testing.T) {
	h, private := laterHome(t)
	var started []string
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		started = append(started, tk.Title)
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	for _, s := range []string{"first idea", "second idea", "third idea"} {
		submit(h, "/later "+s)
	}
	submit(h, "/later")
	if h.laterOn == nil {
		t.Fatal("/later didn't open")
	}
	v := strings.Join(strings.Split(h.View().Content, "\n"), "\n")
	if i, j := strings.Index(v, "first idea"), strings.Index(v, "third idea"); i < 0 || j < i || !strings.Contains(v, "oge") {
		t.Fatalf("list:\n%s", v)
	}
	h.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if len(h.later) != 2 || h.later[0].Text != "second idea" {
		t.Fatalf("after d: %+v", h.later)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if h.laterOn != nil || len(started) != 0 || len(h.views) != 1 {
		t.Fatal("esc closed and still acted")
	}
	submit(h, "/later")
	h.Update(enter())
	if h.laterOn != nil || len(h.views) != 2 || h.num != 2 || len(started) != 1 || started[0] != "second idea" {
		t.Fatalf("Enter: open %v, views %v, started %v", h.laterOn != nil, nums(h), started)
	}
	if got := stored(t, private); len(got) != 1 || got[0].Text != "third idea" {
		t.Errorf("stored after promote: %+v", got)
	}
	if !strings.Contains(h.note, "stays open") {
		t.Errorf("note %q: not /new's path", h.note)
	}
}

// Promoting past the soft cap says the same thing /new does.
func TestLaterPromoteAtTheCapWarns(t *testing.T) {
	h, _ := laterHome(t)
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	h.turn = &chatTurn{}
	for range 2 {
		h.views = append(h.views, &chatView{repoCtx: &repoCtx{}, num: len(h.views) + 1, turn: &chatTurn{}})
	}
	submit(h, "/later cap one")
	submit(h, "/later")
	h.Update(enter())
	if !strings.Contains(h.note, "3 Chats are working") || strings.Contains(h.note, "alt+l") {
		t.Errorf("note %q", h.note)
	}
	// The promote did not use up /new's offer.
	h.note = ""
	h.turn = &chatTurn{}
	h.newCmd(nil)
	if !strings.Contains(h.note, "alt+l keeps it for later") {
		t.Errorf("/new at the cap: note %q", h.note)
	}
}

// One Chat, one kept idea: the rail is drawn and counts it.
func TestRailShowsLaterWithOneChat(t *testing.T) {
	h, _ := railHome(120, 1)
	h.later = []run.LaterItem{{}}
	if !h.railOn() || !strings.Contains(h.View().Content, "1 later") {
		t.Errorf("rail %v\n%s", h.railOn(), h.View().Content)
	}
}

// Nothing becomes a Chat on its own: not a free slot, not a cleared cap,
// not time passing, not a tick or a key elsewhere.
func TestLaterNeverPromotesItself(t *testing.T) {
	h, private := laterHome(t)
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	h.turn = &chatTurn{}
	for range 3 {
		h.views = append(h.views, &chatView{repoCtx: &repoCtx{}, num: len(h.views) + 1, turn: &chatTurn{}})
	}
	submit(h, "/later wait for a slot")
	// The cap clears and a slot frees.
	for _, v := range h.views {
		v.turn = nil
	}
	h.turn = nil
	now = now.Add(72 * time.Hour)
	h.Update(deskTickMsg{view: h.chatView})
	h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if len(h.views) != 4 || len(h.later) != 1 || len(stored(t, private)) != 1 {
		t.Errorf("views %v, later %d", nums(h), len(h.later))
	}
	for _, v := range h.views {
		if v.turn != nil {
			t.Error("a turn started")
		}
	}
}

// The text never reaches a notification, a toast or the away messages.
func TestLaterTextIsNeverPushed(t *testing.T) {
	h, _ := laterHome(t)
	h.notify = &notifier{attention: true, runEnd: true, bell: true}
	typeText(h, "/later SECRET-IDEA-TEXT")
	if _, cmd := h.Update(enter()); cmd != nil {
		t.Errorf("a keep started a command")
	}
	if h.toast != nil || len(h.later) != 1 {
		t.Errorf("toast %v", h.toast)
	}
	if len(h.inboxItems()) != 0 {
		t.Error("an Inbox card")
	}
}

// oge add --later through the Desk's socket lands in the home's list,
// the repo recorded; with the Desk gone it goes straight to the store.
func TestAddLaterOverTheSocketAndDirect(t *testing.T) {
	h, private := laterHome(t)
	reply := make(chan deskAddReply, 1)
	h.update(deskAddMsg{repo: "/elsewhere/proj", task: "from another terminal", later: true, reply: reply})
	if r := <-reply; r.err != nil {
		t.Fatal(r.err)
	}
	if len(h.views) != 1 || len(h.later) != 1 || h.later[0].Repo != "/elsewhere/proj" || h.later[0].Text != "from another terminal" {
		t.Fatalf("views %d later %+v", len(h.views), h.later)
	}
	if got := stored(t, private); len(got) != 1 {
		t.Fatalf("stored %+v", got)
	}
	h.desk.Release()
	direct, err := run.KeepForLater(private, "/r", "no desk open", time.Now())
	if err != nil || !direct {
		t.Fatalf("direct %v, %v", direct, err)
	}
	if got := stored(t, private); len(got) != 2 || got[1].Text != "no desk open" {
		t.Errorf("stored %+v", got)
	}
}
