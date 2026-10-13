package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/task"
)

func vimHome(text string) *home {
	h := testHome(80)
	h.vim = true
	typeText(h, text)
	return h
}

// at is the cursor as row:col.
func at(h *home) [2]int { return [2]int{h.comp.row, h.comp.col} }

func TestVimModeSwitches(t *testing.T) {
	h := vimHome("abc")
	if h.normal {
		t.Fatal("vim starts in INSERT")
	}
	h.Update(escKey())
	if !h.normal || h.comp.col != 2 {
		t.Fatalf("esc: normal=%v col=%d, want NORMAL on the last character", h.normal, h.comp.col)
	}
	typeText(h, "z") // swallowed
	if h.comp.text() != "abc" {
		t.Errorf("a plain key typed in NORMAL: %q", h.comp.text())
	}
	typeText(h, "i")
	typeText(h, "X")
	if h.normal || h.comp.text() != "abXc" {
		t.Errorf("i: normal=%v text=%q", h.normal, h.comp.text())
	}
	h.Update(escKey())
	typeText(h, "a")
	typeText(h, "Y")
	if h.comp.text() != "abXcY" && h.comp.text() != "abXYc" {
		t.Errorf("a: %q", h.comp.text())
	}
	for _, c := range []struct{ keys, want string }{
		{"A!", "ab!"}, {"I!", "!ab"}, {"o!", "ab\n!"}, {"O!", "!\nab"},
	} {
		h := vimHome("ab")
		h.Update(escKey())
		typeText(h, c.keys[:1])
		if h.normal {
			t.Errorf("%s left NORMAL on", c.keys[:1])
		}
		typeText(h, c.keys[1:])
		if h.comp.text() != c.want {
			t.Errorf("%s: %q, want %q", c.keys[:1], h.comp.text(), c.want)
		}
	}
}

func TestVimEnterSubmitsInBothModes(t *testing.T) {
	for _, normal := range []bool{false, true} {
		h := vimHome("fix Add")
		if normal {
			h.Update(escKey())
		}
		var got string
		h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
			got = tk.Text
			return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
		}
		h.hist = &history{}
		h.sel = &setupState{}
		h.Update(enter())
		if got != "fix Add" {
			t.Errorf("normal=%v: enter started %q", normal, got)
		}
	}
}

func TestVimMotions(t *testing.T) {
	norm := func(text string) *home {
		h := vimHome(text)
		h.Update(escKey())
		return h
	}
	h := norm("one two\n  three.four\n\nfin")
	// 0 $ ^ on the last line, then up through the lines.
	typeText(h, "0")
	typeText(h, "$")
	if at(h) != [2]int{3, 2} {
		t.Errorf("$ on fin: %v", at(h))
	}
	typeText(h, "k") // the empty line
	if at(h) != [2]int{2, 0} {
		t.Errorf("k: %v", at(h))
	}
	typeText(h, "k")
	if at(h) != [2]int{1, 0} { // from column 0 of the empty line
		t.Errorf("k from the empty line: %v", at(h))
	}
	typeText(h, "0")
	typeText(h, "^")
	if at(h) != [2]int{1, 2} {
		t.Errorf("^: %v", at(h))
	}
	steps := []struct {
		key  string
		want [2]int
	}{
		{"w", [2]int{1, 7}}, {"w", [2]int{1, 8}}, {"w", [2]int{2, 0}}, {"w", [2]int{3, 0}},
		{"b", [2]int{2, 0}}, {"b", [2]int{1, 8}}, {"b", [2]int{1, 7}}, {"b", [2]int{1, 2}}, {"b", [2]int{0, 4}},
		{"e", [2]int{0, 6}}, {"e", [2]int{1, 6}}, {"e", [2]int{1, 7}},
	}
	for _, s := range steps {
		typeText(h, s.key)
		if at(h) != s.want {
			t.Fatalf("%s: %v, want %v", s.key, at(h), s.want)
		}
	}
	// h and l stop at the line's edges.
	typeText(h, "0")
	typeText(h, "h")
	if at(h) != [2]int{1, 0} {
		t.Errorf("h at the start: %v", at(h))
	}
	typeText(h, "$")
	typeText(h, "l")
	if at(h) != [2]int{1, 11} {
		t.Errorf("l at the end: %v", at(h))
	}
	// j past the last line stays.
	typeText(h, "j")
	typeText(h, "j")
	typeText(h, "j")
	if h.comp.row != 3 {
		t.Errorf("j past the end: row %d", h.comp.row)
	}
}

func TestVimMotionsAreGraphemeAware(t *testing.T) {
	h := vimHome("日本 éa\n日本語ab")
	h.Update(escKey())
	typeText(h, "k")
	typeText(h, "0")
	typeText(h, "l")
	if h.comp.col != 1 {
		t.Errorf("l over a wide character: %d", h.comp.col)
	}
	typeText(h, "w") // next word: "é" (e + combining accent)
	if h.comp.col != 3 {
		t.Errorf("w: %d, want 3", h.comp.col)
	}
	typeText(h, "l") // one cluster, not the accent alone
	if h.comp.col != 5 {
		t.Errorf("l over e+accent: %d, want 5", h.comp.col)
	}
	typeText(h, "h")
	if h.comp.col != 3 {
		t.Errorf("h back over e+accent: %d, want 3", h.comp.col)
	}
	typeText(h, "$")
	if h.comp.col != 4 && h.comp.col != 5 {
		t.Errorf("$: %d", h.comp.col)
	}
	// j keeps the display column: 日本 (4 columns) over 日本語 -> 本 (col 1).
	h = vimHome("日本語ab\nxxxxxx")
	h.Update(escKey())
	typeText(h, "0")
	typeText(h, "j")
	typeText(h, "l")
	typeText(h, "l")
	typeText(h, "l")
	typeText(h, "l") // x at column 4
	typeText(h, "k")
	if at(h) != [2]int{0, 2} { // columns 4-5 are 語
		t.Errorf("k over wide characters: %v", at(h))
	}
}

func TestVimModeIndicator(t *testing.T) {
	h := vimHome("fix Add")
	golden(t, "home-vim-insert", frame(h))
	h.Update(escKey())
	golden(t, "home-vim-normal", frame(h))
	if got := frame(testHome(80)); strings.Contains(got, "--") {
		t.Errorf("vim off shows a mode: %q", got)
	}
}
