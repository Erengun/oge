package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// gitRepo is a repository's root, for a home to open Chats in.
func gitRepo(t *testing.T) string {
	t.Helper()
	root, err := workspace.RepoRoot(newRunFixture(t).repo)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// A task from oge add lands as a card: a new Chat, focus unmoved, no
// turn, an idle glyph and an Inbox card that says where it came from.
func TestDeskAddLandsAsACard(t *testing.T) {
	h := deskHome()
	h.hist, h.sel = &history{}, &setupState{}
	h.root = gitRepo(t)
	h.repoCtx.root = h.root
	first := h.chatView
	reply := make(chan deskAddReply, 1)
	h.update(deskAddMsg{repo: h.root, task: "fix the parser\nand its tests", reply: reply})
	r := <-reply
	if r.err != nil || r.n != 2 || r.name != filepath.Base(h.root) {
		t.Fatalf("reply %+v", r)
	}
	if len(h.views) != 2 || h.chatView != first {
		t.Fatalf("views %v, focus moved to %d", nums(h), h.num)
	}
	v := h.views[1]
	if v.added == "" || v.turn != nil || v.caption() != "fix the parser" {
		t.Errorf("added %q, turn %v, caption %q", v.added, v.turn != nil, v.caption())
	}
	if m, coarse := h.mark(v); m != "○" || coarse != "new task" {
		t.Errorf("glyph %q %q", m, coarse)
	}
	items := h.inboxItems()
	if len(items) != 1 || items[0].v != v || h.wanted(items[0]) != "new task from oge add" {
		t.Fatalf("Inbox cards %+v", items)
	}
	// Nothing starts while the human is elsewhere, however many messages pass.
	h.Update(deskTickMsg{view: v})
	if v.turn != nil || v.added == "" {
		t.Errorf("a turn started without a human focusing the card")
	}
}

// Focusing the card starts its first turn; Enter on its Inbox card
// focuses it.
func TestDeskAddStartsOnFocus(t *testing.T) {
	h := deskHome()
	h.hist, h.sel = &history{}, &setupState{}
	h.root = gitRepo(t)
	h.repoCtx.root = h.root
	h.resolve = func(*setupState) (*resolved, []string) { return nil, []string{"stopped here"} }
	reply := make(chan deskAddReply, 1)
	h.update(deskAddMsg{repo: h.root, task: "do it", reply: reply})
	<-reply
	v := h.views[1]
	h.openInbox()
	h.Update(enter())
	if h.chatView != v || h.inbox != nil {
		t.Fatalf("Enter on the card: focus %d, inbox open %v", h.num, h.inbox != nil)
	}
	// The start was tried: the task left the card and, as the config
	// can't resolve here, waits in the box with the reason shown.
	if v.added != "" || h.comp.text() != "do it" || len(h.msg) == 0 {
		t.Errorf("added %q, box %q, msg %v", v.added, h.comp.text(), h.msg)
	}
}

// What the box would hold back from the human's eyes is held back from
// an added task too: a slash line is never run, hidden text is shown first.
func TestDeskAddHoldsWhatTheBoxWouldHold(t *testing.T) {
	for _, text := range []string{"/apply", "!rm -rf x", "fix it\u200b\u200bnow"} {
		h := deskHome()
		h.root = gitRepo(t)
		h.repoCtx.root = h.root
		reply := make(chan deskAddReply, 1)
		h.update(deskAddMsg{repo: h.root, task: text, reply: reply})
		<-reply
		h.switchTo(h.views[1])
		h.startAdded()
		if h.turn != nil || h.comp.text() != text {
			t.Errorf("%q: turn %v, box %q", text, h.turn != nil, h.comp.text())
		}
	}
}

func TestDeskAddRefusesWhatIsNoRepo(t *testing.T) {
	h := deskHome()
	h.root = gitRepo(t)
	reply := make(chan deskAddReply, 1)
	h.update(deskAddMsg{repo: filepath.Join(h.root, "missing"), task: "x", reply: reply})
	if r := <-reply; r.err == nil || len(h.views) != 1 {
		t.Errorf("reply %+v, views %v", r, nums(h))
	}
}

// With a Desk open, oge add hands the task over, prints where it landed
// and exits 0 without running anything.
func TestAddJoinsTheOpenDesk(t *testing.T) {
	f := newRunFixture(t)
	state, err := ledger.OpenStateRoot(f.state, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	d, err := run.OpenDesk(state.Private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Release()
	var gotRepo, gotTask string
	if err := d.Serve(run.DeskServe{Add: func(repo, task string) (int, string, error) {
		gotRepo, gotTask = repo, task
		return 3, "myrepo", nil
	}}); err != nil {
		t.Skipf("no socket here: %v", err) // a path too long for one
	}
	wantRepo, _ := workspace.RepoRoot(f.repo)
	code, out, errOut := f.run(t, fixScript, "add", "fix Add")
	if code != ExitOK || out != "added to the Desk as Chat 3 (myrepo)\n" || errOut != "" {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if gotTask != "fix Add" || gotRepo != wantRepo {
		t.Errorf("the Desk got %q in %q", gotTask, gotRepo)
	}
}

// With no Desk open, oge add is oge "task", and says so; a socket a dead
// Desk left is removed on the way.
func TestAddWithNoDeskRunsHere(t *testing.T) {
	f := newRunFixture(t)
	state, err := ledger.OpenStateRoot(f.state, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state.Private, run.DeskDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, run.DeskSock)
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.run(t, fixScript, "add", "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "no Desk open: running it here\n") || strings.Contains(out, "added to the Desk") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("the stale socket stayed: %v", err)
	}
}

// The refusal for too many adds is printed as is, with no fallback run.
func TestAddRefusedIsPrinted(t *testing.T) {
	f := newRunFixture(t)
	state, _ := ledger.OpenStateRoot(f.state, f.repo)
	d, err := run.OpenDesk(state.Private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Release()
	if err := d.Serve(run.DeskServe{Now: time.Now, Add: func(string, string) (int, string, error) { return 1, "r", nil }}); err != nil {
		t.Skipf("no socket here: %v", err)
	}
	for range run.AddLimit {
		if code, _, errOut := f.run(t, fixScript, "add", "x"); code != ExitOK {
			t.Fatalf("add: %d %s", code, errOut)
		}
	}
	code, out, errOut := f.run(t, fixScript, "add", "x")
	if code != ExitRefused || out != "" || !strings.HasPrefix(errOut, "oge add: 3 adds in 10 minutes is the limit") {
		t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// A task the socket's side gave up on is never made into a card later:
// the client was told it failed, and a retry would double it.
func TestDeskAddGivenUpMakesNoCard(t *testing.T) {
	h := deskHome()
	h.root = gitRepo(t)
	h.repoCtx.root = h.root
	msg := deskAddMsg{repo: h.root, task: "late", reply: make(chan deskAddReply, 1), state: new(atomic.Int32)}
	msg.state.Store(addGaveUp)
	h.update(msg)
	if len(h.views) != 1 || len(msg.reply) != 0 {
		t.Errorf("views %v, replies %d", nums(h), len(msg.reply))
	}
}

// A Desk that can't answer in time, or is closing, reads as no Desk: the
// task runs here, and no card appears.
func TestAddUnavailableDeskRunsHere(t *testing.T) {
	f := newRunFixture(t)
	state, err := ledger.OpenStateRoot(f.state, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	d, err := run.OpenDesk(state.Private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Release()
	if err := d.Serve(run.DeskServe{Add: func(string, string) (int, string, error) {
		return 0, "", fmt.Errorf("%w: closing", run.ErrDeskUnavailable)
	}}); err != nil {
		t.Skipf("no socket here: %v", err)
	}
	code, out, errOut := f.run(t, fixScript, "add", "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || !strings.Contains(errOut, "didn't take it: running it here") || strings.Contains(out, "added to the Desk") {
		t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// Closing a Chat focuses a neighbour the human didn't choose: an added
// card there starts nothing.
func TestCloseDoesNotStartAnAddedCard(t *testing.T) {
	h := deskHome()
	h.root = gitRepo(t)
	h.repoCtx.root = h.root
	reply := make(chan deskAddReply, 1)
	h.update(deskAddMsg{repo: h.root, task: "do it", reply: reply})
	<-reply
	h.chat.c = nil
	submit(h, "/close")
	if len(h.views) != 2 || h.chatView.added != "" || h.turn != nil {
		t.Fatalf("views %v, focus %d, turn %v", nums(h), h.num, h.turn != nil)
	}
	if h.views[0].added != "do it" {
		t.Errorf("the card lost its task: %q", h.views[0].added)
	}
}
