package cli

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/workspace"
)

// The diff viewer (#169): /diff as an overlay, a file list and one file's
// diff. It shows what oge diff shows and nothing else: its text is the
// patch delivery.Diff returns (the held-out files are never in it; held
// back and Ambiguous files are marked as the CLI marks them), split per
// file. It computes no diff of its own.

// diffFile is one file of the patch.
type diffFile struct {
	path   string
	status string // added, deleted, renamed, binary or modified; " · held back" when marked
	add    int
	del    int
	text   string // the file's part of the patch, marks included
}

// splitPatch cuts a patch, as delivery.Diff returns it, into its files.
// A "# Öge:" line belongs to the file after it.
func splitPatch(patch string) []diffFile {
	var files []diffFile
	var cur *diffFile
	var pending []string
	inHunk, held, sub := false, false, false
	flush := func() {
		if cur != nil {
			cur.text = strings.TrimSuffix(cur.text, "\n")
			files = append(files, *cur)
			cur = nil
		}
	}
	for _, l := range strings.SplitAfter(patch, "\n") {
		body := strings.TrimSuffix(l, "\n")
		switch {
		case strings.HasPrefix(body, "# Öge:"):
			flush()
			pending = append(pending, l)
			held = true
			sub = sub || strings.Contains(body, " is a submodule (gitlink)")
			continue
		case strings.HasPrefix(body, "diff --git "):
			flush()
			cur = &diffFile{path: gitHeaderPath(body[len("diff --git "):]), status: "modified", text: strings.Join(pending, "")}
			switch {
			case sub:
				cur.status = "submodule · not delivered"
			case held:
				cur.status = "held back"
			}
			pending, inHunk, held, sub = nil, false, false, false
		case cur == nil:
			continue
		case strings.HasPrefix(body, "@@"):
			inHunk = true
		case !inHunk:
			if s := headerStatus(body); s != "" && cur.status == "modified" {
				cur.status = s
			}
			switch {
			case strings.HasPrefix(body, "rename to "):
				cur.path = body[len("rename to "):]
			case strings.HasPrefix(body, "+++ b/"):
				cur.path = body[len("+++ b/"):]
			case strings.HasPrefix(body, "--- a/") && !strings.Contains(cur.text, "\n+++ b/"):
				cur.path = body[len("--- a/"):]
			}
		case strings.HasPrefix(body, "+"):
			cur.add++
		case strings.HasPrefix(body, "-"):
			cur.del++
		}
		if cur != nil {
			cur.text += l
		}
	}
	flush()
	return files
}

// gitHeaderPath is the path of a "diff --git a/P b/P" line's rest. Git
// does not quote spaces, so a name holding " b/" is ambiguous when split on
// it; the two halves of an unrenamed file are equal, which is not. When no
// split is certain (a rename), the whole rest is shown, and the later
// header lines (+++, rename to) give the path where they can.
func gitHeaderPath(rest string) string {
	if n := len(rest); n%2 == 1 {
		a, b := rest[:n/2], rest[n/2+1:]
		if strings.HasPrefix(a, "a/") && strings.HasPrefix(b, "b/") && a[2:] == b[2:] && rest[n/2] == ' ' {
			return a[2:]
		}
	}
	return rest
}

func headerStatus(l string) string {
	switch {
	case strings.HasPrefix(l, "new file mode"):
		return "added"
	case strings.HasPrefix(l, "deleted file mode"):
		return "deleted"
	case strings.HasPrefix(l, "rename from"):
		return "renamed"
	case strings.HasPrefix(l, "Binary files"), strings.HasPrefix(l, "GIT binary patch"):
		return "binary"
	}
	return ""
}

// diffLoader is home.diffOf: the Candidate of a Run (the repository's
// latest for "") as files, from the same delivery.Diff that oge diff
// prints. ok is false when there is nothing to show as a list (no Run, a
// refusal, an empty change): the printed /diff then says why.
func diffLoader(env Env) func(id string) (head string, files []diffFile, ok bool) {
	return func(id string) (string, []diffFile, bool) {
		root, err := workspace.RepoRoot(env.Dir)
		if err != nil {
			return "", nil, false
		}
		q := env
		q.Stdout, q.Stderr = io.Discard, io.Discard
		r, code := findRun(q, root, id)
		if code != ExitOK {
			return "", nil, false
		}
		patch, err := delivery.Diff(r)
		if err != nil || len(patch) == 0 {
			return "", nil, false
		}
		files := splitPatch(delivery.Clean(patch))
		return diffHead(r), files, len(files) > 0
	}
}

// diffCmd is /diff: the viewer when it can open, else the CLI's own
// printed diff (flags, a short terminal, nothing to list).
func (h *home) diffCmd(args []string) (tea.Cmd, string) {
	printed := deliverSlash("d", "diff")
	id := ""
	switch {
	case len(args) > 1:
		return printed(h, args)
	case len(args) == 1:
		if strings.HasPrefix(args[0], "-") {
			return printed(h, args)
		}
		id = args[0]
	case h.last != nil:
		id = h.last.id
	}
	if !h.openDiff(id) {
		return printed(h, args)
	}
	return nil, ""
}

// diffMinHeight is the shortest terminal the viewer opens in; below it the
// diff is printed or paged as before.
var diffMinHeight = pickerMinHeight

// openDiff opens the viewer on a Run (the repository's latest for ""), and
// says whether it could; when it can't, the caller prints or pages.
func (h *home) openDiff(id string) bool {
	if h.diffOf == nil || h.height < diffMinHeight {
		return false
	}
	head, files, ok := h.diffOf(id)
	if !ok {
		return false
	}
	h.diff = newDiffViewer(head, files, h.st, h.width, h.height)
	return true
}

type diffViewer struct {
	st      styles
	head    string
	files   []diffFile
	sel     int
	top     int  // the first file shown in the list
	viewing bool // a file's diff is open
	vp      viewport.Model
	rows    int // list rows, and the viewport's height
}

func newDiffViewer(head string, files []diffFile, st styles, width, height int) *diffViewer {
	rows := min(max(height-8, 3), 16)
	if width < 20 {
		width = 20
	}
	return &diffViewer{st: st, head: head, files: files, rows: rows,
		vp: viewport.New(viewport.WithWidth(width-2), viewport.WithHeight(rows))}
}

// resize follows the terminal.
func (d *diffViewer) resize(width int) {
	d.vp.SetWidth(max(width, 20) - 2)
}

// open shows the selected file's diff.
func (d *diffViewer) open() {
	f := d.files[d.sel]
	var b []string
	for _, l := range strings.Split(f.text, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "), strings.HasPrefix(l, "# Öge:"):
			l = d.st.bold(l)
		case strings.HasPrefix(l, "@@"):
			l = d.st.accent(l)
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++ "):
			l = d.st.ok(l)
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "--- "):
			l = d.st.bad(l)
		}
		b = append(b, l)
	}
	d.vp.SoftWrap = true
	shown := 0 // the lines once wrapped
	for _, l := range b {
		shown += max(1, (lipgloss.Width(l)+d.vp.Width()-1)/d.vp.Width())
	}
	d.vp.SetHeight(min(d.rows, shown))
	d.vp.SetContentLines(b)
	d.vp.GotoTop()
	d.viewing = true
}

// diffKey is a key (or a wheel tick, as Up or Down) while the viewer is open.
func (h *home) diffKey(msg tea.KeyPressMsg) tea.Cmd {
	d := h.diff
	key := msg.String()
	if key == "esc" || key == "ctrl+c" {
		if d.viewing {
			d.viewing = false
		} else {
			h.diff = nil
		}
		return nil
	}
	if d.viewing {
		switch key {
		case "up", "k", "ctrl+p":
			d.vp.ScrollUp(1)
		case "down", "j", "ctrl+n":
			d.vp.ScrollDown(1)
		case "pgup", "b":
			d.vp.PageUp()
		case "pgdown", "space", "f":
			d.vp.PageDown()
		case "home", "g":
			d.vp.GotoTop()
		case "end", "G":
			d.vp.GotoBottom()
		}
		return nil
	}
	switch key {
	case "up", "k", "ctrl+p":
		d.sel = max(d.sel-1, 0)
	case "down", "j", "ctrl+n":
		d.sel = min(d.sel+1, len(d.files)-1)
	case "home", "g":
		d.sel = 0
	case "end", "G":
		d.sel = len(d.files) - 1
	case "enter":
		d.open()
	}
	if d.sel < d.top {
		d.top = d.sel
	}
	if d.sel >= d.top+d.rows {
		d.top = d.sel - d.rows + 1
	}
	return nil
}

// lines draws the viewer under the box.
func (d *diffViewer) lines() []string {
	st := d.st
	out := []string{" " + st.dim(clean(d.head))}
	if d.viewing {
		f := d.files[d.sel]
		out = append(out, " "+st.bold(clean(f.path))+"  "+st.dim(fmt.Sprintf("+%d −%d · %s", f.add, f.del, f.status)))
		for _, l := range strings.Split(d.vp.View(), "\n") {
			out = append(out, " "+strings.TrimRight(l, " "))
		}
		out = append(out, " "+st.dim(fmt.Sprintf("↑↓ pgup pgdn scroll · %d%% · esc back", int(d.vp.ScrollPercent()*100))))
		return out
	}
	w := 0
	for _, f := range d.files {
		w = max(w, len([]rune(clean(f.path))))
	}
	for i := d.top; i < min(d.top+d.rows, len(d.files)); i++ {
		f := d.files[i]
		line := fmt.Sprintf("%-*s  +%d −%d  %s", w, clean(f.path), f.add, f.del, f.status)
		if i == d.sel {
			out = append(out, " "+st.accent("> ")+line)
		} else {
			out = append(out, "   "+line)
		}
	}
	out = append(out, " "+st.dim(fmt.Sprintf("%d files · ↑↓ move · ⏎ open · esc close", len(d.files))))
	return out
}
