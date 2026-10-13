package cli

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/delivery"
)

// A large paste (#153) isn't shown: the composer holds one placeholder
// character for it, drawn as "[pasted 3,214 chars · 87 lines]". It is one
// rune, so the cursor, undo and Backspace treat it as a unit. The real text
// is kept beside the box and expanded wherever the Task leaves it: on submit
// and for the external editor.
//
// A Task never carries hidden characters unannounced (#153): they can carry
// instructions to an agent that the human can't see. On submit the text is
// checked with the classifier delivery uses for Candidate content, and the
// human confirms the cleaned text first.

const (
	// pasteChars and pasteLines: a paste over either shows as a placeholder.
	pasteChars = 1500
	pasteLines = 20
	// pasteBase is the first placeholder rune, in a private-use plane that
	// insert refuses from typed or pasted text.
	pasteBase  = 0x10F000
	pasteLimit = 0x10FFFD
)

func isPasteRune(r rune) bool { return r >= pasteBase && r <= pasteLimit }

// sanitizeText is what insert would keep of s.
func sanitizeText(s string) string {
	t := newComposer()
	t.insert(s)
	return t.text()
}

// insertPaste puts a paste in the box: a placeholder when it is large.
func (c *composer) insertPaste(s string) {
	s = sanitizeText(s)
	n := len([]rune(s))
	if (n <= pasteChars && strings.Count(s, "\n")+1 <= pasteLines) || len(c.pastes) >= pasteLimit-pasteBase {
		c.insert(s)
		return
	}
	c.pastes = append(c.pastes, s)
	l := c.lines[c.row]
	r := rune(pasteBase + len(c.pastes) - 1)
	c.lines[c.row] = append(l[:c.col:c.col], append([]rune{r}, l[c.col:]...)...)
	c.col++
}

func (c *composer) pasteLabel(r rune) string {
	i := int(r - pasteBase)
	if i >= len(c.pastes) {
		return "[pasted]"
	}
	s := c.pastes[i]
	return fmt.Sprintf("[pasted %s chars · %d lines]", thousands(len([]rune(s))), strings.Count(s, "\n")+1)
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// expanded is the text with every placeholder replaced by what it stands for.
func (c *composer) expanded() string {
	return c.expand(c.text())
}

func (c *composer) expand(s string) string {
	var b strings.Builder
	for _, r := range s {
		if i := int(r - pasteBase); isPasteRune(r) {
			if i < len(c.pastes) {
				b.WriteString(c.pastes[i])
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hiddenCount is how many runes of s the delivery classifier calls hidden.
func hiddenCount(s string) int {
	n := 0
	for _, r := range s {
		if delivery.Hidden(r) {
			n++
		}
	}
	return n
}

// withoutHidden is s with the hidden runes removed.
func withoutHidden(s string) string {
	return strings.Map(func(r rune) rune {
		if delivery.Hidden(r) {
			return -1
		}
		return r
	}, s)
}

// markHidden is s with each hidden rune written as <U+XXXX>, line by line,
// so the lines stay lines.
func markHidden(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = delivery.Shown(l)
	}
	return strings.Join(lines, "\n")
}

// hiddenPrompt is the confirmation of a Task that held hidden characters.
type hiddenPrompt struct {
	text string // the Task as written
	n    int    // hidden characters in it
}

const hiddenShown = 8 // lines of the cleaned text the prompt shows

func (p *hiddenPrompt) lines(st styles) []string {
	all := strings.Split(markHidden(p.text), "\n")
	var out []string
	for i, l := range all {
		if i == hiddenShown {
			out = append(out, "   "+st.dim(fmt.Sprintf("… %d more lines", len(all)-hiddenShown)))
			break
		}
		out = append(out, "   "+l)
	}
	word := "characters"
	if p.n == 1 {
		word = "character"
	}
	return append(out, " "+st.bold(fmt.Sprintf("%d hidden %s removed", p.n, word))+
		" · [enter] submit cleaned · [e] edit · [esc] cancel")
}

// hiddenKey answers the prompt: Enter submits the cleaned text, e puts the
// text back in the box with the hidden characters written out to be seen
// and deleted, and Esc leaves the box as it was.
func (h *home) hiddenKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := h.hidden
	switch k.String() {
	case "enter":
		h.hidden = nil
		return h.submit(withoutHidden(p.text))
	case "e":
		h.hidden = nil
		h.comp.set(markHidden(p.text))
		h.navigating = false
	case "esc", "ctrl+c":
		h.hidden = nil
	}
	return h, nil
}
