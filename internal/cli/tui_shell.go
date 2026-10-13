package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Shell mode (#160, decision D-3): a composer line that starts with ! is a
// shell command for the human, run in the repo between Runs. Its output
// goes above the composer and nowhere else: never to an agent, a Briefing or
// the Ledger, and the line is kept in a shell history of its own, not the
// Task history. ↑ does not walk that history yet. A leading \! types a Task that starts
// with !; shell mode takes one line.

const (
	shellMaxBytes = 256 << 10 // the output kept: its tail
	shellMaxLines = 2000
	// shellGrace is how long a stopped command has to exit on SIGINT before
	// its group is killed.
	shellGrace = time.Second
)

// shellResult is what a shell command left.
type shellResult struct {
	out  string // the output tail, raw
	cut  int    // lines dropped before it
	code int
	err  error // the command could not start, or was stopped
	// detached: the command exited, but something it started kept the
	// output pipe open and was cut off.
	detached bool
}

// shellJob is the command in flight.
type shellJob struct {
	cancel context.CancelFunc
	ctx    context.Context
	line   string
	// finished closes once the runner has returned.
	finished chan struct{}
}

// shellDoneMsg is a shell command's end.
type shellDoneMsg struct {
	line string
	res  shellResult
	stop bool // Ctrl+C asked for it
}

// shellLine is the command in a composer text that starts with ! (after
// spaces), and whether it is shell mode at all.
func shellLine(text string) (string, bool) {
	t := strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(t, "!") {
		return "", false
	}
	return strings.TrimSpace(t[1:]), true
}

// unescapeBang drops the backslash of a leading \!, so a Task that starts
// with ! can be typed.
func unescapeBang(text string) (string, bool) {
	t := strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(t, "\\!") {
		return text, false
	}
	return t[1:], true
}

// shellArgv is the command line that runs line: the user's $SHELL -c
// (/bin/sh without one), cmd /C on Windows.
func shellArgv(goos string, getenv func(string) string, line string) []string {
	if goos == "windows" {
		return []string{"cmd", "/C", line}
	}
	sh := getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	return []string{sh, "-c", line}
}

// tailWriter keeps the last shellMaxBytes of what is written, and counts
// the lines it dropped.
type tailWriter struct {
	buf []byte
	cut int
	// partial: bytes were dropped, so the first line may be a fragment.
	partial bool
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > 2*shellMaxBytes {
		drop := len(w.buf) - shellMaxBytes
		w.cut += bytes.Count(w.buf[:drop], []byte{'\n'})
		w.buf = append([]byte(nil), w.buf[drop:]...)
		w.partial = true
	}
	return len(p), nil
}

// tail is the kept output: at most shellMaxBytes and shellMaxLines, whole
// lines, and how many lines went before it.
func (w *tailWriter) tail() (string, int) {
	b, cut := w.buf, w.cut
	if len(b) > shellMaxBytes {
		cut += bytes.Count(b[:len(b)-shellMaxBytes], []byte{'\n'})
		b = b[len(b)-shellMaxBytes:]
		w.partial = true
	}
	if w.partial { // the first line is a fragment: drop it
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			b = nil
		}
		cut++
	}
	s := string(b)
	if n := strings.Count(s, "\n"); n > shellMaxLines {
		lines := strings.SplitAfter(s, "\n")
		cut += len(lines) - 1 - shellMaxLines
		s = strings.Join(lines[len(lines)-1-shellMaxLines:], "")
	}
	return s, cut
}

// runShell runs line in root with the user's environment and no stdin,
// capturing stdout and stderr together. Cancelling ctx kills the command's
// whole process group (killGroup).
func runShell(ctx context.Context, root, line string) shellResult {
	argv := shellArgv(runtime.GOOS, os.Getenv, line)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	var w tailWriter
	cmd.Stdout, cmd.Stderr = &w, &w
	setGroup(cmd)
	done := make(chan struct{})
	defer close(done)
	// Ctrl+C: SIGINT to the group first, so git can remove its lock files;
	// the group is killed if it is still there after shellGrace.
	cmd.Cancel = func() error {
		err := interruptGroup(cmd)
		go func() {
			select {
			case <-time.After(shellGrace):
				killGroup(cmd)
			case <-done:
			}
		}()
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	out, cut := w.tail()
	res := shellResult{out: out, cut: cut}
	if err != nil {
		var ee *exec.ExitError
		switch {
		case errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil:
			res.detached = true // the command's own status was 0
		case errors.As(err, &ee) && ctx.Err() == nil:
			res.code = ee.ExitCode()
		default:
			res.code, res.err = -1, err
		}
	}
	return res
}

// shellSubmit starts line, which Enter took from the composer.
func (h *home) shellSubmit(line string) tea.Cmd {
	if line == "" {
		return nil
	}
	if strings.Contains(line, "\n") {
		h.msg = []string{"shell mode runs one line; remove the newlines"}
		return nil
	}
	if hiddenCount(line) > 0 {
		h.msg = []string{"shell: the command holds hidden characters: nothing ran"}
		return nil
	}
	if h.shell == nil {
		h.msg = []string{"shell: not available here"}
		return nil
	}
	h.cleared = ""
	h.shellHist.add(line) // never the Task history
	h.comp.set("")
	h.comp.clearUndo()
	h.navigating = false
	h.visual = 0
	ctx, cancel := context.WithCancel(context.Background())
	job := &shellJob{cancel: cancel, ctx: ctx, line: line, finished: make(chan struct{})}
	h.job = job
	run, root := h.shell, h.root
	echo := h.println(h.st.dim("! " + clean(line)))
	return tea.Sequence(echo, func() tea.Msg {
		res := run(ctx, root, line)
		close(job.finished)
		return shellDoneMsg{line: line, res: res, stop: ctx.Err() != nil}
	})
}

// shellDone prints what the command left, above the composer.
func (h *home) shellDone(m shellDoneMsg) tea.Cmd {
	if h.job != nil {
		h.job.cancel()
	}
	h.job = nil
	var lines []string
	if m.res.cut > 0 {
		lines = append(lines, h.st.dim(fmt.Sprintf("… %d lines cut", m.res.cut)))
	}
	out := strings.ReplaceAll(m.res.out, "\r\n", "\n")
	out = strings.TrimSuffix(out, "\n")
	if out != "" {
		for _, l := range strings.Split(out, "\n") {
			if i := strings.LastIndexByte(l, '\r'); i >= 0 { // a progress line: its last state
				l = l[i+1:]
			}
			lines = append(lines, clean(ansi.Strip(l)))
		}
	}
	switch {
	case m.stop:
		lines = append(lines, h.st.bad("interrupted"))
	case m.res.err != nil:
		lines = append(lines, h.st.bad("exit: "+clean(m.res.err.Error())))
	case m.res.code != 0:
		lines = append(lines, h.st.bad(fmt.Sprintf("exit %d", m.res.code)))
	case m.res.detached:
		lines = append(lines, h.st.dim("exit 0 (background output cut off)"))
	default:
		lines = append(lines, h.st.dim("exit 0"))
	}
	return h.println(strings.Join(lines, "\n"))
}

// shellHistoryFile is the shell history for the repo at root: a file of its
// own beside the Task history's, with the same properties (0600, a hashed
// name, never in the repo).
func shellHistoryFile(histDir, root string) string {
	return historyPath(filepath.Join(histDir, "shell"), root)
}
