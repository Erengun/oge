package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

func bigPaste() string { return strings.Repeat("0123456789", 160) } // 1,600 chars

func TestPasteThreshold(t *testing.T) {
	c := newComposer()
	c.insertPaste(strings.Repeat("x", pasteChars))
	if len(c.pastes) != 0 || len(c.lines[0]) != pasteChars {
		t.Fatal("a paste at the limit became a placeholder")
	}
	c = newComposer()
	c.insertPaste(strings.Repeat("x", pasteChars+1))
	if len(c.pastes) != 1 || c.text() == c.expanded() || len(c.lines[0]) != 1 {
		t.Fatalf("a long paste is not a placeholder: %q", c.text())
	}
	c = newComposer()
	c.insertPaste(strings.Repeat("l\n", pasteLines-1) + "l") // 20 lines
	if len(c.pastes) != 0 {
		t.Fatal("20 lines became a placeholder")
	}
	c.insertPaste(strings.Repeat("l\n", pasteLines)) // 21 lines
	if len(c.pastes) != 1 {
		t.Fatal("21 lines did not become a placeholder")
	}
	// Typed or pasted text can't forge a placeholder.
	c = newComposer()
	c.insert(string(rune(pasteBase)))
	if c.text() != "" {
		t.Fatalf("a placeholder rune was typed: %q", c.text())
	}
}

func TestPasteLabelAndExpand(t *testing.T) {
	h := testHome(80)
	h.comp.insert("see: ")
	h.Update(tea.PasteMsg{Content: bigPaste()})
	h.comp.insert(" ok")
	if f := frame(h); !strings.Contains(f, "[pasted 1,600 chars · 1 lines]") || strings.Contains(f, "0123456789") {
		t.Fatalf("frame:\n%s", f)
	}
	if got := h.comp.expanded(); got != "see: "+bigPaste()+" ok" {
		t.Fatalf("expanded: %.40q", got)
	}
}

func TestPastePlaceholderUndoAndBackspace(t *testing.T) {
	c := newComposer()
	c.insert("a")
	c.change("paste", func() { c.insertPaste(bigPaste()) })
	c.undoStep()
	if c.text() != "a" {
		t.Fatalf("undo: %q", c.text())
	}
	c.change("paste", func() { c.insertPaste(bigPaste()) })
	c.key(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if c.text() != "a" {
		t.Fatalf("one Backspace left %q", c.text())
	}
	c.undoStep()
	if c.expanded() != "a"+bigPaste() {
		t.Fatal("undo of the Backspace lost the paste")
	}
}

func TestHiddenDetectionUsesTheDeliveryClassifier(t *testing.T) {
	// The same set as delivery's (#221): zero-width, bidi, BOM and the word
	// joiner are hidden; U+2065 (unassigned) is not.
	s := "a\u200Bb\u202Ec\uFEFFd\u2060e\u2065f"
	if n := hiddenCount(s); n != 4 {
		t.Fatalf("count %d", n)
	}
	if got := withoutHidden(s); got != "abcde\u2065f" {
		t.Fatalf("cleaned %q", got)
	}
	if got := markHidden("x\u200B\ny"); got != "x<U+200B>\ny" {
		t.Fatalf("marked %q", got)
	}
}

func TestHiddenCharactersAskBeforeSubmitting(t *testing.T) {
	var started []string
	h := testHome(80)
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		started = append(started, tk.Text)
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	h.comp.insert("fix\u200B Add")
	press(h, enter())
	if h.run != nil || h.hidden == nil {
		t.Fatal("hidden characters were submitted unannounced")
	}
	f := frame(h)
	if !strings.Contains(f, "fix<U+200B> Add") || !strings.Contains(f, "1 hidden character removed · [enter] submit cleaned · [e] edit · [esc] cancel") {
		t.Fatalf("prompt:\n%s", f)
	}
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.hidden != nil || h.comp.text() != "fix\u200B Add" || h.run != nil {
		t.Fatal("Esc did not leave the box as it was")
	}
	press(h, enter(), tea.KeyPressMsg{Code: 'e', Text: "e"})
	if h.hidden != nil || h.comp.text() != "fix<U+200B> Add" {
		t.Fatalf("e: %q", h.comp.text())
	}
	h.comp.set("fix\u202E Add")
	press(h, enter(), enter())
	if len(started) != 1 || started[0] != "fix Add" {
		t.Fatalf("started %q", started)
	}
}

// A large paste with a zero-width character in it: placeholder, Enter, the
// prompt, Enter, and the Ledger's Task holds the whole text without it.
func TestBareOgePasteWithHiddenCharacter(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	body := "fix Add\n" + strings.Repeat("Add returns 0 for every input.\n", 60) + "zero\u200Bwidth"
	s.type_("\x1b[200~" + body + "\x1b[201~")
	s.expect("[pasted")
	s.type_("\r")
	s.expect("1 hidden character removed")
	s.type_("\r")
	s.ended()
	dirs := runDirs(t, f)
	if len(dirs) != 1 {
		t.Fatalf("%d Runs", len(dirs))
	}
	recs, err := ledger.Replay(dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := ledger.OpenBlobs(dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if rec.Type != run.RecRunStarted {
			continue
		}
		var d struct{ Task string }
		if err := json.Unmarshal(rec.Data, &d); err != nil {
			t.Fatal(err)
		}
		b, err := blobs.Get(d.Task)
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if hiddenCount(got) != 0 || !strings.Contains(got, "zerowidth") || strings.Count(got, "Add returns 0") != 60 {
			t.Fatalf("the recorded Task:\n%s", got)
		}
		s.quitClosing("\x03")
		return
	}
	t.Fatal("no RunStarted record")
}
