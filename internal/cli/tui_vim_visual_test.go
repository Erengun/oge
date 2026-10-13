package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestVimVisual(t *testing.T) {
	for _, tc := range []struct {
		name, text, keys string
		want             string
		reg              string // the kill ring afterwards; "-" leaves it unchecked
		visual           rune
	}{
		{"v d takes the cursor's character", "foo bar", "0vd", "oo bar", "f", 0},
		{"a motion extends: vw", "foo bar baz", "0vwd", "ar baz", "foo b", 0},
		{"x is d", "foo bar", "0vex", " bar", "foo", 0},
		{"y keeps the text and leaves", "foo bar", "0vey$p", "foo barfoo", "foo", 0},
		{"backwards", "foo bar", "$vbd", "foo ", "bar", 0},
		{"c enters INSERT", "foo bar", "0vecX<esc>", "X bar", "foo", 0},
		{"a count: v3l", "abcdef", "0v3ld", "ef", "abcd", 0},
		{"across lines", "ab\ncd\nef", "kk0vjld", "\nef", "ab\ncd", 0},
		{"across lines, upwards", "ab\ncd\nef", "vkd", "ab\nc", "d\nef", 0},
		{"a flag is one grapheme", "a🇹🇷b", "0lvd", "ab", "🇹🇷", 0},
		{"a combining mark stays with its letter", "aéz", "0lvd", "az", "é", 0},
		{"wide characters", "日本語x", "0v2ld", "x", "日本語", 0},
		{"V takes whole lines", "ab\ncd\nef", "kkVjd", "ef", "ab\ncd", 0},
		{"V on the middle line", "ab\ncd\nef", "kVd", "ab\nef", "cd", 0},
		{"V y then p", "ab\ncd", "kVyjp", "ab\ncd\nab", "ab", 0},
		{"V c", "ab\ncd", "kVcX<esc>", "X\ncd", "ab", 0},
		{"v again leaves", "foo", "0vv", "foo", "-", 0},
		{"V again leaves", "foo", "0VV", "foo", "-", 0},
		{"esc leaves", "foo", "0v<esc>", "foo", "-", 0},
		{"v to V", "ab\ncd", "kvV", "ab\ncd", "-", 'V'},
		{"v stays until an operator", "foo", "0vl", "foo", "-", 'v'},
		{"d on an empty line does nothing", "a\n\nb", "kvd", "a\n\nb", "-", 0},
		{"p replaces and fills the register", "foo bar", "0yewvep", "foo foo", "bar", 0},
		{"p over a word with the yank's space", "one two", "0ywwvep", "one one ", "two", 0},
		{"V p replaces a line", "ab\ncd", "kyyjVp", "ab\nab", "cd", 0},
		{"V p over the only line", "ab", "yyVp", "ab", "ab", 0},
		{"p with an empty register", "foo", "0vp", "foo", "-", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := vimRun(tc.text, tc.keys)
			if got := h.comp.text(); got != tc.want {
				t.Errorf("text %q, want %q", got, tc.want)
			}
			if tc.reg != "-" && h.comp.kills != tc.reg {
				t.Errorf("register %q, want %q", h.comp.kills, tc.reg)
			}
			if h.visual != tc.visual {
				t.Errorf("visual %q, want %q", h.visual, tc.visual)
			}
			if tc.visual == 0 && !strings.Contains(tc.keys, "c") && !h.normal {
				t.Error("left NORMAL")
			}
		})
	}
}

func TestVimVisualPutIsOneUndoStep(t *testing.T) {
	h := vimRun("foo bar", "0yewvepu")
	if got := h.comp.text(); got != "foo bar" {
		t.Errorf("u after a visual put: %q", got)
	}
}

func TestVimVisualIsNotWhatDotRepeats(t *testing.T) {
	h := vimRun("abcdef", "0x0vld.")
	if got := h.comp.text(); got != "ef" { // x, then vld, then . repeats the x, not the visual delete
		t.Errorf("text %q", got)
	}
}

func TestVimVisualModeLineAndSelection(t *testing.T) {
	h := vimRun("one two\nthree", "k0vw")
	if !strings.Contains(frame(h), "-- VISUAL --") {
		t.Errorf("mode line:\n%s", frame(h))
	}
	h.Update(escKey())
	if f := frame(h); !strings.Contains(f, "-- NORMAL --") || strings.Contains(f, "\x1b[7m") {
		t.Errorf("after esc:\n%s", f)
	}
	h = vimRun("one two", "0V")
	if !strings.Contains(frame(h), "-- VISUAL LINE --") {
		t.Errorf("mode line:\n%s", frame(h))
	}
}

// The selection is painted in reverse video; the golden shows it as [ ].
func TestVimVisualGolden(t *testing.T) {
	h := vimRun("fix Add\nand 日本語 too", "k0vjw")
	f := strings.NewReplacer("\x1b[7m", "[", "\x1b[m", "]").Replace(frame(h))
	golden(t, "home-vim-visual", f)
	h = vimRun("fix Add\nand too", "kV")
	f = strings.NewReplacer("\x1b[7m", "[", "\x1b[m", "]").Replace(frame(h))
	golden(t, "home-vim-visual-line", f)
}

func TestVimEscapeSequence(t *testing.T) {
	now := time.Unix(1000, 0)
	build := func(seq string) *home {
		h := testHome(80)
		h.vim, h.vimEscape = true, seq
		h.now = func() time.Time { return now }
		return h
	}
	keys := func(h *home, s string, gap time.Duration) {
		for _, r := range s {
			h.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			now = now.Add(gap)
		}
	}
	t.Run("within the window", func(t *testing.T) {
		h := build("jj")
		keys(h, "abjj", 100*time.Millisecond)
		if !h.normal || h.comp.text() != "ab" {
			t.Errorf("normal=%v text=%q, want NORMAL and ab", h.normal, h.comp.text())
		}
		if h.comp.col != 1 {
			t.Errorf("col %d, want 1 (on the b)", h.comp.col)
		}
	})
	t.Run("after the window", func(t *testing.T) {
		h := build("jj")
		keys(h, "abjj", 400*time.Millisecond)
		if h.normal || h.comp.text() != "abjj" {
			t.Errorf("normal=%v text=%q, want INSERT and abjj", h.normal, h.comp.text())
		}
	})
	t.Run("another key between", func(t *testing.T) {
		h := build("jj")
		keys(h, "jxj", 10*time.Millisecond)
		if h.normal || h.comp.text() != "jxj" {
			t.Errorf("normal=%v text=%q", h.normal, h.comp.text())
		}
	})
	t.Run("three js: the third is typed", func(t *testing.T) {
		h := build("jj")
		keys(h, "jjj", 10*time.Millisecond)
		if !h.normal || h.comp.text() != "" {
			t.Errorf("normal=%v text=%q", h.normal, h.comp.text())
		}
		keys(h, "i", 10*time.Millisecond)
		keys(h, "j", 10*time.Millisecond)
		if h.normal || h.comp.text() != "j" {
			t.Errorf("a lone j after i: normal=%v text=%q", h.normal, h.comp.text())
		}
	})
	t.Run("a different sequence", func(t *testing.T) {
		h := build("jk")
		keys(h, "ajk", 10*time.Millisecond)
		if !h.normal || h.comp.text() != "a" {
			t.Errorf("normal=%v text=%q", h.normal, h.comp.text())
		}
	})
	t.Run("off by default", func(t *testing.T) {
		h := build("")
		keys(h, "jj", 10*time.Millisecond)
		if h.normal || h.comp.text() != "jj" {
			t.Errorf("normal=%v text=%q", h.normal, h.comp.text())
		}
	})
	t.Run("undo takes the typing back", func(t *testing.T) {
		h := build("jj")
		keys(h, "abjj", 10*time.Millisecond)
		keys(h, "u", 10*time.Millisecond)
		if h.comp.text() != "" {
			t.Errorf("u: %q", h.comp.text())
		}
	})
}

func TestVimHistoryAtTheEdges(t *testing.T) {
	h := vimRun("draft", "")
	h.hist = &history{entries: []string{"older", "newer"}}
	typeText(h, "k")
	if h.comp.text() != "newer" || !h.navigating {
		t.Fatalf("k on the first line: %q navigating=%v", h.comp.text(), h.navigating)
	}
	if h.comp.col != lastCol([]rune("newer")) || !h.normal {
		t.Errorf("the cursor should rest on the last character: col %d normal %v", h.comp.col, h.normal)
	}
	typeText(h, "k")
	if h.comp.text() != "older" {
		t.Errorf("k again: %q", h.comp.text())
	}
	typeText(h, "j")
	typeText(h, "j")
	if h.comp.text() != "draft" || h.navigating {
		t.Errorf("j past the newest brings the draft back: %q navigating=%v", h.comp.text(), h.navigating)
	}
	typeText(h, "j") // not navigating: nothing to walk
	if h.comp.text() != "draft" {
		t.Errorf("j with no history walk: %q", h.comp.text())
	}
}

func TestVimJKMoveWithinLinesBeforeTheEdge(t *testing.T) {
	h := vimRun("one\ntwo\nthree", "")
	h.hist = &history{entries: []string{"older"}}
	typeText(h, "k")
	if h.comp.text() != "one\ntwo\nthree" || h.comp.row != 1 {
		t.Fatalf("k from the last line: row %d text %q", h.comp.row, h.comp.text())
	}
	typeText(h, "k")
	typeText(h, "k") // now on the first line
	if h.comp.text() != "older" {
		t.Errorf("k on the first line: %q", h.comp.text())
	}
	h = vimRun("one\ntwo", "k")
	h.hist = &history{entries: []string{"older"}}
	typeText(h, "j")
	typeText(h, "j") // the last line, not navigating
	if h.comp.text() != "one\ntwo" {
		t.Errorf("j on the last line: %q", h.comp.text())
	}
}
