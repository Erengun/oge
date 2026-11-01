package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// twoRepoHome is deskHome in repo a, whose repoFor opens repo b: two git
// repos in a temp dir, each with a fake shell runner that says where it
// ran.
func twoRepoHome(t *testing.T) (h *home, a, b string, ran *[]string) {
	t.Helper()
	dir := t.TempDir()
	a, b = filepath.Join(dir, "alpha"), filepath.Join(dir, "beta")
	for _, r := range []string{a, b} {
		mkRepo(t, r)
	}
	ran = &[]string{}
	h = deskHome()
	h.root, h.title = a, "oge · alpha · main"
	h.hist = &history{}
	h.shell = func(_ context.Context, root, _ string) shellResult {
		*ran = append(*ran, root)
		return shellResult{}
	}
	opened := 0
	h.repoFor = func(root string) (*repoCtx, []string) {
		opened++
		if opened > 1 {
			t.Errorf("repo %s opened %d times", root, opened)
		}
		return &repoCtx{root: root, title: "oge · " + filepath.Base(root) + " · trunk", hist: &history{}, hc: &homeChat{}}, nil
	}
	return h, a, b, ran
}

// /new <path> opens a Chat in that repo, numbered next; the header and
// !shell follow the focused Chat, and /new alone keeps the focused
// Chat's repo.
func TestNewPathOpensAChatInThatRepo(t *testing.T) {
	h, a, b, ran := twoRepoHome(t)
	first := h.chatView
	submit(h, "/new "+b)
	if len(h.views) != 2 || h.chatView == first || h.num != 2 {
		t.Fatalf("views %v, focused %d", nums(h), h.num)
	}
	if h.root != repoRoot(t, b) || first.root != a {
		t.Fatalf("roots: focused %q, first %q", h.root, first.root)
	}
	if h.chat == first.chat || h.chat == nil {
		t.Errorf("the new Chat isn't one of its own: %v", h.chat)
	}
	if f := ansi.Strip(h.header(200)); !strings.Contains(f, "oge · beta · trunk") || strings.Contains(f, "alpha") {
		t.Errorf("the header doesn't follow the focus to beta:\n%s", f)
	}
	runShellLine(h, "!pwd")
	h.switchTo(first)
	if f := ansi.Strip(h.header(200)); !strings.Contains(f, "oge · alpha · main") {
		t.Errorf("the header doesn't follow the focus back to alpha:\n%s", f)
	}
	runShellLine(h, "!pwd")
	if len(*ran) != 2 || (*ran)[0] != repoRoot(t, b) || (*ran)[1] != a {
		t.Errorf("!pwd ran in %q, want beta then alpha", *ran)
	}
	// Back in beta, /new alone stays in beta.
	h.switchTo(h.viewNum(2))
	h.chat.c = first.chat.get() // opened: /new opens another rather than starting afresh
	submit(h, "/new")
	if len(h.views) != 3 || h.root != repoRoot(t, b) {
		t.Errorf("/new alone left beta: views %v, root %q", nums(h), h.root)
	}
}

// /new refuses a path that doesn't exist, isn't a directory or isn't in
// a git repository, in plain words, and opens nothing.
func TestNewPathRefusesWhatIsNoRepo(t *testing.T) {
	h, _, _, _ := twoRepoHome(t)
	plain := t.TempDir()
	file := filepath.Join(plain, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ arg, want string }{
		{"nonexistent", "/new: nonexistent doesn't exist"},
		{plain, "is not inside a git repository"},
		{file, "is not a directory"},
	} {
		h.msg = nil
		h.comp.set("")
		submit(h, "/new "+tc.arg)
		if len(h.views) != 1 || h.num != 1 {
			t.Errorf("/new %s opened a Chat: %v", tc.arg, nums(h))
		}
		if got := strings.Join(h.msg, "\n"); !strings.Contains(got, tc.want) {
			t.Errorf("/new %s says %q, want %q", tc.arg, got, tc.want)
		}
	}
}

// ~ is the home directory; a relative path is from the focused Chat's
// repo; a path inside a repo is that repo.
func TestProjectRootResolves(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mkRepo(t, a)
	mkRepo(t, b)
	if err := os.MkdirAll(filepath.Join(b, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "HOME" {
			return dir
		}
		return ""
	}
	for _, p := range []string{"~/b", "../b", filepath.Join(b, "sub"), "../b/sub"} {
		if got, problem := projectRoot(p, a, getenv); problem != "" || got != repoRoot(t, b) {
			t.Errorf("projectRoot(%q) = %q, %q; want %q", p, got, problem, repoRoot(t, b))
		}
	}
}

// repoRoot is the top level git reports for dir: what the home keys a
// repo by (symlinks resolved, as on macOS's /var).
func repoRoot(t *testing.T, dir string) string {
	t.Helper()
	root, problem := projectRoot(dir, "/", nil)
	if problem != "" {
		t.Fatal(problem)
	}
	return root
}

// mkRepo makes an empty git repo at dir.
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, dir)
}
