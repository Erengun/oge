package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/erengun/oge/internal/receipt/receipttest"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

// vt is the little of a VT100 an inline Bubble Tea program needs: text,
// CR, LF and the cursor and erase controls. Everything else is skipped.
// Its screen keeps the scrollback, so stale lines left behind show up.
type vt struct {
	lines    [][]rune
	row, col int
	modes    map[string]rune // private mode: its last 'h' or 'l'
	kitty    int             // keyboard enhancements pushed and not popped
}

func (t *vt) line() []rune {
	for len(t.lines) <= t.row {
		t.lines = append(t.lines, nil)
	}
	return t.lines[t.row]
}

func (t *vt) put(r rune) {
	l := t.line()
	for len(l) <= t.col {
		l = append(l, ' ')
	}
	l[t.col] = r
	t.lines[t.row] = l
	t.col++
}

func (t *vt) write(b []byte) {
	rs := []rune(string(b))
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\r':
			t.col = 0
		case r == '\n': // as a terminal with ONLCR shows it
			t.row++
			t.col = 0
			t.line()
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j < len(rs) {
				t.csi(string(rs[i+2:j]), rs[j])
			}
			i = j
		case r == 0x1b && i+1 < len(rs) && (rs[i+1] == ']' || rs[i+1] == 'P' || rs[i+1] == '_'):
			// OSC, DCS, APC: skip to ST or BEL.
			j := i + 2
			for j < len(rs) && rs[j] != 0x07 && !(rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\') {
				j++
			}
			if j < len(rs) && rs[j] == 0x1b {
				j++
			}
			i = j
		case r == 0x1b:
			i++ // a two-byte escape
		case r < 0x20:
		default:
			t.put(r)
		}
	}
}

func (t *vt) csi(params string, final rune) {
	switch {
	case strings.HasPrefix(params, "?") && (final == 'h' || final == 'l'):
		if t.modes == nil {
			t.modes = map[string]rune{}
		}
		for _, m := range strings.Split(params[1:], ";") {
			t.modes[m] = final
		}
		return
	case strings.HasPrefix(params, ">") && final == 'u':
		t.kitty++
		return
	case strings.HasPrefix(params, "<") && final == 'u':
		t.kitty--
		return
	case strings.ContainsAny(params, "?<>=$"):
		return // other private sequences and queries
	}
	n := 1
	if params != "" && !strings.Contains(params, ";") {
		n = 0
		for _, c := range params {
			n = n*10 + int(c-'0')
		}
	}
	switch final {
	case 'A':
		t.row = max(t.row-n, 0)
	case 'B':
		t.row += n
		t.line()
	case 'C':
		t.col += n
	case 'D':
		t.col = max(t.col-n, 0)
	case 'G':
		t.col = max(n-1, 0)
	case 'K':
		if params == "" || params == "0" {
			l := t.line()
			if t.col < len(l) {
				t.lines[t.row] = l[:t.col]
			}
		}
	case 'J':
		if params == "" || params == "0" {
			l := t.line()
			if t.col < len(l) {
				t.lines[t.row] = l[:t.col]
			}
			t.lines = t.lines[:t.row+1]
		}
	}
}

func (t *vt) String() string {
	var b strings.Builder
	for _, l := range t.lines {
		b.WriteString(strings.TrimRight(string(l), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// The real program draws a frame that grows (activity appears) and then
// shrinks (the stage ends and the activity goes). The renderer must move
// back over the whole taller frame, or stale lines stay in the scrollback.
func TestTUIProgramLeavesNoStaleLinesWhenAFrameShrinks(t *testing.T) {
	for _, grow := range [][]int{{1, 3}, {3}, {2, 2}, {5}} {
		t.Run(fmt.Sprint(grow), func(t *testing.T) { shrinkCase(t, grow) })
	}
}

func shrinkCase(t *testing.T, grow []int) {
	h := newTUIHarness(t, false, 80)
	var out bytes.Buffer
	m := newModel(task.Parse("fix Add\n"), h.f, newStyles(false), time.Now)
	m.still = true // so the cursor rests below the frame when it shrinks
	u := &tui{in: strings.NewReader(""), out: &out, stderr: &out, plain: &renderer{w: &out, frozen: h.f}, m: m}
	pause := func() { time.Sleep(250 * time.Millisecond) }
	h.res.Dir = t.TempDir()
	if err := receipttest.Named("accepted-fast").WriteRun(h.res.Dir); err != nil {
		t.Fatal(err)
	}
	res, err := u.show(context.Background(), newInterrupts(func() {}), func(_ context.Context, observe func(run.Event)) (*run.Result, error) {
		observe(run.Event{Kind: run.EvStarted, Result: h.res})
		observe(run.Event{Kind: run.EvPreflight, Result: h.res})
		pause()
		for _, n := range grow {
			for i := range n {
				observe(run.Event{Kind: run.EvAgent, Agent: agent.Event{Kind: agent.Claim, Text: fmt.Sprint("step ", i)}, Attempt: h.att})
			}
			pause()
		}
		h.att.Exit, h.att.Candidate, h.att.Changed = "done", "6d1231d9f00d", []string{"add.go"}
		observe(run.Event{Kind: run.EvAttempt, Result: h.res, Attempt: h.att})
		pause()
		h.res.Candidate = h.att.Candidate
		observe(run.Event{Kind: run.EvCheck, Result: h.res, Check: &oracle.Result{Pass: true,
			Commands: []oracle.Execution{{Run: "go test -json ./...", Pass: true, DurationMs: 900}}}})
		h.res.Outcome = run.Accepted
		return h.res, nil
	})
	if err != nil || res.Outcome != run.Accepted {
		t.Fatalf("%v %v", res, err)
	}
	var screen vt
	screen.write(out.Bytes())
	got := screen.String()
	for _, once := range []string{"fix Add\nRun ", "Run 20261005T090000-a1b2c3 · Fast mode · HEAD", "✓ preflight", "✓ Accepted"} {
		if n := strings.Count(got, once); n != 1 {
			t.Errorf("%q is on the screen %d times:\n%s\n%q", once, n, got, out.String())
		}
	}
}

// assertRestored fails unless the terminal is back as a shell expects it:
// cursor shown, every other private mode reset, no keyboard enhancement
// left pushed.
func (t *vt) assertRestored(tb testing.TB) {
	tb.Helper()
	for m, v := range t.modes {
		want := 'l'
		if m == "25" {
			want = 'h'
		}
		if v != want {
			tb.Errorf("private mode ?%s left %c", m, v)
		}
	}
	if t.kitty > 0 {
		tb.Errorf("%d keyboard enhancements left pushed", t.kitty)
	}
}

// A Run panic, or a forced stop, restores the terminal before show
// returns.
func TestTUIRestoresTheTerminalOnEveryExit(t *testing.T) {
	for _, c := range []struct {
		name  string
		start startFunc
		force bool
	}{
		{"panic", func(context.Context, func(run.Event)) (*run.Result, error) {
			time.Sleep(200 * time.Millisecond)
			panic("boom")
		}, false},
		{"forced", func(context.Context, func(run.Event)) (*run.Result, error) {
			select {} // a Run that never stops
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newTUIHarness(t, false, 80)
			var out bytes.Buffer
			u := &tui{in: strings.NewReader(""), out: &out, stderr: &bytes.Buffer{}, plain: &renderer{w: &out, frozen: h.f}, m: h.m}
			in := newInterrupts(func() {})
			if c.force {
				time.AfterFunc(300*time.Millisecond, func() { in.interrupt(); in.interrupt() })
			}
			returned := make(chan any, 1)
			go func() {
				defer func() { returned <- recover() }()
				_, err := u.show(context.Background(), in, c.start)
				if !errors.Is(err, errForced) {
					t.Errorf("show returned %v", err)
				}
			}()
			select {
			case p := <-returned:
				if c.force == (p != nil) {
					t.Errorf("recovered %v", p)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("show never returned")
			}
			var screen vt
			screen.write(out.Bytes())
			if len(screen.modes) == 0 {
				t.Fatal("the view never set a mode, so this test checks nothing")
			}
			screen.assertRestored(t)
		})
	}
}

// The plain view stops waiting once forced.
func TestPlainViewForcedStopReturnsWithoutTheRun(t *testing.T) {
	in := newInterrupts(func() {})
	in.interrupt()
	in.interrupt()
	r := &renderer{w: &bytes.Buffer{}}
	_, err := r.show(context.Background(), in, func(context.Context, func(run.Event)) (*run.Result, error) { select {} })
	if !errors.Is(err, errForced) {
		t.Errorf("show returned %v", err)
	}
}
