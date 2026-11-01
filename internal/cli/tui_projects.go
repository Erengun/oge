package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/workspace"
)

// Projects on the Desk (docs/ux/desk.md, Projects P1 and P2): the rail
// groups Chats by repo, [desk] projects pins repos with no Chat, and
// /projects (or /new then Tab) picks a repo to open a Chat in.

// gitRoot is the top level of the repo at path, "" when path is gone or is
// not inside one.
func gitRoot(path string) string {
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return ""
	}
	root, err := workspace.RepoRoot(path)
	if err != nil {
		return ""
	}
	return root
}

// pinned is [desk] projects that are git repos now, read once: the rail
// is drawn often, and a path that appears later is for the next session.
// A path inside a repo, not its top level, is left out: pinning ~/notes
// must not pin the dotfiles repo around it.
func (h *home) pinned() []string {
	if !h.pinsRead {
		h.pinsRead = true
		for _, p := range h.deskProjects {
			if r := gitRoot(p); r != "" && repoKey(r) == repoKey(p) && !slices.ContainsFunc(h.pins, func(q string) bool { return repoKey(q) == repoKey(r) }) {
				h.pins = append(h.pins, r)
			}
		}
	}
	return h.pins
}

// opened reports whether a Chat is open in the repo at root.
func (h *home) opened(root string) bool {
	k := repoKey(root)
	return slices.ContainsFunc(h.views, func(v *chatView) bool { return repoKey(v.root) == k })
}

// unopened is the pinned projects with no Chat open, in config order.
func (h *home) unopened() []string {
	var out []string
	for _, r := range h.pinned() {
		if !h.opened(r) {
			out = append(out, r)
		}
	}
	return out
}

// place records root's repo in h.order when a Chat first opens there. The
// first call also seeds the pinned projects after it, in config order, so
// a "+ name" row and its later group share one slot that never moves.
func (h *home) place(root string) {
	add := func(r string) {
		if k := repoKey(r); !slices.Contains(h.order, k) {
			h.order = append(h.order, k)
		}
	}
	if len(h.order) == 0 {
		add(root)
		for _, r := range h.pinned() {
			add(r)
		}
	}
	add(root)
}

// railGroups is one slot per repo in h.order (a repo keeps its place for
// the session): the open Chats there in number order, or nil for a pinned
// project with no Chat. roots names each slot's repo.
func (h *home) railGroups() (slots [][]*chatView, roots []string) {
	by := map[string][]*chatView{}
	rootOf := map[string]string{}
	for _, v := range h.views {
		k := repoKey(v.root)
		h.place(v.root) // already placed when a Chat opened; this is for any that was not
		by[k] = append(by[k], v)
		rootOf[k] = v.root
	}
	for _, r := range h.pinned() {
		h.place(r)
		rootOf[repoKey(r)] = r
	}
	for _, k := range h.order {
		if g := by[k]; len(g) > 0 {
			slots, roots = append(slots, g), append(roots, g[0].root)
		} else if r, ok := rootOf[k]; ok && slices.ContainsFunc(h.pinned(), func(q string) bool { return repoKey(q) == k }) {
			slots, roots = append(slots, nil), append(roots, r)
		}
	}
	return slots, roots
}

// projectLabels names each repo by its directory's name; two that share
// one add their parent directory's, and a third still alike a number.
func projectLabels(roots []string) []string {
	out := make([]string, len(roots))
	count := map[string]int{}
	for i, r := range roots {
		out[i] = filepath.Base(r)
		count[out[i]]++
	}
	seen := map[string]int{}
	for i, r := range roots {
		base := filepath.Base(r)
		if count[base] < 2 {
			continue
		}
		out[i] = base + "·" + filepath.Base(filepath.Dir(r))
		if seen[out[i]]++; seen[out[i]] > 1 {
			out[i] += fmt.Sprintf("#%d", seen[out[i]])
		}
	}
	return out
}

// openProject opens a Chat in the repo at root, as /new <path> does; what
// it refuses says so under the box.
func (h *home) openProject(root string) tea.Cmd {
	cmd, problem := h.newCmd([]string{root})
	if problem != "" {
		h.msg = append(h.msg, problem)
	}
	return cmd
}

// recentRoots are the repos of the newest Runs and Chats in the state
// root's private tree, newest first, without repeats, up to n that are
// still git repos. Only the first Ledger record of each is read.
func recentRoots(private string, n int) []string {
	type src struct{ name, root string }
	var all []src
	for _, kind := range []string{"runs", "chats"} {
		entries, err := os.ReadDir(filepath.Join(private, kind))
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, name := range names[:min(len(names), 200)] {
			if r := ledgerSource(filepath.Join(private, kind, name)); r != "" {
				all = append(all, src{name, r})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].name > all[j].name })
	var out, keys []string
	tried := 0
	for _, s := range all {
		if len(out) == n || tried >= 4*n {
			break
		}
		k := repoKey(s.root)
		if slices.Contains(keys, k) {
			continue
		}
		keys = append(keys, k)
		tried++
		if r := gitRoot(s.root); r != "" && repoKey(r) == repoKey(s.root) && !slices.ContainsFunc(out, func(o string) bool { return repoKey(o) == repoKey(r) }) {
			out = append(out, r)
		}
	}
	return out
}

// ledgerSource is the source repo the first record of the Ledger in dir
// names, "" when there is none.
func ledgerSource(dir string) string {
	f, err := os.Open(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	line, _ := bufio.NewReader(io.LimitReader(f, 1<<16)).ReadBytes('\n')
	var rec struct {
		Data struct {
			Source string `json:"source"`
		} `json:"data"`
	}
	if json.Unmarshal(line, &rec) != nil || !filepath.IsAbs(rec.Data.Source) {
		return ""
	}
	return rec.Data.Source
}

type projectItem struct {
	root, label, tag string
}

func (i projectItem) FilterValue() string { return i.label }

type projectDelegate struct{ h *home }

func (projectDelegate) Height() int                         { return 1 }
func (projectDelegate) Spacing() int                        { return 0 }
func (projectDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d projectDelegate) Render(w io.Writer, m list.Model, i int, it list.Item) {
	p := it.(projectItem)
	path := p.root
	if home := d.h.getenv("HOME"); home != "" && (path == home || strings.HasPrefix(path, home+"/")) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	line := clean(p.label)
	if p.tag != "" {
		line += "  " + d.h.st.dim(p.tag) // before the path, which is what gets cut
	}
	line += "  " + d.h.st.dim(clean(path))
	if i == m.Index() {
		fmt.Fprint(w, " "+d.h.st.accent("> ")+line)
		return
	}
	fmt.Fprint(w, "   "+line)
}

// projectPicker reports whether the open picker lists projects, not Chats.
func (h *home) projectPicker() bool {
	if h.chats == nil {
		return false
	}
	_, ok := h.chats.SelectedItem().(projectItem)
	return ok
}

// chatsTitle is the open picker's heading.
func (h *home) chatsTitle() string {
	if h.projectPicker() {
		return "Projects"
	}
	return "Chats"
}

// projectsCmd is /projects: a picker over the pinned projects and the
// recent repos, at any width. Enter opens a Chat in the one chosen.
func (h *home) projectsCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/projects")
	}
	var roots, tags []string
	add := func(root, tag string) {
		if k := repoKey(root); !slices.ContainsFunc(roots, func(r string) bool { return repoKey(r) == k }) {
			roots, tags = append(roots, root), append(tags, tag)
		}
	}
	for _, r := range h.pinned() {
		add(r, "pinned")
	}
	if h.getenv != nil {
		if dir, err := ledger.DefaultStateDir(h.getenv); err == nil {
			for _, r := range recentRoots(filepath.Join(dir, "private"), 12) {
				add(r, "")
			}
		}
	}
	if len(roots) == 0 {
		return nil, "/projects: no recent repos yet; /new <path> opens a Chat in one"
	}
	labels := projectLabels(roots)
	items := make([]list.Item, len(roots))
	for i, r := range roots {
		if h.opened(r) {
			tags[i] = strings.TrimSpace("open " + tags[i])
		}
		items[i] = projectItem{r, labels[i], tags[i]}
	}
	l := list.New(items, projectDelegate{h}, 100, min(len(items), max(h.height-8, 3))+4)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	h.chats = &l
	return nil, ""
}
