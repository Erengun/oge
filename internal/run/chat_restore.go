package run

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
)

// Restore (docs/ux/away.md, "Restore", build slice 4): bare oge reads
// desk.log and each Chat's own records, and gives back the Desk's list
// with each Chat's state as the records show it. It starts no process.
// A state the records don't settle is Unknown, never a guess.

// RestoredState is how a restored Chat stands.
type RestoredState string

const (
	// RestoredIdle: its turns settled and nothing is owed.
	RestoredIdle RestoredState = "idle"
	// RestoredStopped: a turn or a cut was cut off, by a signal or by a
	// process that died without a record.
	RestoredStopped RestoredState = "stopped"
	// RestoredParked: the newest cut's Run is parked at a Gate. The Run's
	// own record is the truth, over a TurnInterrupted in the Chat log.
	RestoredParked RestoredState = "parked"
	// RestoredFailed: the newest cut's Check failed.
	RestoredFailed RestoredState = "failed"
	// RestoredDone: the newest cut was Accepted, and the human hasn't
	// seen it (no Seen covers it) nor taken it.
	RestoredDone RestoredState = "done"
	// RestoredUnknown: the records can't be read, or say too little.
	RestoredUnknown RestoredState = "unknown"
)

// The kinds of RestoredCard.
const (
	CardInterrupted      = "interrupted"       // a turn stopped when oge exited
	CardParked           = "parked"            // a cut waits at a Gate
	CardCheckInterrupted = "check-interrupted" // a cut's Check was killed
)

// RestoredCard is what the Inbox says of a Chat that wants the human. It
// holds Öge's facts only: no agent text.
type RestoredCard struct {
	Kind string
	Turn int
	// Why is the TurnInterrupted's why (hangup, interrupt, quit, stop);
	// "" when the process died without one.
	Why string
	// Gate is the parked Gate's name; Verified says its cut had a Verdict.
	Gate     string
	Verified bool
	At       time.Time
}

// RestoredTurn is one turn as its records show it.
type RestoredTurn struct {
	N int
	// Text is the message, "" when the log holds none (written before
	// TurnStarted kept it, or its blob is gone).
	Text string
	// Oge is a turn Öge sent (feedback or advice).
	Oge     bool
	Reply   string
	Failure string
	// Settled says its AttemptEnded was written: it ended cleanly.
	Settled bool
}

// RestoredChat is one Chat of the Desk's list, restored.
type RestoredChat struct {
	ID    string
	N     int
	Title string
	Repo  string
	// Dir is its private directory, Work its Workspace's parent: kept
	// across a stop, removed when the human closes the Chat.
	Dir, Work string

	State RestoredState
	// Word is the state's own word when State alone doesn't say it (an
	// outcome like rejected); "" otherwise.
	Word string
	// Since is when the state began, as far as the records say; zero
	// when they don't.
	Since time.Time
	// Problem says why the state is Unknown.
	Problem string
	Turns   []RestoredTurn
	Card    *RestoredCard
	// Run is the newest cut's Run id when it was Accepted: what Seen
	// covers, and what a ✓ is of. Seen says one does.
	Run  string
	Seen bool

	// procs are the processes its log recorded, for the residue sweep.
	procs []recordedProc
}

// recordedProc is a process a Chat log recorded (ProcessStarted or
// SessionReopened) with its start identity, "" when none was read.
type recordedProc struct {
	pid, pgid int
	start     string
}

// RestoredDesk is the Desk's list after a restart.
type RestoredDesk struct {
	// Chats are the open ones, by number.
	Chats []*RestoredChat
	// Swept are the process groups the residue sweep killed.
	Swept []SweptGroup
}

// SweptGroup is a process group the residue sweep killed.
type SweptGroup struct {
	Chat      string
	PID, PGID int
}

// SweepResidue kills the process groups the restored Chats' logs recorded
// whose process is still the one recorded: same pid, same start identity,
// and still leading the recorded group. A pid that is gone, that another
// process has now, or whose identity was never recorded or can't be read
// is left alone: Öge never kills what it can't tell is its own. It runs
// before a Workspace is touched, and starts nothing.
func (d *RestoredDesk) SweepResidue() {
	for _, c := range d.Chats {
		for _, p := range c.procs {
			if sweepGroup(p.pid, p.pgid, p.start) {
				d.Swept = append(d.Swept, SweptGroup{Chat: c.ID, PID: p.pid, PGID: p.pgid})
			}
		}
	}
}

// RestoreDesk reads the Desk's Chats under state: those ChatAdded and not
// ChatClosed in desk.log, each with its Chat log and its cuts' Run
// Ledgers. It reads only; SweepResidue is the other half of a restore.
func RestoreDesk(state *ledger.StateRoot) (*RestoredDesk, error) {
	recs, err := ReadDesk(state.Private)
	if err != nil {
		return nil, err
	}
	type held struct {
		ChatAdded
		seen string
	}
	open := map[string]*held{}
	var order []string
	for _, r := range recs {
		switch r.Type {
		case RecChatAdded:
			var d ChatAdded
			if json.Unmarshal(r.Data, &d) != nil || d.Chat == "" {
				continue
			}
			if _, ok := open[d.Chat]; !ok {
				order = append(order, d.Chat)
			}
			open[d.Chat] = &held{ChatAdded: d}
		case RecChatClosed:
			var d ChatClosed
			if json.Unmarshal(r.Data, &d) == nil {
				delete(open, d.Chat)
			}
		case RecSeen:
			var d Seen
			if json.Unmarshal(r.Data, &d) == nil && open[d.Chat] != nil {
				open[d.Chat].seen = d.Upto
			}
		}
	}
	d := &RestoredDesk{}
	if len(open) == 0 {
		return d, nil
	}
	cuts := cutsByChat(state.Private)
	for _, id := range order {
		h := open[id]
		if h == nil {
			continue
		}
		c := &RestoredChat{ID: id, N: h.N, Title: h.Title, Repo: h.Repo,
			Dir: filepath.Join(state.Private, "chats", id), Work: filepath.Join(state.Work, "chats", id)}
		restoreChat(c, cuts[id], h.seen)
		d.Chats = append(d.Chats, c)
	}
	sort.SliceStable(d.Chats, func(i, j int) bool { return d.Chats[i].N < d.Chats[j].N })
	return d, nil
}

// restoredCut is what a cut's Run Ledger says of how the cut ended.
type restoredCut struct {
	id    string
	to    int // the last turn it covers
	at    time.Time
	state cutEnd
	gate  string
	// verdict says a Verdict was recorded: its Check finished.
	verdict bool
	outcome Outcome
}

type cutEnd int

const (
	cutOpen cutEnd = iota // no end recorded
	cutEnded
	cutParked
	cutSuperseded // a newer cut's Check covers it
)

// cutsByChat lists the cuts of every Chat, oldest first. Only the first
// record of each Run Ledger is read to place it; a cut's whole Ledger is
// read when its Chat is restored.
func cutsByChat(private string) map[string][]string {
	runs := filepath.Join(private, "runs")
	entries, err := os.ReadDir(runs)
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, e := range entries { // sorted by name: Run ids lead with their time
		if !e.IsDir() {
			continue
		}
		if chat := runChat(filepath.Join(runs, e.Name())); chat != "" {
			out[chat] = append(out[chat], filepath.Join(runs, e.Name()))
		}
	}
	return out
}

// runChat is the Chat the Run in dir is a cut of, from its first record;
// "" for a one-shot Run or an unreadable Ledger.
func runChat(dir string) string {
	f, err := os.Open(filepath.Join(dir, ledger.LedgerFile))
	if err != nil {
		return ""
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 1<<16).ReadBytes('\n')
	if err != nil {
		return ""
	}
	var r ledger.Record
	if json.Unmarshal(line, &r) != nil || r.Type != RecRunStarted {
		return ""
	}
	var d struct{ Chat string }
	_ = json.Unmarshal(r.Data, &d)
	return d.Chat
}

func readCut(dir string) (restoredCut, bool) {
	log, err := ReadBriefLog(filepath.Base(dir), dir)
	if err != nil {
		return restoredCut{}, false
	}
	c := restoredCut{id: log.ID}
	for _, rec := range log.Records {
		c.at = rec.At
		switch rec.Type {
		case RecRunStarted:
			var d struct{ Turns struct{ To int } }
			_ = json.Unmarshal(rec.Data, &d)
			c.to = d.Turns.To
		case RecVerdict, RecVerdictReused:
			c.verdict = true
		case RecRunParked:
			var d struct{ Gate string }
			_ = json.Unmarshal(rec.Data, &d)
			c.state, c.gate = cutParked, d.Gate
		case RecRunEnded:
			var d struct {
				Outcome Outcome
				Stop    string
			}
			_ = json.Unmarshal(rec.Data, &d)
			c.state, c.outcome = cutEnded, d.Outcome
			if d.Outcome == InfrastructureStop && d.Stop == "superseded" {
				c.state = cutSuperseded
			}
		}
	}
	return c, true
}

// restoreChat reads c's Chat log and its cuts' Ledgers, and settles its
// state.
func restoreChat(c *RestoredChat, cutDirs []string, seen string) {
	log, err := ReadBriefLog(c.ID, c.Dir)
	if err != nil {
		c.State, c.Problem = RestoredUnknown, "its log can't be read"
		return
	}
	blobs, _ := ledger.OpenBlobs(c.Dir)
	turns := map[int]*RestoredTurn{}
	turn := func(n int) *RestoredTurn {
		t := turns[n]
		if t == nil {
			t = &RestoredTurn{N: n}
			turns[n] = t
		}
		return t
	}
	var (
		intr      *RestoredCard
		delivered int // the last turn a delivery took
		last      time.Time
	)
	for _, rec := range log.Records {
		last = rec.At
		switch rec.Type {
		case RecProcessStarted, RecSessionReopened:
			var d struct {
				PID, PGID int
				Start     string
			}
			if json.Unmarshal(rec.Data, &d) == nil && d.PID > 0 {
				c.procs = append(c.procs, recordedProc{d.PID, d.PGID, d.Start})
			}
		case RecTurnStarted:
			var d struct {
				Turn int
				By   string
				Text string
			}
			if json.Unmarshal(rec.Data, &d) != nil || d.Turn <= 0 {
				continue
			}
			t := turn(d.Turn)
			t.Oge = d.By == "oge"
			if d.Text != "" && blobs != nil {
				if b, err := blobs.Get(d.Text); err == nil {
					t.Text = string(b)
				}
			}
		case RecAttemptEnded:
			var d struct {
				Turn           int
				Reply, Failure string
			}
			if json.Unmarshal(rec.Data, &d) != nil || d.Turn <= 0 {
				continue
			}
			t := turn(d.Turn)
			t.Settled, t.Reply, t.Failure = true, d.Reply, d.Failure
		case RecTurnInterrupted:
			var d struct {
				Turn int
				Why  string
			}
			if json.Unmarshal(rec.Data, &d) == nil {
				intr = &RestoredCard{Kind: CardInterrupted, Turn: d.Turn, Why: d.Why, At: rec.At}
			}
		case RecDelivered:
			var d struct{ Turn int }
			if json.Unmarshal(rec.Data, &d) == nil {
				delivered = max(delivered, d.Turn)
			}
		}
	}
	lastStarted, lastSettled, unsettled := 0, 0, 0
	for n, t := range turns {
		c.Turns = append(c.Turns, *t)
		lastStarted = max(lastStarted, n)
		if t.Settled {
			lastSettled = max(lastSettled, n)
		} else if unsettled == 0 || n < unsettled {
			unsettled = n
		}
	}
	sort.Slice(c.Turns, func(i, j int) bool { return c.Turns[i].N < c.Turns[j].N })

	// The newest cut that no newer one's Check superseded.
	var cut *restoredCut
	for i := len(cutDirs) - 1; i >= 0; i-- {
		if k, ok := readCut(cutDirs[i]); ok && k.state != cutSuperseded {
			cut = &k
			break
		}
	}
	covers := cut != nil && cut.to >= lastSettled && unsettled == 0

	switch {
	case covers && cut.state == cutParked:
		c.State, c.Since = RestoredParked, cut.at
		c.Card = &RestoredCard{Kind: CardParked, Gate: cut.gate, Verified: cut.verdict, At: cut.at}
	case covers && cut.state == cutOpen:
		// Its process is gone: a Check killed has no Verdict.
		c.State, c.Since = RestoredStopped, cut.at
		c.Card = &RestoredCard{Kind: CardCheckInterrupted, Turn: cut.to, At: cut.at}
		if intr != nil {
			c.Card.Why, c.Card.At = intr.Why, intr.At
			c.Since = intr.At
		}
	case unsettled != 0:
		c.State, c.Since = RestoredStopped, last
		c.Card = &RestoredCard{Kind: CardInterrupted, Turn: unsettled, At: last}
		if intr != nil {
			c.Card.Why, c.Card.At = intr.Why, intr.At
			c.Since = intr.At
		}
	case covers && cut.state == cutEnded && cut.outcome == Failed:
		c.State, c.Since = RestoredFailed, cut.at
	case covers && cut.state == cutEnded && cut.outcome == Accepted:
		c.Run, c.Since = cut.id, cut.at
		switch {
		case seen == cut.id:
			c.Seen, c.State = true, RestoredIdle
		case delivered >= cut.to:
			c.State = RestoredIdle // applied or branched: nothing is owed
		default:
			c.State = RestoredDone
		}
	case covers && cut.state == cutEnded:
		// Rejected, Overridden and the rest: a known end, not idle.
		c.State, c.Since = RestoredIdle, cut.at
		c.Word = strings.ToLower(string(cut.outcome))
	case intr != nil && lastStarted > 0:
		// Every turn settled, but a cut was in flight and left no Run.
		c.State, c.Since = RestoredStopped, intr.At
		c.Card = &RestoredCard{Kind: CardCheckInterrupted, Turn: lastSettled, Why: intr.Why, At: intr.At}
	case lastStarted == 0 && intr == nil && cut == nil:
		c.State = RestoredIdle // opened, nothing sent
	default:
		c.State, c.Since = RestoredIdle, last
	}
	if c.State == RestoredIdle && c.Word == "" && c.Since.IsZero() {
		c.Since = last
	}
	if log.Torn && c.State == RestoredIdle {
		// The last record died mid-write: whatever it was, no clean end.
		c.State, c.Word, c.Problem = RestoredUnknown, "", "its log ends torn"
	}
}

// CloseRestored closes a restored Chat as the human asked: its ChatClosed
// is recorded, then its Workspace goes. A kept Chat is never closed.
func CloseRestored(d *Desk, c *RestoredChat) {
	d.ChatClosed(c.ID)
	if c.Work != "" {
		_ = oracle.RemoveAll(c.Work)
	}
}
