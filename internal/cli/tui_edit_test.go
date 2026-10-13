package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/task"
)

func edKey(mod tea.KeyMod, r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: mod} }

var edKeys = map[string]tea.KeyPressMsg{
	"kill-end":   edKey(tea.ModCtrl, 'k'),
	"kill-start": edKey(tea.ModCtrl, 'u'),
	"kill-back":  edKey(tea.ModCtrl, 'w'),
	"kill-fwd":   edKey(tea.ModAlt, 'd'),
	"yank":       edKey(tea.ModCtrl, 'y'),
	"word-left":  edKey(tea.ModAlt, 'b'),
	"word-right": edKey(tea.ModAlt, 'f'),
	"undo":       edKey(tea.ModCtrl, '_'),
	"undo-z":     edKey(tea.ModCtrl, 'z'),
	"left":       {Code: tea.KeyLeft},
	"home":       {Code: tea.KeyHome},
	"end":        {Code: tea.KeyEnd},
	"up":         {Code: tea.KeyUp},
}

// edComposer is a composer holding text, its cursor at (row, col).
func edComposer(text string, row, col int) *composer {
	c := newComposer()
	c.insert(text)
	c.row, c.col = row, col
	return c
}

func TestComposerEditKeys(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		row, col int
		keys     []string
		want     string
		wrow     int
		wcol     int
		kills    string // "" skips the check
	}{
		{"kill to end", "hello world", 0, 5, []string{"kill-end"}, "hello", 0, 5, " world"},
		{"kill to end joins the next line at the end", "ab\ncd", 0, 2, []string{"kill-end"}, "abcd", 0, 2, "\n"},
		{"kill to end in the middle line keeps the others", "a\nbcd\ne", 1, 1, []string{"kill-end"}, "a\nb\ne", 1, 1, "cd"},
		{"kill to start", "hello world", 0, 6, []string{"kill-start"}, "world", 0, 0, "hello "},
		{"kill to start at a line start joins", "ab\ncd", 1, 0, []string{"kill-start"}, "abcd", 0, 2, "\n"},
		{"kill word back", "one two three", 0, 13, []string{"kill-back"}, "one two ", 0, 8, "three"},
		{"kill word back skips spaces and punctuation", "foo.bar  ", 0, 9, []string{"kill-back"}, "foo.", 0, 4, "bar  "},
		{"kill word back counts digits as word", "x v2beta", 0, 8, []string{"kill-back"}, "x ", 0, 2, "v2beta"},
		{"kill word back at line start joins", "ab\ncd", 1, 0, []string{"kill-back"}, "abcd", 0, 2, "\n"},
		{"kill word forward", "one two three", 0, 3, []string{"kill-fwd"}, "one three", 0, 3, " two"},
		{"kill word forward at line end joins", "ab\ncd", 0, 2, []string{"kill-fwd"}, "abcd", 0, 2, "\n"},
		{"wide characters are words", "日本語 テスト", 0, 7, []string{"kill-back"}, "日本語 ", 0, 4, "テスト"},
		{"a combining mark stays with its letter", "cafe\u0301 au", 0, 8, []string{"kill-back", "kill-back"}, "", 0, 0, "cafe\u0301 au"},
		{"word left", "one two", 0, 7, []string{"word-left"}, "one two", 0, 4, ""},
		{"word left over a line break", "ab\ncd", 1, 0, []string{"word-left"}, "ab\ncd", 0, 2, ""},
		{"word right", "one two", 0, 0, []string{"word-right", "word-right"}, "one two", 0, 7, ""},
		{"word right over a line break", "ab\ncd", 0, 2, []string{"word-right"}, "ab\ncd", 1, 0, ""},
		{"emoji is not a word", "hi 👍👍 x", 0, 6, []string{"word-left"}, "hi 👍👍 x", 0, 0, ""},
		{"yank", "abc", 0, 3, []string{"kill-start", "yank", "yank"}, "abcabc", 0, 6, "abc"},
		{"yank of a multi-line kill", "a\nb", 0, 1, []string{"kill-end", "kill-end", "yank"}, "a\nb", 1, 1, "\nb"},
		{"kills in a row append", "one two three", 0, 13, []string{"kill-back", "kill-back"}, "one ", 0, 4, "two three"},
		{"forward kills in a row append", "one two three", 0, 0, []string{"kill-fwd", "kill-fwd"}, " three", 0, 0, "one two"},
		{"a move between kills starts a new entry", "one two three", 0, 13, []string{"kill-back", "left", "kill-back"}, "one  ", 0, 4, "two"},
		{"yank with an empty ring does nothing", "abc", 0, 3, []string{"yank"}, "abc", 0, 3, ""},
		{"undo a kill", "one two", 0, 7, []string{"kill-back", "undo"}, "one two", 0, 7, ""},
		{"undo takes a run of kills at once", "one two three", 0, 13, []string{"kill-back", "kill-back", "undo"}, "one two three", 0, 13, ""},
		{"ctrl+z undoes too", "one two", 0, 7, []string{"kill-back", "undo-z"}, "one two", 0, 7, ""},
		{"undo with nothing to undo", "abc", 0, 1, []string{"undo"}, "abc", 0, 1, ""},
		{"undo a yank", "abc", 0, 3, []string{"kill-start", "yank", "undo"}, "", 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := edComposer(tt.text, tt.row, tt.col)
			for _, k := range tt.keys {
				if !c.key(edKeys[k]) {
					t.Fatalf("%s was not taken", k)
				}
			}
			if got := c.text(); got != tt.want {
				t.Errorf("text %q, want %q", got, tt.want)
			}
			if c.row != tt.wrow || c.col != tt.wcol {
				t.Errorf("cursor (%d,%d), want (%d,%d)", c.row, c.col, tt.wrow, tt.wcol)
			}
			if tt.kills != "" && c.kills != tt.kills {
				t.Errorf("kill ring %q, want %q", c.kills, tt.kills)
			}
		})
	}
}

func TestComposerUndoGroupsAndBounds(t *testing.T) {
	c := newComposer()
	typ := func(s string) {
		for _, r := range s {
			c.key(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	typ("hello")
	c.key(edKeys["left"])
	typ("X")
	c.key(edKeys["undo"])
	if got := c.text(); got != "hello" {
		t.Errorf("after the first undo: %q", got)
	}
	c.key(edKeys["undo"])
	if got := c.text(); got != "" {
		t.Errorf("after the second undo: %q", got)
	}

	c = newComposer()
	for i := 0; i < maxUndo+30; i++ {
		c.key(edKeys["yank"]) // empty ring: no change, nothing recorded
		c.change("x", func() { c.insert("a") })
	}
	if len(c.undo) != maxUndo {
		t.Errorf("undo stack holds %d, want %d", len(c.undo), maxUndo)
	}
	c.clearUndo()
	c.key(edKeys["undo"])
	if c.text() != strings.Repeat("a", maxUndo+30) {
		t.Error("undo after clearUndo changed the text")
	}
}

func TestComposerSetIsUndoable(t *testing.T) {
	c := edComposer("draft", 0, 5)
	c.key(edKeys["kill-start"])
	c.set("recalled")
	c.key(edKeys["yank"])
	if got := c.text(); got != "recalleddraft" {
		t.Errorf("the kill ring survives set: %q", got)
	}
	c.key(edKeys["undo"])
	c.key(edKeys["undo"])
	if got := c.text(); got != "" {
		t.Errorf("undo back past the recall: %q", got)
	}
}

// The home keeps keys the menus and history own: Ctrl+K and friends reach
// the composer, and undo is cleared once the Task is submitted.
func TestHomeEditKeysAndSubmit(t *testing.T) {
	h := testHome(70)
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	h.comp.insert("fix Add please")
	press(h, edKeys["kill-back"])
	if got := h.comp.text(); got != "fix Add " {
		t.Fatalf("ctrl+w: %q", got)
	}
	press(h, edKeys["yank"], edKeys["yank"])
	if got := h.comp.text(); got != "fix Add pleaseplease" {
		t.Fatalf("ctrl+y: %q", got)
	}
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.run == nil {
		t.Fatal("Enter did not start the Run")
	}
	if len(h.comp.undo) != 0 {
		t.Error("undo survived the submit")
	}
}

func homeKey(h *home, k tea.KeyPressMsg) tea.Cmd {
	_, cmd := h.Update(k)
	return cmd
}

func TestBackslashEnterInsertsANewlineWithoutSubmitting(t *testing.T) {
	h := testHome(70)
	h.start = func(task.Task, *resolved, int, int) (*model, tea.Cmd) {
		t.Fatal("a backslash then Enter submitted")
		return nil, nil
	}
	h.comp.insert(`fix \ the bug`)
	h.comp.col = 5 // right after the backslash, in the middle of the text
	homeKey(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := h.comp.text(); got != "fix \n the bug" || h.comp.row != 1 || h.comp.col != 0 {
		t.Fatalf("text %q at %d,%d", got, h.comp.row, h.comp.col)
	}
	homeKey(h, edKey(tea.ModCtrl, 'z')) // one undo step
	if got := h.comp.text(); got != `fix \ the bug` {
		t.Fatalf("after undo: %q", got)
	}
}

func TestStashRoundTrip(t *testing.T) {
	h := testHome(70)
	ctrlS := edKey(tea.ModCtrl, 's')
	h.comp.insert("half a thought\nsecond line")
	homeKey(h, ctrlS)
	if !h.comp.empty() || h.stash != "half a thought\nsecond line" {
		t.Fatalf("stash %q, box %q", h.stash, h.comp.text())
	}
	if f := frame(h); !strings.Contains(f, "stashed") {
		t.Errorf("no stashed hint:\n%s", f)
	}
	homeKey(h, ctrlS) // empty box restores
	if h.comp.text() != "half a thought\nsecond line" || h.stash != "" {
		t.Fatalf("restore: box %q, stash %q", h.comp.text(), h.stash)
	}
	if f := frame(h); strings.Contains(f, "stashed") {
		t.Errorf("hint stayed after restore:\n%s", f)
	}
	homeKey(h, ctrlS) // an empty box and an empty stash do nothing
	homeKey(h, ctrlS)
	h.comp.set("")
	homeKey(h, ctrlS)
	if h.stash != "" || !h.comp.empty() {
		t.Fatalf("empty stash: %q %q", h.stash, h.comp.text())
	}
	// A draft with a stash held swaps, so neither is lost.
	h.comp.set("one")
	homeKey(h, ctrlS)
	h.comp.set("two")
	homeKey(h, ctrlS)
	if h.comp.text() != "one" || h.stash != "two" {
		t.Fatalf("swap: box %q, stash %q", h.comp.text(), h.stash)
	}
}

func TestCtrlLRedrawsAndKeepsTheDraft(t *testing.T) {
	h := testHome(70)
	h.comp.insert("keep me")
	cmd := homeKey(h, edKey(tea.ModCtrl, 'l'))
	if cmd == nil {
		t.Fatal("no command")
	}
	if cmd() == nil || h.comp.text() != "keep me" {
		t.Fatalf("draft %q", h.comp.text())
	}
}

func TestCtrlGIsTheEditorAlias(t *testing.T) {
	h := testHome(70)
	var got string
	h.edit = func(text string) tea.Cmd { got = text; return func() tea.Msg { return nil } }
	h.comp.insert("draft")
	if cmd := homeKey(h, edKey(tea.ModCtrl, 'g')); cmd == nil || got != "draft" {
		t.Fatalf("editor not opened: %q", got)
	}
}
