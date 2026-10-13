package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/delivery"
)

// The Run picker (#176): /runs as an overlay over the repository's recent
// Runs. It reads and filters; everything it does to a Run goes through the
// same code as /receipt, /diff, /apply and /branch with a Run id, so the
// refusals are theirs. Delivery never fires on a single key: a and b ask
// for the command's word to be typed first (#196).

// pickerMinHeight is the shortest terminal the picker opens in; below it
// /runs prints the list.
const pickerMinHeight = 10

// runWhen is a Run id's time, UTC as the id says; the id itself if it has none.
func runWhen(id string) string {
	if t, err := time.Parse("20060102T150405", strings.SplitN(id, "-", 2)[0]); err == nil {
		return t.Format("2006-01-02 15:04") + "Z"
	}
	return id
}

type runItem struct{ r *delivery.Run }

func (i runItem) FilterValue() string {
	return i.r.ID + " " + outcomeWord(i.r) + " " + clean(i.r.Title)
}

type runDelegate struct{ st styles }

func (runDelegate) Height() int                         { return 1 }
func (runDelegate) Spacing() int                        { return 0 }
func (runDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d runDelegate) Render(w io.Writer, m list.Model, i int, it list.Item) {
	r := it.(runItem).r
	line := fmt.Sprintf("%s  %-10s  %s  %s", r.ID, outcomeWord(r), runWhen(r.ID), clean(r.Title))
	var tail []string
	if r.Follows != "" {
		tail = append(tail, "continues "+clean(r.Follows))
	}
	if r.Rebased {
		tail = append(tail, "rebased")
	}
	if len(tail) > 0 {
		line += "  " + d.st.dim(strings.Join(tail, " · "))
	}
	if i == m.Index() {
		fmt.Fprint(w, " "+d.st.accent("> ")+line)
		return
	}
	fmt.Fprint(w, "   "+line)
}

type runPicker struct {
	st    styles
	l     list.Model
	cmd   string // "apply" or "branch" while its word is asked for
	run   string // the Run that confirmation is about
	typed string
	note  string
	// typing: the filter is being typed (after /); query is what it holds.
	typing bool
	query  string
}

func newRunPicker(runs []*delivery.Run, st styles, height int) *runPicker {
	items := make([]list.Item, len(runs))
	for i, r := range runs {
		items[i] = runItem{r}
	}
	l := list.New(items, runDelegate{st}, 100, min(len(items), max(height-8, 3))+4)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(true)
	return &runPicker{st: st, l: l}
}

func (p *runPicker) selected() *delivery.Run {
	if it, ok := p.l.SelectedItem().(runItem); ok {
		return it.r
	}
	return nil
}

// pickerKey is a key while the picker is open.
func (h *home) pickerKey(msg tea.KeyPressMsg) tea.Cmd {
	p := h.picker
	key := msg.String()
	p.note = ""
	if p.cmd != "" {
		switch key {
		case "esc", "ctrl+c":
			p.cmd, p.typed = "", ""
		case "backspace":
			if p.typed != "" {
				p.typed = p.typed[:len(p.typed)-1]
			}
		case "enter":
			if p.typed != p.cmd {
				p.note = "type " + p.cmd + " to confirm, esc to cancel"
				return nil
			}
			cmd, id := p.cmd, p.run
			h.picker = nil
			c, problem := deliverSlash(cmd[:1], cmd)(h, []string{id})
			if problem != "" {
				h.msg = []string{problem}
			}
			return c
		default:
			if msg.Text != "" {
				p.typed += msg.Text
			}
		}
		return nil
	}
	if p.typing {
		switch key {
		case "ctrl+c":
			h.picker = nil
			return nil
		case "esc":
			p.typing, p.query = false, ""
			p.l.ResetFilter()
		case "enter":
			p.typing = false
		case "up", "down", "ctrl+p", "ctrl+n":
			l, _ := p.l.Update(msg)
			p.l = l
		case "backspace":
			if p.query != "" {
				p.query = p.query[:len(p.query)-1]
				p.refilter()
			}
		default:
			if msg.Text != "" {
				p.query += msg.Text
				p.refilter()
			}
		}
		return nil
	}
	r := p.selected()
	switch key {
	case "esc":
		if p.l.FilterState() == list.FilterApplied {
			p.l.ResetFilter()
			return nil
		}
		h.picker = nil
	case "ctrl+c":
		h.picker = nil
	case "enter", "d":
		if r == nil {
			return nil
		}
		cli := "receipt"
		if key == "d" {
			cli = "diff"
		}
		h.picker = nil
		c, _ := deliverSlash(cli[:1], cli)(h, []string{r.ID})
		return c
	case "/":
		p.typing, p.query = true, ""
		p.l.ResetFilter()
	case "a", "b":
		if r != nil {
			p.cmd, p.run, p.typed = map[string]string{"a": "apply", "b": "branch"}[key], r.ID, ""
		}
	case "r":
		p.note = "resume: not built yet (#55)"
	default:
		l, _ := p.l.Update(msg)
		p.l = l
	}
	return nil
}

// refilter applies the typed query at once (the list's own filter answers
// on a later message).
func (p *runPicker) refilter() {
	if p.query == "" {
		p.l.ResetFilter()
		return
	}
	p.l.SetFilterText(p.query)
}

// lines draws the picker under the box.
func (p *runPicker) lines() []string {
	st := p.st
	var out []string
	for _, l := range strings.Split(p.l.View(), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, " "))
		}
	}
	if len(out) == 0 || len(p.l.VisibleItems()) == 0 {
		out = append(out, " "+st.dim("no Run matches"))
	}
	switch {
	case p.cmd != "":
		what := "to your working tree"
		if p.cmd == "branch" {
			what = "as a branch"
		}
		out = append(out, " "+st.bold(fmt.Sprintf("%s Run %s %s? type %s", p.cmd, p.run, what, p.cmd))+": "+p.typed+st.dim(" ⏎"))
	case p.typing:
		out = append(out, " "+st.dim("filter: ")+clean(p.query)+st.dim("   enter keep · esc clear"))
	default:
		out = append(out, " "+st.dim("/ filter · enter receipt · d diff · a apply · b branch · r resume · esc"))
	}
	if p.note != "" {
		out = append(out, " "+st.bad(p.note))
	}
	return out
}
