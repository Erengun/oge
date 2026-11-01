package run

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/erengun/oge/internal/ledger"
)

type fixRec struct {
	typ  string
	data any
}

// fixLog writes a Ledger of recs at dir, as a Chat log or a cut's Run.
func fixLog(t *testing.T, dir string, recs ...fixRec) *ledger.Blobs {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, r := range recs {
		if err := l.Append(r.typ, r.data); err != nil {
			t.Fatal(err)
		}
	}
	b, err := ledger.OpenBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func turnStarted(n int, text string, blobs *ledger.Blobs, t *testing.T) fixRec {
	d := map[string]any{"turn": n, "message": sha256Hex(text)}
	if blobs != nil {
		id, err := blobs.Put([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		d["text"] = id
	}
	return fixRec{RecTurnStarted, d}
}

func settled(n int, reply string) fixRec {
	return fixRec{RecAttemptEnded, map[string]any{"turn": n, "reply": reply}}
}

// restoreFixture is a Desk of Chats the last oge left, by the records
// each kind of ending leaves.
func restoreFixture(t *testing.T) *ledger.StateRoot {
	t.Helper()
	root := t.TempDir()
	st := &ledger.StateRoot{Dir: root, Private: filepath.Join(root, "private"), Work: filepath.Join(root, "work")}
	chat := func(id string, recs ...fixRec) *ledger.Blobs {
		return fixLog(t, filepath.Join(st.Private, "chats", id), append([]fixRec{{RecChatStarted, map[string]any{"chat": id}}}, recs...)...)
	}
	cut := func(id, chat string, to int, recs ...fixRec) {
		fixLog(t, filepath.Join(st.Private, "runs", id), append([]fixRec{{RecRunStarted, map[string]any{"run": id, "chat": chat, "turns": map[string]int{"from": 1, "to": to}}}}, recs...)...)
	}
	// c1: a turn cut off mid-way by a hangup.
	dir := filepath.Join(st.Private, "chats", "c1")
	os.MkdirAll(dir, 0o700)
	l, _ := ledger.Create(dir)
	blobs, _ := ledger.OpenBlobs(dir)
	id, _ := blobs.Put([]byte("make the parser accept trailing commas"))
	id2, _ := blobs.Put([]byte("and the lexer"))
	for _, r := range []fixRec{
		{RecChatStarted, map[string]any{"chat": "c1"}},
		{RecTurnStarted, map[string]any{"turn": 1, "message": "h", "text": id}},
		settled(1, "done: commas accepted"),
		{RecTurnStarted, map[string]any{"turn": 2, "message": "h", "text": id2}},
		{RecProcessStarted, map[string]any{"pid": 4242, "pgid": 4242, "start": "123.45"}},
		// The pump ends the turn in flight when the stop closes the Session.
		{RecAttemptEnded, map[string]any{"turn": 2, "failure": "agent_crash: chat_ended"}},
		{RecTurnInterrupted, map[string]any{"turn": 2, "why": "hangup"}},
	} {
		l.Append(r.typ, r.data)
	}
	l.Close()
	// c2: verified, parked at the result Gate when oge exited: the Chat log
	// says a turn was interrupted, the Run says Parked.
	chat("c2", turnStarted(1, "fix auth", nil, t), settled(1, "ok"), fixRec{RecTurnInterrupted, map[string]any{"turn": 1, "why": "hangup"}})
	cut("20261101-0001-c2", "c2", 1, fixRec{RecVerdict, map[string]any{}}, fixRec{RecRunParked, map[string]any{"gate": "gate.result"}})
	// c3: Accepted, and the Desk recorded Seen up to it.
	chat("c3", turnStarted(1, "docs", nil, t), settled(1, "ok"))
	cut("20261101-0002-c3", "c3", 1, fixRec{RecVerdict, map[string]any{}}, fixRec{RecRunEnded, map[string]any{"outcome": Accepted}})
	// c4: Accepted, not seen.
	chat("c4", turnStarted(1, "cache", nil, t), settled(1, "ok"))
	cut("20261101-0003-c4", "c4", 1, fixRec{RecVerdict, map[string]any{}}, fixRec{RecRunEnded, map[string]any{"outcome": Accepted}})
	// c5: the Check failed.
	chat("c5", turnStarted(1, "ci", nil, t), settled(1, "ok"))
	cut("20261101-0004-c5", "c5", 1, fixRec{RecVerdict, map[string]any{}}, fixRec{RecRunEnded, map[string]any{"outcome": Failed}})
	// c6: oge exited while the Check ran: the Run was cancelled and ended.
	cut("20261101-0005-c6", "c6", 1, fixRec{RecCheckStarted, map[string]any{}},
		fixRec{RecCheckEnded, map[string]any{"interrupted": true}},
		fixRec{RecRunEnded, map[string]any{"outcome": InfrastructureStop, "why": []string{interrupted}}})
	// The exit's TurnInterrupted lands after the cut began.
	chat("c6", turnStarted(1, "lint", nil, t), settled(1, "ok"), fixRec{RecTurnInterrupted, map[string]any{"turn": 1, "why": "hangup"}})
	// c12: the human cancelled the Check with Ctrl+C, then quit idle: the
	// cut ends interrupted, but no TurnInterrupted follows.
	chat("c12", turnStarted(1, "vet", nil, t), settled(1, "ok"))
	cut("20261101-0007-c12", "c12", 1, fixRec{RecCheckStarted, map[string]any{}},
		fixRec{RecCheckEnded, map[string]any{"interrupted": true}},
		fixRec{RecRunEnded, map[string]any{"outcome": InfrastructureStop, "why": []string{interrupted}}})
	// c10: oge was killed while the Check ran: its Run has no end.
	chat("c10", turnStarted(1, "fmt", nil, t), settled(1, "ok"))
	cut("20261101-0006-c10", "c10", 1, fixRec{RecCheckStarted, map[string]any{}})
	// c11: a turn settled, then a stop with no cut Ledger: no Check ran.
	chat("c11", turnStarted(1, "tidy", nil, t), settled(1, "ok"), fixRec{RecTurnInterrupted, map[string]any{"turn": 1, "why": "quit"}})
	// c7: closed by the human. c8: its log is gone. c9: killed with no record.
	chat("c7")
	chat("c9", turnStarted(1, "slow", nil, t))

	d, err := OpenDesk(st.Private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10", "c11", "c12"} {
		d.ChatAdded(c, i+1, "chat "+c, "/repo/"+c)
	}
	d.ChatClosed("c7")
	d.Seen("c3", "20261101-0002-c3")
	d.Release()
	return st
}

func TestRestoreDeskStates(t *testing.T) {
	st := restoreFixture(t)
	d, err := RestoreDesk(st)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		state RestoredState
		card  string
		repo  string
	}
	cases := map[string]want{
		"c1":  {RestoredStopped, CardInterrupted, "/repo/c1"},
		"c2":  {RestoredParked, CardParked, "/repo/c2"}, // the Run's Parked beats the Chat log's TurnInterrupted
		"c3":  {RestoredIdle, "", "/repo/c3"},           // Seen covers it
		"c4":  {RestoredDone, "", "/repo/c4"},
		"c5":  {RestoredFailed, "", "/repo/c5"},
		"c6":  {RestoredStopped, CardCheckInterrupted, "/repo/c6"},
		"c8":  {RestoredUnknown, "", "/repo/c8"},
		"c9":  {RestoredStopped, CardInterrupted, "/repo/c9"},
		"c10": {RestoredStopped, CardCheckInterrupted, "/repo/c10"},
		"c11": {RestoredStopped, CardInterrupted, "/repo/c11"},
		"c12": {RestoredIdle, "", "/repo/c12"}, // a cancelled Check is no stop
	}
	if len(d.Chats) != len(cases) {
		t.Fatalf("%d Chats restored, want %d (a closed Chat stays out)", len(d.Chats), len(cases))
	}
	last := 0
	for _, c := range d.Chats {
		if c.N <= last {
			t.Errorf("Chats are not in number order: %d after %d", c.N, last)
		}
		last = c.N
		w, ok := cases[c.ID]
		if !ok {
			t.Fatalf("restored %s, which was closed", c.ID)
		}
		card := ""
		if c.Card != nil {
			card = c.Card.Kind
		}
		if c.State != w.state || card != w.card || c.Repo != w.repo {
			t.Errorf("%s: state %s card %q repo %q, want %s %q %q", c.ID, c.State, card, c.Repo, w.state, w.card, w.repo)
		}
	}
	by := map[string]*RestoredChat{}
	for _, c := range d.Chats {
		by[c.ID] = c
	}
	c1 := by["c1"]
	if len(c1.Turns) != 2 || c1.Turns[0].Text != "make the parser accept trailing commas" || c1.Turns[0].Reply != "done: commas accepted" ||
		c1.Turns[1].Settled || c1.Turns[1].Text != "and the lexer" || c1.Card.Turn != 2 || c1.Card.Why != "hangup" {
		t.Errorf("c1: %+v card %+v", c1.Turns, c1.Card)
	}
	if c := by["c2"]; !c.Card.Verified || c.Card.Gate != "gate.result" {
		t.Errorf("c2 card %+v", c.Card)
	}
	if c := by["c3"]; !c.Seen || c.Run != "20261101-0002-c3" {
		t.Errorf("c3: seen %v run %q", c.Seen, c.Run)
	}
	if c := by["c9"]; c.Card.Why != "" || c.Card.Turn != 1 {
		t.Errorf("c9: a kill leaves no why: %+v", c.Card)
	}
	if c := by["c6"]; c.Card.Why != "hangup" {
		t.Errorf("c6 card %+v", c.Card)
	}
	if c := by["c11"]; c.Card.Turn != 1 || c.Card.Why != "quit" {
		t.Errorf("c11 card %+v", c.Card)
	}
	if c := by["c8"]; c.Problem == "" {
		t.Error("c8: an unknown state says why")
	}
	if len(c1.procs) != 1 || c1.procs[0].start != "123.45" {
		t.Errorf("c1 procs %+v", c1.procs)
	}
}

// A cut a newer one superseded is not the Chat's state: the newer is.
func TestRestoreSkipsASupersededCut(t *testing.T) {
	root := t.TempDir()
	st := &ledger.StateRoot{Dir: root, Private: filepath.Join(root, "private"), Work: filepath.Join(root, "work")}
	fixLog(t, filepath.Join(st.Private, "chats", "x"), fixRec{RecChatStarted, map[string]any{"chat": "x"}}, settled(1, "a"), settled(2, "b"))
	fixLog(t, filepath.Join(st.Private, "runs", "20261101-1"), fixRec{RecRunStarted, map[string]any{"chat": "x", "turns": map[string]int{"to": 1}}},
		fixRec{RecRunEnded, map[string]any{"outcome": InfrastructureStop, "stop": "superseded"}})
	fixLog(t, filepath.Join(st.Private, "runs", "20261101-2"), fixRec{RecRunStarted, map[string]any{"chat": "x", "turns": map[string]int{"to": 2}}},
		fixRec{RecRunEnded, map[string]any{"outcome": Failed}})
	d, _ := OpenDesk(st.Private, func(string) string { return "" })
	d.ChatAdded("x", 1, "x", "/r")
	d.Release()
	r, err := RestoreDesk(st)
	if err != nil || len(r.Chats) != 1 || r.Chats[0].State != RestoredFailed {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestCloseRestoredRecordsAndRemovesTheWorkspace(t *testing.T) {
	st := restoreFixture(t)
	d, _ := RestoreDesk(st)
	var c1 *RestoredChat
	for _, c := range d.Chats {
		if c.ID == "c1" {
			c1 = c
		}
	}
	os.MkdirAll(filepath.Join(c1.Work, "implement"), 0o700)
	desk, err := OpenDesk(st.Private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	CloseRestored(desk, c1)
	desk.Release()
	if _, err := os.Stat(c1.Work); !os.IsNotExist(err) {
		t.Errorf("the Workspace stayed: %v", err)
	}
	again, _ := RestoreDesk(st)
	for _, c := range again.Chats {
		if c.ID == "c1" {
			t.Error("a closed Chat is restored again")
		}
	}
}
