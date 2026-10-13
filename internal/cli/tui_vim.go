package cli

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"
)

// The composer's vim mode (#156), off unless `vim = true` is in the user
// config: INSERT is the composer as it is; Esc enters NORMAL, where h j k l,
// w e b, 0 ^ $ move and i a I A o O go back to INSERT. Operators, edits and
// text objects are #157 (tui_vim_ops.go), visual mode #158. Enter still runs
// the task in both modes (as Claude Code's vim mode does), and Ctrl keys keep
// their Emacs meaning. Any other plain key is swallowed in NORMAL.

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

func runeClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return clsBlank
	case isWordRune(r):
		return clsWord
	}
	return clsPunct
}

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
			out = append(out, cell{r, g, runeClass(l[g])})
		}
		if r+1 < len(c.lines) {
			out = append(out, cell{r, len(l), clsBlank})
		}
	}
	return out
}

// cellIndex is the cell under the cursor.
func (c *composer) cellIndex() ([]cell, int) {
	cs := c.cells()
	i := 0
	for j, x := range cs {
		if x.row == c.row && x.col <= c.col && (x.cls != clsBlank || x.col < len(c.lines[x.row])) {
			i = j
		}
	}
	return cs, i
}

// vimWord moves the cursor by w (to), e (end) or b (previous).
func (c *composer) vimWord(key string) {
	cs, i := c.wordIdx(key)
	i = min(i, len(cs)-1)
	c.row, c.col = cs[i].row, min(cs[i].col, lastCol(c.lines[cs[i].row]))
}

// wordIdx is the cell a word motion lands on; for w it is len(cs) when no
// word follows, which an operator needs (dw on the last word takes it whole).
func (c *composer) wordIdx(key string) ([]cell, int) {
	cs, i := c.cellIndex()
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
	return cs, i
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
	h.endInsert()
	l := h.comp.lines[h.comp.row]
	if h.comp.col > 0 { // vim steps back onto the character it was after
		h.comp.col = snapCol(l, h.comp.col-1, -1)
	}
	h.comp.col = min(h.comp.col, lastCol(l))
}

func (h *home) enterInsert() {
	h.normal = false
	h.comp.lastOp = ""
	h.changed = true
	if h.insMark == 0 {
		h.insMark = len(h.comp.undo) + 1
	}
}

// normalKey applies a NORMAL-mode key and reports whether it took it. Enter,
// Ctrl keys, arrows and the other named keys go on as in INSERT; so does ?
// on an empty box, which opens the key panel.
func (h *home) normalKey(k tea.KeyPressMsg) bool {
	c := h.comp
	if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) != 0 {
		return false
	}
	if k.Text == "?" && c.empty() {
		return false // an empty box opens the panel
	}
	h.cmd = append(h.cmd, k)
	if d := int(k.Text[0] - '0'); len(k.Text) == 1 && d >= 0 && d <= 9 && (d > 0 || h.count > 0) {
		h.count = h.count*10 + d
		return true
	}
	n := max(h.count, 1)
	if h.vimOp != "" {
		h.operatorKey(k.Text, n)
		h.endCommand()
		return true
	}
	l := c.lines[c.row]
	switch k.Text {
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
		h.insMark = len(c.undo) + 1
		c.change("newline", func() { c.col = len(l); c.newline() })
		h.enterInsert()
	case "O":
		h.insMark = len(c.undo) + 1
		c.change("newline", func() { c.col = 0; c.newline(); c.row-- })
		h.enterInsert()
	case "h":
		for range n {
			c.col = snapCol(l, max(c.col-1, 0), -1)
		}
	case "l":
		for range n {
			if c.col < lastCol(l) {
				c.col = snapCol(l, c.col+1, 1)
			}
		}
	case "j":
		for range n {
			c.vimLine(1)
		}
	case "k":
		for range n {
			c.vimLine(-1)
		}
	case "w", "e", "b":
		for range n {
			c.vimWord(k.Text)
		}
	case "0":
		c.col = 0
	case "^":
		c.col = firstNonBlank(l)
	case "$":
		c.col = lastCol(l)
	case "d", "c", "y":
		h.vimOp = k.Text
		return true // the command goes on with the next key
	case "x":
		h.operate("d", "l", n)
	case "D":
		h.operate("d", "$", n)
	case "p":
		h.put(true, n)
	case "P":
		h.put(false, n)
	case "u":
		for range n {
			c.undoStep()
		}
		c.col = min(snapCol(c.lines[c.row], c.col, -1), lastCol(c.lines[c.row]))
	case ".":
		h.repeat()
	}
	h.endCommand()
	return true
}
