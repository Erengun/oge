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
	started := 0
	for _, r := range recs {
		if r.Type != run.RecProcessStarted {
			continue
		}
		started++
		var p struct{ PGID int }
		if err := json.Unmarshal(r.Data, &p); err != nil || p.PGID <= 0 {
			t.Fatalf("ProcessStarted %s: %v", r.Data, err)
		}
		if syscall.Kill(-p.PGID, 0) == nil {
			t.Errorf("the Chat's agent process group %d is still running", p.PGID)
		}
	}
	if started == 0 {
		t.Errorf("the Chat log has no %s: nothing to check the process group of", run.RecProcessStarted)
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

// A hangup or SIGTERM keeps the Chat for next time (A3): desk.log has
// ChatAdded and an unclean DeskClosed, no ChatClosed, and the Workspace
// stays.
func TestSignalKeepsTheChatInDeskLog(t *testing.T) {
	// Sequential: it signals the test process.
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
				{Reply: "working on it", Hold: true},
			}}, nil)
			s.composer()
			s.type_("long job\r")
			s.expect("working on it")
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			s.wait()
			state, err := ledger.OpenStateRoot(f.state, f.repo)
			if err != nil {
				t.Fatal(err)
			}
			recs, err := run.ReadDesk(state.Private)
			if err != nil {
				t.Fatal(err)
			}
			var types []string
			for _, r := range recs {
				types = append(types, r.Type)
			}
			if got, want := strings.Join(types, " "), strings.Join([]string{run.RecDeskOpened, run.RecChatAdded, run.RecDeskClosed}, " "); got != want {
				t.Fatalf("desk.log %s, want %s", got, want)
			}
			var c run.DeskClosed
			_ = json.Unmarshal(recs[len(recs)-1].Data, &c)
			if c.Clean {
				t.Error("a signal wrote a clean DeskClosed")
			}
			if left, _ := filepath.Glob(filepath.Join(f.state, "work", "chats", "*")); len(left) != 1 {
				t.Errorf("Workspaces left %v, want the Chat's kept", left)
			}
		})
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
	if err := os.WriteFile(log, []byte("before\n"), 0o644); err != nil { // an older, looser log
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
	if fi, err := os.Stat(log); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("an older runner.log isn't tightened to 0600: %v %v", fi.Mode(), err)
	}
	fresh := filepath.Join(dir, "fresh.log")
	if err := redirectFDs(fresh, int(out.Fd())); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(fresh); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("fresh runner.log: %v %v", st, err)
	}
}

// A tty read's EIO ends the session as a hangup, once: the SIGHUP that
// follows neither runs the hangup again nor forces.
func TestInterruptsEIOThenSIGHUP(t *testing.T) {
	t.Parallel()
	cancelled, hangups := 0, 0
	in := newInterrupts(func() { cancelled++ })
	in.hangup = func() { hangups++ }
	in.hangUp()
	in.signalled(syscall.SIGHUP)
	if hangups != 1 || cancelled == 0 || in.why() != "hangup" {
		t.Errorf("hangups %d, cancelled %d, why %q", hangups, cancelled, in.why())
	}
	select {
	case <-in.forced:
		t.Error("the late SIGHUP forced the stop")
	default:
	}
}

// A SIGHUP while interrupts go to a Run or an editor still ends the home:
// there is no terminal to return to.
func TestSIGHUPWhileForwardedEndsTheHome(t *testing.T) {
	t.Parallel()
	homeCancelled, runCancelled := 0, 0
	in := newInterrupts(func() { homeCancelled++ })
	in.forwardTo(newInterrupts(func() { runCancelled++ }))
	in.signalled(syscall.SIGHUP)
	if runCancelled != 1 || homeCancelled == 0 || in.why() != "hangup" {
		t.Errorf("run cancelled %d, home cancelled %d, why %q", runCancelled, homeCancelled, in.why())
	}
	diverted := 0
	in2 := newInterrupts(func() { homeCancelled++ })
	homeCancelled = 0
	in2.divert(func() { diverted++ })
	in2.signalled(syscall.SIGHUP)
	if diverted != 1 || homeCancelled == 0 {
		t.Errorf("diverted %d, home cancelled %d", diverted, homeCancelled)
	}
}

// A hangup or SIGTERM while a live Run waits at its Gate is Öge exiting:
// the Gate parks with its pins and the Run ends Parked. Nobody decided, so
// no GateAbandoned (Away slice 2).
func TestHomeExitParksTheLiveGate(t *testing.T) {
	// Sequential: it signals the test process.
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			f := newRunFixture(t)
			s := startHomeSession(t, f, "--fast", "--agent", "fake", "--confirm")
			s.composer()
			s.type_("fix Add\r")
			s.expect("Result gate")
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			s.wait()
			runs := runDirs(t, f)
			if len(runs) != 1 {
				t.Fatalf("want one Run, got %v", runs)
			}
			recs, err := ledger.Replay(runs[0])
			if err != nil {
				t.Fatal(err)
			}
			var types []string
			var parked struct {
				Pins struct{ Gate, Attempt, Candidate string }
			}
			for _, r := range recs {
				types = append(types, r.Type)
				if r.Type == run.RecGateParked {
					if err := json.Unmarshal(r.Data, &parked); err != nil {
						t.Fatal(err)
					}
				}
			}
			all := strings.Join(types, " ")
			if strings.Contains(all, run.RecGateAbandoned) || !strings.HasSuffix(all, run.RecGateOpened+" "+run.RecGateParked+" "+run.RecRunParked) {
				t.Fatalf("the Ledger doesn't end parked at the Gate: %s", all)
			}
			if p := parked.Pins; p.Gate == "" || p.Attempt == "" || p.Candidate == "" {
				t.Errorf("GateParked lacks its pins: %+v", parked)
			}
		})
	}
}

// A hangup or SIGTERM marks Öge as exiting before the forwarded Run is
// cancelled, so the Run's Gate parks; a signalled SIGINT is a Ctrl+C and
// does neither.
func TestExitRunsBeforeTheForwardedCancel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		sig  syscall.Signal
		exit bool
	}{{syscall.SIGHUP, true}, {syscall.SIGTERM, true}, {syscall.SIGINT, false}} {
		var order []string
		in := newInterrupts(func() { order = append(order, "home") })
		in.exit = func() { order = append(order, "exit") }
		in.forwardTo(newInterrupts(func() { order = append(order, "run") }))
		in.signalled(c.sig)
		want := "run"
		if c.exit {
			want = "exit run home"
		}
		if got := strings.Join(order, " "); got != want {
			t.Errorf("%v: %q, want %q", c.sig, got, want)
		}
	}
}
