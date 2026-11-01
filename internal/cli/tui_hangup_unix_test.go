//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// hangUp sends the session a SIGHUP, as closing the terminal does, and
// waits for it to end.
func (s *homeSession) hangUp() {
	s.t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		s.t.Fatal(err)
	}
	s.wait()
	if strings.Contains(s.errOut.String(), "killed") {
		s.t.Errorf("stderr: %s", s.errOut.String())
	}
}

// stoppedChat checks the one Chat a hangup ended: its log ends in
// TurnInterrupted, its Workspace is still there with want in it, and its
// agent's process group is gone.
func stoppedChat(t *testing.T, f *runFixture, want string) {
	t.Helper()
	logs, _ := filepath.Glob(filepath.Join(f.state, "private", "chats", "*"))
	if len(logs) != 1 {
		t.Fatalf("Chat logs: %v", logs)
	}
	recs, err := ledger.Replay(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 || recs[len(recs)-1].Type != run.RecTurnInterrupted {
		var types []string
		for _, r := range recs {
			types = append(types, r.Type)
		}
		t.Fatalf("the Chat log doesn't end in %s: %v", run.RecTurnInterrupted, types)
	}
	var last struct{ Why string }
	if err := json.Unmarshal(recs[len(recs)-1].Data, &last); err != nil || last.Why != "hangup" {
		t.Errorf("TurnInterrupted %s: %v", recs[len(recs)-1].Data, err)
	}
	for _, r := range recs {
		if r.Type != run.RecProcessStarted {
			continue
		}
		var p struct{ PGID int }
		if err := json.Unmarshal(r.Data, &p); err != nil || p.PGID <= 0 {
			t.Fatalf("ProcessStarted %s: %v", r.Data, err)
		}
		if syscall.Kill(-p.PGID, 0) == nil {
			t.Errorf("the Chat's agent process group %d is still running", p.PGID)
		}
	}
	work, _ := filepath.Glob(filepath.Join(f.state, "work", "chats", "*", "implement", want))
	if len(work) != 1 {
		t.Errorf("the Chat's Workspace wasn't kept with %s in it", want)
	}
}

// Closing the terminal mid-turn stops the Chat as an interrupt does: the
// turn is interrupted and scope-checked, the agent's group is killed, and
// the Workspace stays.
func TestHomeChatHangupMidTurn(t *testing.T) {
	// Sequential: it signals the test process.
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "working on it", Writes: []multiturn.File{{Path: "add.go", Content: "package fx\n"}}, Hold: true},
	}}, nil)
	s.composer()
	s.type_("long job\r")
	s.expect("working on it")
	time.Sleep(100 * time.Millisecond) // the write lands before the hold
	s.hangUp()
	stoppedChat(t, f, "add.go")
}

// Closing the terminal while a cut verifies cancels the cut and kills its
// Check, and the Chat stops as it does mid-turn.
func TestHomeChatHangupDuringVerify(t *testing.T) {
	// Sequential: it signals the test process.
	s, f, pids := heldChatSession(t)
	s.hangUp()
	checkGone(t, pids)
	stoppedChat(t, f, "hold.go")
}

// A SIGTERM stops a Chat as a hangup does, logged as an interrupt.
func TestHomeChatTerminatedMidTurn(t *testing.T) {
	// Sequential: it signals the test process.
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "working on it", Hold: true},
	}}, nil)
	s.composer()
	s.type_("long job\r")
	s.expect("working on it")
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	s.wait()
	logs, _ := filepath.Glob(filepath.Join(f.state, "private", "chats", "*"))
	if len(logs) != 1 {
		t.Fatalf("Chat logs: %v", logs)
	}
	recs, err := ledger.Replay(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if last := recs[len(recs)-1]; last.Type != run.RecTurnInterrupted || !strings.Contains(string(last.Data), `"interrupt"`) {
		t.Errorf("last record %s %s", last.Type, last.Data)
	}
}

// redirectFDs points a descriptor at a 0600 log, appending.
func TestRedirectFDs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	log := filepath.Join(dir, "runner.log")
	if err := os.WriteFile(log, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := redirectFDs(log, int(out.Fd())); err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteString("after\n"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if string(b) != "before\nafter\n" {
		t.Errorf("runner.log: %q", b)
	}
	fresh := filepath.Join(dir, "fresh.log")
	if err := redirectFDs(fresh, int(out.Fd())); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(fresh); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("fresh runner.log: %v %v", st, err)
	}
}
