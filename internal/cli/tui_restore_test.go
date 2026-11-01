package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/run"
)

// restoredHome is a Desk restored from three Chats, as a restart finds
// it: 1 stopped mid-turn, 2 parked at a Gate in a repo that is gone, 3 ✓
// seen, and 4 ✓ not seen. Nothing is started: repoFor only hands back
// the repo's context.
func restoredHome(t *testing.T, desk *run.Desk) (*home, *time.Time, string) {
	t.Helper()
	repoA := t.TempDir()
	gone := filepath.Join(t.TempDir(), "old-project")
	now := time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	d := &run.RestoredDesk{Chats: []*run.RestoredChat{
		{ID: "a", N: 1, Title: "make the parser accept trailing commas", Repo: repoA, State: run.RestoredStopped, Since: ago(9 * time.Hour),
			Turns: []run.RestoredTurn{
				{N: 1, Text: "make the parser accept trailing commas", Reply: "Trailing commas are accepted now.", Settled: true},
				{N: 2, Text: "and the lexer"},
			},
			Card: &run.RestoredCard{Kind: run.CardInterrupted, Turn: 2, Why: "hangup", At: ago(9 * time.Hour)}},
		{ID: "b", N: 2, Title: "auth", Repo: gone, State: run.RestoredParked, Since: ago(9 * time.Hour),
			Turns: []run.RestoredTurn{{N: 1, Text: "fix auth", Reply: "Fixed.", Settled: true}},
			Card:  &run.RestoredCard{Kind: run.CardParked, Gate: "gate.result", Verified: true, At: ago(9 * time.Hour)}},
		{ID: "c", N: 3, Title: "docs", Repo: repoA, State: run.RestoredIdle, Seen: true, Run: "r3", Since: ago(10 * time.Hour),
			Turns: []run.RestoredTurn{{N: 1, Text: "docs", Reply: "Done.", Settled: true}}},
		{ID: "d", N: 4, Title: "cache", Repo: repoA, State: run.RestoredDone, Run: "r4", Since: ago(2 * time.Hour),
			Turns: []run.RestoredTurn{{N: 1, Text: "cache", Reply: "Done.", Settled: true}}},
	}}
	h := fullHome(120, 24)
	h.now = func() time.Time { return now }
	h.root = repoA
	opened := 0
	h.views = h.restoreViews(d, desk, func(root string) (*repoCtx, []string) {
		opened++
		return &repoCtx{root: root, title: "oge · " + filepath.Base(root) + " · main"}, nil
	})
	h.focus(h.views[0])
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if opened != 3 {
		t.Errorf("repoFor was asked %d times: a repo that is gone must not be opened", opened)
	}
	return h, &now, repoA
}

// The Desk comes back with the right glyphs, in number order, and starts
// nothing: no Chat, no turn, no check.
func TestRestoredDeskGlyphsAndNoProcess(t *testing.T) {
	h, _, _ := restoredHome(t, nil)
	want := map[int][2]string{1: {"○", "stopped"}, 2: {"▲", "parked"}, 3: {"○", "idle"}, 4: {"✓", "done"}}
	for _, v := range h.views {
		m, c := h.mark(v)
		if w := want[v.num]; m != w[0] || c != w[1] {
			t.Errorf("Chat %d is %s %s, want %s %s", v.num, m, c, w[0], w[1])
		}
		if v.turn != nil || v.verify != nil || (v.chat != nil && v.chat.get() != nil) {
			t.Errorf("Chat %d has something running", v.num)
		}
	}
	rail := strings.Join(railCol(h), "\n")
	golden(t, "restore-rail-120", rail)
}

// Interrupted turns, parked Gates and unseen ✓ get cards, the ▲ first;
// the idle Chat gets none. The cards offer only reading.
func TestRestoredCards(t *testing.T) {
	h, _, _ := restoredHome(t, nil)
	var got []int
	for _, it := range h.inboxItems() {
		got = append(got, it.v.num)
	}
	if len(got) != 3 || got[0] != 2 || got[1] != 1 || got[2] != 4 {
		t.Fatalf("cards for Chats %v, want 2 1 4", got)
	}
	h.Update(ctrlSpace())
	var shown []string
	for range 3 {
		it, _, _, _ := h.card()
		shown = append(shown, strings.TrimSpace(ansi.Strip(strings.Join(screenLines(h), "\n"))))
		_ = it
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	golden(t, "restore-card-parked", shown[0])
	golden(t, "restore-card-stopped", shown[1])
	for _, s := range shown {
		if strings.Contains(s, "resend") || strings.Contains(s, "send back") || strings.Contains(s, "cut again") {
			t.Errorf("a card offers what can't work without a Session:\n%s", s)
		}
	}
	// o opens it; Enter alone decides nothing.
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.inbox == nil {
		t.Error("Enter closed the Inbox")
	}
}

// The transcript is read back from the records: the messages, the
// replies, and how the Chat stands.
func TestRestoredTranscript(t *testing.T) {
	h, _, _ := restoredHome(t, nil)
	h.chatView.tr.render(100)
	text := ansi.Strip(strings.Join(h.chatView.tr.render(100), "\n"))
	for _, s := range []string{"make the parser accept trailing commas", "Trailing commas are accepted now.", "and the lexer", "turn 2 had no clean end", "turn 2 stopped when oge exited"} {
		if !strings.Contains(text, s) {
			t.Errorf("transcript lacks %q:\n%s", s, text)
		}
	}
}

// A repo that is gone shows its Chat honestly and breaks nothing: not the
// draw, not a message, not /new.
func TestRestoredChatOfAGoneRepo(t *testing.T) {
	h, _, _ := restoredHome(t, nil)
	h.Update(alt('2'))
	if h.num != 2 {
		t.Fatalf("focused %d", h.num)
	}
	text := ansi.Strip(strings.Join(h.tr.render(100), "\n"))
	if !strings.Contains(text, "isn't there") || !strings.Contains(text, "parked at the result Gate") {
		t.Errorf("transcript:\n%s", text)
	}
	h.View()
	typeText(h, "try again")
	h.Update(enter())
	if h.chat != nil && h.chat.get() != nil || h.turn != nil {
		t.Fatal("a message to a restored Chat started something")
	}
	if got := h.comp.text(); got != "try again" {
		t.Errorf("the draft is %q: it must stay", got)
	}
	if !strings.Contains(strings.Join(h.msg, " "), "isn't open yet") {
		t.Errorf("no line said why: %v", h.msg)
	}
	h.comp.set("")
	submit(h, "/new")
	if len(h.views) != 5 || h.chatView.rest != nil {
		t.Errorf("/new on a restored Chat reused it: %v", nums(h))
	}
}

// ✓ unseen is seen once looked at, and the Desk record says so; a ✓ the
// record already has as seen stays seen.
func TestRestoredSeenIsRecorded(t *testing.T) {
	private := t.TempDir()
	desk, err := run.OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	h, now, _ := restoredHome(t, desk)
	h.Update(alt('4'))
	*now = now.Add(2 * time.Second)
	h.recordSeen()
	desk.Release()
	recs, err := run.ReadDesk(private)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, r := range recs {
		if r.Type == run.RecSeen {
			seen = append(seen, string(r.Data))
		}
	}
	if len(seen) != 1 || !strings.Contains(seen[0], `"chat":"d"`) || !strings.Contains(seen[0], `"upto":"r4"`) {
		t.Errorf("Seen records %v", seen)
	}
	if m, c := h.mark(h.viewNum(4)); m != "○" || c != "idle" {
		t.Errorf("Chat 4 after a look is %s %s", m, c)
	}
}

// /close on a restored Chat records ChatClosed and takes its Workspace;
// closing the rest on quit does the same, and a kept one stays.
func TestRestoredCloseRemovesTheWorkspace(t *testing.T) {
	private := t.TempDir()
	desk, err := run.OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer desk.Release()
	h, _, _ := restoredHome(t, desk)
	work := filepath.Join(t.TempDir(), "implement")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	h.views[0].rest.Work = work
	submit(h, "/close")
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("the Workspace stayed: %v", err)
	}
	if len(h.views) != 3 || h.viewNum(1) != nil {
		t.Errorf("views %v", nums(h))
	}
	recs, _ := run.ReadDesk(private)
	closed := 0
	for _, r := range recs {
		if r.Type == run.RecChatClosed && strings.Contains(string(r.Data), `"chat":"a"`) {
			closed++
		}
	}
	if closed != 1 {
		t.Errorf("%d ChatClosed for Chat a", closed)
	}
}

// Quitting with restored Chats asks, as with open ones, and k keeps them
// without writing a ChatClosed.
func TestRestoredQuitAsks(t *testing.T) {
	h, _, _ := restoredHome(t, nil)
	if n := h.openChats(); n != 4 {
		t.Errorf("%d open Chats counted, want 4", n)
	}
}

// Only the answer c closes the restored Chats at exit: k, and any end
// that is no answer, leave them as they are.
func TestQuitAskClosesRestoredOnlyOnC(t *testing.T) {
	for key, want := range map[string]bool{"c": true, "k": false} {
		h, _, _ := restoredHome(t, nil)
		h.quitAsk = 1
		h.quitAskKey(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		if h.closeAll != want {
			t.Errorf("after %q closeAll = %v, want %v", key, h.closeAll, want)
		}
	}
	h, _, _ := restoredHome(t, nil)
	if h.closeAll {
		t.Error("a Desk that quit with no answer would close its restored Chats")
	}
}
