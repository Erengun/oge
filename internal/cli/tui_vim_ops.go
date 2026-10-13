package cli

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The vim operators d c y with motions and text objects, x D p P u and the
// dot (#157). The register is the composer's kill ring, so Ctrl+Y brings
// back what d or y took and p puts back what Ctrl+K killed; regLine only
// remembers that the ring's text was taken by whole lines.

// vimState is what a NORMAL command in progress and the last change need.
type vimState struct {
	vimOp  string // the operator waiting for its motion: d, c or y
	vimObj string // "i" once an operator has been followed by i
	count  int    // the digits typed so far

	regText string // what the kill ring held when a line was put in it
	regLine bool   // ...and that it was whole lines

	cmd        []tea.KeyPressMsg // the keys of the command in progress
	changed    bool              // that command edited the text or entered INSERT
	insRec     []tea.KeyPressMsg // the command and what was typed since it entered INSERT
	lastChange []tea.KeyPressMsg // what . replays
	replay     bool
	insMark    int // 1 + len(undo) when INSERT began, so u undoes the whole change
}

func (v *vimState) cancel() { v.vimOp, v.vimObj, v.count, v.cmd = "", "", 0, nil }

// vimRange is the text an operator acts on: from (r0, c0) up to but not
// including (r1, c1), or the whole lines r0..r1.
type vimRange struct {
	r0, c0, r1, c1 int
	line           bool
}

// endCommand closes a command unless an operator is still waiting.
func (h *home) endCommand() {
	if h.vimOp != "" {
		return
	}
	if h.changed && !h.replay {
		if h.normal {
			h.lastChange = h.cmd
		} else {
			h.insRec = slices.Clone(h.cmd) // . gets it when Esc ends the insert
		}
	}
	h.count, h.changed, h.cmd = 0, false, nil
	h.comp.lastOp = ""
	h.endNavigation()
}

// endInsert closes the INSERT that a change began: u takes the whole of it
// back, and . replays it.
func (h *home) endInsert() {
	c := h.comp
	if m := h.insMark - 1; m >= 0 && m < len(c.undo) {
		c.undo = c.undo[:m+1]
	}
	h.insMark = 0
	if h.insRec != nil && !h.replay {
		h.lastChange = h.insRec
	}
	h.insRec = nil
}

// repeat replays the last change, typed text and Esc included.
func (h *home) repeat() {
	keys := h.lastChange
	h.replay = true
	defer func() { h.replay = false }()
	for _, k := range keys {
		h.Update(k)
	}
}

// operatorKey takes the key after d, c or y.
func (h *home) operatorKey(t string, n int) {
	c := h.comp
	op := h.vimOp
	if h.vimObj != "" {
		h.vimOp, h.vimObj = "", ""
		if r, ok := c.textObject(t); ok {
			h.apply(op, r)
		}
		return
	}
	switch t {
	case "i":
		h.vimObj = "i"
		return
	case op:
		t = "dd"
	}
	h.vimOp = ""
	h.operate(op, t, n)
}

// operate runs op over what motion m covers from the cursor.
func (h *home) operate(op, m string, n int) {
	if r, ok := h.comp.motionRange(op, m, n); ok {
		h.apply(op, r)
	}
}

// motionRange is what the motion m (dd, cc, yy for the lines) covers.
func (c *composer) motionRange(op, m string, n int) (vimRange, bool) {
	r0, c0 := c.row, c.col
	l := c.lines[r0]
	r := vimRange{r0: r0, c0: c0, r1: r0, c1: c0}
	switch m {
	case "dd":
		return vimRange{r0: r0, r1: min(r0+n-1, len(c.lines)-1), line: true}, true
	case "j":
		return vimRange{r0: r0, r1: min(r0+n, len(c.lines)-1), line: true}, r0+1 < len(c.lines)
	case "k":
		return vimRange{r0: max(r0-n, 0), r1: r0, line: true}, r0 > 0
	case "l":
		for range n {
			r.c1 = snapCol(l, min(r.c1+1, len(l)), 1)
		}
	case "h":
		for range n {
			r.c0 = snapCol(l, max(r.c0-1, 0), -1)
		}
	case "$":
		r.r1 = min(r0+n-1, len(c.lines)-1)
		r.c1 = len(c.lines[r.r1])
	case "0":
		r.c0 = 0
	case "^":
		r.c0, r.c1 = min(c0, firstNonBlank(l)), max(c0, firstNonBlank(l))
	case "b":
		defer func() { c.row, c.col = r0, c0 }()
		for range n {
			c.vimWord("b")
		}
		r.r0, r.c0 = c.row, c.col
	case "e":
		defer func() { c.row, c.col = r0, c0 }()
		for range n {
			c.vimWord("e")
		}
		r.r1, r.c1 = c.row, snapCol(c.lines[c.row], c.col+1, 1)
	case "w":
		defer func() { c.row, c.col = r0, c0 }()
		if cs, i := c.cellIndex(); op == "c" && cs[i].cls != clsBlank && cs[i].cls != clsEmpty {
			r.r1, r.c1 = c.wordEnd(cs, i, n)
			break
		}
		for k := range n {
			cs, i := c.wordIdx("w")
			if i >= len(cs) {
				r.r1 = len(c.lines) - 1
				r.c1 = len(c.lines[r.r1])
				break
			}
			if t := cs[i]; k == n-1 && t.row > c.row {
				r.r1, r.c1 = c.row, len(c.lines[c.row]) // the last word stops at its line's end
			} else {
				r.r1, r.c1 = t.row, t.col
				c.row, c.col = t.row, t.col
			}
		}
	default:
		return r, false
	}
	return r, r.r1 > r.r0 || r.c1 > r.c0
}

// wordEnd is where cw stops: at the end of the word under the cursor (and
// n-1 more), not at the next one's end as e would.
func (c *composer) wordEnd(cs []cell, i, n int) (int, int) {
	end := cs[i]
	for k := range n {
		for k > 0 && i < len(cs) && cs[i].cls == clsBlank {
			i++
		}
		if i >= len(cs) {
			break
		}
		for cls := cs[i].cls; i < len(cs) && cs[i].cls == cls && cls != clsBlank; i++ {
			end = cs[i]
		}
	}
	return end.row, snapCol(c.lines[end.row], end.col+1, 1)
}

// textObject is what i{w " ( )} covers around the cursor.
func (c *composer) textObject(t string) (vimRange, bool) {
	l := c.lines[c.row]
	switch t {
	case "w":
		if len(l) == 0 {
			return vimRange{}, false
		}
		cls := runeClass(l[c.col])
		lo := c.col
		for _, g := range graphemes(l) {
			if g < c.col && runeClass(l[g]) != cls {
				lo = snapCol(l, g+1, 1)
			}
		}
		hi := len(l)
		for _, g := range graphemes(l) {
			if g > c.col && runeClass(l[g]) != cls {
				hi = g
				break
			}
		}
		return vimRange{r0: c.row, c0: lo, r1: c.row, c1: hi}, true
	case "\"":
		var qs []int
		for i, r := range l {
			if r == '"' {
				qs = append(qs, i)
			}
		}
		for i := 0; i+1 < len(qs); i += 2 {
			if c.col <= qs[i+1] { // the pair around the cursor, or the next one
				return vimRange{r0: c.row, c0: qs[i] + 1, r1: c.row, c1: qs[i+1]}, true
			}
		}
	case "(", ")", "b":
		return c.parenObject()
	}
	return vimRange{}, false
}

// parenObject is the text inside the innermost ( ) around the cursor, which
// may span lines.
func (c *composer) parenObject() (vimRange, bool) {
	var flat []rune
	var pos [][2]int
	cur := 0
	for r, l := range c.lines {
		if r == c.row {
			cur = len(flat) + c.col
		}
		for i, ch := range l {
			flat = append(flat, ch)
			pos = append(pos, [2]int{r, i})
		}
		flat = append(flat, '\n')
		pos = append(pos, [2]int{r, len(l)})
	}
	open, depth := -1, 0
	for i := cur; i >= 0; i-- {
		switch flat[i] {
		case ')':
			if i != cur {
				depth++
			}
		case '(':
			if depth == 0 {
				open = i
			} else {
				depth--
			}
		}
		if open >= 0 {
			break
		}
	}
	if open < 0 {
		return vimRange{}, false
	}
	depth = 0
	for i := open + 1; i < len(flat); i++ {
		switch flat[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				a, b := pos[open+1], pos[i]
				return vimRange{r0: a[0], c0: a[1], r1: b[0], c1: b[1]}, true
			}
			depth--
		}
	}
	return vimRange{}, false
}

// apply runs the operator over r, leaving the text in the register.
func (h *home) apply(op string, r vimRange) {
	c := h.comp
	text := c.rangeText(r)
	if op == "y" {
		c.kills, c.killing = text, false
		h.regText, h.regLine = text, r.line
		if !r.line {
			c.row, c.col = r.r0, r.c0
			c.col = min(c.col, lastCol(c.lines[c.row]))
		} else {
			c.row = min(c.row, r.r0)
			c.col = min(c.col, lastCol(c.lines[c.row]))
		}
		return
	}
	h.changed = true
	if op == "c" {
		h.insMark = len(c.undo) + 1
	}
	c.change("vimdel", func() {
		c.killing = false
		if r.line {
			c.kills = text
			if op == "c" {
				c.lines = slices.Replace(c.lines, r.r0, r.r1+1, []rune(nil))
				c.row, c.col = r.r0, 0
				return
			}
			c.lines = slices.Delete(c.lines, r.r0, r.r1+1)
			if len(c.lines) == 0 {
				c.lines = [][]rune{nil}
			}
			c.row = min(r.r0, len(c.lines)-1)
			c.col = firstNonBlank(c.lines[c.row])
			return
		}
		c.row, c.col = r.r0, r.c0
		c.kill(r.r1, r.c1, false)
		if op != "c" {
			c.col = min(c.col, lastCol(c.lines[c.row]))
		}
	})
	h.regText, h.regLine = c.kills, r.line
	if op == "c" {
		h.enterInsert()
	}
}

// rangeText is what r covers, lines joined by a newline.
func (c *composer) rangeText(r vimRange) string {
	var b strings.Builder
	for i := r.r0; i <= r.r1; i++ {
		l := c.lines[i]
		lo, hi := 0, len(l)
		if !r.line {
			if i == r.r0 {
				lo = r.c0
			}
			if i == r.r1 {
				hi = r.c1
			}
		}
		b.WriteString(string(l[lo:hi]))
		if i < r.r1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// put is p (after the cursor, or below a line) and P.
func (h *home) put(after bool, n int) {
	c := h.comp
	text := c.kills
	if text == "" {
		return
	}
	h.changed = true
	line := h.regLine && h.regText == text
	c.change("vimput", func() {
		if line {
			var add [][]rune
			for range n {
				for _, s := range strings.Split(text, "\n") {
					add = append(add, []rune(s))
				}
			}
			at := c.row
			if after {
				at++
			}
			c.lines = slices.Insert(c.lines, at, add...)
			c.row = at
			c.col = firstNonBlank(c.lines[at])
			return
		}
		if l := c.lines[c.row]; after && len(l) > 0 {
			c.col = snapCol(l, c.col+1, 1)
		}
		r0, c0 := c.row, c.col
		c.insert(strings.Repeat(text, n))
		if c.row == r0 {
			c.col = snapCol(c.lines[c.row], max(c.col-1, 0), -1)
		} else {
			c.row, c.col = r0, c0
		}
	})
}
