package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/erengun/oge/internal/gate"
	"github.com/erengun/oge/internal/run"
)

// The Gate texts below are shared by both views (ADR-0022).

func sendBackText(ev run.Event) string {
	n := fmt.Sprintf("%d of %d", ev.SendBack, ev.SendBacks)
	if ev.SendBack > ev.SendBacks {
		n = fmt.Sprintf("%d (limit %d, extended at the Gate)", ev.SendBack, ev.SendBacks)
	}
	return n + " · the Candidate goes back to the implementer with the failure output"
}

// decidedText echoes a decision once it is recorded (ADR-0015).
func decidedText(ev run.Event) string {
	s := fmt.Sprintf("%s · recorded at the %s", ev.Decision.Choice, gateTitle(ev.Gate.Name))
	if ev.Decision.Reason != "" {
		s += " · reason: " + ev.Decision.Reason
	}
	if ev.Decision.Note != "" {
		s += " · note: " + ev.Decision.Note
	}
	return clean(s)
}

// gateScreen is an open Gate: what happened, what is pinned, what is
// needed and the choices. There is no default. The Check it follows is
// the line just above it, in both views.
func gateScreen(r gate.Request) []string {
	return gateLinesWith(r, clean(r.Need))
}

// gateLinesWith is the Gate's screen with need as the line that says
// what is needed, just above the choices.
func gateLinesWith(r gate.Request, need string) []string {
	lines := []string{gateTitle(r.Name), clean(r.What), pinsLine(r.Pins), "", need}
	for _, c := range r.Choices {
		key := c.Word
		if c.Key != "" {
			key = c.Key + "  " + c.Word
		}
		lines = append(lines, strings.TrimRight(fmt.Sprintf("  %-13s %s", key, c.Says), " "))
	}
	return lines
}

func pinsLine(p gate.Pins) string {
	var v []string
	for _, n := range p.Verdicts {
		v = append(v, fmt.Sprintf("#%d", n))
	}
	cand := p.Candidate
	if len(cand) > 7 {
		cand = cand[:7]
	}
	return fmt.Sprintf("Candidate %s · Attempt %s · Oracle v%d · Verdicts %s", cand, p.Attempt, p.Oracle, strings.Join(v, " "))
}

// gateHint is the line under the choices.
const gateHint = "There is no default: type a choice and press Enter."

// parkedSummary is a parked Run's end.
func (r *renderer) parkedSummary(res *run.Result) {
	r.p("")
	r.p("%-10s at the %s Gate · Candidate %s · Oracle v%d · %s", "PARKED", gateLabel(res), short(res.Candidate), res.Oracle, res.Duration.Round(100*time.Millisecond))
	for _, w := range res.Why {
		r.p("  %s", clean(w))
	}
	r.p("  Unattended Runs never decide a Gate; a human must (exit 10).")
	r.p("Nothing was written to your repository.")
}

func gateLabel(res *run.Result) string {
	if res.Gate == "" {
		return "?"
	}
	return strings.ReplaceAll(strings.TrimPrefix(res.Gate, "gate."), "_", "-")
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// gateTitle is a Gate's heading: "bound-exhaustion Gate", "Result gate".
func gateTitle(name string) string {
	if strings.HasSuffix(strings.ToLower(name), "gate") {
		return name
	}
	return name + " Gate"
}

// viewPort is the terminal implementation of the Gate port: a Gate waits
// on the view, which reads the human's entry.
type viewPort struct{ v view }

func (p viewPort) Decide(ctx context.Context, r gate.Request) (gate.Decision, error) {
	return p.v.gate(ctx, r)
}

// lines reads what the human types, one line at a time, without blocking
// the caller past ctx.
type lines struct {
	once sync.Once
	in   io.Reader
	ch   chan string
}

func (l *lines) next(ctx context.Context) (string, error) {
	l.once.Do(func() {
		l.ch = make(chan string)
		go func() {
			defer close(l.ch)
			sc := bufio.NewScanner(l.in)
			for sc.Scan() {
				l.ch <- sc.Text()
			}
		}()
	})
	select {
	case s, ok := <-l.ch:
		if !ok {
			return "", gate.ErrNoDecision
		}
		return s, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// gate shows an open Gate as plain lines and reads a decision. Enter alone
// does nothing; a choice typed in full takes its reason inline or at a
// prompt, where e opens $EDITOR and Ctrl-C returns to the Gate.
func (r *renderer) gate(ctx context.Context, req gate.Request) (gate.Decision, error) {
	r.p("")
	for _, l := range gateScreen(req) {
		r.p("%s", l)
	}
	r.p("%s", gateHint)
	for {
		fmt.Fprint(r.w, "> ")
		line, err := r.input.next(ctx)
		if err != nil {
			return gate.Decision{}, err
		}
		c, text, ok := req.Match(line)
		if !ok {
			if msg := notAChoice(req, line); msg != "" {
				r.p("  %s", msg)
			}
			continue
		}
		if text == "" && (c.Reason || c.Note) {
			text, ok, err = r.reason(ctx, c)
			if err != nil {
				return gate.Decision{}, err
			}
			if !ok {
				r.p("  back at the Gate. %s", gateHint)
				continue
			}
		}
		d, err := c.Decide(text)
		if err != nil {
			r.p("  %s", err)
			continue
		}
		return d, nil
	}
}

// reason reads c's reason or note. ok is false when Ctrl-C returned to
// the Gate.
func (r *renderer) reason(ctx context.Context, c gate.Choice) (string, bool, error) {
	back := make(chan struct{}, 1)
	if r.intr != nil {
		r.intr.divert(func() { back <- struct{}{} })
		defer r.intr.divert(nil)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type got struct {
		s   string
		err error
	}
	for {
		prompt := "reason (e opens $EDITOR, Ctrl-C goes back): "
		if !c.Reason {
			prompt = "note, optional (Enter for none, e opens $EDITOR): "
		}
		fmt.Fprint(r.w, prompt)
		ch := make(chan got, 1)
		lctx, lcancel := context.WithCancel(ctx)
		go func() {
			s, err := r.input.next(lctx)
			ch <- got{s, err}
		}()
		var g got
		select {
		case <-back:
			lcancel()
			r.p("")
			return "", false, nil
		case g = <-ch:
			lcancel()
		}
		if g.err != nil {
			return "", false, g.err
		}
		text := strings.TrimSpace(g.s)
		if text == "e" {
			if text, g.err = editReason(r.edit, c); g.err != nil {
				r.p("  the editor failed: %v", g.err)
				continue
			}
		}
		if c.Reason && text == "" {
			r.p("  a reason is required for %s", c.Word)
			continue
		}
		return text, true, nil
	}
}

// editReason writes a reason in $EDITOR; lines starting with # are
// dropped.
func editReason(edit func(string) error, c gate.Choice) (string, error) {
	if edit == nil {
		return "", errors.New("no editor")
	}
	tmp, err := os.CreateTemp("", "oge-reason-*.txt")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer os.Remove(path)
	_, werr := fmt.Fprintf(tmp, "\n# Your reason for %s. Lines starting with # are ignored.\n", c.Word)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		return "", errors.Join(werr, cerr)
	}
	if err := edit(path); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var kept []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			kept = append(kept, l)
		}
	}
	return strings.TrimSpace(strings.Join(kept, " ")), nil
}

// notAChoice says why line chose nothing; empty input says nothing.
func notAChoice(req gate.Request, line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	for _, c := range req.Choices {
		if c.Key == "" && strings.HasPrefix(c.Word, line) {
			return fmt.Sprintf("type %q in full", c.Word)
		}
	}
	return fmt.Sprintf("%q isn't a choice here", clean(line))
}
