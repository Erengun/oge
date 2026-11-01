package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

func ctrlSpace() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl} }

func letter(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// inboxHome has Chats 1 (focused) to 5: 2 Check failed, 3 asks, 4 is
// done, 5 asked earlier than 3. Each began its glyph at its own time.
func inboxHome(t *testing.T) (*home, *time.Time) {
	t.Helper()
	h, now := railHome(120, 5)
	t0 := *now
	two, three, four, five := h.views[1], h.views[2], h.views[3], h.views[4]
	two.name, two.side.outcome, two.doneAt = "lexer", run.Failed, t0.Add(-3*time.Minute)
	two.pendingFB = &pendingFeedback{text: "1 failing: c2"}
	three.name, three.turn, three.lastEvent = "auth", &chatTurn{asks: []*agent.Ask{{Tool: "Bash"}}}, t0
	four.name, four.side.outcome, four.last, four.doneAt = "docs", run.Accepted, &lastRun{id: "r4"}, t0.Add(-5*time.Minute)
	four.side.changes = []diffFile{{path: "README.md", add: 3, del: 1}}
	five.name, five.cutGate = "parser", &gateState{}
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 24}) // attend: asks noted at t0
	five.askedAt = t0.Add(-time.Minute)
	return h, now
}

func cardHead(h *home) string {
	for _, l := range screenLines(h) {
		l = ansi.Strip(l)
		for _, m := range []string{" ▲ ", " ✗ ", " ✓ "} {
			if i := strings.Index(l, m); i >= 0 && i < railWidth+6 && strings.Contains(l, "·") {
				return strings.TrimSpace(l[i:])
			}
		}
	}
	return ""
}

// Cards go ▲ then ✗ then ✓, oldest first within each; tab walks them.
func TestInboxCardOrder(t *testing.T) {
	h, _ := inboxHome(t)
	h.Update(ctrlSpace())
	if h.inbox == nil {
		t.Fatal("ctrl+space opened no Inbox")
	}
	var got []int
	for range 4 {
		it, _, _, ok := h.card()
		if !ok {
			t.Fatal("no card")
		}
		got = append(got, it.v.num)
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	if want := []int{5, 3, 2, 4}; !equalInts(got, want) {
		t.Errorf("cards %v, want %v", got, want)
	}
	if it, _, _, _ := h.card(); it.v.num != 5 {
		t.Errorf("tab past the last card shows %d, want 5", it.v.num)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A card goes when its condition clears: the Gate answered, the failure
// answered by a turn.
func TestInboxCardsGoWhenTheirConditionClears(t *testing.T) {
	h, _ := inboxHome(t)
	h.Update(ctrlSpace())
	h.views[4].cutGate = nil // Chat 5's Gate decided
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if it, _, n, _ := h.card(); it.v.num != 3 || n != 3 {
		t.Errorf("after the Gate: card %d of %d, want 3 of 3", it.v.num, n)
	}
	h.views[1].pendingFB, h.views[1].answered = nil, true
	if n := h.inboxCount(); n != 2 {
		t.Errorf("after the failure is answered: %d cards, want 2", n)
	}
}

// Enter alone decides nothing on any card.
func TestInboxEnterDecidesNothing(t *testing.T) {
	h, _ := inboxHome(t)
	h.Update(ctrlSpace())
	for range 4 {
		it, _, _, _ := h.card()
		h.Update(enter())
		if h.inbox == nil || h.num != 1 || len(h.views) != 5 {
			t.Fatalf("Enter on Chat %d's card acted: inbox %v, focused %d, %d Chats", it.v.num, h.inbox != nil, h.num, len(h.views))
		}
		if it.v.cutGate == nil && it.v.num == 5 || it.v.num == 2 && it.v.pendingFB == nil || it.v.num == 3 && len(it.v.turn.asks) == 0 {
			t.Fatalf("Enter on Chat %d's card changed it", it.v.num)
		}
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
}

// o opens the card's Chat; d opens its diff there; s sends back the
// card's Chat, not the focused one, and leaves the Inbox open.
func TestInboxKeysActOnTheCardsChat(t *testing.T) {
	h, _ := inboxHome(t)
	h.chat = &homeChat{c: &run.Chat{}}
	h.Update(ctrlSpace())
	h.Update(letter('o'))
	if h.inbox != nil || h.num != 5 {
		t.Fatalf("o: inbox open %v, focused %d, want Chat 5", h.inbox != nil, h.num)
	}

	h.Update(ctrlSpace())
	for it, _, _, _ := h.card(); it.v.num != 4; it, _, _, _ = h.card() {
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	opened := ""
	h.views[3].last.diff = func(func(string) tea.Cmd) tea.Cmd { opened = "r4"; return nil }
	h.Update(letter('d'))
	if h.inbox != nil || h.num != 4 || opened != "r4" {
		t.Fatalf("d: inbox open %v, focused %d, diff %q", h.inbox != nil, h.num, opened)
	}

	h.Update(ctrlSpace())
	for it, _, _, _ := h.card(); it.v.num != 2; it, _, _, _ = h.card() {
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	two := h.views[1]
	two.chat = &homeChat{c: &run.Chat{}}
	h.Update(letter('s'))
	if h.num != 4 || h.inbox == nil {
		t.Fatalf("s moved the focus to %d or closed the Inbox", h.num)
	}
	if two.pendingFB != nil || two.turn == nil || !two.answered {
		t.Errorf("s didn't send Chat 2 back: pending %v, turn %v", two.pendingFB != nil, two.turn != nil)
	}
	if h.views[3].turn != nil {
		t.Error("s started a turn in the focused Chat")
	}
}

// A task typed in the Inbox and sent opens a new Chat with it; the card
// keys type once the task has begun.
func TestInboxTaskOpensAChat(t *testing.T) {
	h, _ := inboxHome(t)
	h.chat = &homeChat{c: &run.Chat{}}
	h.hist, h.sel = &history{}, &setupState{}
	var started string
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		started = tk.Title
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	h.Update(ctrlSpace())
	typeText(h, "fix the docs")
	if h.inbox == nil || h.num != 1 {
		t.Fatalf("typing a task acted on a card: focused %d", h.num)
	}
	h.Update(enter())
	if h.inbox != nil || len(h.views) != 6 || h.num != 6 {
		t.Fatalf("Enter: inbox %v, %d Chats, focused %d", h.inbox != nil, len(h.views), h.num)
	}
	if started != "fix the docs" || h.caption() != "fix the docs" {
		t.Errorf("Chat 6's first message: %q, named %q", started, h.caption())
	}
	if h.views[0].comp.text() != "" || h.views[0].turn != nil {
		t.Error("the task went to Chat 1")
	}
}

// G1: a ✓ card shown about a second counts as seen; u leaves it unread.
func TestInboxDoneCardSeenAfterASecond(t *testing.T) {
	h, now := inboxHome(t)
	r := &redraws{}
	h.redraw = r.after
	h.Update(ctrlSpace())
	for it, _, _, _ := h.card(); it.v.num != 4; it, _, _, _ = h.card() {
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	four := h.views[3]
	*now = now.Add(seenAfter)
	var seen tea.Msg
	for _, m := range r.msg {
		if _, ok := m.(inboxSeenMsg); ok {
			seen = m
		}
	}
	if seen == nil {
		t.Fatal("no redraw asked for the ✓ card")
	}
	h.Update(seen)
	if m, _ := h.mark(four); m != "○" {
		t.Errorf("Chat 4 after its card was shown: %s, want ○", m)
	}
	if it, _, _, _ := h.card(); it.v != four {
		t.Errorf("the seen card left the screen under the human")
	}
	h.Update(letter('u'))
	if m, _ := h.mark(four); m != "✓" {
		t.Errorf("after u: %s, want ✓", m)
	}
	h.Update(seen)
	if m, _ := h.mark(four); m != "✓" {
		t.Errorf("u undone by the redraw: %s", m)
	}
}

// The card fits 80 columns and paints nothing without colour.
func TestInboxAt80NoColour(t *testing.T) {
	h, _ := inboxHome(t)
	h.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.Update(ctrlSpace())
	for range 4 {
		got := h.View().Content
		if strings.Contains(got, "\x1b") {
			t.Fatalf("colour without colour:\n%q", got)
		}
		for i, l := range strings.Split(got, "\n") {
			if w := ansi.StringWidth(l); w > 80 {
				t.Errorf("row %d is %d columns: %q", i, w, l)
			}
		}
		if !strings.Contains(got, "Inbox") {
			t.Fatalf("no Inbox on screen:\n%s", got)
		}
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	if head := cardHead(h); !strings.HasPrefix(head, "▲ 5 parser") {
		t.Errorf("card head %q", head)
	}
	if !strings.Contains(h.View().Content, "Gate is waiting") {
		t.Errorf("the Gate card doesn't say what is wanted:\n%s", h.View().Content)
	}
}

// The box's status offers the Inbox with its count.
func TestInboxHintInTheBox(t *testing.T) {
	h, _ := inboxHome(t)
	if got := strings.Join(screenLines(h), "\n"); !strings.Contains(got, "^space inbox (4)") {
		t.Errorf("no hint:\n%s", got)
	}
	submit(h, "/inbox")
	if h.inbox == nil {
		t.Error("/inbox opened no Inbox")
	}
}

// cardOn tabs the open Inbox to Chat n's card.
func cardOn(t *testing.T, h *home, n int) {
	t.Helper()
	for range 8 {
		if it, _, _, ok := h.card(); ok && it.v.num == n {
			return
		}
		h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	t.Fatalf("no card for Chat %d", n)
}

// typeSpaced types s one key every gap, as a human at ordinary speed.
func typeSpaced(h *home, now *time.Time, s string, gap time.Duration) {
	for i, r := range s {
		if i > 0 {
			*now = now.Add(gap)
		}
		h.Update(letter(r))
	}
}

// A task typed on an ask's card begins with o: the Chat opens, and the
// rest of the word ("kay") reaches no ask, even typed at 150ms a key,
// past actionsArm from the o: its y doesn't allow the tool. After a
// pause of actionsArm, y is the ask's again.
func TestInboxLetterCarriesIntoNoAsk(t *testing.T) {
	h, now := inboxHome(t)
	h.Update(ctrlSpace())
	cardOn(t, h, 3)
	typeSpaced(h, now, "okay", 150*time.Millisecond)
	three := h.views[2]
	if h.inbox != nil || h.num != 3 {
		t.Fatalf("o: inbox open %v, focused %d, want Chat 3", h.inbox != nil, h.num)
	}
	if len(three.turn.asks) != 1 {
		t.Fatal("keys typed after o answered Chat 3's ask")
	}
	*now = now.Add(actionsArm)
	if h.inboxCarry(letter('y')) {
		t.Error("y is still dropped after a pause of actionsArm")
	}
}

// On a stale card, "okay" at 150ms a key opens the Chat; its a doesn't
// deliver the cut.
func TestInboxLetterCarriesIntoNoStaleChoice(t *testing.T) {
	h, now := inboxHome(t)
	five := h.views[4]
	five.cutGate, five.last = nil, &lastRun{id: "r5"}
	five.stale = &staleAsk{key: "a", last: five.last}
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	h.Update(ctrlSpace())
	cardOn(t, h, 5)
	typeSpaced(h, now, "okay", 150*time.Millisecond)
	if h.num != 5 || five.stale == nil {
		t.Fatalf("focused %d, stale choice open %v: a after o decided it", h.num, five.stale != nil)
	}
}

// A task from the Inbox opens its Chat as /new does: past [desk]
// max_active it says so, once.
func TestInboxTaskNotesTheCap(t *testing.T) {
	h, _ := inboxHome(t)
	h.chat = &homeChat{c: &run.Chat{}}
	h.hist, h.sel = &history{}, &setupState{}
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	h.deskMax = 1       // Chat 3's turn is working
	h.comp.set("draft") // Chat 1 isn't fresh
	h.Update(ctrlSpace())
	typeText(h, "fix the docs")
	h.Update(enter())
	if h.num != 6 || !strings.Contains(h.note, "research says") {
		t.Errorf("focused %d, note %q: want Chat 6 and the cap note", h.num, h.note)
	}
}

// briefOf is a return brief with a Gate waiting in Chat 5 and the human's
// first message in Chat 1.
func briefOf(h *home) *run.Brief {
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	b := run.Brief{Window: run.BriefWindow{From: t0, To: t0.Add(20 * time.Minute), Basis: run.BasisAway},
		Chats: []run.ChatBrief{
			{ID: "c1", N: 1, Title: "parser", First: "SECRET-INTENT make the parser accept trailing commas", Accepted: 1,
				Decisions: []run.Decision{{At: t0.Add(-time.Hour), What: "chose acknowledge at tamper Gate"}}},
			{ID: "c5", N: 5, Title: "docs", Gates: []run.OpenGate{{Gate: "tamper", Since: t0.Add(5 * time.Minute)}}},
		}}
	b.Totals = run.BriefTotals{Accepted: 1, NeedsYou: 1}
	return &b
}

// After an absence the brief is the Inbox's first card, once: it takes no
// key but tab, typing goes to the box, and the next opening has no brief.
func TestInboxBriefIsFirstCard(t *testing.T) {
	h, _ := inboxHome(t)
	h.absence.back = briefOf(h)
	h.Update(ctrlSpace())
	if h.inbox == nil || h.inbox.brief == nil {
		t.Fatal("the Inbox didn't open on the brief")
	}
	if h.absence.back != nil {
		t.Error("the brief is seen but not cleared")
	}
	screen := ansi.Strip(strings.Join(screenLines(h), "\n"))
	since := time.Date(2026, 11, 1, 9, 5, 0, 0, time.UTC).In(time.Local).Format("15:04")
	for _, want := range []string{"while you were away", "You were: SECRET-INTENT", "Your decisions: chose acknowledge at tamper Gate",
		"Waiting for you: ▲ tamper Gate since " + since, "Next: ▲ tamper Gate", "tab next"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the card lacks %q:\n%s", want, screen)
		}
	}
	// Typing goes to the box; Enter alone does nothing.
	h.Update(letter('o'))
	h.Update(letter('k'))
	if got := h.inbox.comp.expanded(); got != "ok" || h.inbox.brief == nil {
		t.Fatalf("typed %q, brief %v", got, h.inbox.brief)
	}
	h.inbox.comp.set("")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.inbox == nil || h.inbox.brief == nil {
		t.Fatal("Enter moved the brief")
	}
	// tab goes on to the first real card.
	h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if h.inbox.brief != nil {
		t.Fatal("tab kept the brief")
	}
	if it, _, _, ok := h.card(); !ok || it.v.num != 5 {
		t.Errorf("first card after the brief = %v", it.v.num)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	h.Update(ctrlSpace())
	if h.inbox.brief != nil {
		t.Error("the brief came back on the next opening")
	}
}

// With NO_COLOR the card carries no escape sequence, and the text of "You
// were" never reaches a notification (A8): the bell says the Chat's number.
func TestInboxBriefPlainAndQuiet(t *testing.T) {
	h, _ := inboxHome(t)
	h.st = newStyles(false)
	h.absence.back = briefOf(h)
	h.Update(ctrlSpace())
	if s := strings.Join(screenLines(h), "\n"); strings.ContainsRune(s, 0x1b) {
		t.Errorf("escape sequence in the plain card: %q", s)
	}
	for _, c := range briefOf(h).Chats {
		for _, got := range []string{staleText(c.N, c.First), staleText(c.N, c.Title)} {
			if strings.Contains(got, "SECRET") || strings.Contains(got, c.First) && c.First != "" {
				t.Errorf("the notification carries the intention: %q", got)
			}
		}
	}
}
