package cli

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"
)

// The composer's Emacs-style editing (#151): a kill ring of one entry, word
// motions and undo. Ctrl+K kills to the end of the line, Ctrl+U to its start,
// Ctrl+W the word before the cursor, Alt+D the word after it; Ctrl+Y yanks
// the last kill. Kills in a row (nothing but kills between them) join into
// one entry, so a yank brings back all of them. Alt+B and Alt+F move a word.
// Ctrl+_ (Ctrl+/ on terminals that send it so) and Ctrl+Z undo. Bubble Tea
// v2 doesn't suspend on Ctrl+Z by itself, so it is free here.

// maxUndo bounds the undo stack.
const maxUndo = 100

// snap is the box as it was: text and cursor.
type snap struct {
	text     string
	row, col int
}

func (c *composer) snap() snap { return snap{c.text(), c.row, c.col} }

func (c *composer) restore(s snap) {
	c.lines = c.lines[:0]
	for _, l := range strings.Split(s.text, "\n") {
		c.lines = append(c.lines, []rune(l))
	}
	c.row, c.col = s.row, s.col
}

// change runs one edit, op naming its kind, and records what it replaced so
// undo can bring it back. Typing, deleting and kills in a row are one undo
// step each; a yank, a paste or any other edit is its own.
func (c *composer) change(op string, fn func()) {
	killing := c.killing // kill reads it: a kill after a kill appends
	before := c.snap()
	fn()
	if c.text() == before.text {
		c.killing = killing && op == "kill"
		if op == "move" {
			c.lastOp = ""
		}
		return
	}
	join := (op == "kill" && killing) || ((op == "type" || op == "del") && c.lastOp == op)
	if !join {
		c.undo = append(c.undo, before)
		if len(c.undo) > maxUndo {
			c.undo = c.undo[len(c.undo)-maxUndo:]
		}
	}
	c.lastOp = op
	c.killing = op == "kill"
}

// clearUndo forgets what undo could bring back (a submitted Task).
func (c *composer) clearUndo() { c.undo, c.lastOp, c.killing = nil, "", false }

func (c *composer) undoStep() {
	c.killing, c.lastOp = false, "undo"
	if n := len(c.undo); n > 0 {
		c.restore(c.undo[n-1])
		c.undo = c.undo[:n-1]
	}
}

// isWordRune is what a word is made of: letters and digits, with the marks
// that join them, so a combined character is never cut in two.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

// snapCol moves col to a grapheme cluster boundary of l: back when dir < 0,
// forward otherwise.
func snapCol(l []rune, col, dir int) int {
	n := 0
	for rest := string(l); rest != ""; {
		cl, tail, _, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
		k := len([]rune(cl))
		if col < n+k {
			if col == n || dir < 0 {
				return n
			}
			return n + k
		}
		n += k
		rest = tail
	}
	return len(l)
}

// wordLeft is where the word before col starts: it skips what isn't a word,
// then the word.
func wordLeft(l []rune, col int) int {
	i := col
	for i > 0 && !isWordRune(l[i-1]) {
		i--
	}
	for i > 0 && isWordRune(l[i-1]) {
		i--
	}
	return snapCol(l, i, -1)
}

// wordRight is where the word after col ends.
func wordRight(l []rune, col int) int {
	i := col
	for i < len(l) && !isWordRune(l[i]) {
		i++
	}
	for i < len(l) && isWordRune(l[i]) {
		i++
	}
	return snapCol(l, i, 1)
}

// kill removes the text from (row, col) to the cursor's other side, or the
// line break at a line's edge, and keeps it in the kill ring: back is true
// for a kill before the cursor, which goes in front of what the ring holds
// when it follows another kill.
func (c *composer) kill(toRow, toCol int, back bool) {
	r0, c0, r1, c1 := c.row, c.col, toRow, toCol
	if back {
		r0, c0, r1, c1 = toRow, toCol, c.row, c.col
	}
	var b strings.Builder
	for r := r0; r <= r1; r++ {
		l := c.lines[r]
		lo, hi := 0, len(l)
		if r == r0 {
			lo = c0
		}
		if r == r1 {
			hi = c1
		}
		b.WriteString(string(l[lo:hi]))
		if r < r1 {
			b.WriteByte('\n')
		}
	}
	text := b.String()
	if text == "" {
		return
	}
	merged := append(append([]rune(nil), c.lines[r0][:c0]...), c.lines[r1][c1:]...)
	c.lines = append(c.lines[:r0], append([][]rune{merged}, c.lines[r1+1:]...)...)
	c.row, c.col = r0, c0
	switch {
	case !c.killing:
		c.kills = text
	case back:
		c.kills = text + c.kills
	default:
		c.kills += text
	}
}

func (c *composer) killToEnd() {
	l := c.lines[c.row]
	if c.col == len(l) && c.row+1 < len(c.lines) {
		c.kill(c.row+1, 0, false)
		return
	}
	c.kill(c.row, len(l), false)
}

func (c *composer) killToStart() {
	if c.col == 0 && c.row > 0 {
		c.kill(c.row-1, len(c.lines[c.row-1]), true)
		return
	}
	c.kill(c.row, 0, true)
}

func (c *composer) killWordBack() {
	if c.col == 0 && c.row > 0 {
		c.kill(c.row-1, len(c.lines[c.row-1]), true)
		return
	}
	c.kill(c.row, wordLeft(c.lines[c.row], c.col), true)
}

func (c *composer) killWordForward() {
	l := c.lines[c.row]
	if c.col == len(l) && c.row+1 < len(c.lines) {
		c.kill(c.row+1, 0, false)
		return
	}
	c.kill(c.row, wordRight(l, c.col), false)
}

func (c *composer) wordMove(dir int) {
	l := c.lines[c.row]
	switch {
	case dir < 0 && c.col == 0 && c.row > 0:
		c.row--
		c.col = len(c.lines[c.row])
	case dir > 0 && c.col == len(l) && c.row+1 < len(c.lines):
		c.row, c.col = c.row+1, 0
	case dir < 0:
		c.col = wordLeft(l, c.col)
	default:
		c.col = wordRight(l, c.col)
	}
}

// editKey applies the keys of this file; it reports whether k was one.
func (c *composer) editKey(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "ctrl+k":
		c.change("kill", c.killToEnd)
	case "ctrl+u":
		c.change("kill", c.killToStart)
	case "ctrl+w", "alt+backspace":
		c.change("kill", c.killWordBack)
	case "alt+d":
		c.change("kill", c.killWordForward)
	case "ctrl+y":
		c.change("yank", func() { c.insert(c.kills) })
	case "alt+b", "ctrl+left", "alt+left":
		c.change("move", func() { c.wordMove(-1) })
	case "alt+f", "ctrl+right", "alt+right":
		c.change("move", func() { c.wordMove(1) })
	case "ctrl+_", "ctrl+/", "ctrl+z":
		c.undoStep()
	default:
		return false
	}
	return true
}
