package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The key table (#155): every key the TUI answers to, in one place. The `?`
// panel and /help are both generated from it, so neither can drift from the
// other; TestKeyTableCoversTheHandlers checks it against the handlers' own
// case labels. Keys that are typed, not bound (/, @, ?), have no probe.

// keyGroups are the panel's sections, in order.
var keyGroups = []string{"Composer", "History", "Menus & pickers", "Run", "Delivery", "Setup"}

// keyBinding is one row: what the human reads, the key names (as
// tea.KeyPressMsg.String() spells them) the handlers switch on, and what it does.
type keyBinding struct {
	group string
	keys  string
	probe []string
	desc  string
}

var keyTable = []keyBinding{
	{"Composer", "enter", []string{"enter"}, "run the task"},
	{"Composer", "ctrl+j · \\+enter", []string{"ctrl+j", "shift+enter", "alt+enter"}, "newline"},
	{"Composer", "ctrl+e · ctrl+g", []string{"ctrl+e", "ctrl+g"}, "write the task in $EDITOR"},
	{"Composer", "ctrl+s", []string{"ctrl+s"}, "stash the draft (again on an empty box restores)"},
	{"Composer", "ctrl+c", []string{"ctrl+c"}, "clear the draft (↑ restores), then leave"},
	{"Composer", "ctrl+l", []string{"ctrl+l"}, "redraw"},
	{"Composer", "tab", []string{"tab"}, "open setup"},
	{"Composer", "/ · @ · ?", []string{"?"}, "commands · files · this panel (on an empty box)"},
	{"Composer", "← → · ctrl+b ctrl+f", []string{"left", "right", "ctrl+b", "ctrl+f"}, "move"},
	{"Composer", "home · end · ctrl+a", []string{"home", "end", "ctrl+a"}, "line start · end"},
	{"Composer", "alt+b alt+f", []string{"alt+b", "alt+f", "ctrl+left", "ctrl+right", "alt+left", "alt+right"}, "move by word"},
	{"Composer", "backspace · delete", []string{"backspace", "ctrl+h", "delete", "ctrl+d"}, "delete back · forward"},
	{"Composer", "ctrl+k · ctrl+u", []string{"ctrl+k", "ctrl+u"}, "kill to line end · start"},
	{"Composer", "ctrl+w · alt+d", []string{"ctrl+w", "alt+backspace", "alt+d"}, "kill word back · forward"},
	{"Composer", "ctrl+y", []string{"ctrl+y"}, "yank what was killed"},
	{"Composer", "ctrl+_ · ctrl+z", []string{"ctrl+_", "ctrl+/", "ctrl+z"}, "undo"},

	{"History", "↑ ↓ · ctrl+p ctrl+n", []string{"up", "down", "ctrl+p", "ctrl+n"}, "earlier · later tasks (↑ on the first line)"},
	{"History", "ctrl+r", []string{"ctrl+r"}, "search history (again: older)"},
	{"History", "enter · esc", nil, "in a search: use the match · cancel"},

	{"Menus & pickers", "↑ ↓", nil, "move in the slash menu or the @ picker"},
	{"Menus & pickers", "tab · enter", nil, "complete a command · insert a path"},
	{"Menus & pickers", "esc", []string{"esc"}, "close it"},
	{"Menus & pickers", "/ · esc", []string{"/"}, "in the Run picker: filter · clear it"},
	{"Menus & pickers", "enter · d", nil, "in the Run picker: receipt · diff of a Run"},
	{"Menus & pickers", "a · b", nil, "in the Run picker: apply · branch (confirm by typing)"},

	{"Run", "ctrl+o", []string{"ctrl+o"}, "expand or collapse the progress"},
	{"Run", "↑ ↓ · pgup pgdown", []string{"pgup", "pgdown"}, "scroll it, expanded"},
	{"Run", "esc", nil, "collapse"},
	{"Run", "ctrl+c", nil, "cancel the Run (twice: force Öge to stop)"},

	{"Delivery", "↓ on an empty box", nil, "the actions of the last Run"},
	{"Delivery", "a · b · d · r", []string{"a", "b", "d", "r"}, "apply · branch · diff · receipt (each ↓ allows one)"},

	{"Setup", "tab · esc", nil, "close setup"},
	{"Setup", "↑ ↓ · enter", nil, "move · change the row"},
	{"Setup", "s · w", []string{"s", "w"}, "save as default · write the project Checks"},
	{"Setup", "d · x", []string{"x"}, "remove the row"},
}

// keyLines is the table as plain lines: a heading per group, then its rows
// with the keys aligned. /help and the panel both read it.
func keyLines() []string {
	w := 0
	for _, b := range keyTable {
		w = max(w, ansi.StringWidth(b.keys))
	}
	var out []string
	for _, g := range keyGroups {
		out = append(out, g)
		for _, b := range keyTable {
			if b.group == g {
				out = append(out, fmt.Sprintf("  %-*s  %s", w, b.keys, b.desc))
			}
		}
	}
	return out
}

// keyPanel is the open `?` overlay (#155).
type keyPanel struct{ scroll int }

// openKeys opens the panel on an empty composer; elsewhere `?` is a character.
func (h *home) openKeys() bool {
	if h.comp.text() != "" {
		return false
	}
	h.keys = &keyPanel{}
	return true
}

// keysKey is a key while the panel is open: Esc, ? or Ctrl+C close it, the
// arrows scroll it, and nothing else reaches the composer.
func (h *home) keysKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "esc", "?", "ctrl+c":
		h.keys = nil
	case "up", "ctrl+p":
		h.keys.scroll--
	case "down", "ctrl+n":
		h.keys.scroll++
	case "pgup":
		h.keys.scroll -= 5
	case "pgdown":
		h.keys.scroll += 5
	}
}

// keysFrame draws the panel in at most height lines (scrolled when the table
// is taller), the last being the footer.
func (h *home) keysFrame(height int) []string {
	st := h.st
	all := keyLines()
	rows := max(height-1, 1)
	if h.height <= 0 {
		rows = len(all)
	}
	h.keys.scroll = max(min(h.keys.scroll, len(all)-rows), 0)
	end := min(h.keys.scroll+rows, len(all))
	var out []string
	for _, l := range all[h.keys.scroll:end] {
		if strings.HasPrefix(l, " ") {
			out = append(out, " "+l)
		} else {
			out = append(out, " "+st.bold(l))
		}
	}
	foot := "esc or ? close"
	if len(all) > rows {
		foot = fmt.Sprintf("↑ ↓ scroll (%d-%d of %d) · %s", h.keys.scroll+1, end, len(all), foot)
	}
	return append(out, " "+st.dim(foot))
}
