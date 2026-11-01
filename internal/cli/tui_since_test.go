package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
)

// sinceRows are the card's rows on screen, stripped, nil for none.
func sinceRows(h *home) []string {
	var out []string
	col := -1
	for _, l := range screenLines(h) {
		l = ansi.Strip(l)
		if col < 0 {
			if i := strings.Index(l, "╭─ since you left"); i >= 0 {
				col = ansi.StringWidth(l[:i])
			}
			continue
		}
		row := ansi.Cut(l, col, col+h.width)
		if strings.HasPrefix(row, "╰") {
			return out
		}
		out = append(out, strings.TrimSpace(strings.Trim(row, "│")))
	}
	return out
}

// The card lists what happened in Chat 2 while Chat 1 had the focus, in
// time order, from the human's last message to the open ask; what
// happened before the human left is not on it.
func TestSinceCardListsTheUnfocusedEvents(t *testing.T) {
	h, now := railHome(120, 2)
	two := h.views[1]
	h.Update(alt('2'))
	two.turn, two.lastEvent = &chatTurn{text: "make the parser accept trailing commas"}, *now
	two.side.steps, two.side.step = 1, "read parser/lex.go" // seen while focused
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.Update(alt('1'))
	*now = now.Add(4 * time.Minute)

	two.side.steps, two.side.step = 2, "edit parser/lex.go"
	h.Update(chatEvMsg{view: two, ok: true})
	two.turn = nil
	two.verify = &chatVerify{step: "go test ./parser"}
	h.Update(chatEvMsg{view: two, ok: true})
	two.verify = nil
	two.side.verdict, two.side.outcome, two.doneAt = "Check failed: 1 of 3 criteria", run.Failed, *now
	h.Update(chatEvMsg{view: two, ok: true})
	two.turn = &chatTurn{asks: []*agent.Ask{{Tool: "Bash"}}}
	h.Update(askIn(two))

	if rows := sinceRows(h); rows != nil {
		t.Fatalf("a card on the focused Chat 1: %q", rows)
	}
	h.Update(alt('2'))
	want := []string{
		"you  make the parser accept trailing commas",
		"●  edit parser/lex.go",
		"●  go test ./parser",
		"✗  Check failed: 1 of 3 criteria",
		"▲  Allow Bash?",
	}
	got := sinceRows(h)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("card:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Typing goes to the box with the card up; sending collapses it, and so
// does scrolling.
func TestSinceCardSwallowsNoKey(t *testing.T) {
	h, _ := railHome(120, 2)
	h.chat = &homeChat{c: &run.Chat{}}
	h.hist, h.sel = &history{}, &setupState{}
	two := h.views[1]
	two.repoCtx = h.repoCtx // one repo: its history and setup are Chat 1's (#374)
	two.chat = &homeChat{c: &run.Chat{}}
	two.side.steps, two.side.step = 1, "edit a.go"
	h.Update(chatEvMsg{view: two, ok: true})
	h.Update(alt('2'))
	if len(sinceRows(h)) == 0 {
		t.Fatal("no card")
	}
	typeText(h, "go on")
	if got := h.comp.text(); got != "go on" {
		t.Fatalf("box %q with the card up", got)
	}
	if len(sinceRows(h)) == 0 {
		t.Fatal("typing collapsed the card")
	}
	h.Update(enter())
	if rows := sinceRows(h); rows != nil {
		t.Errorf("card after send: %q", rows)
	}

	two.side.steps, two.side.step = 0, ""
	h.Update(alt('1'))
	two.side.steps, two.side.step = 1, "edit b.go"
	h.Update(chatEvMsg{view: two, ok: true})
	h.Update(alt('2'))
	if len(sinceRows(h)) == 0 {
		t.Fatal("no card the second time")
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if rows := sinceRows(h); rows != nil {
		t.Errorf("card after scrolling: %q", rows)
	}
}

// At 80 columns, without colour, the card fits and paints nothing.
func TestSinceCardAt80NoColour(t *testing.T) {
	h, _ := railHome(80, 2)
	two := h.views[1]
	two.side.steps, two.side.step = 1, "edit "+strings.Repeat("deep/", 30)+"x.go"
	h.Update(chatEvMsg{view: two, ok: true})
	h.Update(alt('2'))
	got := h.View().Content
	if strings.Contains(got, "\x1b") || !strings.Contains(got, "since you left") {
		t.Fatalf("card:\n%q", got)
	}
	for i, l := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("row %d is %d columns", i, w)
		}
	}
}
