package cli

import (
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
