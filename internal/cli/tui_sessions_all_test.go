package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/agent"
)

// allSessionsHome has three repos (alpha on the Desk, beta, gamma), a linked
// worktree of beta, and a directory that is no repo. Discovery is faked at
// the files, but grouping is the real code.
func allSessionsHome(t *testing.T) (h *home, roots map[string]string, wt, plain string) {
	t.Helper()
	h, roots = reposHome(t, "alpha", "beta", "gamma")
	for _, n := range []string{"alpha", "beta", "gamma"} {
		gitIn(t, roots[n], "commit", "-q", "--allow-empty", "-m", "init")
	}
	base := t.TempDir()
	wt = filepath.Join(base, "beta-wt")
	gitIn(t, roots["beta"], "worktree", "add", "-q", "-b", "wt", wt)
	plain = filepath.Join(base, "scratch")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	h.discover = func(_ context.Context, rs []string, all bool) sessionFound {
		sessions := []agent.NativeSession{
			{Provider: "claude", Profile: "claude", ID: "a1", Title: "alpha work", LastActive: ago(30), Cwd: roots["alpha"]},
			{Provider: "claude", Profile: "claude@default", ID: "b1", Title: "beta work", LastActive: ago(5), Cwd: roots["beta"]},
			{Provider: "codex", ID: "b2", Title: "beta from a worktree", LastActive: ago(2), Cwd: wt},
			{Provider: "claude", Profile: "claude", ID: "g1", Title: "gamma work", LastActive: ago(60), Cwd: roots["gamma"]},
			{Provider: "claude", Profile: "claude", ID: "p1", Title: "scratch work", LastActive: ago(120), Cwd: plain},
		}
		if !all {
			return sessionFound{sessions: sessionsInProjects(sessions, rs)}
		}
		var f sessionFound
		f.sessions, f.viewOnly = sessionsByProject(sessions)
		return f
	}
	return h, roots, wt, plain
}

func TestSessionsAllGroupsEveryProjectAndMarksTheDesk(t *testing.T) {
	h, _, _, _ := allSessionsHome(t)
	slash(h, "/sessions")
	got := screenText(h)
	if !strings.Contains(got, "alpha work") || strings.Contains(got, "beta work") || strings.Contains(got, "gamma work") || strings.Contains(got, " · on the Desk") {
		t.Errorf("/sessions is not the focused project alone:\n%s", got)
	}
	h.chats, h.catalog = nil, nil
	slash(h, "/sessions all")
	got = screenText(h)
	// Projects by their newest session: beta (2m, worktree folded in), alpha, gamma, scratch.
	order := []string{"beta", "beta from a worktree", "beta work", "alpha · on the Desk", "alpha work", "gamma", "gamma work", "scratch · view only", "scratch work"}
	last := -1
	for _, w := range order {
		i := strings.Index(got[max(last, 0):], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order:\n%s", w, got)
		}
		last += i + 1
	}
	if strings.Contains(got, "beta · on the Desk") || strings.Contains(got, "beta-wt") {
		t.Errorf("beta is not on the Desk, and its worktree is not a project:\n%s", got)
	}
}

func TestSessionsAllWorktreeSessionListsUnderItsMainRepo(t *testing.T) {
	_, roots, wt, _ := allSessionsHome(t)
	got, _ := sessionsByProject([]agent.NativeSession{{ID: "x", Cwd: wt}})
	if len(got) != 1 || repoKey(got[0].Project) != repoKey(roots["beta"]) {
		t.Fatalf("project = %q, want %q", got[0].Project, roots["beta"])
	}
	in := sessionsInProjects([]agent.NativeSession{{ID: "x", Cwd: wt}}, []string{roots["beta"]})
	if len(in) != 1 || in[0].Project != roots["beta"] {
		t.Fatalf("not folded into the Desk's beta: %+v", in)
	}
}

func TestSessionsAllAddFromANonDeskProjectAddsTheProject(t *testing.T) {
	h, roots, _, _ := allSessionsHome(t)
	slash(h, "/sessions all")
	// The selection starts on the newest row: beta's worktree session.
	if _, roots2 := h.railGroups(); len(roots2) != 0 && slicesHasRoot(roots2, roots["beta"]) {
		t.Fatal("beta was on the Desk already")
	}
	typeText(h, "a")
	if got := screenText(h); !strings.Contains(got, "added to the Desk") {
		t.Fatalf("not added:\n%s", got)
	}
	if _, rs := h.railGroups(); !slicesHasRoot(rs, roots["beta"]) {
		t.Errorf("beta is not a Desk project after adding its session: %v", rs)
	}
	if len(h.onDesk) != 1 || repoKey(h.onDesk[0].repo) != repoKey(roots["beta"]) {
		t.Errorf("onDesk = %+v", h.onDesk)
	}
}

func TestSessionsAllNonRepoProjectIsViewOnly(t *testing.T) {
	h, _, _, _ := allSessionsHome(t)
	slash(h, "/sessions all")
	for range 8 { // down to the last row
		h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	typeText(h, "a")
	got := screenText(h)
	if !strings.Contains(got, "scratch work") || !strings.Contains(got, "not a git repo: resume it from there") {
		t.Errorf("no view-only reason:\n%s", got)
	}
	if len(h.onDesk) != 0 {
		t.Errorf("a non-repo session was put on the Desk: %+v", h.onDesk)
	}
}

func slicesHasRoot(rs []string, root string) bool {
	for _, r := range rs {
		if repoKey(r) == repoKey(root) {
			return true
		}
	}
	return false
}

// A codex thread started in a worktree comes back from codex only when it
// is listed whole, so discovery folds it on the client in both modes, and
// the ◌ row it becomes can be looked up again.
func TestDiscoverFoldsCodexWorktreeThreadsOnTheClient(t *testing.T) {
	_, roots, wt, _ := allSessionsHome(t)
	sub := filepath.Join(roots["beta"], "internal")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	listed := []agent.NativeSession{
		{Provider: "codex", ID: "w1", Title: "in a worktree", Cwd: wt},
		{Provider: "codex", ID: "w2", Title: "in a subdirectory", Cwd: sub},
	}
	find := discoverWith(func(string) string { return "" }, func(context.Context) ([]agent.NativeSession, string) { return listed, "" })
	for _, all := range []bool{false, true} {
		f := find(context.Background(), []string{roots["beta"]}, all)
		n := 0
		for _, s := range f.sessions {
			if s.Provider == "codex" && repoKey(s.Project) == repoKey(roots["beta"]) {
				n++
			}
		}
		if n != 2 { // the worktree thread and the subdirectory one
			t.Errorf("all=%v: %d codex sessions under beta, want 2: %+v", all, n, f.sessions)
		}
	}
}

func TestAddSessionKeepsTheDeskRootOfAWorktree(t *testing.T) {
	h, roots, wt, _ := allSessionsHome(t)
	h.onDesk = append(h.onDesk, deskSession{provider: "claude", id: "x", repo: wt})
	if got := h.deskRootFor(roots["beta"]); got != wt {
		t.Fatalf("deskRootFor = %q, want the worktree %q", got, wt)
	}
	h.addSession(agent.NativeSession{Provider: "claude", ID: "y", Project: roots["beta"]})
	if got := h.onDesk[len(h.onDesk)-1].repo; got != wt {
		t.Errorf("repo = %q, want %q: a second rail slot for the same project", got, wt)
	}
}
