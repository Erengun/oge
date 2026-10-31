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
	h.deliver = func(cmd string, args []string) string {
		calls = append(calls, cmd+" "+strings.Join(args, ","))
		return ""
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

// A Run whose Ledger ends in a torn record is marked, not hidden.
func TestRunPickerMarksATornRun(t *testing.T) {
	h := testHome(80)
	h.runs = func(int) ([]*delivery.Run, string) {
		return []*delivery.Run{{ID: "20261013T101010-abcdef", Title: "fix Add", Outcome: run.Accepted, Torn: true}}, ""
	}
	submit(h, "/runs")
	if f := frame(h); !strings.Contains(f, "ledger torn") {
		t.Errorf("torn run unmarked:\n%s", f)
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
	h.Update(enter()) // nothing typed: cancelled, the picker stays
	if len(*calls) != 0 || h.picker == nil || h.picker.cmd != "" {
		t.Fatalf("an empty word didn't cancel: %v", *calls)
	}
	key(h, "a")
	key(h, "app")
	h.Update(enter()) // a wrong word cancels too
	if len(*calls) != 0 || h.picker == nil || h.picker.cmd != "" {
		t.Fatalf("applied without the word: %v", *calls)
	}
	h.Update(enter()) // Enter on the row is the Receipt
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

func labelHome(t *testing.T) (*home, *[]string) {
	t.Helper()
	runs := pickerRuns()
	runs[0].Label = "the retry fix"
	runs[2].Label = "old \x1b[31mlogging try"
	h := testHome(80)
	h.runs = func(int) ([]*delivery.Run, string) { return runs, "" }
	var calls []string
	h.label = func(id, text string) (string, string) {
		calls = append(calls, id+"="+text)
		return strings.TrimSpace(text), ""
	}
	submit(h, "/runs")
	if h.picker == nil {
		t.Fatal("/runs didn't open the picker")
	}
	return h, &calls
}

func TestRunPickerLabelsGolden(t *testing.T) {
	h, _ := labelHome(t)
	golden(t, "runs-picker-labels", frame(h))
	key(h, "l")
	golden(t, "runs-picker-label-edit", frame(h))
}

func TestRunPickerLabelEdit(t *testing.T) {
	h, calls := labelHome(t)
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	key(h, "l")
	key(h, "keep")
	h.Update(enter())
	if !reflect.DeepEqual(*calls, []string{"20261013T090000-123456=keep"}) {
		t.Fatalf("calls %v", *calls)
	}
	if !strings.Contains(frame(h), "[keep]") {
		t.Errorf("row doesn't show the label:\n%s", frame(h))
	}
	// Esc cancels; an empty text clears.
	key(h, "l")
	key(h, "x")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.picker == nil || h.picker.labelling || len(*calls) != 1 {
		t.Fatalf("esc: %v", *calls)
	}
	key(h, "l")
	for range "keep" {
		h.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	h.Update(enter())
	if len(*calls) != 2 || strings.Contains(frame(h), "[keep]") {
		t.Errorf("clear: %v\n%s", *calls, frame(h))
	}
}

func TestLabelCommand(t *testing.T) {
	h := testHome(80)
	var calls []string
	h.label = func(id, text string) (string, string) {
		calls = append(calls, id+"="+text)
		return text, ""
	}
	if _, problem := h.labelCmd([]string{"hi"}); !strings.Contains(problem, "no Run in this session") {
		t.Errorf("no Run yet: %q", problem)
	}
	h.last = &lastRun{id: "20261013T101010-abcdef"}
	if _, problem := h.labelCmd([]string{"the", "retry", "fix"}); problem != "" {
		t.Fatal(problem)
	}
	if _, problem := h.labelCmd([]string{"20261012T080000-aaaaaa", "older", "one"}); problem != "" {
		t.Fatal(problem)
	}
	if _, problem := h.labelCmd([]string{"20261012T080000"}); problem != "" {
		t.Fatal(problem)
	}
	want := []string{"20261013T101010-abcdef=the retry fix", "20261012T080000-aaaaaa=older one", "20261012T080000="}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
	submit(h, "/label tidy")
	if calls[len(calls)-1] != "20261013T101010-abcdef=tidy" {
		t.Errorf("through the composer: %v", calls)
	}
}

func TestLabelEscapedInRunLine(t *testing.T) {
	r := &delivery.Run{ID: "20261013T101010-abcdef", Title: "t", Label: "a\x1b[31mb"}
	if got := runLine(r); strings.Contains(got, "\x1b") || !strings.Contains(got, "[") {
		t.Errorf("runLine %q", got)
	}
}

func TestRunUsageSuffix(t *testing.T) {
	for _, c := range []struct {
		u    *run.CutUsage
		want string
	}{
		{nil, ""},
		{&run.CutUsage{You: 4, Reported: 4, OutTokens: 12345}, "4 turns · 12.3k out tok"},
		{&run.CutUsage{You: 1, Reported: 1, OutTokens: 800}, "1 turn · 800 out tok"},
		{&run.CutUsage{You: 3, Feedback: 1, Reported: 2, OutTokens: 900}, "4 turns"}, // partial: no tokens
		{&run.CutUsage{You: 2}, "2 turns"},
	} {
		if got := runUsage(&delivery.Run{Usage: c.u}); got != c.want {
			t.Errorf("runUsage(%+v) = %q, want %q", c.u, got, c.want)
		}
	}
}
