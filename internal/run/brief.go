package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/erengun/oge/internal/ledger"
)

// The return brief (docs/ux/away.md, slice 7): what happened in the
// Chats and their cuts over a window, built from the records alone. No
// agent text reaches it: it reads outcomes, counts, turn numbers, Gate
// names and times, never a reply, a failure or a why.

// SleepFloor is the least gap between wall-clock and monotonic time
// counted as the machine sleeping: smaller ones are scheduling noise.
const SleepFloor = 2 * time.Minute

// recDelivery is delivery.RecDelivery (which imports this package): a
// human applied or branched a Run.
const recDelivery = "Delivery"

// BriefLog is one Chat log or Run Ledger as read.
type BriefLog struct {
	ID      string
	Records []ledger.Record
	// Torn says the last record died mid-write (ledger.ErrTornTail).
	Torn bool
}

// ReadBriefLog reads the log in dir. A torn tail is kept as Torn, with
// the records before it.
func ReadBriefLog(id, dir string) (BriefLog, error) {
	recs, err := ledger.Replay(dir)
	if err != nil && (!errors.Is(err, ledger.ErrTornTail) || len(recs) == 0) {
		return BriefLog{}, err
	}
	return BriefLog{ID: id, Records: recs, Torn: err != nil}, nil
}

// BriefChat is a Chat's log and what the caller knows of it.
type BriefChat struct {
	BriefLog
	// N is the Chat's number on the Desk; 0 numbers it by when it started.
	N int
	// Title is the user's own title for it ("" takes its newest cut's).
	Title string
	// Asks are the asks waiting now. No record holds them, so only the
	// process that holds the Session knows: 0 anywhere else.
	Asks int
}

// BriefRun is a Run's Ledger: a Chat's cut or a one-shot Run.
type BriefRun struct {
	BriefLog
	Title string // the Task's title
	// Live says a process runs it now (Live): a Run that isn't, with no
	// end recorded, was stopped without one.
	Live bool
}

// Basis is what a brief's window measures.
type Basis string

const (
	// BasisAway: no key or mouse in the home over the window.
	BasisAway Basis = "away"
	// BasisLastAct: the window starts at the human's last recorded act
	// (a message, a Gate decision, a delivery). Another process can't
	// know when the human left, only this.
	BasisLastAct Basis = "last_act"
)

// BriefWindow is the span a brief covers.
type BriefWindow struct {
	From, To time.Time
	Basis    Basis
	// Slept is how long the machine slept in it, as the caller measured
	// (wall clock against monotonic time); 0 when unknown.
	Slept time.Duration
}

// Brief is the return brief.
type Brief struct {
	Window BriefWindow
	// Slept is the longer of the window's measure and what the records'
	// own wall-clock and monotonic pairs show.
	Slept  time.Duration
	Chats  []ChatBrief // by number
	Totals BriefTotals
}

// BriefTotals add up the Chats.
type BriefTotals struct {
	Settled, Accepted, Failed, Parked, NeedsYou, Stopped int
	// Ended counts the Chats that ended unexpectedly.
	Ended int
}

// ChatBrief is one Chat's part, or a one-shot Run's (Run set).
type ChatBrief struct {
	ID    string
	N     int
	Title string
	Run   bool // a one-shot Run, not a Chat
	// Settled counts the turns that settled in the window.
	Settled int
	// Accepted, Failed and Parked count the cuts that ended so in it.
	Accepted, Failed, Parked int
	// Gates are the Gates open now, wherever they opened.
	Gates []OpenGate
	Asks  int
	// Stopped counts the cuts that ended without an end recorded: their
	// process is gone, or their Ledger is torn.
	Stopped int
	// Ended says how the Chat ended unexpectedly: "hangup" (its terminal
	// closed with work in flight), "torn" (its log's last record died
	// mid-write); "" when it didn't.
	Ended string
}

// NeedsYou counts what waits for the human: open Gates and asks.
func (c ChatBrief) NeedsYou() int { return len(c.Gates) + c.Asks }

// OpenGate is a Gate waiting for a decision.
type OpenGate struct {
	Run   string
	Gate  string // its name: "tamper", "result"
	Since time.Time
}

// Empty says nothing happened and nothing waits.
func (b Brief) Empty() bool { return len(b.Chats) == 0 }

// Away is the window's length.
func (b Brief) Away() time.Duration { return b.Window.To.Sub(b.Window.From) }

// LastAct is the newest record of a human act in the logs: a user's
// turn, a Gate decision or a delivery. Zero when there is none.
func LastAct(chats []BriefChat, runs []BriefRun) time.Time {
	var last time.Time
	note := func(r ledger.Record) {
		human := false
		switch r.Type {
		case RecTurnStarted:
			var d struct{ By string }
			_ = json.Unmarshal(r.Data, &d)
			human = d.By == ""
		case RecGateDecided, RecDelivered, recDelivery:
			human = true
		}
		if human && r.At.After(last) {
			last = r.At
		}
	}
	for _, c := range chats {
		for _, r := range c.Records {
			note(r)
		}
	}
	for _, r := range runs {
		for _, rec := range r.Records {
			note(rec)
		}
	}
	return last
}

// BuildBrief builds the brief over w from the Chats' logs and the Runs'
// Ledgers, the Chats' cuts and one-shot Runs alike.
func BuildBrief(w BriefWindow, chats []BriefChat, runs []BriefRun) Brief {
	in := func(t time.Time) bool { return !t.Before(w.From) && !t.After(w.To) }
	b := Brief{Window: w}
	byID := map[string]*ChatBrief{}
	started := map[string]time.Time{}
	shown := map[string]bool{}
	var order []*ChatBrief
	var spans []sleepSpan
	for _, c := range chats {
		cb := &ChatBrief{ID: c.ID, N: c.N, Title: c.Title, Asks: c.Asks}
		byID[c.ID], order = cb, append(order, cb)
		newest := time.Time{}
		for _, r := range c.Records {
			if r.At.After(newest) {
				newest = r.At
			}
			switch r.Type {
			case RecChatStarted:
				started[c.ID] = r.At
			case RecAttemptEnded:
				if in(r.At) {
					cb.Settled++
				}
			case RecTurnInterrupted:
				var d struct{ Why string }
				_ = json.Unmarshal(r.Data, &d)
				if d.Why == "hangup" {
					cb.Ended = "hangup"
				}
			case RecTurnStarted, RecSessionReopened:
				cb.Ended = "" // it went on after
			}
		}
		spans = append(spans, logSleeps(c.Records)...)
		if c.Torn {
			cb.Ended = "torn"
		}
		if cb.Ended != "" && !in(newest) {
			cb.Ended = "" // ended before the window: old news
		}
		if _, ok := started[c.ID]; !ok && len(c.Records) > 0 {
			started[c.ID] = c.Records[0].At
		}
		shown[c.ID] = cb.Settled > 0 || cb.Asks > 0 || cb.Ended != ""
	}
	for _, r := range runs {
		chat, rs := r.read(in)
		spans = append(spans, logSleeps(r.Records)...)
		cb := byID[chat]
		if cb == nil {
			cb = &ChatBrief{ID: r.ID, Title: r.Title, Run: true}
			if len(r.Records) > 0 {
				started[r.ID] = r.Records[0].At
			}
			order = append(order, cb)
			if chat == "" {
				byID[r.ID] = cb
			}
		} else if cb.Title == "" && r.Title != "" {
			cb.Title = r.Title // the newest cut's: Runs come newest first
		}
		cb.Accepted += rs.accepted
		cb.Failed += rs.failed
		cb.Parked += rs.parked
		cb.Stopped += rs.stopped
		cb.Gates = append(cb.Gates, rs.gates...)
		if rs.accepted+rs.failed+rs.parked+rs.stopped+len(rs.gates) > 0 {
			shown[cb.ID] = true
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, c := order[i], order[j]
		if a.N != c.N && a.N != 0 && c.N != 0 {
			return a.N < c.N
		}
		return started[a.ID].Before(started[c.ID])
	})
	n := 0
	for _, cb := range order {
		if cb.N > n {
			n = cb.N
		}
	}
	for _, cb := range order {
		if !shown[cb.ID] {
			continue
		}
		if cb.N == 0 && !cb.Run {
			n++
			cb.N = n
		}
		sort.Slice(cb.Gates, func(i, j int) bool { return cb.Gates[i].Since.Before(cb.Gates[j].Since) })
		b.Chats = append(b.Chats, *cb)
		t := &b.Totals
		t.Settled += cb.Settled
		t.Accepted += cb.Accepted
		t.Failed += cb.Failed
		t.Parked += cb.Parked
		t.NeedsYou += cb.NeedsYou()
		t.Stopped += cb.Stopped
		if cb.Ended != "" {
			t.Ended++
		}
	}
	b.Slept = max(w.Slept, sleptIn(spans, w))
	return b
}

// runState is what one Run brings to its Chat's part.
type runState struct {
	accepted, failed, parked, stopped int
	gates                             []OpenGate
}

// read reads a Run's Ledger: its Chat ("" for a one-shot Run), the end it
// reached in the window, and its Gate still open.
func (r BriefRun) read(in func(time.Time) bool) (chat string, s runState) {
	var open *OpenGate
	ended := false
	for _, rec := range r.Records {
		switch rec.Type {
		case RecRunStarted:
			var d struct{ Chat string }
			_ = json.Unmarshal(rec.Data, &d)
			chat = d.Chat
		case RecGateOpened:
			var d struct{ Pins struct{ Gate string } }
			_ = json.Unmarshal(rec.Data, &d)
			open = &OpenGate{Run: r.ID, Gate: gateWord(d.Pins.Gate), Since: rec.At}
		case RecGateDecided, RecGateParked, RecGateAbandoned:
			open = nil
		case RecRunParked:
			open, ended = nil, true
			s = runState{}
			if in(rec.At) {
				s.parked = 1
			}
		case RecRunEnded:
			open, ended = nil, true
			var d struct {
				Outcome Outcome
				Stop    string
			}
			_ = json.Unmarshal(rec.Data, &d)
			s = runState{}
			if !in(rec.At) {
				continue
			}
			switch d.Outcome {
			case Accepted:
				s.accepted = 1
			case Failed, Rejected:
				s.failed = 1
			case Parked:
				s.parked = 1
			case InfrastructureStop:
				if d.Stop != "superseded" { // a newer cut's Check covers it
					s.stopped = 1
				}
			}
		}
	}
	switch {
	case !ended && (r.Torn || !r.Live):
		// Gone without an end: whatever it waited at waits no more.
		if last := r.last(); in(last) || open != nil {
			s.stopped = 1
		}
	case open != nil:
		s.gates = []OpenGate{*open}
	}
	return chat, s
}

func (r BriefRun) last() time.Time {
	if len(r.Records) == 0 {
		return time.Time{}
	}
	return r.Records[len(r.Records)-1].At
}

// gateWord is a Gate's name from its pinned id: gate.bound_exhaustion is
// bound-exhaustion, as delivery's Why says it.
func gateWord(id string) string {
	return strings.ReplaceAll(strings.TrimPrefix(id, "gate."), "_", "-")
}

// sleepSpan is a stretch of wall-clock time the machine slept for at
// least slept of, from a record pair that measured it both ways.
type sleepSpan struct {
	from, to time.Time
	slept    time.Duration
}

// logSleeps finds the record pairs that timed the same stretch on both
// clocks: their records' times are wall clock, their durations monotonic,
// and monotonic time stops while the machine sleeps. The pairs:
//   - an agent's ProcessStarted and its responsiveness Observation;
//   - a ConsultationStarted and its ConsultationEnded;
//   - one Check command's start and the next one's.
func logSleeps(recs []ledger.Record) []sleepSpan {
	var out []sleepSpan
	add := func(from, to time.Time, ms int64) {
		if from.IsZero() || to.Before(from) {
			return
		}
		if s := to.Sub(from) - time.Duration(ms)*time.Millisecond; s >= SleepFloor {
			out = append(out, sleepSpan{from, to, s})
		}
	}
	procs := map[string]time.Time{}
	consults := map[string]time.Time{}
	for _, r := range recs {
		switch r.Type {
		case RecProcessStarted:
			var d struct{ Attempt string }
			_ = json.Unmarshal(r.Data, &d)
			if d.Attempt != "" {
				procs[d.Attempt] = r.At
			}
		case RecObservation:
			var d struct {
				Attempt, Kind string
				DurationMs    int64 `json:"duration_ms"`
			}
			_ = json.Unmarshal(r.Data, &d)
			if d.Kind == "responsiveness" {
				add(procs[d.Attempt], r.At, d.DurationMs)
			}
		case RecConsultationStarted:
			var d struct{ Consultation string }
			_ = json.Unmarshal(r.Data, &d)
			consults[d.Consultation] = r.At
		case RecConsultationEnded:
			var d struct {
				Consultation string
				DurationMs   int64 `json:"duration_ms"`
			}
			_ = json.Unmarshal(r.Data, &d)
			add(consults[d.Consultation], r.At, d.DurationMs)
		case RecCheckEnded:
			var d struct {
				Result struct {
					Commands []struct {
						StartedAt  time.Time `json:"started_at"`
						DurationMs int64     `json:"duration_ms"`
					}
				}
			}
			_ = json.Unmarshal(r.Data, &d)
			cmds := d.Result.Commands
			for i := 0; i+1 < len(cmds); i++ {
				add(cmds[i].StartedAt, cmds[i+1].StartedAt, cmds[i].DurationMs)
			}
		}
	}
	return out
}

// sleptIn is how long the spans show the machine slept within w: spans
// that overlap measured the same sleep, so each group counts its
// longest; apart, they add up.
func sleptIn(spans []sleepSpan, w BriefWindow) time.Duration {
	var in []sleepSpan
	for _, s := range spans {
		if s.to.After(w.From) && s.from.Before(w.To) {
			in = append(in, s)
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].from.Before(in[j].from) })
	var total, group time.Duration
	var end time.Time
	for i, s := range in {
		if i > 0 && !s.from.Before(end) {
			total += group
			group = 0
		}
		group = max(group, s.slept)
		if s.to.After(end) {
			end = s.to
		}
	}
	return total + group
}

// Text is the brief as plain lines: the summary, then one line per Chat.
// Times are in loc.
func (b Brief) Text(loc *time.Location) string {
	var lines []string
	head := b.lead()
	if b.Empty() {
		return "nothing to report " + head + "\n"
	}
	lines = append(lines, head+": "+b.summary())
	for _, c := range b.Chats {
		lines = append(lines, "  "+c.line(loc))
	}
	return strings.Join(lines, "\n") + "\n"
}

// lead names the window: "while you were away (2h 10m, slept about 40m)".
func (b Brief) lead() string {
	span := Span(b.Away())
	if b.Slept > 0 {
		span += ", the machine slept about " + Span(b.Slept)
	}
	if b.Window.Basis == BasisLastAct {
		return "since you last acted (" + span + ")"
	}
	return "while you were away (" + span + ")"
}

// summary is the totals in a line, the first ▲ named.
func (b Brief) summary() string {
	t := b.Totals
	var parts []string
	if t.Accepted > 0 {
		parts = append(parts, fmt.Sprintf("✓ %d accepted", t.Accepted))
	}
	if t.NeedsYou > 0 {
		s := fmt.Sprintf("▲ %d needs you", t.NeedsYou)
		for _, c := range b.Chats {
			if len(c.Gates) > 0 {
				s += " (" + c.name() + ": " + c.Gates[0].Gate + " Gate)"
				break
			}
		}
		parts = append(parts, s)
	}
	if t.Failed > 0 {
		parts = append(parts, fmt.Sprintf("✗ %d Check failed", t.Failed))
	}
	if t.Parked > 0 {
		parts = append(parts, fmt.Sprintf("%d parked", t.Parked))
	}
	if t.Ended > 0 {
		parts = append(parts, plural(t.Ended, "Chat")+" ended unexpectedly")
	}
	if t.Stopped > 0 {
		parts = append(parts, plural(t.Stopped, "cut")+" stopped")
	}
	if t.Settled > 0 {
		parts = append(parts, plural(t.Settled, "turn")+" settled")
	}
	return strings.Join(parts, " · ")
}

// Counts is the totals in the header's short form: ▲1 ✗1 ✓2.
func (b Brief) Counts() string {
	t := b.Totals
	var parts []string
	if t.NeedsYou > 0 {
		parts = append(parts, fmt.Sprintf("▲%d", t.NeedsYou))
	}
	if t.Failed > 0 {
		parts = append(parts, fmt.Sprintf("✗%d", t.Failed))
	}
	if t.Accepted > 0 {
		parts = append(parts, fmt.Sprintf("✓%d", t.Accepted))
	}
	if len(parts) == 0 && t.Settled > 0 {
		parts = append(parts, plural(t.Settled, "turn")+" settled")
	}
	return strings.Join(parts, " ")
}

func (c ChatBrief) name() string {
	n := "Chat " + fmt.Sprint(c.N)
	if c.Run {
		n = "Run " + shortID(c.ID)
	}
	if c.Title != "" {
		n += " " + c.Title
	}
	return n
}

func (c ChatBrief) line(loc *time.Location) string {
	var parts []string
	switch c.Ended {
	case "hangup":
		parts = append(parts, "stopped when its terminal closed")
	case "torn":
		parts = append(parts, "ended unexpectedly: its log's last record is torn")
	}
	for _, g := range c.Gates {
		parts = append(parts, "▲ "+g.Gate+" Gate since "+g.Since.In(loc).Format("15:04"))
	}
	if c.Asks > 0 {
		parts = append(parts, "▲ "+plural(c.Asks, "ask")+" waiting")
	}
	if c.Failed > 0 {
		parts = append(parts, "✗ "+plural(c.Failed, "Check")+" failed")
	}
	if c.Accepted > 0 {
		parts = append(parts, fmt.Sprintf("✓ %d accepted", c.Accepted))
	}
	if c.Parked > 0 {
		parts = append(parts, fmt.Sprintf("%d parked", c.Parked))
	}
	if c.Stopped > 0 {
		parts = append(parts, plural(c.Stopped, "cut")+" stopped without an end")
	}
	if c.Settled > 0 {
		parts = append(parts, plural(c.Settled, "turn")+" settled")
	}
	return c.name() + " · " + strings.Join(parts, " · ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Span is a duration as the brief says it: 40m, 2h 10m, 3d 4h; under a
// minute is 0m.
func Span(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h >= 48:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}
