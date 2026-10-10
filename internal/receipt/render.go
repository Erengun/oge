package receipt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/redact"
	"github.com/erengun/oge/internal/run"
)

// Tone is how a line's text is painted.
type Tone int

const (
	Plain Tone = iota
	Good       // an Accepted outcome
	Bad        // a Run that wasn't Accepted
	Warn       // Overridden, or a Run waiting for a human: never success
	Dim        // the agent's Claim and traceability details
)

// Line is one labelled line of the Receipt. A continuation has no label.
type Line struct {
	Label string
	Text  string
	Tone  Tone
}

// Paint styles the terminal rendering. A nil func leaves text as it is,
// so the zero Paint is plain text (NO_COLOR, or not a terminal).
type Paint struct {
	Bold, Dim, Good, Bad, Warn func(string) string
}

// Colors is the coloured Paint for a terminal that allows colour, in the
// 16 basic colours so the user's palette decides the shades; the zero
// Paint otherwise.
func Colors(color bool) Paint {
	if !color {
		return Paint{}
	}
	style := func(s lipgloss.Style) func(string) string { return func(t string) string { return s.Render(t) } }
	return Paint{
		Bold: style(lipgloss.NewStyle().Bold(true)),
		Dim:  style(lipgloss.NewStyle().Faint(true)),
		Good: style(lipgloss.NewStyle().Foreground(lipgloss.Green)),
		Bad:  style(lipgloss.NewStyle().Foreground(lipgloss.Red)),
		Warn: style(lipgloss.NewStyle().Foreground(lipgloss.Yellow)),
	}
}

func apply(f func(string) string, s string) string {
	if f == nil {
		return s
	}
	return f(s)
}

// labelWidth fits the longest label, "Agent claimed".
const labelWidth = 14

// Lines are the Receipt as labelled lines, outcome first. Every rendering
// of the text comes from them.
func (r *Receipt) Lines() []Line {
	var ls []Line
	add := func(label, text string, tone Tone) { ls = append(ls, Line{label, text, tone}) }
	more := func(label string, texts []string, tone Tone) {
		for i, t := range texts {
			if i == 0 {
				add(label, t, tone)
			} else {
				add("", t, tone)
			}
		}
	}

	head := "Run " + orNone(r.Run)
	if r.Mode != "" {
		head += " · " + r.Mode + " mode"
	}
	add("ÖGE RECEIPT", head, Plain)
	add("Result", r.Headline, r.tone())
	for _, w := range r.Why {
		add("", w, Plain)
	}
	if r.Task != "" {
		add("Task", val(r.Task), Plain)
	}
	if r.Claim != nil {
		text := "(no final message) · Exit " + r.Claim.Exit
		if r.Claim.Text != "" {
			text = `"` + val(r.Claim.Text) + `"`
		}
		add("Agent claimed", text+" (Claim, "+val(r.Claim.Attempt)+")", Dim)
		more("Öge found", r.Found, Plain)
	}
	if c := r.Candidate; c != nil {
		add("Candidate", fmt.Sprintf("%s · %s · Oracle v%d", short(c.Commit), plural(c.FilesChanged, "file changed", "files changed"), r.Oracle), Plain)
		for _, l := range c.agentConfig(r.Deliveries) {
			add("", l, Plain)
		}
	}
	switch c, last := r.final(), r.last(); {
	case c != nil:
		add("Checks", checkText(c, len(r.Checks)), Plain)
	case last != nil:
		// The Run ended on a Candidate no Check judged: the last Check's
		// Verdict is another Candidate's.
		v := map[string]string{"pass": "passed", "fail": "failed"}[last.Verdict]
		if v == "" {
			v = "reached no Verdict"
		}
		add("Checks", fmt.Sprintf("no Check on this Candidate (Check #%d %s on %s)", last.Number, v, short(last.Candidate)), Plain)
	default:
		add("Checks", "none ran", Plain)
	}
	if q := r.QA; q != nil && r.Mode != "Fast" {
		add("QA", qaText(q), Plain)
	}
	add("Protected", protectedText(r.Protected), Plain)
	add("Scope", scopeText(r.Scope), Plain)
	if len(r.Decisions) > 0 {
		var ds []string
		for _, d := range r.Decisions {
			s := fmt.Sprintf("%s at the %s", d.Choice, GateTitle(d.Gate))
			if len(d.Files) > 0 {
				s += ": " + list(d.Files)
			}
			if d.Reason != "" {
				s += " · reason: " + val(d.Reason)
			}
			if d.Note != "" {
				s += " · note: " + val(d.Note)
			}
			ds = append(ds, s)
		}
		more("Decisions", ds, Plain)
	}
	add("Handled", r.Supervised.text(), Plain)
	if f := r.Supervised.Friction; f != nil && (f.Denied > 0 || f.LostTurns > 0) {
		s := fmt.Sprintf("policy friction %s (%d denied)", plural(f.LostTurns, "turn", "turns"), f.Denied)
		if f.EnvelopeRefusals > 0 {
			s += fmt.Sprintf(" · %d refused before the envelope passed", f.EnvelopeRefusals)
		}
		add("Friction", s, Plain)
	}
	add("Time", r.Time.text(), Plain)
	var obs []string
	for _, o := range r.Observed {
		obs = append(obs, val(o))
	}
	if c := r.final(); c != nil && c.Stray > 0 {
		obs = append(obs, fmt.Sprintf("%d stray lines on the attestation channel", c.Stray))
	}
	if len(obs) > 0 {
		add("Observed", strings.Join(obs, " · ")+" (tripwires: signals, not proof)", Plain)
	}
	more("Not covered", r.NotCovered, Plain)
	for i, d := range r.Deliveries {
		label := ""
		if i == 0 {
			label = "Delivered"
		}
		add(label, d.text(), Plain)
	}
	if r.LedgerHead != "" {
		add("Ledger", "head "+r.LedgerHead[:min(12, len(r.LedgerHead))]+" · identifies this Receipt; not tamper-proof", Dim)
	}
	return ls
}

func (r *Receipt) tone() Tone {
	switch r.Outcome {
	case string(run.Accepted):
		return Good
	case string(run.Overridden), string(run.Parked), NotEnded:
		return Warn
	}
	return Bad
}

func checkText(c *Check, n int) string {
	var s string
	switch c.Verdict {
	case "pass":
		s = fmt.Sprintf("Check #%d passed", c.Number)
	case "fail":
		s = fmt.Sprintf("Check #%d failed: %s", c.Number, failureText(c, "failing"))
	default:
		s = fmt.Sprintf("Check #%d reached no Verdict", c.Number)
		if c.Infra != "" {
			s += " (" + val(c.Infra) + ")"
		}
	}
	if total := c.Passed + c.Failed; total > 0 {
		s += fmt.Sprintf(" · %d of %d Oracle tests attested passing", c.Passed, total)
		if c.HeldOut > 0 {
			s += fmt.Sprintf(" · %d held out from the implementer", c.HeldOut)
		}
	} else if len(c.Commands) > 0 {
		passed := 0
		for _, e := range c.Commands {
			if e.Pass {
				passed++
			}
		}
		s += fmt.Sprintf(" · %d of %d commands passed", passed, len(c.Commands))
	}
	if n > 1 {
		s += fmt.Sprintf(" · %d Checks in all", n)
	}
	return s
}

func qaText(q *QA) string {
	s := fmt.Sprintf("+%d held-out", q.HeldOutAdded)
	if q.Unmapped > 0 {
		s += fmt.Sprintf(" (%d unmapped)", q.Unmapped)
	}
	if q.LeftOut > 0 {
		s += " · " + plural(q.LeftOut, "addition", "additions") + " left out"
	}
	if q.Attempts > 1 {
		s += fmt.Sprintf(" · %d QA passes", q.Attempts)
	}
	return s
}

func protectedText(p Protected) string {
	kept := keptText(p.Kept)
	if len(p.Reverted) == 0 {
		if kept == "" {
			return "no test changes kept"
		}
		return "no test changes reverted · " + kept
	}
	var paths []string
	acked := 0
	for _, t := range p.Reverted {
		paths = append(paths, t.Path)
		if t.Acknowledged {
			acked++
		}
	}
	s := fmt.Sprintf("no test changes kept (%d reverted: %s)", len(paths), list(paths))
	if kept != "" {
		s = fmt.Sprintf("%d reverted: %s", len(paths), list(paths))
	}
	switch {
	case acked == len(paths):
		s += " · acknowledged"
	case acked > 0:
		s += fmt.Sprintf(" · %d of %d acknowledged", acked, len(paths))
	default:
		s += " · not acknowledged"
	}
	if kept != "" {
		s += " · " + kept
	}
	return s
}

// keptText is "2 test additions kept in a_test.go", or "" when nothing was
// kept. An addition that adds no test func (a helper) is named as one.
func keptText(ks []KeptFile) string {
	if len(ks) == 0 {
		return ""
	}
	var paths []string
	n := 0
	for _, k := range ks {
		paths = append(paths, k.Path)
		n += len(k.Added)
	}
	what := "additions"
	if n > 0 {
		what = plural(n, "test addition", "test additions")
	}
	return what + " kept in " + list(paths)
}

func scopeText(s Scope) string {
	var parts []string
	if n := len(s.Reverted); n > 0 {
		parts = append(parts, fmt.Sprintf("%s reverted: %s", plural(n, "out-of-scope write", "out-of-scope writes"), list(s.Reverted)))
	}
	if n := len(s.QADiscarded); n > 0 {
		parts = append(parts, fmt.Sprintf("%s outside QA's scope: %s", plural(n, "discarded write", "discarded writes"), list(s.QADiscarded)))
	}
	if len(parts) == 0 {
		return "no out-of-scope changes"
	}
	return strings.Join(parts, " · ")
}

func (s Supervised) text() string {
	parts := []string{plural(s.Approved, "operation approved automatically", "operations approved automatically")}
	if s.Denied > 0 {
		parts = append(parts, fmt.Sprintf("%d denied", s.Denied))
	}
	if s.Cancelled > 0 {
		parts = append(parts, plural(s.Cancelled, "question cancelled", "questions cancelled"))
	}
	if s.SentBack > 0 {
		parts = append(parts, fmt.Sprintf("%d sent back to the implementer", s.SentBack))
	}
	parts = append(parts, fmt.Sprintf("Human interruptions: %d", s.Interruptions))
	return strings.Join(parts, " · ")
}

func (t Time) text() string {
	parts := []string{dur(t.TotalMs)}
	for _, s := range t.Stages {
		if s.Ms > 0 {
			parts = append(parts, s.Stage+" "+dur(s.Ms))
		}
	}
	parts = append(parts, "your attention "+dur(t.AttentionMs))
	return strings.Join(parts, " · ")
}

func (d Delivery) text() string {
	var s string
	switch d.Kind {
	case "apply":
		s = "applied to your working tree (" + plural(d.Files, "file", "files") + ")"
	case "branch":
		s = "branch " + val(d.Branch) + " created"
	default:
		s = d.Kind
	}
	if d.Flag != "" {
		s += " with " + val(d.Flag)
	}
	if n := delivery.Count(d.Included, delivery.HeldAgentConfig); n > 0 {
		s += " · included " + plural(n, "agent-config change", "agent-config changes") + " not covered by the Check"
	}
	if n := delivery.Count(d.Included, delivery.HeldUnresolved); n > 0 {
		s += " · included " + plural(n, "unresolved Ambiguous file", "unresolved Ambiguous files")
	}
	if n := delivery.Count(d.HeldBack, delivery.HeldUnresolved); n > 0 {
		s += " · left out " + plural(n, "unresolved Ambiguous file", "unresolved Ambiguous files")
	}
	if n := delivery.Count(d.HeldBack, delivery.HeldAgentConfig); n > 0 {
		s += " · held back " + plural(n, "agent-config change", "agent-config changes")
	}
	return s
}

// dur is 0s, 1.2s, or 2m 14s.
func dur(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return d.Round(100 * time.Millisecond).String()
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm %ds", int(d/time.Minute), int(d%time.Minute/time.Second))
}

// Text is the Receipt for a terminal, painted with p.
func (r *Receipt) Text(p Paint) string {
	var b strings.Builder
	for i, l := range r.Lines() {
		label := fmt.Sprintf("%-*s", labelWidth, l.Label)
		if i == 0 {
			label = apply(p.Bold, label)
		}
		text := unmark.Replace(l.Text)
		switch l.Tone {
		case Good:
			text = apply(p.Good, text)
		case Bad:
			text = apply(p.Bad, text)
		case Warn:
			text = apply(p.Warn, text)
		case Dim:
			text = apply(p.Dim, text)
		}
		if l.Label == "Result" {
			text = apply(p.Bold, text)
		}
		b.WriteString(strings.TrimRight(label+text, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// Markdown is the Receipt for a PR or an issue: the same lines, then the
// full Evidence in a collapsible section. Every value from the Ledger is a
// code span, so a Claim, test name or path can't link, mention, reference
// an issue or add markup; held-out tests are named by criteria only
// (ADR-0009): a PR description can reach a later agent session.
func (r *Receipt) Markdown() string {
	var b strings.Builder
	ls := r.Lines()
	fmt.Fprintf(&b, "### Öge Receipt: %s\n\n", mdText(r.Headline, false))
	fmt.Fprintf(&b, "%s\n\n", mdText(ls[0].Text, false))
	var notCovered []string
	label := ""
	b.WriteString("| | |\n|---|---|\n")
	for _, l := range ls[2:] {
		if l.Label != "" {
			label = l.Label
		}
		if label == "Not covered" {
			notCovered = append(notCovered, l.Text)
			continue
		}
		text := mdText(l.Text, true)
		if label == "Agent claimed" {
			text = "_" + text + "_"
		}
		if l.Label == "" {
			fmt.Fprintf(&b, "| | %s |\n", text)
		} else {
			fmt.Fprintf(&b, "| **%s** | %s |\n", md(l.Label), text)
		}
	}
	b.WriteString("\n**Not covered**\n\n")
	for _, n := range notCovered {
		fmt.Fprintf(&b, "- %s\n", mdText(n, false))
	}
	b.WriteString("\n<details><summary>Full evidence</summary>\n\n")
	for _, c := range r.Checks {
		v := c.Verdict
		if v == "" {
			v = "no Verdict"
		}
		line := fmt.Sprintf("Check #%d on %s · Oracle v%d · %s · cache %s", c.Number, val(short(c.Candidate)), c.Oracle, v, val(orNone(c.Cache)))
		if c.CacheWhy != "" {
			line += " (" + val(c.CacheWhy) + ")"
		}
		fmt.Fprintf(&b, "- %s\n", mdText(line, false))
		for _, e := range c.Commands {
			part := e.Part
			if part == "" {
				part = "command"
			}
			res := "pass"
			if !e.Pass {
				res = "fail (" + val(orNone(e.Why)) + ")"
			}
			fmt.Fprintf(&b, "  - %s\n", mdText(fmt.Sprintf("%s · %s · %s · %s", part, val(e.Run), res, dur(e.Ms)), false))
		}
		for _, f := range c.Failing {
			s := "not attested passing: " + val(f.Test) + " (visible)"
			if f.HeldOut {
				s = "not attested passing: a held-out test" + criteria([]FailingTest{f})
			}
			fmt.Fprintf(&b, "  - %s\n", mdText(s, false))
		}
	}
	if c := r.Candidate; c != nil && len(c.Files) > 0 {
		fmt.Fprintf(&b, "- Files changed: %s\n", mdText(vals(c.Files, ", "), false))
		for _, l := range c.agentConfig(r.Deliveries) {
			fmt.Fprintf(&b, "- %s\n", mdText(l, false))
		}
	}
	fmt.Fprintf(&b, "- Ledger head %s (identifies this Receipt; not tamper-proof)\n", code(r.LedgerHead, false))
	b.WriteString("\n</details>\n")
	return b.String()
}

// refs are what GitHub would read as an issue reference in Öge's own
// words: "#12", "implement#1".
var refs = regexp.MustCompile(`(?:Check )?\S*#\d+`)

// mdText renders a line for Markdown: Öge's words escaped, anything GitHub
// would read as an issue reference in a code span, and each marked value a
// code span. In a table cell a pipe is escaped, even in a code span.
func mdText(s string, cell bool) string {
	var b strings.Builder
	for {
		i := strings.Index(s, valOpen)
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], valClose)
		if j < 0 {
			break
		}
		b.WriteString(mdWords(s[:i], cell))
		b.WriteString(code(s[i+len(valOpen):i+j], cell))
		s = s[i+j+len(valClose):]
	}
	b.WriteString(mdWords(unmark.Replace(s), cell))
	return b.String()
}

func mdWords(s string, cell bool) string {
	var b strings.Builder
	last := 0
	for _, m := range refs.FindAllStringIndex(s, -1) {
		b.WriteString(md(s[last:m[0]]))
		b.WriteString(code(s[m[0]:m[1]], cell))
		last = m[1]
	}
	b.WriteString(md(s[last:]))
	return b.String()
}

// code is s as a code span whose fence is longer than any run of
// backticks in it, padded so a leading or trailing backtick stays text.
func code(s string, cell bool) string {
	if s == "" {
		return ""
	}
	run, longest := 0, 0
	for _, c := range s {
		if c == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	if cell {
		s = strings.ReplaceAll(s, "|", `\|`)
	}
	fence := strings.Repeat("`", longest+1)
	return fence + " " + s + " " + fence
}

// md escapes Öge's own words for Markdown.
func md(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch c {
		case '\\', '`', '*', '_', '[', ']', '<', '>', '|', '~', '@':
			b.WriteByte('\\')
		case '&':
			b.WriteString("&amp;")
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// JSON is the Receipt as the versioned --json document (schema 1).
func (r *Receipt) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // <U+202E> reads as written; JSON isn't HTML
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return unmarkJSON(buf.Bytes()), nil
}

// unmarkJSON drops the value marks, which JSON encodes as \u000e and
// \u000f, stepping over every other escape so an escaped backslash
// followed by "u000e" in a value stays as it is.
func unmarkJSON(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 >= len(b) {
			out = append(out, b[i])
			continue
		}
		if s := string(b[i:min(i+6, len(b))]); s == `\u000e` || s == `\u000f` {
			i += 5
			continue
		}
		out = append(out, b[i], b[i+1])
		i++
	}
	return out
}

// clean redacts text from the Ledger and makes it safe to show
// (delivery.Shown): control characters dropped, bidirectional and
// invisible runes escaped as <U+XXXX>, invalid UTF-8 as U+FFFD.
func clean(s string) string {
	return delivery.Shown(string(redact.Redact([]byte(s))))
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// agentConfig says which of the Candidate's agent-config changes were
// declared as output, and which delivery holds back (#107).
func (c *Candidate) agentConfig(ds []Delivery) []string {
	var out []string
	if n := len(c.Declared); n > 0 {
		out = append(out, "declared agent-config output: "+list(c.Declared))
	}
	included := false
	for _, d := range ds {
		included = included || delivery.Count(d.Included, delivery.HeldAgentConfig) > 0
	}
	switch n := len(c.HeldBack); {
	case n > 0 && included:
		out = append(out, plural(n, "agent-config change", "agent-config changes")+" held back by default, not covered by the Check: "+list(c.HeldBack))
	case n > 0:
		out = append(out, plural(n, "agent-config change", "agent-config changes")+" held back, not covered by the Check: "+list(c.HeldBack)+
			" · not delivered unless oge apply or oge branch --with-agent-config")
	}
	return out
}
