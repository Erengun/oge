package cli

import (
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"
)

// The composer's vim mode (#156), off unless `vim = true` is in the user
// config: INSERT is the composer as it is; Esc enters NORMAL, where h j k l,
// w e b, 0 ^ $ move and i a I A o O go back to INSERT. Operators, edits and
// text objects are #157 (tui_vim_ops.go), visual mode and the vim_escape
// sequence #158. Enter still runs
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
	if h.visual != 0 && k.Code == tea.KeyBackspace && k.Mod == 0 {
		h.motion("h", max(h.count, 1)) // as h, never a deletion
		h.count = 0
		return true
	}
	if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) != 0 {
		return false
	}
	if h.kb.actionFor(ctxComposer, k.Text) == actKeysPanel && c.empty() {
		return false // an empty box opens the panel
	}
	if d := int(k.Text[0] - '0'); len(k.Text) == 1 && d >= 0 && d <= 9 && (d > 0 || h.count > 0) {
		h.count = h.count*10 + d
		return true
	}
	if h.visual != 0 {
		h.visualKey(k.Text, max(h.count, 1))
		h.count = 0
		return true
	}
	h.cmd = append(h.cmd, k)
	n := max(h.count, 1)
	if h.vimOp != "" {
		n *= max(h.opCount, 1)
		h.effCount = n
		h.operatorKey(k.Text, n)
		h.endCommand()
		return true
	}
	h.effCount = n
	l := c.lines[c.row]
	if (k.Text == "k" && c.row == 0) || (k.Text == "j" && c.row == len(c.lines)-1) {
		h.walkHistory(k.Text, n) // k on the first line and j on the last are ↑ and ↓
		h.endCommand()
		return true
	}
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
	case "d", "c", "y":
		h.vimOp, h.opCount, h.count = k.Text, h.count, 0
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
	case "v", "V":
		h.visual, h.anchor = rune(k.Text[0]), [2]int{c.row, c.col}
	default:
		h.motion(k.Text, n)
	}
	h.endCommand()
	return true
}

// motion moves the cursor by one of the NORMAL motions and reports whether
// t was one.
func (h *home) motion(t string, n int) bool {
	c := h.comp
	l := c.lines[c.row]
	switch t {
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
			c.vimWord(t)
		}
	case "0":
		c.col = 0
	case "^":
		c.col = firstNonBlank(l)
	case "$":
		c.col = lastCol(l)
	default:
		return false
	}
	return true
}

// walkHistory is k on the first line or j on the last, n times: ↑ and ↓ as
// history knows them. j only walks while history is being walked: it never
// focuses the action row, so a key typed in NORMAL can't apply a Run
// (ADR-0015). The cursor rests on a character, as NORMAL keeps it.
func (h *home) walkHistory(t string, n int) {
	for range n {
		if t == "k" {
			h.recall(-1)
		} else if h.navigating {
			h.recall(1)
		}
	}
	c := h.comp
	c.col = min(snapCol(c.lines[c.row], c.col, -1), lastCol(c.lines[c.row]))
}

// vimEscapeWindow is how soon the second key of vim_escape must follow the first.
const vimEscapeWindow = 300 * time.Millisecond

// escapeSeq handles the vim_escape sequence ("jj") in INSERT: the second key
// within the window removes the first and acts as Esc. It reports whether it
// took the key.
func (h *home) escapeSeq(k tea.KeyPressMsg) bool {
	seq := []rune(h.vimEscape)
	if len(seq) != 2 {
		return false
	}
	armed := h.escArmed && h.clock().Sub(h.escAt) < vimEscapeWindow
	h.escArmed = false
	r := []rune(k.Text)
	if len(r) != 1 || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) != 0 {
		return false
	}
	c := h.comp
	if armed && r[0] == seq[1] && c.col > 0 && c.lines[c.row][c.col-1] == seq[0] {
		m := len(c.undo)
		c.key(tea.KeyPressMsg{Code: tea.KeyBackspace})
		c.undo = c.undo[:min(m, len(c.undo))] // taking the key back is no undo step of its own
		if h.menu != nil {                    // the first key may have opened the slash menu
			h.menu, h.menuOff, h.menuOffText = nil, true, c.text()
		}
		if n := len(h.insRec); n > 0 {
			h.insRec = h.insRec[:n-1] // . never replays the first key
		}
		h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		return true
	}
	h.escArmed, h.escAt = r[0] == seq[0], h.clock()
	return false
}

// visualRange is what the selection covers: the cursor's grapheme included,
// or whole lines in VISUAL LINE.
func (h *home) visualRange() vimRange {
	c := h.comp
	ar := min(h.anchor[0], len(c.lines)-1)
	al := c.lines[ar]
	a := [2]int{ar, snapCol(al, min(h.anchor[1], len(al)), -1)}
	b := [2]int{c.row, c.col}
	if b[0] < a[0] || (b[0] == a[0] && b[1] < a[1]) {
		a, b = b, a
	}
	if h.visual == 'V' {
		return vimRange{r0: a[0], r1: b[0], line: true}
	}
	return vimRange{r0: a[0], c0: a[1], r1: b[0], c1: snapCol(c.lines[b[0]], min(b[1]+1, len(c.lines[b[0]])), 1)}
}

// visualKey is a key in VISUAL or VISUAL LINE: motions extend the selection;
// d x c y p act on it and leave; v or V again leaves.
func (h *home) visualKey(t string, n int) {
	switch t {
	case "v", "V":
		if h.visual == rune(t[0]) {
			h.visual = 0
		} else {
			h.visual = rune(t[0])
		}
	case "d", "x", "c", "y":
		r := h.visualRange()
		h.visual = 0
		op := t
		if t == "x" {
			op = "d"
		}
		if !r.line && r.r0 == r.r1 && r.c0 == r.c1 { // an empty line: nothing to take
			if op == "c" {
				h.enterInsert()
			}
		} else {
			h.apply(op, r)
		}
	case "p":
		h.visualPut(h.visualRange())
	default:
		h.motion(t, n)
	}
	// A visual command isn't what . repeats.
	h.changed, h.cmd = false, nil
}

// visualPut replaces the selection with the register, and the register gets
// what was replaced, as in vim. It is one undo step.
func (h *home) visualPut(r vimRange) {
	c := h.comp
	h.visual = 0
	text := c.kills
	if text == "" {
		return
	}
	m := len(c.undo)
	last := r.line && r.r1 == len(c.lines)-1
	toEnd := !r.line && r.c1 >= len(c.lines[r.r1])
	whole := r.line && r.r0 == 0 && last
	h.apply("d", r)
	deleted, dline := c.kills, c.killLine
	c.kills, c.killLine = text, r.line // V takes the text as lines, v as characters
	h.put(last || toEnd, 1)
	if whole { // the emptied box's blank line is not part of the text
		c.lines, c.row, c.col = c.lines[1:], 0, firstNonBlank(c.lines[1])
	}
	c.kills, c.killLine = deleted, dline
	if len(c.undo) > m+1 {
		c.undo = c.undo[:m+1]
	}
}

// selection is what the composer paints in reverse video: nil outside VISUAL.
func (h *home) selection() *vimRange {
	if h.visual == 0 || !h.normal {
		return nil
	}
	r := h.visualRange()
	return &r
}
