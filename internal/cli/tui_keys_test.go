package cli

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/gate"
)

// What Esc and Ctrl+C mean on every screen (#154). Esc closes the innermost
// overlay or cancels, and never quits. Ctrl+C clears or cancels the innermost
// overlay, and quits only from an empty composer with no overlay. During a
// Run the first Ctrl+C cancels it and the second forces Öge to stop (exit 130,
// unchanged). A pager or $EDITOR run through tea.Exec holds interrupts: its
// Ctrl+C is its own.
//
//	screen                  Esc                        Ctrl+C
//	composer, empty         nothing                    quits
//	composer, with a Task   nothing (never wipes)      clears it; ↑ brings it back
//	slash menu              closes the menu            clears the Task (menu goes)
//	@ picker                closes the picker          clears the Task (picker goes)
//	shortcut panel (?)      closes it                  closes it
//	history search          leaves the search          leaves the search
//	hidden-char prompt      leaves the box as it was   same
//	setup panel             closes the panel           closes the panel
//	setup picker/editor     closes it, panel stays     same
//	Run picker              closes it                  same (a filter first)
//	Run picker confirm      cancels the confirmation   same
//	Gate                    nothing                    cancels the Run
//	Gate reason prompt      back to the Gate           back to the Gate
//	Run in progress         nothing (collapses ^O)     cancels; the second forces
//	pager or $EDITOR        theirs                     theirs (interrupts held)
func TestEscAndCtrlCPerScreen(t *testing.T) {
	// state names what is open, so a table row reads as one line.
	state := func(h *home) string {
		var s []string
		add := func(on bool, name string) {
			if on {
				s = append(s, name)
			}
		}
		add(h.menu != nil, "menu")
		add(h.at.list != nil, "at")
		add(h.search != nil, "search")
		add(h.hidden != nil, "hidden")
		add(h.keys != nil, "keys")
		add(h.panel != nil, "panel")
		add(h.panel != nil && h.panel.pick != nil, "pick")
		add(h.panel != nil && h.panel.input != nil, "input")
		add(h.picker != nil, "picker")
		add(h.picker != nil && h.picker.cmd != "", "confirm")
		add(h.quit, "QUIT")
		if h.comp.empty() {
			s = append(s, "empty")
		} else {
			s = append(s, "task")
		}
		return strings.Join(s, "+")
	}
	homes := []struct {
		name      string
		build     func(t *testing.T) *home
		esc, ctrl string
	}{
		{"composer empty", func(*testing.T) *home { return testHome(80) }, "empty", "QUIT+empty"},
		{"composer with a Task", func(*testing.T) *home {
			h := testHome(80)
			typeText(h, "fix Add")
			return h
		}, "task", "empty"},
		{"slash menu", func(*testing.T) *home {
			h := testHome(80)
			typeText(h, "/ru")
			return h
		}, "task", "empty"},
		{"@ picker", func(*testing.T) *home {
			h := atHome("add.go", "main.go")
			typeText(h, "fix @a")
			return h
		}, "task", "empty"},
		{"shortcut panel", func(*testing.T) *home {
			h := testHome(80)
			typeText(h, "?")
			return h
		}, "empty", "empty"},
		{"history search", func(*testing.T) *home {
			h := testHome(80)
			h.hist = &history{entries: []string{"older"}}
			h.Update(ctrlKey('r'))
			return h
		}, "empty", "empty"},
		{"hidden-char prompt", func(*testing.T) *home {
			h := testHome(80)
			typeText(h, "fix\u200b Add")
			h.Update(enter())
			return h
		}, "task", "task"},
		{"setup panel", func(t *testing.T) *home {
			h, _ := setupHome(t)
			h.Update(tab())
			return h
		}, "empty", "empty"},
		{"setup picker", func(t *testing.T) *home {
			h, _ := setupHome(t)
			h.Update(tab())
			cursorTo(h, rowProfile)
			h.Update(enter())
			return h
		}, "panel+empty", "panel+empty"},
		{"setup editor", func(t *testing.T) *home {
			h, _ := setupHome(t)
			h.Update(tab())
			cursorTo(h, rowModel)
			h.Update(enter())
			return h
		}, "panel+empty", "panel+empty"},
		{"Run picker", func(t *testing.T) *home {
			h, _ := pickerHome(t)
			return h
		}, "empty", "empty"},
		{"Run picker confirmation", func(t *testing.T) *home {
			h, _ := pickerHome(t)
			key(h, "a")
			return h
		}, "picker+empty", "picker+empty"},
	}
	for _, c := range homes {
		for _, k := range []struct {
			key  string
			msg  tea.KeyPressMsg
			want string
		}{{"esc", tea.KeyPressMsg{Code: tea.KeyEscape}, c.esc}, {"ctrl+c", ctrlKey('c'), c.ctrl}} {
			h := c.build(t)
			if state(h) == k.want && c.name != "composer empty" && c.name != "composer with a Task" {
				t.Fatalf("%s: nothing is open to close", c.name)
			}
			h.Update(k.msg)
			if got := state(h); got != k.want {
				t.Errorf("%s: %s leaves %q, want %q", c.name, k.key, got, k.want)
			}
		}
	}

	// The Run's view: the Gate and the Run in progress.
	cancelled := func(h *tuiHarness) *int {
		n := new(int)
		h.m.interrupt = func() { *n++ }
		return n
	}
	gateAt := func(h *tuiHarness) {
		h.working()
		h.implemented("")
		h.checked(false)
		h.openGate(boundExhaustionAt(h))
	}
	runs := []struct {
		name       string
		build      func(h *tuiHarness)
		esc, ctrl  string // what is open after: gate, reason or none
		escN, ctrN int
	}{
		{"Gate", gateAt, "gate", "none", 0, 1},
		{"Gate reason prompt", func(h *tuiHarness) {
			gateAt(h)
			h.m.gate.choice = new(gate.Choice)
		}, "gate", "gate", 0, 0},
		{"Run in progress", func(h *tuiHarness) { h.working() }, "none", "none", 0, 1},
	}
	for _, c := range runs {
		for _, k := range []struct {
			key  string
			msg  tea.KeyPressMsg
			want string
			n    int
		}{{"esc", tea.KeyPressMsg{Code: tea.KeyEscape}, c.esc, c.escN}, {"ctrl+c", ctrlKey('c'), c.ctrl, c.ctrN}} {
			h := newTUIHarness(t, false, 80)
			n := cancelled(h)
			c.build(h)
			h.m.Update(k.msg)
			got := "none"
			if g := h.m.gate; g != nil {
				got = "gate"
				if g.choice != nil {
					got = "reason"
				}
			}
			if got != k.want {
				t.Errorf("%s: %s leaves %s, want %s", c.name, k.key, got, k.want)
			}
			if *n != k.n {
				t.Errorf("%s: %s interrupted the Run %d times, want %d", c.name, k.key, *n, k.n)
			}
		}
	}

	// The first Ctrl+C cancels the Run; the second forces (the same
	// interrupts the real session counts, which exit 130 on the second).
	h := newTUIHarness(t, false, 80)
	h.working()
	in := newInterrupts(func() {})
	h.m.interrupt = in.interrupt
	h.m.Update(ctrlKey('c'))
	select {
	case <-in.forced:
		t.Fatal("the first Ctrl+C forced the stop")
	default:
	}
	h.m.Update(ctrlKey('c'))
	select {
	case <-in.forced:
	default:
		t.Fatal("the second Ctrl+C didn't force the stop")
	}

	// A pager or $EDITOR holds interrupts for as long as it has the terminal.
	var held []string
	e := editorCmd{run: func() error { return nil }, hold: func(b bool) { held = append(held, fmt.Sprint(b)) }}
	if err := e.Run(); err != nil || strings.Join(held, ",") != "true,false" {
		t.Errorf("editor held %v, err %v", held, err)
	}
}

// Ctrl+C clears the Task and ↑ brings it back, before any history entry; a
// second ↑ then walks the history, with the restored Task as what ↓ returns to.
func TestCtrlCThenUpRestoresTheDraft(t *testing.T) {
	h := testHome(80)
	h.hist = &history{entries: []string{"older", "newer"}}
	typeText(h, "fix Add")
	h.Update(ctrlKey('c'))
	if !h.comp.empty() {
		t.Fatal("Ctrl+C left the Task")
	}
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	h.Update(up)
	if got := h.comp.text(); got != "fix Add" {
		t.Fatalf("↑ after Ctrl+C: %q", got)
	}
	h.Update(up)
	if got := h.comp.text(); got != "newer" {
		t.Fatalf("second ↑: %q", got)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := h.comp.text(); got != "fix Add" {
		t.Fatalf("↓ back: %q", got)
	}
	// It comes back once, and a submitted Task is not offered again.
	h.Update(ctrlKey('c'))
	h.Update(up)
	h.Update(up)
	h.Update(up)
	if got := h.comp.text(); got != "older" {
		t.Errorf("history after the draft: %q", got)
	}
}

// The same through the real program: Ctrl+C clears the Task, ↑ brings it back.
func TestBareOgeCtrlCThenUpRestoresTheTask(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add")
	s.expectScreen("fix Add")
	s.type_("\x03")
	s.expectScreen("Describe the task")
	s.type_("\x1b[A")
	s.expectScreen("fix Add")
	s.type_("\x03\x03") // clear it, then leave
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}
