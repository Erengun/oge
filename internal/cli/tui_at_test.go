package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func atHome(files ...string) *home {
	h := testHome(80)
	h.listFiles = func() ([]string, error) { return files, nil }
	return h
}

func TestAtPickerGolden(t *testing.T) {
	h := atHome("README.md", "add.go", "add_test.go", "cmd/oge/main.go", "docs/adr/0001.md")
	typeText(h, "fix @")
	golden(t, "at-picker", frame(h))
	typeText(h, "add")
	golden(t, "at-picker-filtered", frame(h))
}

func TestAtPickerKeys(t *testing.T) {
	h := atHome("add.go", "add_test.go", "main.go", "has space.go")
	typeText(h, "fix @ad")
	if h.at.list == nil {
		t.Fatal("no picker after @ad")
	}
	if got := len(h.at.list.Items()); got != 2 {
		t.Fatalf("%d matches for ad (a name with a space is never offered)", got)
	}
	// Down moves in the picker, never into history; Tab inserts the path.
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	h.Update(tab())
	if h.comp.text() != "fix @add_test.go " || h.at.list != nil || h.panel != nil {
		t.Fatalf("after Tab: %q", h.comp.text())
	}
	// Enter inserts and never submits.
	typeText(h, "and @mai")
	h.Update(enter())
	if h.comp.text() != "fix @add_test.go and @main.go " || h.run != nil {
		t.Fatalf("after Enter: %q (run %v)", h.comp.text(), h.run != nil)
	}
	// Esc closes it and keeps the text; more typing reopens it.
	h.comp.set("see @ma")
	h.syncAt()
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.at.list != nil || h.comp.text() != "see @ma" || h.quit {
		t.Fatalf("Esc: picker %v text %q quit %v", h.at.list != nil, h.comp.text(), h.quit)
	}
	typeText(h, "i")
	if h.at.list == nil {
		t.Error("typing again didn't reopen the picker")
	}
	// An @ inside a word (an address) opens nothing.
	h.comp.set("mail me@add")
	h.syncAt()
	if h.at.list != nil {
		t.Error("picker on an @ inside a word")
	}
	// A mention in the middle of the text is replaced in place.
	h.comp.set("look at @mai now")
	for range len(" now") {
		h.comp.move(0, -1)
	}
	h.syncAt()
	h.Update(tab())
	if h.comp.text() != "look at @main.go  now" {
		t.Errorf("mid-text mention: %q", h.comp.text())
	}
}

func TestAtPickerLargeRepoAndCache(t *testing.T) {
	var files []string
	for i := range 500 {
		files = append(files, fmt.Sprintf("pkg/f%03d.go", i))
	}
	loads := 0
	h := testHome(80)
	h.listFiles = func() ([]string, error) { loads++; return files, nil }
	typeText(h, "@pkg")
	got := frame(h)
	if !strings.Contains(got, "+492 more") || strings.Count(got, "pkg/f") != 8 {
		t.Errorf("large list:\n%s", got)
	}
	typeText(h, "/f00")
	if loads != 1 {
		t.Errorf("file list loaded %d times", loads)
	}
}

func TestAtPickerEscapesPaths(t *testing.T) {
	h := atHome("a\x1b]0;pwned\x07.go")
	h.listFiles = func() ([]string, error) { return []string{"x y.go", "ok.go"}, nil }
	typeText(h, "@")
	if strings.ContainsAny(frame(h), "\x1b\x07") {
		t.Error("control bytes reached the picker")
	}
}

func TestAtMentionEndToEnd(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSessionWith(t, f, func(*Env) {
		writeFile(t, filepath.Join(filepath.Dir(f.repo), "agent.sh"), []byte(recordTurn+fixScript))
	}, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add in @add")
	s.expect("add.go")
	s.type_("\r") // picks add.go; never submits
	s.expect("in @add.go")
	s.type_("\r")
	s.ended()
	turn := f.out(t, "implementer-1.turn")
	if !strings.Contains(turn, "@add.go") {
		t.Errorf("the Briefing's task lacks @add.go:\n%s", turn)
	}
}
