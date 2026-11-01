package cli

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Off-terminal reach (docs/ux/away.md, slice 8, decision A8). While the
// human is away, three messages and nothing else leave the terminal: a
// Chat needs you, every Chat settled, the Desk stopped. The texts are
// fixed and carry the Chat number, counts and the repo's name. A Chat's
// title is the human's own Task text, so it appears only with
// [notify] titles = true. Agent text never does.

const (
	// reachCoalesce is how long a "needs you" holds back the next one.
	reachCoalesce = 15 * time.Minute
	// reachTimeout bounds the [notify] command.
	reachTimeout = 10 * time.Second
	// reachExitWait is the longest the stop message holds Öge's exit.
	reachExitWait = 3 * time.Second
)

// reachChat is what the policy reads of one Chat.
type reachChat struct {
	num     int
	repo    string // the repo's base name; "" when unknown
	title   string
	mark    string // the Desk glyph: ▲ ✗ ✓ ● ? ○
	askedAt time.Time
}

// reach is the three-message policy. It holds no clock and no I/O: the
// caller passes the time and whether the human is away, and sends what
// comes back.
type reach struct {
	titles bool
	stale  time.Duration // [desk] stale_gate

	lastNeed    time.Time    // when a "needs you" last went out
	told        map[int]bool // the Chats the last "needs you" counted
	staled      map[int]bool // the Chats whose stale Gate was re-sent
	worked      bool         // a Chat worked while away
	settledSent bool
}

// reachText is a message and the Desk state herdr shows it under.
type reachText struct {
	text    string
	blocked bool // true: needs you; false: settled or stopped
}

func (r *reach) reset() {
	r.lastNeed, r.told, r.staled = time.Time{}, nil, nil
	r.worked, r.settledSent = false, false
}

// observe returns the messages to send now. wake, when positive, is when
// to look again: a coalesced "needs you" is due then.
func (r *reach) observe(now time.Time, away bool, chats []reachChat) (out []reachText, wake time.Duration) {
	if !away {
		r.reset()
		return nil, 0
	}
	var need []reachChat
	working := false
	for _, c := range chats {
		switch c.mark {
		case "▲":
			need = append(need, c)
		case "●", "?":
			working = true
		}
	}
	if working {
		r.worked = true
	}
	// A Chat that is no longer waiting is forgotten: its next ask is news.
	for n := range r.told {
		if !slices.ContainsFunc(need, func(c reachChat) bool { return c.num == n }) {
			delete(r.told, n)
			delete(r.staled, n)
		}
	}
	var needText string
	if len(need) > 0 {
		fresh := slices.ContainsFunc(need, func(c reachChat) bool { return !r.told[c.num] })
		stale := false
		for _, c := range need {
			if !c.askedAt.IsZero() && r.stale > 0 && now.Sub(c.askedAt) >= r.stale && !r.staled[c.num] {
				stale = true
				if r.staled == nil {
					r.staled = map[int]bool{}
				}
				r.staled[c.num] = true
			}
		}
		due := r.lastNeed.IsZero() || now.Sub(r.lastNeed) >= reachCoalesce
		switch {
		case fresh && due, stale:
			needText = r.needText(need)
			r.lastNeed = now
			r.told = map[int]bool{}
			for _, c := range need {
				r.told[c.num] = true
			}
		case fresh:
			wake = r.lastNeed.Add(reachCoalesce).Sub(now)
		}
	}
	if r.worked && !working && !r.settledSent {
		r.settledSent = true
		out = append(out, reachText{r.settledText(chats), false})
		// The settled message already counts what needs the human.
		if len(need) > 0 {
			needText = ""
			r.lastNeed, r.told = now, map[int]bool{}
			for _, c := range need {
				r.told[c.num] = true
			}
		}
	}
	if needText != "" {
		out = append([]reachText{{needText, true}}, out...)
	}
	return out, wake
}

// needText is "oge · api · Chat 2 needs you" or "oge · api · 2 Chats need you".
func (r *reach) needText(need []reachChat) string {
	if len(need) == 1 {
		c := need[0]
		who := "Chat " + strconv.Itoa(c.num)
		if t := r.title(c); t != "" {
			who += " " + t
		}
		return reachLine(repoLabel(need), who+" needs you")
	}
	return reachLine(repoLabel(need), strconv.Itoa(len(need))+" Chats need you")
}

// title is the Chat's title when [notify] titles is on, cleaned and cut.
func (r *reach) title(c reachChat) string {
	if !r.titles {
		return ""
	}
	return ansi.Truncate(strings.TrimSpace(clean(ansi.Strip(c.title))), 24, "…")
}

// settledText is "oge · api · settled · 2 ready · 1 needs you · 1 failed".
func (r *reach) settledText(chats []reachChat) string {
	var ready, need, failed int
	for _, c := range chats {
		switch c.mark {
		case "✓":
			ready++
		case "▲":
			need++
		case "✗":
			failed++
		}
	}
	parts := []string{"settled"}
	if ready > 0 {
		parts = append(parts, strconv.Itoa(ready)+" ready")
	}
	if need > 0 {
		parts = append(parts, strconv.Itoa(need)+" needs you")
	}
	if failed > 0 {
		parts = append(parts, strconv.Itoa(failed)+" failed")
	}
	return reachLine(repoLabel(chats), strings.Join(parts, " · "))
}

// stoppedText is the message for a Desk that ended on SIGHUP or SIGTERM.
func (r *reach) stoppedText(chats []reachChat) string {
	return reachLine(repoLabel(chats), "the Desk stopped · run oge to restore")
}

func reachLine(repo, what string) string {
	if repo == "" {
		return "oge · " + what
	}
	return "oge · " + repo + " · " + what
}

// repoLabel is the repo's name when the Chats share one repo, else the
// number of repos, which stays true of the counts across them.
func repoLabel(chats []reachChat) string {
	var repos []string
	for _, c := range chats {
		if c.repo != "" && !slices.Contains(repos, c.repo) {
			repos = append(repos, c.repo)
		}
	}
	switch len(repos) {
	case 0:
		return ""
	case 1:
		return repos[0]
	}
	return strconv.Itoa(len(repos)) + " repos"
}

// argvRunner runs an argv, bounded by ctx. Tests replace it.
type argvRunner func(ctx context.Context, argv []string) error

// runArgv runs argv without a shell and discards its output.
func runArgv(ctx context.Context, argv []string) error {
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
}

// reachOut carries a message through the [notify] command: fire and
// forget, off the UI goroutine. Its failure is kept for the home to show
// once.
type reachOut struct {
	argv []string
	run  argvRunner

	mu     sync.Mutex
	failed string
	shown  bool
	wg     sync.WaitGroup
}

// send runs the command with text as its last argument.
func (o *reachOut) send(text string) {
	if o == nil || len(o.argv) == 0 {
		return
	}
	argv := append(slices.Clone(o.argv), text)
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), reachTimeout)
		defer cancel()
		if err := o.run(ctx, argv); err != nil {
			o.mu.Lock()
			if o.failed == "" {
				o.failed = "[notify] command " + filepath.Base(argv[0]) + " failed: " + err.Error()
			}
			o.mu.Unlock()
		}
	}()
}

// failure is the first failure, once; "" when none or already shown.
func (o *reachOut) failure() string {
	if o == nil {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.shown || o.failed == "" {
		return ""
	}
	o.shown = true
	return o.failed
}

// wait holds until the sends in flight end, for at most d.
func (o *reachOut) wait(d time.Duration) {
	if o == nil {
		return
	}
	done := make(chan struct{})
	go func() { o.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// reachDesk is the home's part: the policy, the command it sends through,
// and what herdr shows under the last message.
type reachDesk struct {
	reach
	out    *reachOut
	wakeAt time.Time // the latest look-again already scheduled
	// herdrNeed and herdrSettled are the messages herdr carries under a
	// blocked and an idle pane while away; "" is the usual fixed word.
	herdrNeed, herdrSettled string
}

// isAway reports whether the human is away: the terminal reported focus
// lost, or there was no key or mouse for awayMin.
func (h *home) isAway(now time.Time) bool {
	if !h.absence.blurred.IsZero() {
		return true
	}
	return !h.absence.last.IsZero() && wallSince(h.absence.last, now) >= awayMin
}

// reachChats is the Desk's Chats as the policy reads them.
func (h *home) reachChats() []reachChat {
	out := make([]reachChat, 0, len(h.views))
	for _, v := range h.views {
		c := reachChat{num: v.num, title: v.name, mark: v.attnMark, askedAt: v.askedAt}
		if v.repoCtx != nil && v.root != "" {
			c.repo = filepath.Base(v.root)
		}
		out = append(out, c)
	}
	return out
}

// reachCheck runs after every update: it sends what the policy says
// while the human is away, and returns the wake-up it asks for. At the
// terminal nothing is sent and the bell and OSC path is untouched.
func (h *home) reachCheck(now time.Time) tea.Cmd {
	d := h.reachDesk
	if d == nil {
		return nil
	}
	d.stale = h.staleAfter()
	away := h.isAway(now)
	chats := h.reachChats()
	texts, wake := d.observe(now, away, chats)
	if !away {
		d.herdrNeed, d.herdrSettled = "", ""
		// A Gate waiting while the human sits here: look again when ten
		// quiet minutes would make it away.
		if h.absence.blurred.IsZero() && !h.absence.last.IsZero() &&
			slices.ContainsFunc(chats, func(c reachChat) bool { return c.mark == "▲" }) {
			wake = h.absence.last.Add(awayMin).Sub(now)
		}
	}
	for _, t := range texts {
		d.out.send(t.text)
		if t.blocked {
			d.herdrNeed = t.text
		} else {
			d.herdrSettled = t.text
		}
	}
	var cmds []tea.Cmd
	if f := d.out.failure(); f != "" {
		cmds = append(cmds, h.notice(f, false))
		h.toast.plain = true
	}
	if wake > 0 && h.redraw != nil && now.Add(wake).After(d.wakeAt) {
		d.wakeAt = now.Add(wake)
		cmds = append(cmds, h.redraw(wake+time.Second, func(time.Time) tea.Msg { return staleTickMsg{} }))
	}
	return tea.Batch(cmds...)
}

// reachStopped sends the stopped message when a hangup or SIGTERM ended a
// session while the human was away, holding the exit for it briefly.
func (h *home) reachStopped() {
	d := h.reachDesk
	if d == nil || !h.isAway(h.clock()) {
		return
	}
	text := d.stoppedText(h.reachChats())
	d.out.send(text)
	d.out.wait(reachExitWait)
}

// reachDesk is the home's away messages: nil when neither a [notify]
// command nor herdr could carry them.
func (u *tui) reachDesk() *reachDesk {
	if len(u.reachCmd) == 0 && u.herdr == nil {
		return nil
	}
	run := u.reachRun
	if run == nil {
		run = runArgv
	}
	return &reachDesk{
		reach: reach{titles: u.reachTitles},
		out:   &reachOut{argv: u.reachCmd, run: run},
	}
}
