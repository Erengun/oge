package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// fullHome is a home on the full screen at w by h, its WindowSizeMsg sent.
func fullHome(w, h int) *home {
	hm := testHome(w)
	hm.full, hm.tr = true, &transcript{}
	hm.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return hm
}

func screenLines(h *home) []string { return strings.Split(h.View().Content, "\n") }

func TestFullScreenFillsTheScreenWithTheBoxAtTheBottom(t *testing.T) {
	h := fullHome(80, 24)
	v := h.View()
	if !v.AltScreen {
		t.Fatal("not on the alternate screen")
	}
	lines := screenLines(h)
	if len(lines) != 24 {
		t.Fatalf("%d rows, want 24", len(lines))
	}
	if !strings.HasPrefix(lines[len(lines)-1], "╰─ ● ready") || !strings.Contains(lines[len(lines)-1], "/ commands") {
		t.Errorf("the status is not in the bottom border: %q", lines[len(lines)-1])
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("row %d is %d columns", i, w)
		}
	}
}

func TestFullScreenTranscriptScrollsAndShowsTheChip(t *testing.T) {
	h := fullHome(80, 24)
	for i := range 60 {
		h.println("line " + string(rune('A'+i%26)) + strings.Repeat("x", i))
	}
	last := func() string { return strings.Join(screenLines(h), "\n") }
	if !strings.Contains(last(), "line H") || strings.Contains(last(), "jump to latest") {
		t.Fatalf("not pinned to the bottom:\n%s", last())
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if !strings.Contains(last(), "jump to latest") {
		t.Errorf("no chip after scrolling up:\n%s", last())
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	if strings.Contains(last(), "jump to latest") {
		t.Errorf("the chip stayed after ctrl+end")
	}
	// New output while scrolled up keeps the view where it is.
	h.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	before := last()
	h.println("a new line")
	if got := last(); strings.Contains(got, "a new line") || !strings.Contains(got, "jump to latest") {
		t.Errorf("the view moved under the human:\n%s\n--- before\n%s", got, before)
	}
}

func TestFullScreenSidebarNeedsRoomAndToggles(t *testing.T) {
	if h := fullHome(109, 30); h.sidebarOn() || strings.Contains(h.View().Content, "SESSION") {
		t.Error("sidebar at 109 columns")
	}
	h := fullHome(120, 30)
	if !strings.Contains(h.View().Content, "SESSION") || !strings.Contains(h.View().Content, "ATTENTION") {
		t.Fatal("no sidebar at 120 columns")
	}
	if h.width != 120-sidebarWidth-1 {
		t.Errorf("main column is %d wide", h.width)
	}
	h.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if strings.Contains(h.View().Content, "SESSION") || h.width != 120 {
		t.Error("ctrl+t did not hide the sidebar")
	}
}

func TestFullScreenKeysNeverDecide(t *testing.T) {
	// The scroll, sidebar and toast keys must leave the composer and any
	// pending decision alone.
	h := fullHome(120, 30)
	h.comp.insert("draft")
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyPgUp}, {Code: tea.KeyPgDown}, {Code: 't', Mod: tea.ModCtrl}, {Code: 'y', Mod: tea.ModAlt}} {
		h.Update(k)
	}
	if h.comp.text() != "draft" || h.quit || h.run != nil || h.turn != nil {
		t.Errorf("a navigation key changed the session: %q", h.comp.text())
	}
}

func TestMarkdownWithoutColourWritesNoEscapes(t *testing.T) {
	st := newStyles(false)
	got := st.renderMarkdown("## Title\n\nSome `code` and **bold** text.\n\n- one\n- two\n\n```go\nfunc A() {}\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n", 60, 2)
	if strings.Contains(got, "\x1b") {
		t.Errorf("escapes with no colour: %q", got)
	}
	for _, want := range []string{"Title", "`code`", "bold text", "• one", "func A() {}", "┌", "│ 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
	if strings.Contains(got, "##") || strings.Contains(got, "**") || strings.Contains(got, "```") {
		t.Errorf("raw marks left:\n%s", got)
	}
}

func TestDiffBlockDropsGitHeaders(t *testing.T) {
	patch := "diff --git a/add.go b/add.go\nindex 1..2 100644\n--- a/add.go\n+++ b/add.go\n@@ -1,2 +1,2 @@\n-old\n+new\n ctx"
	got := diffBlock(newStyles(false), patch, 60, false)
	if strings.Contains(got, "index ") || strings.Contains(got, "--- ") || strings.Contains(got, "diff --git") {
		t.Errorf("headers left:\n%s", got)
	}
	if !strings.Contains(got, "add.go +1 -1") || !strings.Contains(got, "+new") {
		t.Errorf("no summary or hunk:\n%s", got)
	}
}

func TestSelectionIsClampedToThePaneAndCopiesRawText(t *testing.T) {
	h := fullHome(80, 24)
	h.println("first line of text")
	h.println("second line")
	h.View()
	t0 := h.tr
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: headRows})
	h.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 500, Y: 500}) // far past the pane
	if got := t0.sel[1]; got.col >= t0.vw || got.line >= t0.start+t0.vh {
		t.Errorf("head %v is outside the pane", got)
	}
	if text := t0.selected(); !strings.Contains(text, "rst line of text") {
		t.Errorf("selection %q", text)
	}
}

func TestDiffBlockKeepsDashedDeletionsAndHeldBackNotices(t *testing.T) {
	patch := "# Öge: .claude/settings.json is an agent-config change, held back\n" +
		"diff --git a/.claude/settings.json b/.claude/settings.json\nindex 1..2 100644\n--- a/.claude/settings.json\n+++ b/.claude/settings.json\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/schema.sql b/schema.sql\nindex 1..2 100644\n--- a/schema.sql\n+++ b/schema.sql\n@@ -1,2 +1 @@\n--- old comment\n keep\n"
	got := diffBlock(newStyles(false), patch, 80, true)
	for _, want := range []string{"# Öge: .claude/settings.json", "held back", "--- old comment", "schema.sql -1"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
	if strings.Index(got, "# Öge:") > strings.Index(got, "schema.sql") {
		t.Errorf("notice is not above its file:\n%s", got)
	}
}

func TestFullKeyLeavesKeysTheFileRebound(t *testing.T) {
	h := fullHome(80, 24)
	h.setKeys(defaultKeyMap.with(map[keyOverride][]string{{ctxComposer, actStash}: {"alt+y"}}))
	if _, ok := h.fullKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt}); ok {
		t.Error("alt+y was taken though the file bound it")
	}
	if _, ok := h.fullKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}); !ok || !h.sidebarOff {
		t.Error("ctrl+t did not toggle the sidebar by default")
	}
}
