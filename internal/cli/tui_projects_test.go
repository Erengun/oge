package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/pipeline"
)

// reposHome is a 120-column home on repo "alpha" whose repoFor opens any
// repo it is asked for, each once. dir holds the repos made by mk.
func reposHome(t *testing.T, names ...string) (h *home, roots map[string]string) {
	t.Helper()
	dir := t.TempDir()
	roots = map[string]string{}
	for _, n := range names {
		mkRepo(t, filepath.Join(dir, n))
		roots[n] = repoRoot(t, filepath.Join(dir, n))
	}
	h, _ = railHome(120, 1)
	h.root, h.title = roots[names[0]], "oge · "+names[0]+" · main"
	h.hist = &history{}
	h.chat = &homeChat{}
	made := map[string]*repoCtx{}
	h.repoFor = func(root string) (*repoCtx, []string) {
		k := repoKey(root)
		if made[k] == nil {
			made[k] = &repoCtx{root: root, title: "oge · " + filepath.Base(root), hist: &history{}, hc: &homeChat{}}
		}
		return made[k], nil
	}
	h.getenv = func(string) string { return "" }
	return h, roots
}

// pin sets [desk] projects, before the home has read them.
func pin(h *home, paths []string) { h.deskProjects, h.pins, h.pinsRead = paths, nil, false }

func railText(h *home) string { return strings.Join(railCol(h), "\n") }

func TestSingleProjectRailShowsNoHeader(t *testing.T) {
	h, _ := reposHome(t, "alpha")
	h.views = append(h.views, &chatView{repoCtx: h.repoCtx, num: 2, comp: newComposer(), tr: &transcript{}})
	if got := railText(h); strings.Contains(got, "alpha") || !strings.Contains(got, "DESK") {
		t.Errorf("one project drew a header:\n%s", got)
	}
}

// Two repos: a header each, Chats under their repo with global numbers,
// first-opened first, and nothing moves as states change, a Chat closes
// or a third project joins.
func TestRailGroupsByProjectAndNeverReorders(t *testing.T) {
	h, roots := reposHome(t, "alpha", "beta", "gamma")
	a1 := h.chatView
	a1.name = "one"
	h.chat = &homeChat{c: nil}
	add := func(root, name string) *chatView {
		rc, _ := h.repoFor(root)
		v := h.newViewIn(rc)
		v.name = name
		h.views = append(h.views, v)
		return v
	}
	add(roots["beta"], "two")
	add(roots["alpha"], "three")
	add(roots["beta"], "four")
	order := func() []string {
		var out []string
		for _, l := range railCol(h)[1:] {
			l = strings.TrimSpace(strings.Trim(l, "│ "))
			for _, w := range []string{"alpha", "beta", "gamma"} {
				if l == w {
					out = append(out, w)
				}
			}
		}
		return out
	}
	if got := order(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("headers %v\n%s", got, railText(h))
	}
	if got := railNums(h); !slices.Equal(got, []int{1, 3, 2, 4}) {
		t.Fatalf("rail numbers %v\n%s", got, railText(h))
	}
	// State changes: beta's Chat 2 asks, alpha's 1 goes quiet: same shape.
	h.views[1].turn = &chatTurn{}
	if got := railNums(h); !slices.Equal(got, []int{1, 3, 2, 4}) {
		t.Errorf("after a state change %v", got)
	}
	// Closing alpha's first Chat keeps alpha above beta.
	h.views = slices.Delete(h.views, 0, 1)
	if got := railNums(h); !slices.Equal(got, []int{3, 2, 4}) || !slices.Equal(order(), []string{"alpha", "beta"}) {
		t.Errorf("after closing 1: %v, %v", got, order())
	}
	// A new project goes last.
	add(roots["gamma"], "five")
	if got := order(); !slices.Equal(got, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("after a third project: %v\n%s", got, railText(h))
	}
	// Even when alpha's Chats are all gone and one comes back: same place.
	if got := railNums(h); !slices.Equal(got, []int{3, 2, 4, 5}) {
		t.Errorf("numbers %v", got)
	}
}

func TestProjectLabelsDisambiguate(t *testing.T) {
	got := projectLabels([]string{"/w/api", "/p/api", "/p/web"})
	if got[0] != "api·w" || got[1] != "api·p" || got[2] != "web" {
		t.Errorf("labels %q", got)
	}
}

// A pinned project with no Chat is a dim + row; clicking it opens a Chat
// there, and the row gives way to its header. No colour paints nothing.
func TestPinnedProjectRowOpensAChatOnClick(t *testing.T) {
	h, roots := reposHome(t, "alpha", "beta")
	pin(h, []string{roots["beta"], filepath.Join(t.TempDir(), "gone")})
	if !h.railOn() {
		t.Fatal("a pinned project with no Chat shows no rail")
	}
	got := railText(h)
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "+ beta") || strings.Contains(got, "gone") {
		t.Fatalf("rail:\n%s", got)
	}
	if strings.Contains(h.View().Content, "\x1b") {
		t.Errorf("NO_COLOR rail painted:\n%q", h.View().Content)
	}
	y := slices.IndexFunc(railCol(h), func(l string) bool { return strings.Contains(l, "+ beta") })
	h.chat = &homeChat{c: nil}
	h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: y})
	if h.root != roots["beta"] {
		t.Fatalf("click didn't open beta: focused %q, views %v", h.root, nums(h))
	}
	if got := railText(h); strings.Contains(got, "+ beta") {
		t.Errorf("the + row stayed once beta has a Chat:\n%s", got)
	}
}

func ledgerOf(t *testing.T, state, kind, id, source string) {
	t.Helper()
	d := filepath.Join(state, "private", kind, id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"format":1,"seq":0,"type":"X","data":{"source":"` + source + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(d, "ledger.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The picker lists pinned then recent repos (Runs and Chats), each once,
// skips paths that are gone or not repos, and Enter opens a Chat there.
// It fits 80 columns.
func TestProjectsPicker(t *testing.T) {
	h, roots := reposHome(t, "alpha", "beta", "gamma", "delta")
	h.full, h.termW = true, 80
	state := t.TempDir()
	notRepo := t.TempDir()
	h.getenv = func(k string) string {
		if k == "OGE_STATE_DIR" {
			return state
		}
		return ""
	}
	pin(h, []string{roots["delta"]})
	ledgerOf(t, state, "runs", "20261001T000000-aa", roots["beta"])
	ledgerOf(t, state, "chats", "20261002T000000-bb", roots["gamma"])
	ledgerOf(t, state, "runs", "20261003T000000-cc", filepath.Join(notRepo, "gone"))
	ledgerOf(t, state, "runs", "20261004T000000-dd", notRepo)
	ledgerOf(t, state, "chats", "20261005T000000-ee", roots["beta"])
	submit(h, "/projects")
	if h.chats == nil || !h.projectPicker() {
		t.Fatalf("no picker; msg %v", h.msg)
	}
	var got []string
	for _, it := range h.chats.Items() {
		got = append(got, it.(projectItem).root)
	}
	want := []string{roots["delta"], roots["beta"], roots["gamma"]}
	if !slices.Equal(got, want) {
		t.Fatalf("picker lists %q, want %q", got, want)
	}
	h.width = 80
	for _, l := range h.chatsLines() {
		if ansi.StringWidth(l) > 80 {
			t.Errorf("line over 80 columns: %q", l)
		}
	}
	if v := ansi.Strip(h.View().Content); !strings.Contains(v, "Projects") || !strings.Contains(v, "pinned") {
		t.Errorf("picker view:\n%s", v)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	h.chat = &homeChat{c: nil}
	h.Update(enter())
	if h.chats != nil || h.root != roots["beta"] {
		t.Errorf("Enter opened %q, picker %v", h.root, h.chats != nil)
	}
	// Esc closes it without opening anything.
	submit(h, "/projects")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.chats != nil {
		t.Error("Esc left the picker open")
	}
}

func TestNewThenTabOpensThePicker(t *testing.T) {
	h, roots := reposHome(t, "alpha", "beta")
	pin(h, []string{roots["beta"]})
	typeText(h, "/new ")
	h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !h.projectPicker() {
		t.Fatalf("no project picker; msg %v", h.msg)
	}
}

func TestNewTypedWholeThenTabOpensThePicker(t *testing.T) {
	h, roots := reposHome(t, "alpha", "beta")
	pin(h, []string{roots["beta"]})
	typeText(h, "/new") // the slash menu is open on /new
	h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !h.projectPicker() {
		t.Fatalf("no project picker; msg %v", h.msg)
	}
}

func TestOrderIsFirstOpenedWithoutDrawingTheRail(t *testing.T) {
	h, roots := reposHome(t, "alpha", "beta")
	h.place(roots["alpha"])
	h.place(roots["beta"])
	h.place(roots["alpha"])
	if want := []string{repoKey(roots["alpha"]), repoKey(roots["beta"])}; !slices.Equal(h.order, want) {
		t.Errorf("order %v want %v", h.order, want)
	}
}

func TestPinnedRowKeepsItsSlot(t *testing.T) {
	h, roots := reposHome(t, "alpha", "p1", "p2")
	pin(h, []string{roots["p1"], roots["p2"]})
	h.place(roots["alpha"])
	_, got := h.railGroups()
	want := []string{roots["alpha"], roots["p1"], roots["p2"]}
	if !slices.Equal(got, want) {
		t.Fatalf("slots %v want %v", got, want)
	}
	h.views = append(h.views, &chatView{repoCtx: &repoCtx{root: roots["p2"]}, num: 2, comp: newComposer(), tr: &transcript{}})
	_, got = h.railGroups()
	if !slices.Equal(got, want) {
		t.Errorf("opening P2 moved slots: %v", got)
	}
}

func TestPinInsideAnotherRepoIsLeftOut(t *testing.T) {
	h, roots := reposHome(t, "alpha")
	sub := filepath.Join(roots["alpha"], "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	pin(h, []string{sub})
	if got := h.pinned(); len(got) != 0 {
		t.Errorf("pinned %v", got)
	}
}

// A pin spelled in another case than the directory on disk is still the
// repo on a case-insensitive filesystem (macOS by default).
func TestPinInAnotherCaseIsKept(t *testing.T) {
	h, roots := reposHome(t, "Alpha")
	other := filepath.Join(filepath.Dir(roots["Alpha"]), "alpha")
	if _, err := os.Stat(other); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	pin(h, []string{other})
	if got := h.pinned(); len(got) != 1 {
		t.Errorf("pinned %v, want the repo", got)
	}
}

func TestProjectsWithNothingSaysSo(t *testing.T) {
	h, _ := reposHome(t, "alpha")
	submit(h, "/projects")
	if h.chats != nil || !strings.Contains(strings.Join(h.msg, " "), "no recent repos") {
		t.Errorf("picker %v, msg %v", h.chats != nil, h.msg)
	}
}

// With nothing to pick, /new and Tab keep "/new " for a typed path.
func TestNewThenTabWithNothingKeepsTheBox(t *testing.T) {
	for _, typed := range []string{"/new ", "/new"} {
		h, _ := reposHome(t, "alpha")
		typeText(h, typed)
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		if got := h.comp.text(); got != "/new " || h.projectPicker() {
			t.Errorf("%q then Tab: box %q, picker %v", typed, got, h.projectPicker())
		}
	}
}

func TestDeskProjectsConfig(t *testing.T) {
	c := &pipeline.UserConfig{Desk: &pipeline.DeskConfig{Projects: []string{"~/dev/api", "/work/web", "/work/web"}}}
	if got := c.DeskProjects("/home/u"); !slices.Equal(got, []string{"/home/u/dev/api", "/work/web"}) {
		t.Errorf("DeskProjects %q", got)
	}
	c.Desk.Projects = []string{"rel/path", ""}
	if len(c.DeskProjects("/home/u")) != 0 {
		t.Error("relative paths kept")
	}
}
