package cli

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"
)

// The composer's vim mode (#156), off unless `vim = true` is in the user
// config: INSERT is the composer as it is; Esc enters NORMAL, where h j k l,
// w e b, 0 ^ $ move and i a I A o O go back to INSERT. Operators, edits and
// text objects are #157, visual mode #158. Enter still runs the task in
// both modes (as Claude Code's vim mode does), and Ctrl keys keep their
// Emacs meaning. Any other plain key is swallowed in NORMAL.

// graphemes returns where each grapheme cluster of l starts.
func graphemes(l []rune) []int {
	var out []int
	n := 0
	for rest := string(l); rest != ""; {
		cl, tail, _, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
		out = append(out, n)
		n += len([]rune(cl))
		rest = tail
	}
	return out
}

// lastCol is where the cursor may go furthest in NORMAL: on the last
// grapheme, or 0 on an empty line.
func lastCol(l []rune) int {
	if g := graphemes(l); len(g) > 0 {
		return g[len(g)-1]
	}
	return 0
}

// Cell classes for w, e and b, as vim's "word": runs of word characters,
// runs of other printable ones, and blanks. An empty line is a word of its own.
const (
	clsBlank = iota
	clsWord
	clsPunct
	clsEmpty
)

type cell struct{ row, col, cls int }

// cells lays the text out as one list: a cluster each, a blank for every
// line break, and one cell for an empty line.
func (c *composer) cells() []cell {
	var out []cell
	for r, l := range c.lines {
		if len(l) == 0 {
			out = append(out, cell{r, 0, clsEmpty})
		}
		for _, g := range graphemes(l) {
			cls := clsPunct
			switch {
			case unicode.IsSpace(l[g]):
				cls = clsBlank
			case isWordRune(l[g]):
				cls = clsWord
			}
			out = append(out, cell{r, g, cls})
		}
		if r+1 < len(c.lines) {
			out = append(out, cell{r, len(l), clsBlank})
		}
	}
	return out
}

// vimWord moves the cursor by w (dir 1, to), e (dir 1, end) or b (dir -1).
func (c *composer) vimWord(key string) {
	cs := c.cells()
	i := 0
	for j, x := range cs {
		if x.row == c.row && x.col <= c.col && (x.cls != clsBlank || x.col < len(c.lines[x.row])) {
			i = j
		}
	}
	n := len(cs)
	switch key {
	case "w":
		if k := cs[i].cls; k == clsWord || k == clsPunct {
			for i < n && cs[i].cls == k {
				i++
			}
		} else {
			i++
		}
		for i < n && cs[i].cls == clsBlank {
			i++
		}
		i = min(i, n-1)
	case "e":
		i = min(i+1, n-1)
		for i < n-1 && (cs[i].cls == clsBlank || cs[i].cls == clsEmpty) {
			i++
		}
		for k := cs[i].cls; i+1 < n && (k == clsWord || k == clsPunct) && cs[i+1].cls == k; {
			i++
		}
	case "b":
		i = max(i-1, 0)
		for i > 0 && cs[i].cls == clsBlank {
			i--
		}
		for k := cs[i].cls; i > 0 && (k == clsWord || k == clsPunct) && cs[i-1].cls == k; {
			i--
		}
	}
	c.row, c.col = cs[i].row, min(cs[i].col, lastCol(c.lines[cs[i].row]))
}

// vimLine moves up or down a line, keeping the display column, so a wide
// character above or below doesn't throw the cursor off.
func (c *composer) vimLine(dr int) {
	r := c.row + dr
	if r < 0 || r >= len(c.lines) {
		return
	}
	want := uniseg.StringWidth(string(c.lines[c.row][:c.col]))
	l := c.lines[r]
	col := 0
	for _, g := range graphemes(l) {
		if uniseg.StringWidth(string(l[:g])) > want {
			break
		}
		col = g
	}
	c.row, c.col = r, min(col, lastCol(l))
}

// firstNonBlank is where ^ lands.
func firstNonBlank(l []rune) int {
	for _, g := range graphemes(l) {
		if !unicode.IsSpace(l[g]) {
			return g
		}
	}
	return lastCol(l)
}

func (h *home) enterNormal() {
	h.normal = true
	h.comp.lastOp = ""
	l := h.comp.lines[h.comp.row]
	if h.comp.col > 0 { // vim steps back onto the character it was after
		h.comp.col = snapCol(l, h.comp.col-1, -1)
	}
	h.comp.col = min(h.comp.col, lastCol(l))
}

func (h *home) enterInsert() {
	h.normal = false
	h.comp.lastOp = ""
}

// normalKey applies a NORMAL-mode key and reports whether it took it. Enter,
// Ctrl keys, arrows and the other named keys go on as in INSERT; so does ?
// on an empty box, which opens the key panel.
func (h *home) normalKey(k tea.KeyPressMsg) bool {
	c := h.comp
	if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) != 0 {
		return false
	}
	l := c.lines[c.row]
	switch k.Text {
	case "?":
		return !c.empty() // an empty box opens the panel
	case "i":
		h.enterInsert()
	case "a":
		if len(l) > 0 {
			c.col = snapCol(l, min(c.col+1, len(l)), 1)
		}
		h.enterInsert()
	case "I":
		c.col = firstNonBlank(l)
		h.enterInsert()
	case "A":
		c.col = len(l)
		h.enterInsert()
	case "o":
		c.change("newline", func() { c.col = len(l); c.newline() })
		h.enterInsert()
	case "O":
		c.change("newline", func() { c.col = 0; c.newline(); c.row-- })
		h.enterInsert()
	case "h":
		c.col = snapCol(l, max(c.col-1, 0), -1)
	case "l":
		if c.col < lastCol(l) {
			c.col = snapCol(l, c.col+1, 1)
		}
	case "j":
		c.vimLine(1)
	case "k":
		c.vimLine(-1)
	case "w", "e", "b":
		c.vimWord(k.Text)
	case "0":
		c.col = 0
	case "^":
		c.col = firstNonBlank(l)
	case "$":
		c.col = lastCol(l)
	}
	if h.normal {
		c.lastOp = ""
	}
	h.endNavigation()
	return true
}
