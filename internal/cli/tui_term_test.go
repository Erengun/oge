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

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

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
	height   int             // screen rows; 0 is a screen that never scrolls
	top      int             // the first screen row (a line index) once height is set
	width    int             // screen columns; 0 is 80
	pend     bool            // the last column was written: the wrap waits for the next printable
	cont     []bool          // cont[i]: row i continues row i-1 because it wrapped
	pen      uv.Style        // the SGR state new text is drawn with
	styles   [][]uv.Style    // styles[i][j]: the pen lines[i][j] was drawn with
	alt      *vt             // the main screen, saved while the alternate one (?1049) is shown
	margin   [2]int          // DECSTBM: first and last scrolling row of the screen, 1-based (0: unset)
	lastRune rune            // for REP
}

// cols is the screen width, 80 unless set.
func (t *vt) cols() int {
	if t.width > 0 {
		return t.width
	}
	return 80
}

// lf moves down a row; at the bottom of a screen with a height the screen
// scrolls and the top line joins the scrollback.
func (t *vt) lf() {
	if t.margin != [2]int{} && t.row == t.top+t.regionBottom() {
		// At the bottom margin the region scrolls, the cursor stays.
		t.scrollRegion(t.top+t.regionTop(), -1)
		return
	}
	if t.height > 0 && t.row == t.top+t.height-1 {
		t.top++
	}
	t.row++
	t.pend = false
	t.line()
}

// up and down move the cursor within the screen, as CUU and CUD do.
func (t *vt) up(n int) { t.pend = false; t.row = max(t.row-n, t.top) }
func (t *vt) down(n int) {
	t.pend = false
	t.row += n
	if t.height > 0 {
		t.row = min(t.row, t.top+t.height-1)
	}
	t.line()
}

// insertLines is IL: blank lines at the cursor row push the rows below
// down, and the ones pushed off the bottom of the screen are lost.
func (t *vt) insertLines(n int) {
	t.line()
	t.pend = false
	blank := make([][]rune, n)
	t.lines = append(t.lines[:t.row], append(blank, t.lines[t.row:]...)...)
	t.cont = append(t.cont[:t.row], append(make([]bool, n), t.cont[t.row:]...)...)
	t.styles = append(t.styles[:t.row], append(make([][]uv.Style, n), t.styles[t.row:]...)...)
	if t.height > 0 && len(t.lines) > t.top+t.height {
		t.lines = t.lines[:t.top+t.height]
		t.cont = t.cont[:t.top+t.height]
		t.styles = t.styles[:t.top+t.height]
	}
	t.col = 0
}

func (t *vt) line() []rune {
	for len(t.lines) <= t.row {
		t.lines = append(t.lines, nil)
	}
	for len(t.cont) < len(t.lines) {
		t.cont = append(t.cont, false)
	}
	for len(t.styles) < len(t.lines) {
		t.styles = append(t.styles, nil)
	}
	return t.lines[t.row]
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// regionTop and regionBottom are the scrolling region's rows, 0-based in the
// screen.
func (t *vt) regionTop() int {
	if t.margin[0] > 0 {
		return t.margin[0] - 1
	}
	return 0
}

func (t *vt) regionBottom() int {
	if t.margin[1] > 0 {
		return t.margin[1] - 1
	}
	return max(t.height, 1) - 1
}

// scrollRegion moves the rows from row (a line index) to the region's bottom
// up (n < 0) or down (n > 0), blank rows coming in.
func (t *vt) scrollRegion(row, n int) {
	bottom := t.top + t.regionBottom()
	for len(t.lines) <= bottom {
		t.lines = append(t.lines, nil)
	}
	t.line()
	for len(t.cont) < len(t.lines) {
		t.cont = append(t.cont, false)
	}
	for len(t.styles) < len(t.lines) {
		t.styles = append(t.styles, nil)
	}
	if row > bottom {
		return
	}
	k := min(abs(n), bottom-row+1)
	if n < 0 {
		copy(t.lines[row:bottom+1], t.lines[row+k:bottom+1])
		copy(t.styles[row:bottom+1], t.styles[row+k:bottom+1])
		for i := bottom - k + 1; i <= bottom; i++ {
			t.lines[i], t.styles[i] = nil, nil
		}
	} else {
		copy(t.lines[row+k:bottom+1], t.lines[row:bottom+1-k])
		copy(t.styles[row+k:bottom+1], t.styles[row:bottom+1-k])
		for i := row; i < row+k; i++ {
			t.lines[i], t.styles[i] = nil, nil
		}
	}
	t.pend = false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// altScreen switches to the alternate screen (on) or back to the main one.
func (t *vt) altScreen(on bool) {
	if on == (t.alt != nil) {
		return
	}
	if on {
		saved := *t
		saved.modes = nil
		t.alt = &saved
		t.lines, t.cont, t.styles = nil, nil, nil
		t.row, t.col, t.top, t.pend = 0, 0, 0, false
		t.margin = [2]int{}
		return
	}
	s := t.alt
	t.lines, t.cont, t.styles = s.lines, s.cont, s.styles
	t.row, t.col, t.top, t.pend = s.row, s.col, s.top, s.pend
	t.margin = [2]int{}
	t.alt = nil
}

func (t *vt) put(r rune) {
	t.lastRune = r
	if t.pend { // deferred wrap, as xterm does it
		t.pend = false
		t.col = 0
		t.lf()
		t.cont[t.row] = true
	}
	l := t.line()
	// Cells past the line's end were erased: so are their styles.
	st := t.styles[t.row][:min(len(t.styles[t.row]), len(l))]
	for len(l) <= t.col {
		l = append(l, ' ')
	}
	l[t.col] = r
	t.lines[t.row] = l
	for len(st) < len(l) {
		st = append(st, uv.Style{})
	}
	st[t.col] = t.pen
	t.styles[t.row] = st
	t.col++
	if t.col >= t.cols() {
		t.col = t.cols() - 1
		t.pend = true
	}
}

func (t *vt) write(b []byte) {
	rs := []rune(string(b))
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\r':
			t.col = 0
			t.pend = false
		case r == '\n': // as a terminal with ONLCR shows it
			t.lf()
			t.col = 0
		case r == '\t': // to the next tab stop, every 8 columns
			t.col = min((t.col/8+1)*8, t.cols()-1)
			t.pend = false
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
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == 'M': // RI: up a row, or the region down
			t.pend = false
			if t.row == t.top+t.regionTop() {
				t.scrollRegion(t.row, 1)
			} else {
				t.row = max(t.row-1, t.top)
			}
			i++
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == 'D': // IND
			t.lf()
			i++
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
			if m == "1049" || m == "1047" || m == "47" {
				t.altScreen(final == 'h')
			}
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
	case final == 'm':
		p := ansi.NewParser()
		ansi.DecodeSequence("\x1b["+params+"m", 0, p)
		uv.ReadStyle(p.Params(), &t.pen)
		return
	}
	n := 1
	if params != "" && !strings.Contains(params, ";") {
		n = 0
		for _, c := range params {
			n = n*10 + int(c-'0')
		}
	}
	switch final {
	case 'C', 'D', 'G', 'H', 'f', 'd':
		t.pend = false
	}
	switch final {
	case 'H', 'f': // CUP: row;col, 1-based, in the screen
		r, c := 1, 1
		ps := strings.Split(params, ";")
		if v := atoi(ps[0]); v > 0 {
			r = v
		}
		if len(ps) > 1 {
			if v := atoi(ps[1]); v > 0 {
				c = v
			}
		}
		t.row, t.col = t.top+r-1, c-1
		t.line()
		return
	case 'd': // VPA
		t.row = t.top + max(n, 1) - 1
		t.line()
		return
	case 'r': // DECSTBM
		t.margin = [2]int{}
		if ps := strings.Split(params, ";"); len(ps) == 2 && atoi(ps[0]) > 0 && atoi(ps[1]) > 0 {
			t.margin = [2]int{atoi(ps[0]), atoi(ps[1])}
		}
		t.row, t.col = t.top, 0
		return
	case 'M': // DL
		t.scrollRegion(t.row, -n)
		return
	case 'S':
		t.scrollRegion(t.top+t.regionTop(), -n)
		return
	case 'T':
		t.scrollRegion(t.top+t.regionTop(), n)
		return
	case 'P': // DCH
		l := t.line()
		st := t.styles[t.row]
		if t.col < len(l) {
			e := min(t.col+n, len(l))
			t.lines[t.row] = append(l[:t.col:t.col], l[e:]...)
			if t.col < len(st) {
				t.styles[t.row] = append(st[:t.col:t.col], st[min(e, len(st)):]...)
			}
		}
		return
	case '@': // ICH
		l := t.line()
		for len(l) < t.col {
			l = append(l, ' ')
		}
		blank := []rune(strings.Repeat(" ", n))
		t.lines[t.row] = append(l[:t.col:t.col], append(blank, l[t.col:]...)...)
		t.styles[t.row] = nil
		return
	case 'b': // REP
		for range n {
			t.put(t.lastRune)
		}
		return
	}
	switch final {
	case 'A':
		t.up(n)
	case 'B':
		t.down(n)
	case 'L':
		t.insertLines(n)
	case 'C':
		t.col = min(t.col+n, t.cols()-1)
	case 'X': // ECH: blank n cells from the cursor, no move
		l := t.line()
		for i := t.col; i < t.col+n && i < len(l); i++ {
			l[i] = ' '
		}
	case 'D':
		t.col = max(t.col-n, 0)
	case 'G':
		t.col = max(n-1, 0)
	case 'K':
		switch params {
		case "", "0":
			l := t.line()
			if t.col < len(l) {
				t.lines[t.row] = l[:t.col]
			}
		case "1":
			l := t.line()
			for i := 0; i <= t.col && i < len(l); i++ {
				l[i] = ' '
			}
		case "2":
			t.line()
			t.lines[t.row] = nil
			t.styles[t.row] = nil
		}
	case 'J':
		if params == "2" || params == "3" {
			t.line()
			for i := t.top; i < len(t.lines); i++ {
				t.lines[i], t.styles[i] = nil, nil
			}
		}
		if params == "" || params == "0" {
			l := t.line()
			if t.col < len(l) {
				t.lines[t.row] = l[:t.col]
			}
			t.lines = t.lines[:t.row+1]
			t.cont = t.cont[:t.row+1]
			t.styles = t.styles[:t.row+1]
		}
	}
}

func (t *vt) String() string {
	var b strings.Builder
	for i, l := range t.lines {
		// A wrapped row is joined to the one before, so a long line reads as
		// the one line it was written as.
		if i+1 < len(t.lines) && i+1 < len(t.cont) && t.cont[i+1] {
			b.WriteString(string(l))
			continue
		}
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

func TestVTWrapsLongLinesAndJoinsThemInString(t *testing.T) {
	var s vt
	long := strings.Repeat("a", 80) + strings.Repeat("b", 5)
	s.write([]byte(long + "\nnext\n"))
	if len(s.lines) < 3 || string(s.lines[0]) != strings.Repeat("a", 80) || string(s.lines[1]) != "bbbbb" {
		t.Fatalf("rows: %q", s.lines)
	}
	if want := long + "\nnext\n\n"; s.String() != want {
		t.Errorf("String() = %q, want %q", s.String(), want)
	}
}

func TestVTDefersTheWrapAtTheLastColumn(t *testing.T) {
	var s vt
	s.write([]byte(strings.Repeat("a", 80) + "\r\nb\n"))
	if len(s.lines) != 3 || s.row != 2 {
		t.Errorf("an exact-width line took %d rows: %q", len(s.lines), s.lines)
	}
	if want := strings.Repeat("a", 80) + "\nb\n\n"; s.String() != want {
		t.Errorf("String() = %q", s.String())
	}
}

func TestVTInsertLinesAfterAWrappedLine(t *testing.T) {
	var s vt
	s.write([]byte(strings.Repeat("a", 85) + "\nz"))
	s.write([]byte("\x1b[1A\x1b[1A\x1b[2L")) // row 0 is the first half of the wrapped line
	if len(s.lines) != 5 || s.lines[0] != nil || s.lines[1] != nil || s.cont[3] != true {
		t.Fatalf("rows %q cont %v", s.lines, s.cont)
	}
	if want := "\n\n" + strings.Repeat("a", 85) + "\nz\n"; s.String() != want {
		t.Errorf("String() = %q", s.String())
	}
}

func TestVTEraseCharactersDoesNotMoveTheCursor(t *testing.T) {
	var s vt
	s.write([]byte("abcdef\r\x1b[2C\x1b[2Xg"))
	if got := s.String(); got != "abg ef\n" {
		t.Errorf("got %q", got)
	}
}
