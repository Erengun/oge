package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
	return h.printBlock("> "+text, func(w int, _ bool) string {
		var out []string
		out = append(out, "")
		for _, l := range strings.Split(ansi.Wrap(text, max(w-4, 8), ""), "\n") {
			bar := " ❯ " + l
			bar += strings.Repeat(" ", max(w-1-ansi.StringWidth(bar), 0))
			out = append(out, st.user(bar))
		}
		return strings.Join(out, "\n")
	})
}

// replyEntry is the agent's words, drawn as markdown.
func (h *home) replyEntry(text string) tea.Cmd {
	st := h.st
	return h.printBlock(agentLines(text), func(w int, _ bool) string {
		return st.renderMarkdown(text, w-1, 2)
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

// toolEntry is one tool step: the call, never its output (Öge's events
// carry none). bad marks a step the host denied.
func (h *home) toolEntry(tool, target string, bad bool) tea.Cmd {
	st := h.st
	raw := "  ● " + strings.TrimSpace(tool+" "+target)
	return h.printBlock(raw, func(w int, expand bool) string {
		dot := st.accent("●")
		if bad {
			dot = st.bad("●")
		}
		if toolKind(tool) == toolShell && target != "" && !bad {
			return shellBox(st, tool, target, w, expand)
		}
		line := "  " + dot + " " + st.bold(clean(tool))
		if target != "" {
			line += " " + st.dim(target)
		}
		return ansi.Truncate(line, max(w-1, 8), "…")
	})
}

// shellBox draws a command in a rounded box under the tool's name. A long
// command is cut to a few rows unless the human expanded the output.
func shellBox(st styles, tool, cmd string, width int, expand bool) string {
	title := " " + strings.ToLower(clean(tool)) + " "
	inner := min(max(ansi.StringWidth(cmd)+2, ansi.StringWidth(title)+4, 20), max(width-6, 12))
	lines := strings.Split(ansi.Wrap("$ "+cmd, inner, ""), "\n")
	const collapsed = 4
	if !expand && len(lines) > collapsed {
		more := len(lines) - collapsed
		lines = append(lines[:collapsed:collapsed], fmt.Sprintf("… %d more lines · ctrl+o", more))
		lines[collapsed] = st.dim(lines[collapsed])
	}
	b := st.border
	var out []string
	out = append(out, "  "+b("╭─")+st.dim(title)+b(strings.Repeat("─", max(inner+1-ansi.StringWidth(title)-1, 0))+"╮"))
	for _, l := range lines {
		pad := strings.Repeat(" ", max(inner-ansi.StringWidth(l), 0))
		out = append(out, "  "+b("│")+" "+l+pad+b("│"))
	}
	out = append(out, "  "+b("╰"+strings.Repeat("─", inner+1)+"╯"))
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
		path     string
		add, del int
		body     []string
	}
	var files []*file
	var cur *file
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			cur = &file{path: gitHeaderPath(l[len("diff --git "):])}
			files = append(files, cur)
		case cur == nil:
			continue
		case strings.HasPrefix(l, "index "), strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "),
			strings.HasPrefix(l, "new file mode"), strings.HasPrefix(l, "deleted file mode"),
			strings.HasPrefix(l, "similarity index"):
			continue
		case strings.HasPrefix(l, "+"):
			cur.add++
			cur.body = append(cur.body, l)
		case strings.HasPrefix(l, "-"):
			cur.del++
			cur.body = append(cur.body, l)
		default:
			cur.body = append(cur.body, l)
		}
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
		out = append(out, "  "+st.bold(clean(f.path))+" "+counts(st, f.add, f.del))
		for _, l := range f.body {
			if budget == 0 {
				hidden++
				continue
			}
			budget--
			var s string
			switch {
			case strings.HasPrefix(l, "@@"):
				s = st.dim(l)
			default:
				s = st.diffLine(l)
			}
			out = append(out, ansi.Truncate("    "+s, max(width-1, 8), "…"))
		}
	}
	if hidden > 0 {
		out = append(out, st.dim(fmt.Sprintf("    … %d more lines · ctrl+o to expand", hidden)))
	}
	if len(files) > 1 {
		out = append(out, "  "+st.dim(plural(len(files), "file")+" ")+counts(st, add, del))
	}
	return strings.Join(out, "\n")
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
