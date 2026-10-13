package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/run"
)

func pickerRuns() []*delivery.Run {
	return []*delivery.Run{
		{ID: "20261013T101010-abcdef", Title: "fix Add", Outcome: run.Accepted},
		{ID: "20261013T090000-123456", Title: "try again", Outcome: run.Rejected, Follows: "20261012T080000-aaaaaa", Rebased: true},
		{ID: "20261012T080000-aaaaaa", Title: "add \x1b[31mlogging", Parked: true},
	}
}

func pickerHome(t *testing.T) (*home, *[]string) {
	t.Helper()
	h := testHome(80)
	h.runs = func(int) ([]*delivery.Run, string) { return pickerRuns(), "" }
	var calls []string
	h.deliver = func(cmd string, args []string) tea.Cmd {
		calls = append(calls, cmd+" "+strings.Join(args, ","))
		return nil
	}
	submit(h, "/runs")
	if h.picker == nil {
		t.Fatal("/runs didn't open the picker")
	}
	return h, &calls
}

func key(h *home, s string) { typeText(h, s) }

func TestRunPickerGolden(t *testing.T) {
	h, _ := pickerHome(t)
	golden(t, "runs-picker", frame(h))
	key(h, "a")
	golden(t, "runs-picker-confirm", frame(h))
}

func TestRunPickerKeys(t *testing.T) {
	h, calls := pickerHome(t)
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	key(h, "d")
	if h.picker != nil || !reflect.DeepEqual(*calls, []string{"diff 20261013T090000-123456"}) {
		t.Fatalf("d: %v", *calls)
	}
	submit(h, "/runs")
	h.Update(enter())
	if h.picker != nil || (*calls)[1] != "receipt 20261013T101010-abcdef" {
		t.Fatalf("enter: %v", *calls)
	}
	submit(h, "/runs")
	key(h, "r")
	if h.picker == nil || !strings.Contains(frame(h), "not built yet (#55)") {
		t.Errorf("resume:\n%s", frame(h))
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.picker != nil {
		t.Error("esc didn't close the picker")
	}
	if len(*calls) != 2 {
		t.Errorf("calls %v", *calls)
	}
}

func TestRunPickerFilter(t *testing.T) {
	h, _ := pickerHome(t)
	key(h, "/")
	key(h, "logging")
	h.Update(enter())
	if h.picker == nil {
		t.Fatal("enter closed the picker")
	}
	f := frame(h)
	if !strings.Contains(f, "20261012T080000-aaaaaa") || strings.Contains(f, "abcdef") {
		t.Errorf("filter:\n%s", f)
	}
}

// Delivery never fires on a bare key: a asks, and only the typed word
// confirms.
func TestRunPickerApplyNeedsTheWord(t *testing.T) {
	h, calls := pickerHome(t)
	key(h, "a")
	if len(*calls) != 0 {
		t.Fatal("a delivered on a bare key")
	}
	h.Update(enter()) // nothing typed
	key(h, "app")
	h.Update(enter())
	if len(*calls) != 0 || h.picker == nil {
		t.Fatalf("applied without the word: %v", *calls)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	h.Update(enter())
	if len(*calls) != 1 || (*calls)[0] != "receipt 20261013T101010-abcdef" {
		t.Fatalf("after cancel: %v", *calls)
	}
	submit(h, "/runs")
	key(h, "b")
	key(h, "branch")
	h.Update(enter())
	if len(*calls) != 2 || (*calls)[1] != "branch 20261013T101010-abcdef" {
		t.Fatalf("branch: %v", *calls)
	}
	submit(h, "/runs")
	key(h, "a")
	key(h, "apply")
	h.Update(enter())
	if len(*calls) != 3 || (*calls)[2] != "apply 20261013T101010-abcdef" || h.picker != nil {
		t.Fatalf("apply: %v", *calls)
	}
}

func TestRunsPrintsOnATinyTerminal(t *testing.T) {
	h := testHome(80)
	h.height = 5
	h.runs = func(int) ([]*delivery.Run, string) { return pickerRuns(), "" }
	cmd, _ := h.slash("/runs")
	if h.picker != nil || cmd == nil {
		t.Fatal("expected the printed list")
	}
}

// Two Runs, the picker: Enter shows the first one's Receipt, a then the
// word applies it, and a Rejected Run's apply refuses as the CLI does.
func TestRunPickerEndToEnd(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	f.attended("reject the test can't pass\n")
	if code, out, errOut := f.run(t, wrongScript, "fix Add", "--fast", "--agent", "fake", "--plain"); code != ExitRejected {
		t.Fatalf("the cheating Run was Accepted\n%s%s", out, errOut)
	}
	rejected := filepath.Base(f.onlyRun(t))

	s := startHomeSession(t, f, "--standard", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	var fresh string
	for _, r := range runDirs(t, f) {
		if filepath.Base(r) != rejected {
			fresh = filepath.Base(r)
		}
	}

	s.type_("/runs\r")
	s.expectScreen(fresh + "  Accepted")
	s.type_("\r") // the first: the newest
	s.expectScreen("Run " + fresh)
	s.settledScreen()

	// A Rejected Run's apply refuses as oge apply does.
	s.type_("/runs\r")
	s.expectScreen(rejected + "  Rejected")
	s.type_("\x1b[B")
	s.type_("a")
	s.expectScreen("type apply")
	s.type_("apply\r")
	s.expectScreen("NOT Accepted")
	s.settledScreen()
	if b := readFile(t, filepath.Join(f.repo, "add.go")); b != fxBroken {
		t.Fatalf("the refused apply wrote add.go:\n%s", b)
	}

	// An Accepted one: a alone does nothing, then the word applies it.
	s.type_("/runs\r")
	s.expectScreen(fresh + "  Accepted")
	s.type_("a")
	s.expectScreen("type apply")
	if b := readFile(t, filepath.Join(f.repo, "add.go")); b != fxBroken {
		t.Fatalf("a alone applied:\n%s", b)
	}
	s.type_("apply\r")
	s.expectScreen("Applied Candidate")
	if b := readFile(t, filepath.Join(f.repo, "add.go")); b != fxFixed {
		t.Errorf("add.go after the apply:\n%s", b)
	}
	s.type_("\x03\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}
