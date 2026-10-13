package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHistoryAppendReadAndDedupe(t *testing.T) {
	path := historyPath(t.TempDir(), t.TempDir())
	h := loadHistory(path)
	for _, s := range []string{"fix Add", "multi\nline", "multi\nline", "  ", "add tests"} {
		if err := h.add(s); err != nil {
			t.Fatal(err)
		}
	}
	got := loadHistory(path).entries
	if want := "fix Add|multi\nline|add tests"; strings.Join(got, "|") != want {
		t.Errorf("entries %q, want %q", got, want)
	}
}

func TestHistoryCap(t *testing.T) {
	path := historyPath(t.TempDir(), t.TempDir())
	h := loadHistory(path)
	for i := range historyCap + 20 {
		h.add(fmt.Sprintf("task %d", i))
	}
	got := loadHistory(path).entries
	if len(got) != historyCap || got[0] != "task 20" || got[len(got)-1] != fmt.Sprintf("task %d", historyCap+19) {
		t.Errorf("%d entries from %q to %q", len(got), got[0], got[len(got)-1])
	}
}

func TestHistoryPerRepoKey(t *testing.T) {
	dir, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	if historyPath(dir, a) == historyPath(dir, b) {
		t.Error("two repos share a history file")
	}
	if historyPath(dir, a) != historyPath(dir, filepath.Join(a, "sub", "..")) {
		t.Error("one repo, two spellings, two files")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(a, link); err == nil && historyPath(dir, a) != historyPath(dir, link) {
		t.Error("a symlink to the repo has its own history")
	}
	if p := historyPath(dir, a); filepath.Dir(p) != dir || strings.Contains(p, filepath.Base(a)) {
		t.Errorf("%s should be one opaque file in the state dir", p)
	}
}

func TestHistoryFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	path := historyPath(filepath.Join(t.TempDir(), "history"), t.TempDir())
	h := loadHistory(path)
	h.add("one")
	h.add("two")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	if d, _ := os.Stat(filepath.Dir(path)); d.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", d.Mode().Perm())
	}
}

func TestHistoryToleratesACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	os.WriteFile(path, []byte("\"good one\"\n{not json\n\x00\xff\n\"\"\n\"good two\"\n"), 0o600)
	if got := strings.Join(loadHistory(path).entries, "|"); got != "good one|good two" {
		t.Errorf("entries %q", got)
	}
	if n := loadHistory(filepath.Join(t.TempDir(), "none")).len(); n != 0 {
		t.Errorf("%d entries from a missing file", n)
	}
}

func histHome(entries ...string) *home {
	h := testHome(70)
	h.hist = &history{entries: entries}
	return h
}

func up() tea.Msg    { return tea.KeyPressMsg{Code: tea.KeyUp} }
func ctrlR() tea.Msg { return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl} }
func typed(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

func TestHomeRecallWalksTheHistoryAndBack(t *testing.T) {
	h := histHome("first", "second")
	press(h, up())
	golden(t, "home-history-recall", frame(h))
	press(h, up())
	if h.comp.text() != "first" {
		t.Errorf("↑↑: %q", h.comp.text())
	}
	press(h, up()) // the oldest stays
	if h.comp.text() != "first" {
		t.Errorf("↑ past the oldest: %q", h.comp.text())
	}
	press(h, down())
	if h.comp.text() != "second" {
		t.Errorf("↓: %q", h.comp.text())
	}
	press(h, down())
	if !h.comp.empty() || h.navigating {
		t.Errorf("↓ past the newest: %q, navigating %v", h.comp.text(), h.navigating)
	}
}

func TestHomeRecallKeepsTheDraftAndNeverEditsHistory(t *testing.T) {
	h := histHome("first")
	press(h, typed("draft")...)
	press(h, up())
	press(h, typed(" edited")...)
	press(h, down())
	if h.comp.text() != "draft" {
		t.Errorf("the draft came back as %q", h.comp.text())
	}
	if h.hist.entries[0] != "first" {
		t.Errorf("history changed: %q", h.hist.entries)
	}
}

// ↓ on an empty composer focuses the action row (#184) unless ↑ has started
// walking the history.
func TestHomeDownIsHistoryOnlyAfterUp(t *testing.T) {
	h := histHome("first")
	h.last = &lastRun{}
	press(h, down())
	if !h.actions {
		t.Fatal("↓ on an empty composer without history navigation should focus the actions")
	}
	h.actions = false
	press(h, up())
	press(h, down()) // back to the empty draft
	if h.actions || h.navigating || !h.comp.empty() {
		t.Fatalf("↓ while navigating: actions %v navigating %v text %q", h.actions, h.navigating, h.comp.text())
	}
	press(h, down()) // navigation is over: now the action row
	if !h.actions {
		t.Error("↓ after leaving history should focus the actions")
	}
}

func TestHomeUpMovesWithinMultilineText(t *testing.T) {
	h := histHome("old")
	h.comp.insert("a\nb")
	press(h, up())
	if h.comp.text() != "a\nb" || h.comp.row != 0 {
		t.Fatalf("↑ on line 2 should move the cursor: %q row %d", h.comp.text(), h.comp.row)
	}
	press(h, up())
	if h.comp.text() != "old" {
		t.Errorf("↑ on line 1 should recall: %q", h.comp.text())
	}
}

func TestHomeReverseSearch(t *testing.T) {
	h := histHome("fix Add", "add tests", "refactor Sub", "add docs")
	h.comp.insert("typing")
	press(h, ctrlR())
	press(h, typed("add")...)
	if got := frame(h); !strings.Contains(got, "(reverse-i-search)'add': add docs") {
		t.Errorf("search line:\n%s", got)
	}
	golden(t, "home-history-search", frame(h))
	press(h, ctrlR())
	if h.search.hit != 1 {
		t.Errorf("ctrl+r again: hit %d", h.search.hit)
	}
	press(h, ctrlR()) // "fix Add" matches case-insensitively
	press(h, ctrlR()) // no older match: stays
	if h.search.hit != 0 {
		t.Errorf("hit %d", h.search.hit)
	}
	press(h, typed("zz")...)
	if got := frame(h); !strings.Contains(got, "(failed reverse-i-search)'addzz'") {
		t.Errorf("no failed line:\n%s", got)
	}
	press(h, tea.KeyPressMsg{Code: tea.KeyBackspace}, tea.KeyPressMsg{Code: tea.KeyBackspace})
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.search != nil || h.comp.text() != "add docs" {
		t.Errorf("enter should accept without submitting: search %v text %q", h.search, h.comp.text())
	}
}

func TestHomeReverseSearchEscCancels(t *testing.T) {
	h := histHome("fix Add")
	h.comp.insert("typing")
	press(h, ctrlR())
	press(h, typed("fix")...)
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.search != nil || h.comp.text() != "typing" {
		t.Errorf("esc: search %v text %q", h.search, h.comp.text())
	}
}
