package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/run"
	"golang.org/x/term"
)

// afterRun is what follows an Accepted Run's summary: --apply applies the
// Candidate, the live view offers the action bar, and plain lines print
// the equivalent commands (#54, ADR-0019). Nothing here runs for a Run
// that isn't Accepted, except --apply saying it applied nothing.
func afterRun(env Env, f runFlags, v view, root string, res *run.Result) int {
	if res.Outcome != run.Accepted {
		if f.apply && res.Candidate != "" {
			fmt.Fprintf(env.Stdout, "--apply applies only an Accepted Candidate; this Run is %s, so nothing was applied.\n", res.Outcome)
		}
		return ExitOK
	}
	r, err := delivery.Load(res.Dir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: reading the Run for delivery: %v\n", err)
		return ExitInternal
	}
	if f.apply {
		// TODO(#54-decision): an Accepted Run whose --apply is refused
		// (the working tree changed in a conflicting way during the Run)
		// exits 2, not 0: what was asked for didn't happen. The Run stays
		// Accepted and oge apply delivers it later.
		return applyCommand(env, r, root, "")
	}
	// --unattended never prompts, even when the live view draws.
	if _, live := v.(*tui); live && !f.unattended {
		var bar *actionBar
		bar = newActionBar(env, r, root, func() {
			// Once applied, the summary no longer says nothing was written.
			p := plainOf(v)
			p.applying = bar.applied
			p.summary(res)
		})
		fmt.Fprintln(env.Stdout)
		bar.run()
		return ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-10s oge apply %s · oge diff %s · oge branch %s\n", "next", r.ID, r.ID, r.ID)
	return ExitOK
}

// actionBar is the end-of-run choice after an Accepted Run in the live
// view: [a] applies (the human's explicit act of taking the result), [d]
// pages the diff and comes back, [r] shows the summary again, q or Enter
// leaves. It is never offered for a Run that isn't Accepted, which takes
// oge apply --overridden or --rejected (ADR-0015).
type actionBar struct {
	in      io.Reader
	out     io.Writer
	st      styles
	applied bool
	// raw puts the terminal in raw mode, so a key needs no Enter, and
	// returns the restore; nil when the input isn't a terminal.
	raw     func() func()
	apply   func() string // the confirmation (or refusal) line
	diff    func()
	receipt func()
}

func newActionBar(env Env, r *delivery.Run, root string, receipt func()) *actionBar {
	// TODO(#63): [r] shows the Receipt; until it lands, the summary.
	b := &actionBar{in: env.Stdin, out: env.Stdout, st: newStyles(colorAllowed(env.Getenv)), receipt: receipt}
	if f, ok := env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b.raw = func() func() {
			old, err := term.MakeRaw(int(f.Fd()))
			if err != nil {
				return func() {}
			}
			// Keys typed before the bar (or in the pager) are dropped:
			// none of them was meant for it.
			flushInput(int(f.Fd()))
			return func() { _ = term.Restore(int(f.Fd()), old) }
		}
	}
	b.apply = func() string {
		a, err := delivery.Apply(r, root, "")
		var conflict *delivery.ConflictError
		switch {
		case errors.As(err, &conflict):
			line := "Not applied: your working tree changed since the Snapshot. Nothing was written."
			for _, c := range conflict.Conflicts {
				line += "\n  " + clean(c)
			}
			return line + fmt.Sprintf("\nCommit, stash or undo those edits, then oge apply %s.", r.ID)
		case err != nil:
			return "Not applied: " + clean(err.Error())
		}
		b.applied = true
		return appliedLine(r, a.Plan)
	}
	b.diff = func() {
		patch, err := delivery.Diff(r)
		switch {
		case err != nil:
			fmt.Fprintf(b.out, "oge: %v\n", err)
		case len(patch) == 0:
			fmt.Fprintf(b.out, "Candidate %s changes nothing since the Snapshot.\n", delivery.Short(r.Candidate))
		default:
			if err := showDiff(env, r, patch); err != nil {
				fmt.Fprintf(b.out, "oge: showing the diff: %v\n", err)
			}
		}
	}
	return b
}

// render is the bar's line.
func (b *actionBar) render() string {
	keys := "[a] apply   [d] diff   [r] receipt"
	if b.applied {
		keys = "[d] diff   [r] receipt"
	}
	return b.st.bold(keys) + "   " + b.st.dim("q to leave")
}

// run shows the bar until the human leaves. Each key's output goes below
// it in ordinary (cooked) mode, then the bar comes back.
func (b *actionBar) run() {
	restore := func() {}
	if b.raw != nil {
		restore = b.raw()
	}
	defer func() { restore() }()
	draw := func() { fmt.Fprint(b.out, "\r\x1b[K"+b.render()) }
	clear := func() { fmt.Fprint(b.out, "\r\x1b[K") }
	act := func(f func()) {
		clear()
		restore()
		f()
		if b.raw != nil {
			restore = b.raw()
		}
		draw()
	}
	draw()
	var keys keyParser
	buf := make([]byte, 256)
	for {
		n, err := b.in.Read(buf)
		if n == 0 && err != nil {
			clear()
			return
		}
		// A lone q or Enter is a keypress even inside a paste that never
		// ended: the human can always leave.
		if n == 1 && keys.paste && (buf[0] == 'q' || buf[0] == '\r' || buf[0] == '\n') {
			clear()
			return
		}
		got := keys.feed(buf[:n])
		if keys.interrupted {
			clear()
			return
		}
		// Only a single real keypress acts: one byte read alone, outside
		// any escape sequence or paste. An arrow key, a paste or typing
		// ahead never applies.
		if n != 1 || len(got) != 1 {
			continue
		}
		switch got[0] {
		case 'a':
			if !b.applied {
				act(func() { fmt.Fprintln(b.out, b.apply()) })
			}
		case 'd':
			act(b.diff)
		case 'r':
			act(b.receipt)
		case 'q', '\r', '\n':
			clear()
			return
		}
	}
}

// keyParser splits terminal input into plain keys, dropping whole escape
// sequences (CSI, SS3, Alt+key) and everything in a bracketed paste. Its
// state carries across reads, so a sequence split over two reads is still
// one sequence.
type keyParser struct {
	state       int // 0 plain, 1 after ESC, 2 in CSI, 3 after SS3
	params      []byte
	paste       bool
	interrupted bool // Ctrl-C or Ctrl-D, in any state
}

func (k *keyParser) feed(b []byte) []byte {
	var keys []byte
	for _, c := range b {
		if c == 0x03 || c == 0x04 {
			// Ctrl-C and Ctrl-D interrupt from any state.
			k.interrupted = true
			return keys
		}
		switch k.state {
		case 1:
			switch c {
			case '[':
				k.state, k.params = 2, k.params[:0]
			case 'O':
				k.state = 3
			default:
				k.state = 0 // Alt+key: dropped with its ESC
			}
		case 2:
			if c >= 0x40 && c <= 0x7e {
				switch p := string(k.params); {
				case c == '~' && p == "200":
					k.paste = true
				case c == '~' && p == "201":
					k.paste = false
				}
				k.state = 0
			} else {
				k.params = append(k.params, c)
			}
		case 3:
			k.state = 0
		default:
			switch {
			case c == 0x1b:
				k.state = 1
			case k.paste:
			default:
				keys = append(keys, c)
			}
		}
	}
	return keys
}

// plainOf is the plain renderer behind a view: the view itself, or the
// one the live view prints its summary with.
func plainOf(v view) *renderer {
	if u, ok := v.(*tui); ok {
		return u.plain
	}
	return v.(*renderer)
}
