package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/agent"
)

// The full-screen transcript's blocks: the human's turn, the agent's reply
// as markdown, a tool step as pitago draws it (a shell command in a rounded
// box, any other tool as a bullet and its target), and a cut's diff as hunks.
// Each is a view of an event: what /export and the clipboard keep is the
// text the event carried, never what is drawn here.

// printBlock puts a block in the transcript; raw is what /export keeps.
func (h *home) printBlock(raw string, render func(width int, expand bool) string) tea.Cmd {
	h.logScroll(raw)
	h.tr.add(trEntry{text: raw, render: render})
	return nil
}

// userEntry is the human's message: a tinted bar with a ❯ prompt.
func (h *home) userEntry(text string) tea.Cmd {
	st := h.st
	first := len(h.tr.entries) == 0
	return h.printBlock("> "+text, func(w int, _ bool) string {
		var out []string
		out = append(out, "")
		if !first {
			// A dim rule sets one turn off from the last.
			out = append(out, "  "+st.border(strings.Repeat("─", max(w-5, 4))), "")
		}
		for _, l := range strings.Split(ansi.Wrap(text, max(w-5, 8), ""), "\n") {
			bar := "  ❯ " + l
			bar += strings.Repeat(" ", max(w-1-ansi.StringWidth(bar), 0))
			out = append(out, st.user(bar))
		}
		return strings.Join(out, "\n") + "\n"
	})
}

// replyEntry is the agent's words, drawn as markdown.
func (h *home) replyEntry(text string) tea.Cmd {
	st := h.st
	// A reply that follows a tool step is set off from it by a blank row.
	gap := ""
	if n := len(h.tr.entries); n > 0 && strings.HasPrefix(h.tr.entries[n-1].text, "  ● ") {
		gap = "\n"
	}
	return h.printBlock(agentLines(text), func(w int, _ bool) string {
		return gap + st.renderMarkdown(text, w-1, 2)
	})
}

// Tool kinds, as pitago names them: what a step does tells how it is drawn.
const (
	toolShell = "shell"
	toolRead  = "read"
	toolWrite = "write"
	toolOther = "other"
)

// toolKind classifies a tool by its name; an unknown tool is other.
func toolKind(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexAny(n, ".:/"); i >= 0 && i+1 < len(n) {
		n = n[i+1:]
	}
	n = strings.NewReplacer("_", "", "-", "").Replace(n)
	switch n {
	case "bash", "powershell", "shell", "sh", "zsh", "run", "runcommand", "exec", "terminal", "bashoutput", "execcommand":
		return toolShell
	case "read", "view", "cat", "readfile", "viewfile", "notebookread", "grep", "glob", "search", "ls", "list", "find":
		return toolRead
	case "write", "edit", "multiedit", "patch", "replace", "strreplace", "writetofile", "replacefilecontent", "create", "createfile":
		return toolWrite
	}
	return toolOther
}

// toolStep is one tool step in the transcript and, once the agent reports it,
// what the tool returned: display only, as the event's Output is.
type toolStep struct {
	id     string
	done   bool
	failed bool
	output string
}

// toolEntry is one tool step: the call, and under it what the tool returned
// once the agent's ToolResult arrives. bad marks a step the host denied.
func (h *home) toolEntry(tool, target, id string, bad bool) tea.Cmd {
	st := h.st
	step := &toolStep{id: id}
	h.steps = append(h.steps[max(len(h.steps)-63, 0):], step)
	raw := "  ● " + strings.TrimSpace(tool+" "+target)
	return h.printBlock(raw, func(w int, expand bool) string {
		dot := st.dim("●")
		if bad {
			dot = st.bad("●")
		}
		if toolKind(tool) == toolShell && target != "" && !bad {
			return shellBox(st, tool, target, step, w, expand, h.kb.expandLabel())
		}
		line := ansi.Truncate("  "+dot+" "+st.bold(clean(tool))+optional(" ", st.dim(target), target != ""), max(w-1, 8), "…")
		return line + resultTree(st, tool, step, w, expand, h.kb.expandLabel())
	})
}

func optional(pre, s string, ok bool) string {
	if !ok {
		return ""
	}
	return pre + s
}

// toolResult hangs a ToolResult on its step: the one with its id, or, when
// the agent gave none, the oldest step still waiting. The view is redrawn.
func (h *home) toolResult(ev agent.Event) {
	var step *toolStep
	for _, s := range h.steps {
		if s.done || (ev.ToolID != "" && s.id != ev.ToolID) {
			continue
		}
		if ev.ToolID != "" || s.id == "" {
			step = s
			break
		}
	}
	if step == nil {
		return
	}
	step.done, step.failed, step.output = true, ev.Failed, cleanLines(strings.ReplaceAll(ev.Output, "\t", "    "))
	h.tr.gen++
}

// outputLines is a step's output as rows, empty when it printed nothing.
func outputLines(out string) []string {
	out = strings.TrimRight(strings.ReplaceAll(out, "\t", "    "), "\n ")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// resultTree is what a bullet tool returned, as pitago draws it: a ├── row
// per line and a └── row last. Folded, it is one row that says what came
// back: how many lines a read returned, the first line of anything else.
func resultTree(st styles, tool string, step *toolStep, width int, expand bool, key string) string {
	if !step.done {
		return ""
	}
	lines := outputLines(step.output)
	paint := st.dim
	if step.failed {
		paint = st.bad
	}
	row := func(last bool, l string) string {
		mark := "├──"
		if last {
			mark = "└──"
		}
		return "\n    " + st.dim(mark) + " " + paint(ansi.Truncate(l, max(width-10, 8), "…"))
	}
	switch {
	case len(lines) == 0 && step.failed:
		return row(true, "failed")
	case len(lines) == 0:
		return row(true, "ok")
	case !expand || len(lines) == 1:
		sum := lines[0]
		switch {
		case step.failed:
		case toolKind(tool) == toolRead && len(lines) > 1:
			sum = plural(len(lines), "line")
		case len(lines) > 1:
			sum += " · +" + plural(len(lines)-1, "line") + " · " + key
		}
		return row(true, sum)
	}
	const shown = 12
	var b strings.Builder
	n := min(len(lines), shown)
	for i, l := range lines[:n] {
		b.WriteString(row(i == n-1 && len(lines) <= shown, l))
	}
	if len(lines) > shown {
		b.WriteString(row(true, fmt.Sprintf("+%d more lines", len(lines)-shown)))
	}
	return b.String()
}

// shellBox draws a command in a rounded box under the tool's name, and under
// it what the command printed. A long command or output is cut to a few rows
// unless the human expanded the output.
func shellBox(st styles, tool, cmd string, step *toolStep, width int, expand bool, expandKey string) string {
	title := " " + strings.ToLower(clean(tool)) + " "
	// The box takes the pane's width, as pitago's does: the command on top.
	inner := max(width-6, 12)
	lines := strings.Split(ansi.Wrap("$ "+cmd, inner, ""), "\n")
	const collapsed = 4
	if !expand && len(lines) > collapsed {
		more := len(lines) - collapsed
		lines = append(lines[:collapsed:collapsed], fmt.Sprintf("… %d more lines · %s to expand", more, expandKey))
		lines[collapsed] = st.dim(lines[collapsed])
	}
	b := st.border
	out := []string{""} // a blank row before and after, so the box breathes
	out = append(out, "  "+b("╭─")+st.dim(title)+b(strings.Repeat("─", max(inner+1-ansi.StringWidth(title)-1, 0))+"╮"))
	row := func(l string) {
		pad := strings.Repeat(" ", max(inner-ansi.StringWidth(l), 0))
		out = append(out, "  "+b("│")+" "+l+pad+b("│"))
	}
	for _, l := range lines {
		row(l)
	}
	if step.done {
		if res := outputLines(step.output); len(res) > 0 {
			const folded = 5
			paint := st.dim
			if step.failed {
				paint = st.bad
			}
			out = append(out, "  "+b("├"+strings.Repeat("─", inner+1)+"┤"))
			var wrapped []string
			for _, l := range res {
				wrapped = append(wrapped, strings.Split(ansi.Hardwrap(l, inner, true), "\n")...)
			}
			shown := wrapped
			if !expand && len(shown) > folded {
				shown = shown[:folded]
			}
			for _, l := range shown {
				row(paint(l))
			}
			if len(shown) < len(wrapped) {
				row(st.dim(fmt.Sprintf("… +%d lines · %s to expand", len(wrapped)-len(shown), expandKey)))
			}
		} else if step.failed {
			out = append(out, "  "+b("├"+strings.Repeat("─", inner+1)+"┤"))
			row(st.bad("failed"))
		}
	}
	out = append(out, "  "+b("╰"+strings.Repeat("─", inner+1)+"╯"), "")
	return strings.Join(out, "\n")
}

// diffEntry is a cut's patch as hunks: the git headers are dropped, each file
// is a line of its path and its +N -M, changes are painted, and a summary
// ends it. Long diffs show their first lines until the human expands them.
func (h *home) diffEntry(patch string) tea.Cmd {
	st := h.st
	patch = strings.TrimRight(cleanLines(patch), "\n")
	return h.printBlock(patch, func(w int, expand bool) string {
		return diffBlock(st, patch, w, expand)
	})
}

// diffCollapsed is how many diff lines show before the rest is folded.
const diffCollapsed = 14

func diffBlock(st styles, patch string, width int, expand bool) string {
	type file struct {
		diffFile
		notes []string // the "# Öge:" lines: why the file is held back
		body  []string // the hunks
	}
	var files []*file
	for _, df := range splitPatch(patch) {
		f := &file{diffFile: df}
		inHunk := false
		for _, l := range strings.Split(df.text, "\n") {
			switch {
			case strings.HasPrefix(l, "@@"):
				inHunk = true
				f.body = append(f.body, l)
			case inHunk:
				f.body = append(f.body, l)
			case strings.HasPrefix(l, "# Öge:"):
				f.notes = append(f.notes, l)
			}
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return ""
	}
	var out []string
	budget := diffCollapsed
	if expand {
		budget = 1 << 30
	}
	hidden, add, del := 0, 0, 0
	for _, f := range files {
		add, del = add+f.add, del+f.del
		head := "  " + st.bold(clean(f.path)) + " " + counts(st, f.add, f.del)
		if heldStatus(f.status) {
			head += " " + st.warn(f.status)
		}
		out = append(out, head)
		for _, n := range f.notes {
			out = append(out, ansi.Truncate("    "+st.warn(n), max(width-1, 8), "…"))
		}
		for _, l := range f.body {
			if budget == 0 {
				hidden++
				continue
			}
			budget--
			// Tabs become spaces first so the measured width is the drawn one.
			row := ansi.Truncate(strings.ReplaceAll(l, "\t", "    "), max(width-7, 8), "…")
			// The rows are raw hunk lines: the first byte is the marker.
			switch {
			case strings.HasPrefix(l, "@@"):
				out = append(out, "    "+st.ask(row))
			case strings.HasPrefix(l, "+"):
				out = append(out, "    "+st.diffTint(st.diffAdd(row), true, max(width-7, 8)))
			case strings.HasPrefix(l, "-"):
				out = append(out, "    "+st.diffTint(st.diffDel(row), false, max(width-7, 8)))
			default:
				out = append(out, "    "+st.dim(row))
			}
		}
	}
	if hidden > 0 {
		out = append(out, st.dim(fmt.Sprintf("    … %d more lines · ctrl+o to expand", hidden)))
	}
	if len(files) > 1 {
		out = append(out, "  "+st.dim(plural(len(files), "file")+" ")+counts(st, add, del))
	}
	// A blank row on both sides sets the diff off from the Verdict above it
	// and the actions below.
	return "\n" + strings.Join(out, "\n") + "\n"
}

// heldStatus is a file status that says the file is not delivered.
func heldStatus(s string) bool {
	return strings.HasPrefix(s, "held back") || strings.HasPrefix(s, "submodule")
}

// counts is "+N -M" in the diff colours, a zero side left out.
func counts(st styles, add, del int) string {
	var parts []string
	if add > 0 {
		parts = append(parts, st.diffAdd(fmt.Sprintf("+%d", add)))
	}
	if del > 0 {
		parts = append(parts, st.diffDel(fmt.Sprintf("-%d", del)))
	}
	return strings.Join(parts, " ")
}
