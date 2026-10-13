package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/task"
)

// shellHome is a home with a fake runner, its histories in a temp dir.
func shellHome(t *testing.T, run func(ctx context.Context, root, line string) shellResult) *home {
	t.Helper()
	dir := t.TempDir()
	h := testHome(70)
	h.shell, h.root = run, dir
	h.hist = loadHistory(historyPath(filepath.Join(dir, "state"), dir))
	h.shellHist = loadHistory(shellHistoryFile(filepath.Join(dir, "state"), dir))
	return h
}

// seqCmds is the commands in what tea.Sequence returned (its message type
// is unexported: a slice of commands).
func seqCmds(msg tea.Msg) []tea.Cmd {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice {
		return nil
	}
	var cmds []tea.Cmd
	for i := 0; i < v.Len(); i++ {
		if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// play runs cmd and feeds a shell command's end back to the home, as the
// program would.
func play(h *home, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch m := cmd().(type) {
	case shellDoneMsg:
		h.Update(m)
	case tea.BatchMsg:
		for _, c := range m {
			play(h, c)
		}
	default:
		for _, c := range seqCmds(m) {
			play(h, c)
		}
	}
}

// runShellLine types line, presses Enter and plays the command's end.
func runShellLine(h *home, line string) {
	typeText(h, line)
	_, cmd := h.Update(enter())
	play(h, cmd)
}

func fakeRunner(res shellResult) func(context.Context, string, string) shellResult {
	return func(context.Context, string, string) shellResult { return res }
}

func scrollText(h *home) string { return strings.Join(h.scroll, "\n") }

func TestShellPrintsCleanedOutputAndExitStatus(t *testing.T) {
	h := shellHome(t, fakeRunner(shellResult{out: "ok \x1b[31mred\x1b[0m\nnext\n", code: 1}))
	runShellLine(h, "!make test")
	got := scrollText(h)
	for _, want := range []string{"! make test", "ok red", "next", "exit 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("scroll lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[31m") || strings.Contains(got, "[31m") {
		t.Errorf("the escape sequence was not removed:\n%q", got)
	}
	if h.job != nil || !h.comp.empty() {
		t.Errorf("after the command: job=%v box=%q", h.job, h.comp.text())
	}
}

func TestShellExitZeroAndStartFailure(t *testing.T) {
	h := shellHome(t, fakeRunner(shellResult{out: "hi\n"}))
	runShellLine(h, "  !echo hi")
	if got := scrollText(h); !strings.Contains(got, "exit 0") {
		t.Errorf("no exit status:\n%s", got)
	}
	h = shellHome(t, fakeRunner(shellResult{code: -1, err: os.ErrNotExist}))
	runShellLine(h, "!x")
	if got := scrollText(h); !strings.Contains(got, "exit: ") {
		t.Errorf("start failure:\n%s", got)
	}
}

func TestShellCtrlCStopsTheCommandAndDoesNotQuit(t *testing.T) {
	started := make(chan struct{})
	h := shellHome(t, func(ctx context.Context, _, _ string) shellResult {
		close(started)
		<-ctx.Done()
		return shellResult{code: -1, err: ctx.Err()}
	})
	typeText(h, "!sleep 99")
	_, cmd := h.Update(enter())
	if h.job == nil {
		t.Fatal("no job after Enter")
	}
	if !strings.Contains(frame(h), "running… (ctrl+c to stop)") {
		t.Errorf("the composer does not say it runs:\n%s", frame(h))
	}
	done := make(chan tea.Msg)
	go func() {
		for _, c := range seqCmds(cmd()) {
			if m, ok := c().(shellDoneMsg); ok {
				done <- m
			}
		}
	}()
	<-started
	h.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // typed ahead: waits
	if _, c := h.Update(ctrlKey('c')); c != nil || h.quit {
		t.Fatalf("Ctrl+C quit or returned %v", c)
	}
	if h.job.ctx.Err() == nil {
		t.Error("Ctrl+C did not cancel the command's context")
	}
	h.Update(<-done)
	if got := scrollText(h); !strings.Contains(got, "interrupted") {
		t.Errorf("no interrupted line:\n%s", got)
	}
	if !h.comp.empty() {
		t.Errorf("a key typed during the command reached the box: %q", h.comp.text())
	}
}

func TestShellOutputIsCapped(t *testing.T) {
	var w tailWriter
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString("line\n")
	}
	w.Write([]byte(b.String()))
	out, cut := w.tail()
	if n := strings.Count(out, "\n"); n != shellMaxLines || cut != 5000-shellMaxLines {
		t.Errorf("kept %d lines, cut %d", n, cut)
	}
	// By bytes: long lines.
	w = tailWriter{}
	long := strings.Repeat("x", 1023) + "\n"
	for i := 0; i < 2000; i++ {
		w.Write([]byte(long))
	}
	out, cut = w.tail()
	if len(out) > shellMaxBytes || cut == 0 || !strings.HasPrefix(out, "x") || !strings.HasSuffix(out, "\n") {
		t.Errorf("kept %d bytes, cut %d", len(out), cut)
	}
	h := shellHome(t, fakeRunner(shellResult{out: "a\nb\n", cut: 7}))
	runShellLine(h, "!x")
	if got := scrollText(h); !strings.HasPrefix(got, "! x\n… 7 lines cut") {
		t.Errorf("no cut note:\n%s", got)
	}
}

func TestShellIsNotATask(t *testing.T) {
	h := shellHome(t, fakeRunner(shellResult{out: "x\n"}))
	h.start = func(task.Task, *resolved, int, int) (*model, tea.Cmd) {
		t.Fatal("a shell line started a Run")
		return nil, nil
	}
	runShellLine(h, "!echo secret-ish")
	if h.run != nil || h.hist.len() != 0 {
		t.Errorf("Task history has %d entries, run=%v", h.hist.len(), h.run)
	}
	if h.shellHist.len() != 1 || h.shellHist.entries[0] != "echo secret-ish" {
		t.Errorf("shell history: %v", h.shellHist.entries)
	}
	b, err := os.ReadFile(h.shellHist.path)
	if err != nil || !strings.Contains(string(b), "echo secret-ish") {
		t.Fatalf("shell history file: %v %q", err, b)
	}
	if _, err := os.Stat(h.hist.path); err == nil {
		t.Error("the Task history file was written")
	}
	if h.shellHist.path == h.hist.path {
		t.Error("one file for both histories")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(h.shellHist.path); fi.Mode().Perm() != 0o600 {
			t.Errorf("shell history mode %v", fi.Mode().Perm())
		}
	}
}

func TestShellRefusedWhileARunIsActive(t *testing.T) {
	called := false
	h := shellHome(t, func(context.Context, string, string) shellResult { called = true; return shellResult{} })
	h.run = &model{}
	typeText(h, "!ls")
	h.Update(enter())
	if called || h.job != nil {
		t.Error("a shell command started during a Run")
	}
}

func TestShellHiddenCharactersAreRefused(t *testing.T) {
	called := false
	h := shellHome(t, func(context.Context, string, string) shellResult { called = true; return shellResult{} })
	typeText(h, "!echo a\u202eb")
	h.Update(enter())
	if called || h.job != nil || len(h.msg) == 0 {
		t.Errorf("ran=%v msg=%v", called, h.msg)
	}
}

func TestShellComposerGolden(t *testing.T) {
	h := testHome(70)
	typeText(h, "!git status")
	golden(t, "home-shell", frame(h))
	h.shell = func(context.Context, string, string) shellResult { return shellResult{} }
	h.Update(enter())
	golden(t, "home-shell-running", frame(h))
}

func TestShellArgv(t *testing.T) {
	env := func(k string) string { return map[string]string{"SHELL": "/bin/zsh"}[k] }
	if got := shellArgv("linux", env, "ls"); strings.Join(got, "|") != "/bin/zsh|-c|ls" {
		t.Errorf("%v", got)
	}
	if got := shellArgv("linux", func(string) string { return "" }, "ls"); got[0] != "/bin/sh" {
		t.Errorf("%v", got)
	}
	if got := shellArgv("windows", env, "dir"); strings.Join(got, "|") != "cmd|/C|dir" {
		t.Errorf("%v", got)
	}
}

// One real command, no fakes: output and exit status in the repo dir.
func TestRunShellRealCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Setenv("SHELL", "/bin/sh") // not the login shell, whose rc files may print
	}
	dir := t.TempDir()
	res := runShell(context.Background(), dir, "echo hi")
	if res.code != 0 || res.err != nil || strings.TrimSpace(res.out) != "hi" {
		t.Errorf("%+v", res)
	}
	res = runShell(context.Background(), dir, "exit 3")
	if res.code != 3 || res.err != nil {
		t.Errorf("%+v", res)
	}
}
