package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"
)

// @file mentions (#146): typing @ at the start of a word opens a fuzzy
// picker over the repository's tracked files. Choosing one types
// "@path/to/file " into the Task and nothing else: the agent reads the file
// from its Workspace, so Öge attaches no content.

// atMax caps how many matches the picker keeps; atShown is how many it draws.
const (
	atMax   = 200
	atShown = 8
)

// atState is the picker while it is open. query is the text after the @;
// row and col locate the @ in the composer. total counts every match.
type atState struct {
	list       *list.Model
	query      string
	text       string // the composer text it was built for
	row, col   int    // the @'s position
	total      int
	off        bool // Esc closed it for offText
	offText    string
	files      []string // the tracked files, loaded once per composer open
	filesReady bool
}

type fileItem struct{ path string }

func (i fileItem) FilterValue() string { return i.path }

type fileDelegate struct{ st styles }

func (fileDelegate) Height() int                         { return 1 }
func (fileDelegate) Spacing() int                        { return 0 }
func (fileDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d fileDelegate) Render(w io.Writer, m list.Model, i int, it list.Item) {
	p := clean(it.(fileItem).path)
	if i == m.Index() {
		fmt.Fprint(w, " "+d.st.accent("> ")+d.st.bold(p))
		return
	}
	fmt.Fprint(w, "   "+p)
}

// mention finds the @word the cursor ends: an @ at the start of the line or
// after whitespace, then no whitespace up to the cursor.
func (c *composer) mention() (query string, col int, ok bool) {
	line := c.lines[c.row][:c.col]
	for i := len(line) - 1; i >= 0; i-- {
		switch {
		case unicode.IsSpace(line[i]):
			return "", 0, false
		case line[i] == '@' && (i == 0 || unicode.IsSpace(line[i-1])):
			return string(line[i+1:]), i, true
		}
	}
	return "", 0, false
}

// mentionable is a path the picker can offer: a name with no whitespace or
// control characters keeps "@path " one word.
func mentionable(p string) bool {
	return !strings.ContainsFunc(p, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// trackedFiles loads the list once; it is kept until the next Run starts.
func (h *home) trackedFiles() []string {
	if !h.at.filesReady {
		h.at.filesReady = true
		if h.listFiles != nil {
			if files, err := h.listFiles(); err == nil {
				for _, f := range files {
					if mentionable(f) {
						h.at.files = append(h.at.files, f)
					}
				}
			}
		}
	}
	return h.at.files
}

// syncAt opens, filters or closes the picker after every update.
func (h *home) syncAt() {
	a := &h.at
	query, col, ok := h.comp.mention()
	text := h.comp.text()
	if !ok || h.run != nil || h.panel != nil || h.saving || h.review != nil || h.search != nil || h.actions ||
		h.menu != nil || (a.off && text == a.offText) {
		a.list = nil
		return
	}
	if a.list != nil && a.text == text && a.row == h.comp.row && a.col == col {
		return
	}
	files := h.trackedFiles()
	var items []list.Item
	if query == "" {
		a.total = len(files)
		for _, f := range files[:min(len(files), atMax)] {
			items = append(items, fileItem{f})
		}
	} else {
		ms := fuzzy.Find(query, files)
		a.total = len(ms)
		for _, m := range ms[:min(len(ms), atMax)] {
			items = append(items, fileItem{m.Str})
		}
	}
	if len(items) == 0 {
		a.list = nil
		return
	}
	l := list.New(items, fileDelegate{h.st}, 100, min(len(items), atShown)+1)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	a.list, a.query, a.text, a.row, a.col = &l, query, text, h.comp.row, col
}

// atKey is a key while the picker is open; handled is false when the key
// is the composer's. Tab and Enter insert the highlighted path and never
// submit the Task; Esc closes the picker and keeps the text.
func (h *home) atKey(msg tea.KeyPressMsg) (handled bool) {
	a := &h.at
	switch msg.String() {
	case "up", "down", "ctrl+p", "ctrl+n":
		if h.navigating && (msg.String() == "up" || msg.String() == "down") {
			return false // history's, while it is being walked
		}
		l, _ := a.list.Update(msg)
		a.list = &l
	case "tab", "enter":
		sel, _ := a.list.SelectedItem().(fileItem)
		line := h.comp.lines[h.comp.row]
		h.comp.lines[h.comp.row] = append(line[:a.col:a.col], line[h.comp.col:]...)
		h.comp.col = a.col
		h.comp.insert("@" + sel.path + " ")
		h.navigating = false
	case "esc":
		a.off, a.offText = true, h.comp.text()
	default:
		return false
	}
	return true
}

// atLines draws the open picker.
func (h *home) atLines() []string {
	var out []string
	for _, l := range strings.Split(h.at.list.View(), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, " "))
		}
	}
	if n := h.at.total - len(out); n > 0 {
		out = append(out, " "+h.st.dim(fmt.Sprintf("+%d more · ↓", n)))
	}
	return append(out, " "+h.st.dim("tab or enter insert the path · esc close"))
}
