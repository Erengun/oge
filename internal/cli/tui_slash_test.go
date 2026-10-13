package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

func typeText(h *home, s string) {
	for _, r := range s {
		h.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// submit types a command and presses Enter, as the human does.
func submit(h *home, s string) {
	typeText(h, s)
	h.Update(enter())
}

func TestParseSlash(t *testing.T) {
	for _, c := range []struct {
		in   string
		word string
		cmd  string // "" is no command
		args []string
		ok   bool
	}{
		{"/help", "/help", "/help", nil, true},
		{"/mode  fast", "/mode", "/mode", []string{"fast"}, true},
		{"/agent implement claude@work", "/agent", "/agent", []string{"implement", "claude@work"}, true},
		{"/branch 20261013T101010-abcdef my-name", "/branch", "/branch", []string{"20261013T101010-abcdef", "my-name"}, true},
		{"/aply", "/aply", "", nil, true},
		{"/tmp/foo is broken", "", "", nil, false},
		{"fix /new", "", "", nil, false},
	} {
		word, cmd, args, ok := parseSlash(c.in)
		got := ""
		if cmd != nil {
			got = cmd.name
		}
		if word != c.word || got != c.cmd || len(args) != len(c.args) || (len(args) > 0 && !reflect.DeepEqual(args, c.args)) || ok != c.ok {
			t.Errorf("%q: word %q cmd %q args %q ok %v", c.in, word, got, args, ok)
		}
	}
}

func TestSlashHelpListsEveryCommand(t *testing.T) {
	got := helpText(nil)
	for _, c := range slashCommands() {
		if !strings.Contains(got, c.usage) {
			t.Errorf("/help lacks %q", c.usage)
		}
	}
	if !strings.Contains(got, "not built yet (#55)") {
		t.Error("/help doesn't say /resume isn't built")
	}
	golden(t, "slash-help", got+"\n")
}

func TestSlashMenuGolden(t *testing.T) {
	h := testHome(80)
	typeText(h, "/")
	golden(t, "slash-menu", frame(h))
	typeText(h, "re")
	golden(t, "slash-menu-filtered", frame(h))
}

func TestSlashMenuKeys(t *testing.T) {
	applied := 0
	h := testHome(80)
	at := time.Unix(0, 0)
	h.now = func() time.Time { return at }
	h.last = &lastRun{apply: func() string { applied++; return "applied" }}
	typeText(h, "/ap")
	if h.menu == nil {
		t.Fatal("no menu after /ap")
	}
	// Enter on a highlight completes; it never applies.
	h.Update(enter())
	if h.comp.text() != "/apply " || applied != 0 {
		t.Fatalf("Enter on the highlight: text %q, applied %d", h.comp.text(), applied)
	}
	h.Update(enter()) // a double tap: typed ahead of the completion
	if applied != 0 || h.comp.text() != "/apply " {
		t.Fatalf("an immediate Enter: applied %d, text %q", applied, h.comp.text())
	}
	at = at.Add(actionsArm)
	h.Update(enter()) // the whole command, explicitly
	if applied != 1 || !h.comp.empty() || h.menu != nil {
		t.Fatalf("Enter on /apply: applied %d, text %q", applied, h.comp.text())
	}
	// Tab completes a command that takes arguments, with a space; the menu closes.
	typeText(h, "/mo")
	h.Update(tab())
	if h.comp.text() != "/mode " || h.menu != nil {
		t.Fatalf("Tab: %q, menu %v", h.comp.text(), h.menu != nil)
	}
	// Enter on a bare /mode completes too, rather than refusing.
	h.comp.set("/mode")
	h.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	typeText(h, "e")
	h.Update(enter())
	if h.comp.text() != "/mode " || len(h.msg) != 0 {
		t.Fatalf("Enter on /mode: %q %q", h.comp.text(), h.msg)
	}
	// Arrows move the highlight; Esc closes the menu and keeps the text.
	h.comp = newComposer()
	typeText(h, "/re")
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	h.Update(tab())
	if h.comp.text() != "/resume" {
		t.Fatalf("down then Tab: %q", h.comp.text())
	}
	h.comp.set("/re")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.menu != nil || h.comp.text() != "/re" || h.quit {
		t.Fatalf("Esc: menu %v text %q quit %v", h.menu != nil, h.comp.text(), h.quit)
	}
	typeText(h, "c")
	if h.menu == nil {
		t.Error("typing again didn't reopen the menu")
	}
	// A path is no menu; nor is text with a space.
	h.comp.set("/tmp/x")
	h.syncMenu()
	if h.menu != nil {
		t.Error("menu on a path")
	}
}

func TestSlashUnknownAndUsage(t *testing.T) {
	h, _ := setupHome(t)
	for in, want := range map[string]string{
		"/aply":                     "/aply: unknown command",
		"/mode":                     "usage: /mode fast|standard",
		"/mode loud":                "usage: /mode",
		"/mode blind":               "not built yet (#52)",
		"/agent implement":          "usage: /agent <stage>",
		"/agent build claude":       `the stage is implement or verify, not "build"`,
		"/agent implement nothing":  `no agent "nothing"`,
		"/agent implement codex@x":  "profiles are Claude's",
		"/agent implement claude@x": `no Claude profile "x"`,
		"/model implement":          "usage: /model",
		"/effort implement turbo":   "/effort: the level is",
		"/effort implement":         "usage: /effort",
		"/resume":                   "not built yet (#55)",
		"/receipt a b":              "usage: /receipt [run]",
		"/apply --rejected":         "flags are for oge apply",
		"/branch a b c":             "usage: /branch",
		"/runs now":                 "usage: /runs",
		"/new now":                  "usage: /new",
		"/help me":                  "usage: /help",
	} {
		h.comp.set(in)
		h.msg = nil
		h.slash(in)
		if len(h.msg) == 0 || !strings.Contains(h.msg[0], want) {
			t.Errorf("%s: msg %q, want %q", in, h.msg, want)
		}
		if h.comp.text() != in || h.run != nil {
			t.Errorf("%s: the text was %q", in, h.comp.text())
		}
	}
}

func TestSlashSetupCommandsGoThroughTheSetupState(t *testing.T) {
	h, _ := setupHome(t)
	submit(h, "/mode fast")
	if h.sel.mode != pipeline.Fast || !h.sel.tMode || !strings.Contains(h.note, "(from setup)") || !h.comp.empty() {
		t.Fatalf("mode %v tMode %v note %q", h.sel.mode, h.sel.tMode, h.note)
	}
	if h.saving != true {
		t.Error("the first change doesn't offer the save, as the panel does")
	}
	h.saving = false
	if f := h.sel.flags(runFlags{}); !f.o.Fast || f.o.ModeFrom != pipeline.FromTUI {
		t.Errorf("flags %+v", f.o)
	}
	submit(h, "/mode standard")
	submit(h, "/agent implement claude@z")
	submit(h, "/model implement opus")
	submit(h, "/effort implement high")
	submit(h, "/agent verify claude")
	if len(h.msg) != 0 {
		t.Fatalf("msg %q", h.msg)
	}
	f := h.sel.flags(runFlags{})
	if want := []string{"implement=claude@z:opus", "verify=claude"}; !reflect.DeepEqual(f.o.TUIAgents, want) {
		t.Errorf("agents %q, want %q", f.o.TUIAgents, want)
	}
	if f.o.TUIEfforts["implement"] != "high" {
		t.Errorf("efforts %v", f.o.TUIEfforts)
	}
	// Effort is Claude's; the verifier is gone in Fast.
	submit(h, "/agent verify codex")
	h.comp = newComposer()
	submit(h, "/effort verify high")
	if len(h.msg) == 0 || !strings.Contains(h.msg[0], "Claude's") {
		t.Errorf("effort on codex: %q", h.msg)
	}
	h.comp = newComposer()
	submit(h, "/mode fast")
	submit(h, "/model verify x")
	if len(h.msg) == 0 || !strings.Contains(h.msg[len(h.msg)-1], "no verifier") {
		t.Errorf("verify in Fast: %q", h.msg)
	}
}

func TestSlashSetupOpensThePanel(t *testing.T) {
	h, _ := setupHome(t)
	submit(h, "/setup")
	if h.panel == nil || !h.comp.empty() {
		t.Fatal("/setup didn't open the panel")
	}
}

func TestSlashRunsAndDeliveryOfANamedRun(t *testing.T) {
	h := testHome(80)
	h.runs = func(n int) ([]*delivery.Run, string) {
		return []*delivery.Run{{ID: "20261013T101010-abcdef", Title: "fix Add", Outcome: run.Accepted}}, ""
	}
	var calls []string
	h.deliver = func(cmd string, args []string) string {
		calls = append(calls, cmd+" "+strings.Join(args, ","))
		return ""
	}
	submit(h, "/runs")
	if !h.comp.empty() || len(h.msg) != 0 || h.picker == nil {
		t.Fatalf("/runs: %q %q", h.comp.text(), h.msg)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	// With a Run id, the CLI's code is used, with or without a last Run.
	for _, in := range []string{"/receipt 20261013", "/diff 20261013", "/apply 20261013", "/branch 20261013T1010 mine", "/branch mine 20261013T1010", "/receipt"} {
		submit(h, in)
	}
	want := []string{"receipt 20261013", "diff 20261013", "apply 20261013", "branch 20261013T1010,mine", "branch mine,20261013T1010", "receipt "}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %q, want %q", calls, want)
	}
	// Applying or branching with no id needs a Run just seen in this session.
	submit(h, "/apply")
	if len(h.msg) == 0 || !strings.Contains(h.msg[0], "no Accepted Run") || len(calls) != len(want) {
		t.Errorf("/apply with nothing to take: %q %q", h.msg, calls)
	}
}

func TestSlashApplyOfTheThreadsRunEndsTheThread(t *testing.T) {
	h := testHome(80)
	h.deliver = func(string, []string) string { return "" }
	h.thread = &thread{}
	h.thread.from.Run = "20261013T101010-abcdef"
	submit(h, "/apply 20261013T1010")
	if h.thread != nil {
		t.Error("applying the thread's Run kept the thread")
	}
}

func TestSlashQuit(t *testing.T) {
	h := testHome(80)
	typeText(h, "/quit")
	if _, cmd := h.Update(enter()); !h.quit || cmd == nil {
		t.Fatal("/quit didn't leave")
	}
}

// The whole path in a terminal: /mode fast is what the next Run uses and
// its Receipt says where it came from; /runs lists it; /receipt <id>
// prints it again; and /apply <id> of a Rejected Run refuses as oge apply
// does, writing nothing.
func TestBareOgeSlashCommandsEndToEnd(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	// A Run a human Rejects at its Gate, before the session.
	f.attended("reject the test can't pass\n")
	if code, out, errOut := f.run(t, wrongScript, "fix Add", "--fast", "--agent", "fake", "--plain"); code != ExitRejected {
		t.Fatalf("the cheating Run was Accepted\n%s%s", out, errOut)
	}
	rejected := filepath.Base(f.onlyRun(t))

	s := startHomeSession(t, f, "--standard", "--agent", "fake")
	s.composer()
	s.type_("/mode fast\r")
	s.expect("mode Fast (from setup)")
	s.type_("n") // the save prompt: no
	s.type_("fix Add\r")
	s.ended()
	if screen := s.settledScreen(); !strings.Contains(screen, "(from setup)") || !strings.Contains(screen, "Fast") {
		t.Errorf("the Receipt doesn't show Fast (from setup):\n%s", screen)
	}
	runs := runDirs(t, f)
	if len(runs) != 2 {
		t.Fatalf("runs %v", runs)
	}
	var fresh string
	for _, r := range runs {
		if filepath.Base(r) != rejected {
			fresh = filepath.Base(r)
		}
	}

	s.type_("/runs\r")
	s.expectScreen(fresh + "  Accepted")
	s.expectScreen(rejected + "  Rejected")
	s.type_("\x1b") // the picker closes
	time.Sleep(300 * time.Millisecond)

	s.type_("/receipt " + rejected + "\r")
	s.expectScreen("Not accepted: 1 test still failing")
	s.type_("/apply " + rejected + "\r")
	s.expectScreen("NOT Accepted")
	s.type_("\x03\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if b := readFile(t, filepath.Join(f.repo, "add.go")); b != fxBroken {
		t.Errorf("the refused apply wrote add.go:\n%s", b)
	}
	if d := deliveries(t, filepath.Join(f.state, "private", "runs", rejected)); len(d) != 0 {
		t.Errorf("deliveries %v", d)
	}
}

func TestSlashMenuYieldsToHistoryAndHintsAtMore(t *testing.T) {
	h := testHome(80)
	h.hist = &history{entries: []string{"older", "/diff"}}
	h.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if h.comp.text() != "/diff" || h.menu == nil {
		t.Fatalf("recall: %q", h.comp.text())
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if h.comp.text() != "older" {
		t.Fatalf("↑ went to the menu: %q", h.comp.text())
	}
	h = testHome(80)
	typeText(h, "/")
	if !strings.Contains(frame(h), "more · ↓") {
		t.Errorf("no hint of the commands below:\n%s", frame(h))
	}
}
