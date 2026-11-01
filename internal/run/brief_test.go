package run

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/ledger"
)

var updateBrief = flag.Bool("update", false, "rewrite the golden files")

// briefT0 is when the human left in every case: 01:00 UTC.
var briefT0 = time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)

func briefAt(min int) time.Time { return briefT0.Add(time.Duration(min) * time.Minute) }

func briefRec(min int, typ string, data any) ledger.Record {
	raw, _ := json.Marshal(data)
	return ledger.Record{At: briefAt(min), Type: typ, Data: raw}
}

func briefChat(id string, n int, title string, recs ...ledger.Record) BriefChat {
	return BriefChat{BriefLog: BriefLog{ID: id, Records: append([]ledger.Record{briefRec(-60, RecChatStarted, map[string]any{"chat": id})}, recs...)}, N: n, Title: title}
}

func briefCut(id, chat string, live bool, recs ...ledger.Record) BriefRun {
	start := briefRec(-1, RecRunStarted, map[string]any{"run": id, "chat": chat})
	if len(recs) > 0 {
		start.At = recs[0].At.Add(-time.Minute)
	}
	return BriefRun{BriefLog: BriefLog{ID: id, Records: append([]ledger.Record{start}, recs...)}, Live: live}
}

func briefSettled(min, turn int) ledger.Record {
	return briefRec(min, RecAttemptEnded, map[string]any{"turn": turn, "reply": "agent text never shows"})
}

func briefGate(min int, gate string) ledger.Record {
	return briefRec(min, RecGateOpened, map[string]any{"pins": map[string]any{"gate": gate}})
}

func briefEnded(min int, o Outcome) ledger.Record {
	return briefRec(min, RecRunEnded, map[string]any{"outcome": o, "why": []string{"agent text never shows"}})
}

// briefWindow is two hours from briefT0.
var briefWindow = BriefWindow{From: briefT0, To: briefAt(120), Basis: BasisAway}

func TestBriefGolden(t *testing.T) {
	cases := []struct {
		name  string
		w     BriefWindow
		chats []BriefChat
		runs  []BriefRun
	}{
		{name: "accepted",
			chats: []BriefChat{briefChat("c1", 1, "parser", briefSettled(10, 3), briefSettled(30, 4))},
			runs:  []BriefRun{briefCut("r1", "c1", false, briefGate(40, "gate.result"), briefRec(41, RecGateDecided, map[string]any{}), briefEnded(42, Accepted))}},
		{name: "failed",
			chats: []BriefChat{briefChat("c1", 2, "ci-flake", briefSettled(10, 1))},
			runs:  []BriefRun{briefCut("r1", "c1", false, briefEnded(20, Failed))}},
		{name: "parked",
			chats: []BriefChat{briefChat("c1", 4, "docs")},
			runs: []BriefRun{briefCut("r1", "c1", false, briefGate(30, "gate.result"),
				briefRec(50, RecGateParked, map[string]any{}), briefRec(50, RecRunParked, map[string]any{"gate": "gate.result"}))}},
		{name: "open_gate",
			// The Gate opened before the briefWindow and still needs you; an
			// ask waits in another Chat.
			chats: []BriefChat{briefChat("c3", 3, "parser"), func() BriefChat { c := briefChat("c1", 1, "auth", briefSettled(5, 2)); c.Asks = 1; return c }()},
			runs:  []BriefRun{briefCut("r3", "c3", true, briefGate(-20, "gate.tamper"))}},
		{name: "hangup",
			chats: []BriefChat{briefChat("c1", 1, "parser", briefSettled(10, 1), briefRec(11, RecTurnStarted, map[string]any{"turn": 2}),
				// The Chat ends the turn in flight before it records the hangup.
				briefRec(30, RecAttemptEnded, map[string]any{"turn": 2, "failure": "chat_ended: agent text never shows"}),
				briefRec(30, RecTurnInterrupted, map[string]any{"turn": 2, "why": "hangup"}))},
			runs: []BriefRun{briefCut("r1", "c1", false, briefGate(25, "gate.result"))}},
		{name: "sleep_gap",
			// An agent answered 1m after its launch by the monotonic clock,
			// yet 41m later by the wall clock. A Check's commands look the
			// same, but their duration is wall clock too: no sleep in them.
			w:     BriefWindow{From: briefT0, To: briefAt(120), Basis: BasisAway, Slept: 10 * time.Minute},
			chats: []BriefChat{briefChat("c1", 1, "parser", briefSettled(5, 1))},
			runs: []BriefRun{briefCut("r1", "c1", false,
				briefRec(10, RecProcessStarted, map[string]any{"attempt": "a1"}),
				briefRec(51, RecObservation, map[string]any{"attempt": "a1", "kind": "responsiveness", "duration_ms": 60_000}),
				briefRec(52, RecCheckStarted, map[string]any{}),
				briefRec(90, RecCheckEnded, map[string]any{"result": map[string]any{"commands": []map[string]any{
					{"started_at": briefAt(52), "duration_ms": 1000}, {"started_at": briefAt(89), "duration_ms": 1000}}}}),
				briefEnded(91, Accepted))}},
		{name: "interrupt",
			// SIGTERM while away: the turn in flight is cut off, and neither
			// it nor its failure reads as settled.
			chats: []BriefChat{briefChat("c1", 1, "parser", briefRec(11, RecTurnStarted, map[string]any{"turn": 1}),
				briefRec(30, RecAttemptEnded, map[string]any{"turn": 1, "failure": "chat_ended: agent text never shows"}),
				briefRec(30, RecTurnInterrupted, map[string]any{"turn": 1, "why": "interrupt"}))}},
		{name: "turn_failed",
			// The agent died mid-turn and the Chat went on.
			chats: []BriefChat{briefChat("c1", 1, "parser", briefRec(11, RecAttemptEnded, map[string]any{"turn": 1, "failure": "agent text never shows"}),
				briefSettled(20, 2))}},
		{name: "real_ids",
			// Ids as newID makes them: the date alone names nothing. Two cuts
			// of a Chat this home doesn't list are one entry, not two Runs.
			w: BriefWindow{From: briefT0, To: briefAt(130), Basis: BasisLastAct},
			chats: []BriefChat{briefChat("20261101T090000-a1b2c3", 0, "", briefSettled(10, 1)),
				briefChat("20261101T091500-d4e5f6", 0, "", briefSettled(20, 1))},
			runs: []BriefRun{
				briefCut("20261101T093000-0a0b0c", "20261101T080000-778899", true, briefGate(40, "gate.result")),
				briefCut("20261101T092000-0d0e0f", "20261101T080000-778899", false, briefEnded(35, Accepted)),
				briefCut("20261101T091000-101112", "", false, briefEnded(30, Failed))}},
		{name: "five_parts",
			// The Chat's intention, the human's decisions in order, a Gate
			// still open and the first actionable card. The agent's words
			// in the records never show.
			chats: []BriefChat{func() BriefChat {
				c := briefChat("c1", 1, "parser", briefSettled(10, 2))
				c.First = "make the parser accept trailing commas\nand nested arrays"
				return c
			}()},
			runs: []BriefRun{
				briefCut("r1", "c1", false, briefGate(-50, "gate.tamper"),
					briefRec(-40, RecGateDecided, map[string]any{"pins": map[string]any{"gate": "gate.tamper"}, "actor": "human", "choice": "acknowledge", "reason": "agent text never shows"}),
					briefEnded(-35, Accepted), briefRec(-30, recDelivery, map[string]any{"kind": "apply"})),
				briefCut("r2", "c1", true, briefGate(20, "gate.result"))}},
		{name: "no_decisions",
			chats: []BriefChat{func() BriefChat {
				c := briefChat("c1", 2, "auth", briefSettled(10, 1))
				c.First = "fix the login redirect"
				return c
			}()},
			runs: []BriefRun{briefCut("r1", "c1", false, briefEnded(20, Failed))}},
		{name: "no_first_message",
			// Nothing records the first message and no cut has a title: no
			// "You were" line, and nothing is made up.
			chats: []BriefChat{briefChat("c1", 1, "", briefSettled(10, 1))},
			runs:  []BriefRun{briefCut("r1", "c1", false, briefEnded(20, Accepted))}},
		{name: "first_from_cut_title",
			// oge status has no screen: the oldest cut's Task title stands in.
			chats: []BriefChat{briefChat("c1", 0, "", briefSettled(10, 1))},
			runs: []BriefRun{func() BriefRun {
				r := briefCut("r2", "c1", false, briefEnded(30, Accepted))
				r.Title = "second try"
				return r
			}(),
				func() BriefRun {
					r := briefCut("r1", "c1", false, briefEnded(20, Failed))
					r.Title = "fix the parser"
					return r
				}()}},
		{name: "next_order",
			// A ✗ in Chat 1 and a ▲ in Chat 2: the Inbox's order puts ▲ first.
			chats: []BriefChat{briefChat("c1", 1, "parser", briefSettled(10, 1)), briefChat("c2", 2, "auth", briefSettled(11, 1))},
			runs: []BriefRun{briefCut("r1", "c1", false, briefEnded(20, Failed)),
				briefCut("r2", "c2", true, briefGate(30, "gate.tamper"))}},
		{name: "empty",
			chats: []BriefChat{briefChat("c1", 1, "parser", briefSettled(-30, 1))},
			runs:  []BriefRun{briefCut("r1", "c1", false, briefEnded(-20, Accepted))}},
		{name: "last_act",
			w:     BriefWindow{From: briefT0, To: briefAt(130), Basis: BasisLastAct},
			chats: []BriefChat{briefChat("c1", 0, "", briefSettled(10, 1)), briefChat("c2", 0, "", briefSettled(20, 1))},
			runs: []BriefRun{func() BriefRun {
				r := briefCut("r1", "c2", false, briefEnded(30, Accepted))
				r.Title = "fix the parser"
				return r
			}(),
				func() BriefRun {
					r := briefCut("r9", "", false, briefEnded(40, Rejected))
					r.Title = "one-shot task"
					return r
				}()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.w
			if w.To.IsZero() {
				w = briefWindow
			}
			got := BuildBrief(w, tc.chats, tc.runs).Text(time.UTC)
			briefGolden(t, filepath.Join("testdata", "brief", tc.name+".golden"), got)
		})
	}
}

func briefGolden(t *testing.T, path, got string) {
	t.Helper()
	if *updateBrief {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs (run with -update to accept)\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

func TestBriefTotals(t *testing.T) {
	b := BuildBrief(briefWindow, []BriefChat{briefChat("c3", 3, "parser")}, []BriefRun{briefCut("r3", "c3", true, briefGate(-20, "gate.tamper"))})
	if b.Totals.NeedsYou != 1 || len(b.Chats) != 1 || b.Chats[0].Gates[0].Gate != "tamper" || b.Counts() != "▲1" {
		t.Fatalf("brief = %+v, counts %q", b, b.Counts())
	}
	// The same Gate in a Run no process runs any more needs nobody. When
	// that cut stopped the records can't say, so no window counts it.
	b = BuildBrief(briefWindow, []BriefChat{briefChat("c3", 3, "parser")}, []BriefRun{briefCut("r3", "c3", false, briefGate(-20, "gate.tamper"))})
	if b.Totals != (BriefTotals{}) || !b.Empty() {
		t.Fatalf("dead Run's old Gate: brief = %+v", b)
	}
	// One whose last record is in the window stopped in it.
	b = BuildBrief(briefWindow, []BriefChat{briefChat("c3", 3, "parser")}, []BriefRun{briefCut("r3", "c3", false, briefGate(20, "gate.tamper"))})
	if b.Totals.NeedsYou != 0 || b.Totals.Stopped != 1 {
		t.Fatalf("dead Run's Gate in the window: totals = %+v", b.Totals)
	}
}

func TestBriefSleepPairs(t *testing.T) {
	// Overlapping pairs measured the same sleep; apart, they add up.
	chat := briefChat("c1", 1, "x",
		briefRec(10, RecConsultationStarted, map[string]any{"consultation": "a"}),
		briefRec(70, RecConsultationEnded, map[string]any{"consultation": "a", "duration_ms": 10 * 60_000}))
	run := briefCut("r1", "c1", false,
		briefRec(12, RecProcessStarted, map[string]any{"attempt": "a1"}),
		briefRec(68, RecObservation, map[string]any{"attempt": "a1", "kind": "responsiveness", "duration_ms": 16 * 60_000}),
		briefRec(90, RecProcessStarted, map[string]any{"attempt": "a2"}),
		briefRec(110, RecObservation, map[string]any{"attempt": "a2", "kind": "responsiveness", "duration_ms": 5 * 60_000}),
		// Under the floor: noise.
		briefRec(111, RecProcessStarted, map[string]any{"attempt": "a3"}),
		briefRec(112, RecObservation, map[string]any{"attempt": "a3", "kind": "responsiveness", "duration_ms": 0}))
	b := BuildBrief(briefWindow, []BriefChat{chat}, []BriefRun{run})
	if want := 50*time.Minute + 15*time.Minute; b.Slept != want {
		t.Fatalf("slept = %v, want %v", b.Slept, want)
	}
	// The briefWindow's own measure wins when it is longer.
	w := briefWindow
	w.Slept = 90 * time.Minute
	if b := BuildBrief(w, []BriefChat{chat}, []BriefRun{run}); b.Slept != w.Slept {
		t.Fatalf("slept = %v, want the briefWindow's %v", b.Slept, w.Slept)
	}
}

func TestBriefTornLog(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{RecChatStarted, RecTurnStarted, RecAttemptEnded} {
		if err := l.Append(typ, map[string]any{"turn": 1}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	p := filepath.Join(dir, ledger.LedgerFile)
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, b[:len(b)-20], 0o600); err != nil { // the last record dies mid-write
		t.Fatal(err)
	}
	log, err := ReadBriefLog("c1", dir)
	if err != nil || !log.Torn || len(log.Records) != 2 {
		t.Fatalf("ReadBriefLog = %+v, %v", log, err)
	}
	now := time.Now()
	brief := BuildBrief(BriefWindow{From: now.Add(-time.Hour), To: now.Add(time.Minute)}, []BriefChat{{BriefLog: log, N: 1, Title: "parser"}}, nil)
	if len(brief.Chats) != 1 || !brief.Chats[0].Torn || brief.Totals.Ended != 0 || brief.Totals.Torn != 1 {
		t.Fatalf("brief = %+v", brief)
	}
	briefGolden(t, filepath.Join("testdata", "brief", "torn.golden"), strings.Join(brief.Lines(time.UTC)[1:], "\n")+"\n")

	if _, err := ReadBriefLog("none", t.TempDir()); err == nil || errors.Is(err, ledger.ErrTornTail) {
		t.Fatalf("no log: err = %v", err)
	}
}

func TestLastAct(t *testing.T) {
	chats := []BriefChat{briefChat("c1", 1, "x",
		briefRec(10, RecTurnStarted, map[string]any{"turn": 1}),
		briefRec(20, RecTurnStarted, map[string]any{"turn": 2, "by": "oge"}), // a feedback turn: not the human
		briefRec(25, RecAttemptEnded, map[string]any{}))}
	runs := []BriefRun{briefCut("r1", "c1", false, briefRec(15, RecGateDecided, map[string]any{}))}
	if got := LastAct(chats, runs); !got.Equal(briefAt(15)) {
		t.Fatalf("LastAct = %v, want %v", got, briefAt(15))
	}
}

func TestSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second: "0m", 40 * time.Minute: "40m", 2*time.Hour + 10*time.Minute: "2h 10m",
		3 * time.Hour: "3h", 52 * time.Hour: "2d 4h",
	} {
		if got := Span(d); got != want {
			t.Errorf("Span(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestBriefNext(t *testing.T) {
	chats := []BriefChat{briefChat("c1", 1, "parser", briefSettled(10, 1)), briefChat("c2", 2, "auth", briefSettled(11, 1))}
	runs := []BriefRun{briefCut("r1", "c1", false, briefEnded(20, Failed)), briefCut("r2", "c2", true, briefGate(30, "gate.tamper"))}
	b := BuildBrief(briefWindow, chats, runs)
	c, what, ok := b.Next(time.UTC)
	if !ok || c.N != 2 || what != "▲ tamper Gate since 01:30" {
		t.Fatalf("Next = %d %q %v", c.N, what, ok)
	}
	// A ✓ already applied is no card.
	b = BuildBrief(briefWindow, chats[:1], []BriefRun{briefCut("r1", "c1", false, briefEnded(20, Accepted), briefRec(25, recDelivery, map[string]any{"kind": "apply"}))})
	if _, _, ok := b.Next(time.UTC); ok {
		t.Errorf("an applied cut is next")
	}
}

// A Chat's first message and a Gate's choice are cleaned: one line, no
// control sequences, only Öge's own words for a choice.
func TestBriefCleansText(t *testing.T) {
	c := briefChat("c1", 1, "x", briefSettled(10, 1))
	c.First = "\x1b[31mred\x1b]0;title\x07 " + strings.Repeat("long ", 40)
	b := BuildBrief(briefWindow, []BriefChat{c}, []BriefRun{briefCut("r1", "c1", false,
		briefRec(12, RecGateDecided, map[string]any{"pins": map[string]any{"gate": "gate.tamper"}, "actor": "human", "choice": "x\x1b[31m"}))})
	got := b.Text(time.UTC)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 7) || !strings.Contains(got, "decided at tamper Gate") {
		t.Errorf("text = %q", got)
	}
	if n := len([]rune(b.Chats[0].First)); n > firstMax {
		t.Errorf("First has %d runes", n)
	}
}
