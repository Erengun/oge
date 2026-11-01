package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

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
	// First is the human's first message in it, as the caller has it ("" when
	// no record or screen holds the text: the Chat log keeps hashes only).
	First string
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
	// BasisAway: the home had no key, mouse, focus or resize over the
	// window, or its terminal reported focus lost.
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
	Settled, Unsettled, Accepted, Failed, Rejected, Parked, NeedsYou, Stopped int
	// Ended counts the Chats a signal stopped with work in flight.
	Ended int
	// Torn counts the Chats whose log ends torn.
	Torn int
}

// ChatBrief is one Chat's part, or a one-shot Run's (Run set).
type ChatBrief struct {
	ID    string
	N     int
	Title string
	Run   bool // a one-shot Run, not a Chat
	// First is the human's first message (the intention), cut to one
	// line; "" when nothing records it. Terminal only: never in a push (A8).
	First string
	// Decisions are the human's Gate decisions and applies in this Chat's
	// cuts, oldest first, whenever they were made.
	Decisions []Decision
	// Delivered counts the applies and branches inside the window.
	Delivered int
	// Settled counts the turns that settled in the window; Unsettled the
	// ones whose Attempt ended in a failure (an agent that died, a Chat
	// stopped mid-turn).
	Settled, Unsettled int
	// Accepted, Failed, Rejected and Parked count the cuts and Runs that
	// ended so in it.
	Accepted, Failed, Rejected, Parked int
	// Gates are the Gates open now, wherever they opened.
	Gates []OpenGate
	Asks  int
	// Stopped counts the cuts that ended without an end recorded: their
	// process is gone, or their Ledger is torn.
	Stopped int
	// Ended says how a signal stopped the Chat with work in flight, as its
	// TurnInterrupted says: "hangup" (its terminal closed), "interrupt" (a
	// second Ctrl-C or SIGTERM); "" when none did.
	Ended string
	// Torn says its log's last record is torn. Unless the caller knows
	// the Chat isn't live, that is a record still being written or one
	// cut off: the records can't say which.
	Torn bool
}

// Decision is one act of the human, in Öge's fixed words.
type Decision struct {
	At   time.Time
	What string // "chose acknowledge at tamper Gate", "applied cut"
	// Delivery is an apply or a branch, not a Gate decision.
	Delivery bool
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
// turn, a one-shot Run started, a Gate decision or a delivery. Zero when there is none.
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
		case RecRunStarted:
			var d struct{ Chat string }
			_ = json.Unmarshal(r.Data, &d)
			human = d.Chat == "" // a one-shot Run, not a cut
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
	oldest := map[string]string{} // a Chat's oldest cut's Task title
	var order []*ChatBrief
	var spans []sleepSpan
	for _, c := range chats {
		cb := &ChatBrief{ID: c.ID, N: c.N, Title: c.Title, Asks: c.Asks, First: oneLine(c.First)}
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
				// Only whether it failed is read, never the failure.
				var d struct{ Failure string }
				_ = json.Unmarshal(r.Data, &d)
				switch {
				case !in(r.At):
				case d.Failure != "":
					cb.Unsettled++
				default:
					cb.Settled++
				}
			case RecTurnInterrupted:
				var d struct{ Why string }
				_ = json.Unmarshal(r.Data, &d)
				cb.Ended = d.Why
				if cb.Ended == "" {
					cb.Ended = "interrupt"
				}
			case RecTurnStarted, RecSessionReopened:
				cb.Ended = "" // it went on after
			}
		}
		spans = append(spans, logSleeps(c.Records)...)
		if !in(newest) {
			cb.Ended = "" // ended before the window: old news
		} else {
			cb.Torn = c.Torn
		}
		if _, ok := started[c.ID]; !ok && len(c.Records) > 0 {
			started[c.ID] = c.Records[0].At
		}
		shown[c.ID] = cb.Settled+cb.Unsettled+cb.Asks > 0 || cb.Ended != "" || cb.Torn
	}
	for _, r := range runs {
		chat, rs := r.read(in)
		spans = append(spans, logSleeps(r.Records)...)
		cb := byID[chat]
		if cb == nil {
			// A one-shot Run, or a cut of a Chat not listed here (closed,
			// or open in another home): one entry per Chat, never per cut.
			id := chat
			if id == "" {
				id = r.ID
			}
			cb = &ChatBrief{ID: id, Title: r.Title, Run: chat == ""}
			if len(r.Records) > 0 {
				started[id] = r.Records[0].At
			}
			order = append(order, cb)
			byID[id] = cb
		} else if cb.Title == "" && r.Title != "" {
			cb.Title = r.Title // the newest cut's: Runs come newest first
		}
		if r.Title != "" {
			oldest[cb.ID] = r.Title // each older cut replaces it
		}
		cb.Decisions = append(cb.Decisions, rs.decided...)
		for _, d := range rs.decided {
			if d.Delivery && in(d.At) {
				cb.Delivered++
			}
		}
		cb.Accepted += rs.accepted
		cb.Failed += rs.failed
		cb.Rejected += rs.rejected
		cb.Parked += rs.parked
		cb.Stopped += rs.stopped
		cb.Gates = append(cb.Gates, rs.gates...)
		if rs.accepted+rs.failed+rs.rejected+rs.parked+rs.stopped+len(rs.gates) > 0 {
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
	for _, cb := range order {
		if !shown[cb.ID] {
			continue
		}
		sort.Slice(cb.Gates, func(i, j int) bool { return cb.Gates[i].Since.Before(cb.Gates[j].Since) })
		sort.SliceStable(cb.Decisions, func(i, j int) bool { return cb.Decisions[i].At.Before(cb.Decisions[j].At) })
		if cb.First == "" {
			// The Chat log keeps hashes only: the Task title of the oldest
			// cut read is the human's first message, cut short.
			cb.First = oneLine(oldest[cb.ID])
		}
		b.Chats = append(b.Chats, *cb)
		t := &b.Totals
		t.Settled += cb.Settled
		t.Unsettled += cb.Unsettled
		t.Accepted += cb.Accepted
		t.Failed += cb.Failed
		t.Rejected += cb.Rejected
		t.Parked += cb.Parked
		t.NeedsYou += cb.NeedsYou()
		t.Stopped += cb.Stopped
		if cb.Ended != "" {
			t.Ended++
		}
		if cb.Torn {
			t.Torn++
		}
	}
	b.Slept = max(w.Slept, sleptIn(spans, w))
	return b
}

// runState is what one Run brings to its Chat's part.
type runState struct {
	accepted, failed, rejected, parked, stopped int
	gates                                       []OpenGate
	decided                                     []Decision
}

// read reads a Run's Ledger: its Chat ("" for a one-shot Run), the end it
// reached in the window, and its Gate still open.
func (r BriefRun) read(in func(time.Time) bool) (chat string, s runState) {
	var open *OpenGate
	var decided []Decision
	ended := false
	defer func() { s.decided = decided }()
	for _, rec := range r.Records {
		switch rec.Type {
		case recDelivery:
			var d struct{ Kind string }
			_ = json.Unmarshal(rec.Data, &d)
			if d.Kind == "apply" || d.Kind == "branch" {
				decided = append(decided, Decision{At: rec.At, What: map[string]string{"apply": "applied", "branch": "branched"}[d.Kind] + " cut", Delivery: true})
			}
		case RecRunStarted:
			var d struct{ Chat string }
			_ = json.Unmarshal(rec.Data, &d)
			chat = d.Chat
		case RecGateOpened:
			var d struct{ Pins struct{ Gate string } }
			_ = json.Unmarshal(rec.Data, &d)
			open = &OpenGate{Run: r.ID, Gate: gateWord(d.Pins.Gate), Since: rec.At}
		case RecGateDecided, RecGateParked, RecGateAbandoned:
			if rec.Type == RecGateDecided {
				var d struct {
					Pins   struct{ Gate string }
					Actor  string
					Choice string
				}
				_ = json.Unmarshal(rec.Data, &d)
				if d.Actor == "" || d.Actor == "human" {
					decided = append(decided, Decision{At: rec.At, What: decisionWords(d.Choice, gateWord(d.Pins.Gate))})
				}
			}
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
			case Failed:
				s.failed = 1
			case Rejected: // a Check or a human at a Gate
				s.rejected = 1
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
	case !ended && !r.Live:
		// Gone without an end, its tail torn or not: whatever it waited
		// at waits no more. A live one's torn tail is a record mid-write.
		// When it died the records can't say, so it counts only when its
		// last record is in the window, not in every window after.
		if in(r.last()) {
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
//   - a ConsultationStarted and its ConsultationEnded.
//
// A Check command's duration is no such measure: its start is stored in
// UTC, which drops the monotonic reading, so its duration is wall clock.
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

// Text is the brief as plain lines: the summary, then each Chat's five
// parts. Times are in loc.
func (b Brief) Text(loc *time.Location) string {
	head := b.lead()
	if b.Empty() {
		return "nothing to report " + head + "\n"
	}
	return strings.Join(b.Lines(loc), "\n") + "\n"
}

// Lines are Text's lines. Per Chat, in this order, rebuilding where the
// human was rather than summing up activity (docs/ux/desk.md, I4): You
// were, Since you left, Your decisions, Waiting for you, Next. A part
// with nothing to say is left out, except the decisions and the waiting,
// which say so. Every word is Öge's or the human's own.
func (b Brief) Lines(loc *time.Location) []string {
	lines := []string{b.lead() + ": " + b.summary()}
	for _, c := range b.Chats {
		lines = append(lines, c.name())
		if c.First != "" {
			lines = append(lines, "  You were: "+c.First)
		}
		if s := c.since(loc); s != "" {
			lines = append(lines, "  Since you left: "+s)
		}
		lines = append(lines, "  Your decisions: "+c.decisions(loc))
		if w := c.waiting(loc); w != "" {
			lines = append(lines, "  Waiting for you: "+w)
		} else {
			lines = append(lines, "  Waiting for you: nothing")
		}
		if n := c.Next(loc); n != "" {
			lines = append(lines, "  Next: "+n)
		}
	}
	return lines
}

// Next is the Chat whose card the Inbox shows first and what it is: ▲,
// then ✗, then ✓, the earlier Chat first within each. ok is false when
// nothing is actionable.
func (b Brief) Next(loc *time.Location) (c ChatBrief, what string, ok bool) {
	best := 3
	for _, cc := range b.Chats {
		if r := cc.rank(); r < best {
			c, best = cc, r
		}
	}
	if best == 3 {
		return ChatBrief{}, "", false
	}
	return c, c.Next(loc), true
}

// rank is the Inbox's order of the Chat's first card: 0 ▲, 1 ✗, 2 ✓, 3 none.
func (c ChatBrief) rank() int {
	switch {
	case c.NeedsYou() > 0:
		return 0
	case c.Failed+c.Rejected > 0:
		return 1
	case c.Accepted > c.Delivered:
		return 2
	}
	return 3
}

// Next is the Chat's first actionable card, "" when it has none.
func (c ChatBrief) Next(loc *time.Location) string {
	switch c.rank() {
	case 0:
		if len(c.Gates) > 0 {
			g := c.Gates[0]
			return "▲ " + g.Gate + " Gate since " + g.Since.In(loc).Format("15:04")
		}
		return "▲ " + plural(c.Asks, "ask") + " waiting"
	case 1:
		if c.Failed > 0 {
			return "✗ " + plural(c.Failed, "Check") + " failed"
		}
		return fmt.Sprintf("✗ %d rejected", c.Rejected)
	case 2:
		return "✓ accepted, not applied"
	}
	return ""
}

// decisions are the human's acts in the Chat, oldest first.
func (c ChatBrief) decisions(loc *time.Location) string {
	if len(c.Decisions) == 0 {
		return "none yet"
	}
	var parts []string
	for _, d := range c.Decisions {
		parts = append(parts, d.What+" "+d.At.In(loc).Format("15:04"))
	}
	return strings.Join(parts, " · ")
}

// waiting is what waits for the human now: open Gates, asks, failed Checks.
func (c ChatBrief) waiting(loc *time.Location) string {
	var parts []string
	for _, g := range c.Gates {
		parts = append(parts, "▲ "+g.Gate+" Gate since "+g.Since.In(loc).Format("15:04"))
	}
	if c.Asks > 0 {
		parts = append(parts, "▲ "+plural(c.Asks, "ask")+" waiting")
	}
	if c.Failed > 0 {
		parts = append(parts, "✗ "+plural(c.Failed, "Check")+" failed")
	}
	return strings.Join(parts, " · ")
}

// decisionWords is a Gate decision as a fixed phrase. A choice is one of
// Öge's words; anything else is not repeated.
func decisionWords(choice, gate string) string {
	for _, r := range choice {
		if !(r >= 'a' && r <= 'z' || r == '-') {
			choice = ""
			break
		}
	}
	what := "decided"
	if choice != "" {
		what = "chose " + choice
	}
	if gate == "" {
		return what + " at a Gate"
	}
	return what + " at " + gate + " Gate"
}

// firstMax is how long "You were" runs, in runes.
const firstMax = 72

// oneLine is s as one trimmed line cut to firstMax, control characters
// (an escape sequence's) gone.
func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if rs := []rune(s); len(rs) > firstMax {
		s = strings.TrimSpace(string(rs[:firstMax-1])) + "…"
	}
	return s
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
	if t.Rejected > 0 {
		parts = append(parts, fmt.Sprintf("✗ %d rejected", t.Rejected))
	}
	if t.Parked > 0 {
		parts = append(parts, fmt.Sprintf("%d parked", t.Parked))
	}
	if t.Ended > 0 {
		parts = append(parts, plural(t.Ended, "Chat")+" stopped with work in flight")
	}
	if t.Torn > 0 {
		parts = append(parts, plural(t.Torn, "Chat log")+" torn")
	}
	if t.Stopped > 0 {
		parts = append(parts, plural(t.Stopped, "cut")+" stopped")
	}
	if t.Settled > 0 {
		parts = append(parts, plural(t.Settled, "turn")+" settled")
	}
	if t.Unsettled > 0 {
		parts = append(parts, plural(t.Unsettled, "turn")+" did not settle")
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
	if t.Failed+t.Rejected > 0 {
		parts = append(parts, fmt.Sprintf("✗%d", t.Failed+t.Rejected))
	}
	if t.Accepted > 0 {
		parts = append(parts, fmt.Sprintf("✓%d", t.Accepted))
	}
	if len(parts) == 0 && t.Settled > 0 {
		parts = append(parts, plural(t.Settled, "turn")+" settled")
	}
	return strings.Join(parts, " ")
}

// name is "Chat 3 parser" on the Desk, which numbers its Chats. Elsewhere
// no record holds that number, so none is made up: "Chat <id> parser".
func (c ChatBrief) name() string {
	n := "Chat " + fmt.Sprint(c.N)
	switch {
	case c.Run:
		n = "Run " + c.ID // whole: oge apply and oge receipt take it
	case c.N == 0:
		n = "Chat " + shortID(c.ID)
	}
	if c.Title != "" {
		n += " " + c.Title
	}
	return n
}

// since is the Chat's events in the window, "" when it had none.
func (c ChatBrief) since(loc *time.Location) string {
	var parts []string
	switch c.Ended {
	case "":
	case "hangup":
		parts = append(parts, "stopped when its terminal closed")
	default:
		parts = append(parts, "stopped by an interrupt (a second Ctrl-C or SIGTERM)")
	}
	if c.Torn {
		parts = append(parts, "its log's last record is torn (still being written, or cut off)")
	}
	if c.Failed > 0 {
		parts = append(parts, "✗ "+plural(c.Failed, "Check")+" failed")
	}
	if c.Rejected > 0 {
		parts = append(parts, fmt.Sprintf("✗ %d rejected", c.Rejected))
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
	if c.Unsettled > 0 {
		parts = append(parts, plural(c.Unsettled, "turn")+" did not settle")
	}
	return strings.Join(parts, " · ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// shortID drops the date from a Chat id ("20261101T150405-abcdef" is
// "150405-abcdef"): the date alone tells this morning's Chats apart from
// nothing. Other shapes are kept whole.
func shortID(id string) string {
	if len(id) > 9 && id[8] == 'T' {
		return id[9:]
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
