package cli

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// The live view tells the human when the Run needs them and when it ends: a
// bell and a desktop notification (#170). The texts are fixed. They never
// carry agent, Candidate or Task text, so nothing a Run can write reaches the
// terminal's notification centre.
const (
	notifyWaiting  = "oge: waiting for you"
	notifyAccepted = "oge: Run accepted"
)

// notifyEnded is the notification for a Run that ended with outcome, which
// is one of Öge's own words ("" when the Run itself failed).
func notifyEnded(outcome run.Outcome) string {
	switch outcome {
	case run.Accepted:
		return notifyAccepted
	case "":
		return "oge: Run ended: stopped"
	}
	return "oge: Run ended: " + strings.ToLower(string(outcome))
}

// notifier decides what the live view rings. A nil *notifier is off: the
// plain view, --unattended and dry-run never build one.
type notifier struct {
	attention, runEnd, bell bool
	// How the terminal takes an OSC: through tmux's passthrough (needs
	// `allow-passthrough on`), not at all under GNU screen (BEL only), or as
	// OSC 777 for the terminals that use it instead of OSC 9.
	tmux, screen, osc777 bool
}

// notifierFor reads the [notify] settings for a Run, or returns nil when the
// view must stay silent. The user config is global: all three default on.
func notifierFor(env Env, f runFlags, user *pipeline.UserConfig) *notifier {
	if f.unattended || f.dryRun || f.plain {
		return nil
	}
	n := &notifier{attention: true, runEnd: true, bell: true}
	if c := user.NotifyConfig(); c != nil {
		n.attention = pick(c.Attention, n.attention)
		n.runEnd = pick(c.RunEnd, n.runEnd)
		n.bell = pick(c.Bell, n.bell)
	}
	term := env.Getenv("TERM")
	n.tmux = env.Getenv("TMUX") != ""
	n.screen = !n.tmux && (env.Getenv("STY") != "" || strings.HasPrefix(term, "screen"))
	n.osc777 = strings.HasPrefix(term, "foot") || strings.HasPrefix(term, "rxvt")
	return n
}

func pick(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// sequence is what to write for msg: the bell, then the OSC notification.
func (n *notifier) sequence(msg string) string {
	var b strings.Builder
	if n.bell {
		b.WriteString("\a")
	}
	osc := "\x1b]9;" + msg + "\a"
	if n.osc777 {
		osc = "\x1b]777;notify;oge;" + msg + "\a"
	}
	switch {
	case n.tmux:
		// Every ESC inside the wrapped sequence is doubled.
		b.WriteString("\x1bPtmux;" + strings.ReplaceAll(osc, "\x1b", "\x1b\x1b") + "\x1b\\")
	case n.screen:
		// screen would take the OSC as its own: keep the bell only.
	default:
		b.WriteString(osc)
	}
	return b.String()
}

// needsYou queues the "waiting for you" alert on m.
func (m *model) needsYou() {
	if m.notify != nil && m.notify.attention {
		m.alerts = append(m.alerts, m.notify.sequence(notifyWaiting))
	}
}

// runEnded queues the end-of-Run alert on m.
func (m *model) runEnded(res *run.Result, failed bool) {
	if m.notify == nil || !m.notify.runEnd {
		return
	}
	var outcome run.Outcome
	if res != nil && !failed {
		outcome = res.Outcome
	}
	m.alerts = append(m.alerts, m.notify.sequence(notifyEnded(outcome)))
}

// flushAlerts hands the queued alerts to Bubble Tea's raw output path, which
// writes them between frames, so they never land inside one.
func (m *model) flushAlerts() tea.Cmd {
	if len(m.alerts) == 0 {
		return nil
	}
	seq := strings.Join(m.alerts, "")
	m.alerts = nil
	return tea.Raw(seq)
}

// notifyCommand is /notify on|off in the home: it turns both alerts on or
// off for the session. It returns the line to show.
func (n *notifier) notifyCommand(arg string) string {
	switch arg {
	case "on", "off":
		n.attention, n.runEnd = arg == "on", arg == "on"
		return "notifications " + arg + " for this session"
	}
	return "/notify: say on or off"
}
