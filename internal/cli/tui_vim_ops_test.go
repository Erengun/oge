package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/task"
)

// vimRun sets the text up, presses Esc and then keys, where <esc> is Esc.
func vimRun(text, keys string) *home {
	h := testHome(80)
	h.vim = true
	h.comp.insert(text)
	h.Update(escKey())
	for keys != "" {
		if rest, ok := strings.CutPrefix(keys, "<esc>"); ok {
			h.Update(escKey())
			keys = rest
			continue
		}
		typeText(h, keys[:1])
		keys = keys[1:]
	}
	return h
}

func TestVimOperators(t *testing.T) {
	for _, tc := range []struct {
		name, text, keys string
		want             string
		pos              [2]int
		normal           bool
	}{
		{"dw", "foo bar baz", "0dw", "bar baz", [2]int{0, 0}, true},
		{"dw on the last word", "foo bar baz", "dw", "foo bar ba", [2]int{0, 9}, true},
		{"dw in the middle", "foo bar baz", "0wdw", "foo baz", [2]int{0, 4}, true},
		{"dw stops at the line end", "foo bar\nbaz", "kwdw", "foo \nbaz", [2]int{0, 3}, true},
		{"2dw", "a b c d", "02dw", "c d", [2]int{0, 0}, true},
		{"de", "foo bar", "0de", " bar", [2]int{0, 0}, true},
		{"db", "foo bar", "$db", "foo r", [2]int{0, 4}, true},
		{"d$", "foo bar", "0wd$", "foo ", [2]int{0, 3}, true},
		{"d0", "foo bar", "$d0", "r", [2]int{0, 0}, true},
		{"cw acts like ce", "foo bar", "0cwX<esc>", "X bar", [2]int{0, 0}, true},
		{"cw inside a word", "foo bar", "0lcwX<esc>", "fX bar", [2]int{0, 1}, true},
		{"cw on a word's last letter", "foo bar", "0llcwX<esc>", "foX bar", [2]int{0, 2}, true},
		{"cw stays INSERT until Esc", "foo bar", "0cwX", "X bar", [2]int{0, 1}, false},
		{"ciw", "foo bar", "0wciwX<esc>", "foo X", [2]int{0, 4}, true},
		{"diw on a blank", "foo  bar", "0lllldiw", "foobar", [2]int{0, 3}, true},
		{"diw", "foo bar", "0wdiw", "foo ", [2]int{0, 3}, true},
		{"x", "foo bar", "0x", "oo bar", [2]int{0, 0}, true},
		{"3x", "foo bar", "03x", " bar", [2]int{0, 0}, true},
		{"x at the end", "foo", "x", "fo", [2]int{0, 1}, true},
		{"x on an empty line", "", "x", "", [2]int{0, 0}, true},
		{"x takes a whole grapheme", "a👍🏽b", "0lx", "ab", [2]int{0, 1}, true},
		{"D", "foo bar", "0wD", "foo ", [2]int{0, 3}, true},
		{"D all", "foo bar", "0D", "", [2]int{0, 0}, true},
		{"dd middle", "a\nb\nc", "kdd", "a\nc", [2]int{1, 0}, true},
		{"dd last", "a\nb\nc", "dd", "a\nb", [2]int{1, 0}, true},
		{"dd only line", "abc", "dd", "", [2]int{0, 0}, true},
		{"2dd", "a\nb\nc", "kk2dd", "c", [2]int{0, 0}, true},
		{"dj", "a\nb\nc", "kkdj", "c", [2]int{0, 0}, true},
		{"dk", "a\nb\nc", "dk", "a", [2]int{0, 0}, true},
		{"cc", "a\nb", "ccX<esc>", "a\nX", [2]int{1, 0}, true},
		{"cc over lines", "a\nb\nc", "kk2ccX<esc>", "X\nc", [2]int{0, 0}, true},
		{"yy p", "a\nb", "yyp", "a\nb\nb", [2]int{2, 0}, true},
		{"yy P", "a\nb", "yyP", "a\nb\nb", [2]int{1, 0}, true},
		{"yy k p", "one\ntwo", "kyyjp", "one\ntwo\none", [2]int{2, 0}, true},
		{"dd p", "a\nb\nc", "kddp", "a\nc\nb", [2]int{2, 0}, true},
		{"2p", "a", "yy2p", "a\na\na", [2]int{1, 0}, true},
		{"yw $ p", "foo bar", "0yw$p", "foo barfoo ", [2]int{0, 10}, true},
		{"dw P", "foo bar", "0dwP", "foo bar", [2]int{0, 3}, true},
		{"yiw leaves the text", "foo bar", "0yiw$p", "foo barfoo", [2]int{0, 9}, true},
		{"y keeps the cursor on a line", "a\nb", "yy", "a\nb", [2]int{1, 0}, true},
		{"di quote", `say "hi there" ok`, "0wdi\"", `say "" ok`, [2]int{0, 5}, true},
		{"ci quote before the pair", `say "hi" ok`, "0ci\"X<esc>", `say "X" ok`, [2]int{0, 5}, true},
		{"di(", "f(a b) x", "0lldi(", "f() x", [2]int{0, 2}, true},
		{"ci(", "f(a b)", "0llci(X<esc>", "f(X)", [2]int{0, 2}, true},
		{"di( nested", "f(a, (b)) x", "0llllllldi(", "f(a, ()) x", [2]int{0, 6}, true},
		{"di( on the close", "f(a b)", "di(", "f()", [2]int{0, 2}, true},
		{"di( over lines", "f(a\nb)", "0di(", "f()", [2]int{0, 2}, true},
		{"di( outside", "ab", "di(", "ab", [2]int{0, 1}, true},
		{"unknown after d cancels", "ab", "0dzx", "b", [2]int{0, 0}, true},
		{"esc cancels d", "ab", "0d<esc>x", "b", [2]int{0, 0}, true},
		{"dot dw", "a b c d", "0dw.", "c d", [2]int{0, 0}, true},
		{"dot x", "abcd", "0x.", "cd", [2]int{0, 0}, true},
		{"dot dd", "a\nb\nc", "kkdd.", "c", [2]int{0, 0}, true},
		{"dot ciw", "foo bar baz", "0ciwX<esc>w.", "X X baz", [2]int{0, 2}, true},
		{"dot cw with text", "ab cd ef", "0cwxy<esc>w.", "xy xy ef", [2]int{0, 4}, true},
		{"dot of an insert", "ab", "0iX<esc>.", "XXab", [2]int{0, 0}, true},
		{"dot of p", "a b", "0yw$p.", "a ba a ", [2]int{0, 6}, true},
		{"dot without a change", "ab", "0.", "ab", [2]int{0, 0}, true},
		{"u", "ab", "0xu", "ab", [2]int{0, 0}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := vimRun(tc.text, tc.keys)
			if got := h.comp.text(); got != tc.want || at(h) != tc.pos || h.normal != tc.normal {
				t.Errorf("%q + %q: %q at %v normal=%v, want %q at %v normal=%v",
					tc.text, tc.keys, got, at(h), h.normal, tc.want, tc.pos, tc.normal)
			}
		})
	}
}

func TestVimUndoTakesBackTheWholeChange(t *testing.T) {
	for _, tc := range []struct{ text, keys string }{
		{"foo bar", "0dw"}, {"foo bar", "0wde"}, {"foo bar", "0x"}, {"foo bar", "0wD"},
		{"a\nb\nc", "kdd"}, {"a\nb\nc", "dd"}, {"a\nb\nc", "2dd"},
		{"foo bar", "0cwXYZ<esc>"}, {"foo bar", "0ciwXYZ<esc>"}, {"a\nb", "ccXYZ<esc>"},
		{"a\nb", "yyp"}, {"a\nb", "yyP"}, {"foo bar", "0yw$p"},
		{`say "hi"`, "0wci\"XYZ<esc>"}, {"f(a)", "0lldi("}, {"ab", "oXYZ<esc>"}, {"ab", "OXYZ<esc>"},
		{"foo bar", "0ciwX<esc>w."},
	} {
		h := vimRun(tc.text, tc.keys)
		if h.comp.text() == tc.text {
			t.Errorf("%q + %q changed nothing", tc.text, tc.keys)
		}
		typeText(h, "u")
		if tc.keys == "0ciwX<esc>w." { // the second change comes back first
			if h.comp.text() != "X bar" {
				t.Errorf("%q + %q + u: %q", tc.text, tc.keys, h.comp.text())
			}
			typeText(h, "u")
		}
		if h.comp.text() != tc.text || !h.normal {
			t.Errorf("%q + %q + u: %q normal=%v, want the text back", tc.text, tc.keys, h.comp.text(), h.normal)
		}
	}
}

func TestVimRegisterIsTheKillRing(t *testing.T) {
	h := vimRun("foo bar", "0dw")
	h.Update(edKey(tea.ModCtrl, 'y'))
	if h.comp.text() != "foo bar" {
		t.Errorf("ctrl+y after dw: %q", h.comp.text())
	}
	h = vimRun("foo bar", "0w")
	h.Update(edKey(tea.ModCtrl, 'k'))
	typeText(h, "0P")
	if h.comp.text() != "barfoo " {
		t.Errorf("P after ctrl+k: %q", h.comp.text())
	}
}

func TestVimDotNeverSubmits(t *testing.T) {
	h := vimRun("foo bar", "0cwX")
	runs := 0
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		runs++
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	h.hist = &history{}
	h.sel = &setupState{}
	h.Update(enter())
	if runs != 1 {
		t.Fatalf("enter started %d runs", runs)
	}
	h.comp.set("")
	h.Update(escKey())
	typeText(h, "inext")
	h.Update(escKey())
	typeText(h, ".")
	if runs != 1 {
		t.Errorf("dot started another run: %d", runs)
	}
	// a menu opened in the insert is not replayed either
	h = vimRun("foo", "0cw")
	typeText(h, "/")
	h.Update(escKey())
	h.Update(escKey())
	typeText(h, ".")
	if h.menu != nil {
		t.Error("dot opened the slash menu")
	}
}

func TestVimCountsMultiply(t *testing.T) {
	for _, tc := range []struct{ text, keys, want string }{
		{"a b c d e f g", "02d3w", "g"},
		{"a b c d e f g", "03d2w", "g"},
		{"a b c d e f g", "02dw3.", "f g"},
		{"a b c d e f g", "02dw.", "e f g"},
		{"a\nb\nc\nd\ne", "kkkk2d2d", "e"},
	} {
		if h := vimRun(tc.text, tc.keys); h.comp.text() != tc.want {
			t.Errorf("%q + %q: %q, want %q", tc.text, tc.keys, h.comp.text(), tc.want)
		}
	}
}

func TestVimPutsLinesOnlyAfterALineKill(t *testing.T) {
	h := vimRun("a b\nc", "yykyw")
	typeText(h, "$p") // charwise now: the yw replaced the line
	if h.comp.text() != "a ba \nc" {
		t.Errorf("p after yy then yw: %q", h.comp.text())
	}
	h = vimRun("ab\ncd", "ddk")
	h.Update(edKey(tea.ModCtrl, 'k'))
	typeText(h, "p")
	if h.comp.text() != "ab" {
		t.Errorf("p after dd then ctrl+k put a line: %q", h.comp.text())
	}
}
