package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// A key after ten minutes without one ends an absence: the header gains
// its brief, read from the Ledgers, until the human sends a message.
func TestHomeAwayBrief(t *testing.T) {
	h := testHome(100)
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	h.away.last = now

	// A cut waits at its tamper Gate in a live process.
	h.root = t.TempDir()
	h.away.private = t.TempDir()
	dir := filepath.Join(h.away.private, "runs", "20261101T090000-abcdef")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		typ  string
		data any
	}{
		{run.RecRunStarted, map[string]any{"run": "20261101T090000-abcdef", "source": h.root, "chat": "c1"}},
		{run.RecGateOpened, map[string]any{"pins": map[string]any{"gate": "gate.tamper"}}},
	} {
		if err := l.Append(r.typ, r.data); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	if err := os.WriteFile(filepath.Join(dir, "live.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	key := tea.KeyPressMsg{Code: 'a', Text: "a"}
	now = now.Add(5 * time.Minute)
	h.Update(key)
	if _, ok := h.brief(); ok || h.briefLine() != "" {
		t.Fatalf("five minutes is no absence: line %q", h.briefLine())
	}

	// Away twenty minutes: the brief so far is there before any key.
	now = now.Add(20 * time.Minute)
	if b, ok := h.brief(); !ok || b.Totals.NeedsYou != 1 {
		t.Fatalf("brief under way = %+v, %v", b, ok)
	}
	if h.briefLine() != "" {
		t.Errorf("the header line shows before the human is back: %q", h.briefLine())
	}

	h.Update(key)
	want := "while you were away · 20m · ▲1"
	if got := h.briefLine(); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
	if !strings.Contains(h.header(100), want) {
		t.Errorf("header = %q", h.header(100))
	}
	// The next key keeps it: the brief waits to be read.
	now = now.Add(time.Second)
	h.Update(key)
	if b, ok := h.brief(); !ok || b.Away() != 20*time.Minute {
		t.Fatalf("after another key: brief = %+v, %v", b, ok)
	}
	h.clearBrief()
	if _, ok := h.brief(); ok || h.briefLine() != "" {
		t.Errorf("cleared: line %q", h.briefLine())
	}
}

// The machine's sleep is the wall clock's lead over the monotonic one.
func TestAwayWindowSleep(t *testing.T) {
	now := time.Now()
	if w := awayWindow(now, now.Add(time.Hour)); w.Slept != 0 {
		t.Errorf("no sleep: %v", w.Slept)
	}
	// Without a monotonic reading both clocks agree: no sleep is claimed.
	if w := awayWindow(now.Round(0), now.Round(0).Add(time.Hour)); w.Slept != 0 {
		t.Errorf("injected clock: %v", w.Slept)
	}
}

// The window is wall clock, so an absence over a closed lid counts in
// full: neither end keeps its monotonic reading.
func TestAwayWindowWallClock(t *testing.T) {
	now := time.Now()
	w := awayWindow(now, now.Add(time.Hour))
	if w.From != w.From.Round(0) || w.To != w.To.Round(0) {
		t.Errorf("the window keeps a monotonic reading: %v – %v", w.From, w.To)
	}
}

// Focus lost starts away at once; focus regained or a resize (a
// reattach) ends it without a key.
func TestHomeAwayFocus(t *testing.T) {
	h := testHome(100)
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	h.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})

	// Read for five minutes without a key, then leave.
	now = now.Add(5 * time.Minute)
	left := now
	h.Update(tea.BlurMsg{})
	now = now.Add(9 * time.Minute)
	if _, ok := h.brief(); ok {
		t.Fatal("nine minutes since focus was lost is no absence yet")
	}
	now = now.Add(3 * time.Minute)
	h.Update(tea.FocusMsg{})
	b := h.away.back
	if b == nil || !b.Window.From.Equal(left) || b.Away() != 12*time.Minute {
		t.Fatalf("focus-in: brief = %+v, want one from %v", b, left)
	}
	if h.briefLine() != "while you were away · 12m" {
		t.Errorf("line = %q", h.briefLine())
	}

	// A reattach's resize ends away as well.
	h.clearBrief()
	h.Update(tea.BlurMsg{})
	now = now.Add(30 * time.Minute)
	h.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if b := h.away.back; b == nil || b.Away() != 30*time.Minute {
		t.Fatalf("resize: brief = %+v", b)
	}
	// A short blur is no absence.
	h.clearBrief()
	h.Update(tea.BlurMsg{})
	now = now.Add(time.Minute)
	h.Update(tea.FocusMsg{})
	if h.away.back != nil {
		t.Errorf("a minute's blur made a brief: %+v", h.away.back)
	}
}

// The brief reads every Run its window can hold, not the newest few:
// each live one whatever its age, and each written in the window.
func TestRunsSince(t *testing.T) {
	private, root, other := t.TempDir(), t.TempDir(), t.TempDir()
	from := time.Now().Add(-time.Hour)
	mk := func(id, source string, written time.Time, live bool) {
		dir := filepath.Join(private, "runs", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		l, err := ledger.Create(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Append(run.RecRunStarted, map[string]any{"run": id, "source": source, "chat": "c1"}); err != nil {
			t.Fatal(err)
		}
		l.Close()
		if err := os.Chtimes(filepath.Join(dir, ledger.LedgerFile), written, written); err != nil {
			t.Fatal(err)
		}
		if live {
			if err := os.WriteFile(filepath.Join(dir, "live.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := from.Add(-24 * time.Hour)
	mk("20261030T090000-aaaaaa", root, old, true)  // live, a day old: kept
	mk("20261030T100000-bbbbbb", root, old, false) // dead before the window
	mk("20261031T090000-cccccc", root, from.Add(time.Minute), false)
	mk("20261031T100000-dddddd", other, from.Add(time.Minute), false) // another repository
	// Enough newer Runs to push the live one past any cap of the newest.
	for i := range 60 {
		mk(fmt.Sprintf("20261101T09%04d-eeeeee", i), root, from.Add(2*time.Minute), false)
	}
	runs, err := runsSince(private, root, from)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range runs {
		got[r.ID] = true
	}
	if len(runs) != 62 || !got["20261030T090000-aaaaaa"] || !got["20261031T090000-cccccc"] ||
		got["20261030T100000-bbbbbb"] || got["20261031T100000-dddddd"] {
		t.Fatalf("runsSince read %d Runs: %v", len(runs), got)
	}
	if !runs[len(runs)-1].Live {
		t.Errorf("the oldest, live Run isn't Live: %+v", runs[len(runs)-1])
	}
}
