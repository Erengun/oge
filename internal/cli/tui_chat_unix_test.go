//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// startChatSession is a bare oge whose implementer is claude, played by
// fakeclaude serving sc: the home talks to a Chat (#259). setup changes
// the fixture first.
func startChatSession(t *testing.T, sc multiturn.Scenario, setup func(*runFixture)) (*homeSession, *runFixture) {
	t.Helper()
	f := newRunFixture(t)
	if setup != nil {
		setup(f)
	}
	scen := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(scen); err != nil {
		t.Fatal(err)
	}
	ad := &claude.Adapter{Path: multiturn.BuildClaude(t), CloseGrace: time.Second,
		Environ: func() []string { return append(os.Environ(), "OGE_FAKE_CLAUDE_SCENARIO="+scen) }}
	s := startHomeSessionWith(t, f, func(e *Env) { e.Agents = map[string]agent.Adapter{"claude": ad} }, "--fast", "--agent", "claude")
	return s, f
}

// quitChat ends the session on an empty composer: exit 0 (D8), and the
// Chat is closed: its Workspace is gone, its log stays, and its agent
// process has exited.
func (s *homeSession) quitChat(f *runFixture) {
	s.t.Helper()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		s.t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if left, _ := filepath.Glob(filepath.Join(f.state, "work", "chats", "*")); len(left) > 0 {
		s.t.Errorf("the Chat's Workspace outlived the session: %v", left)
	}
	logs, _ := filepath.Glob(filepath.Join(f.state, "private", "chats", "*"))
	if len(logs) == 0 {
		s.t.Fatal("no Chat log: the session never opened a Chat")
	}
	for _, dir := range logs {
		recs, err := ledger.Replay(dir)
		if err != nil {
			s.t.Fatal(err)
		}
		for _, r := range recs {
			if r.Type != run.RecProcessStarted {
				continue
			}
			var p struct{ PID int }
			if err := json.Unmarshal(r.Data, &p); err != nil || p.PID <= 0 {
				s.t.Fatalf("ProcessStarted %s: %v", r.Data, err)
			}
			if syscall.Kill(p.PID, 0) == nil {
				s.t.Errorf("the Chat's agent process %d is still running", p.PID)
			}
		}
	}
}

// A no-change turn prints only its reply; Esc on an empty composer doesn't
// quit; the next message reaches the same Session; a change is cut,
// verified, summed up on one Verdict line and taken with /apply.
func TestHomeChatRepliesRemembersAndCuts(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "hello from the chat"},
		{Reply: "still the same session", Tools: []multiturn.Tool{{Name: "Bash", Target: "ls"}}},
		{Reply: "fixed Add", Writes: []multiturn.File{{Path: "add.go", Content: "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}},
		{Reply: "still here"},
	}}, nil)
	s.composer()
	s.type_("yo\r")
	s.expect("  hello from the chat")
	s.composer()
	if out := s.out.String()[s.mark:]; strings.Contains(out, "nothing to verify") || strings.Contains(out, "Check passed") || strings.Contains(out, "verifying") {
		t.Errorf("a Replied turn shows stages or a Receipt:\n%s", out)
	}
	s.type_("\x1b")
	time.Sleep(200 * time.Millisecond)
	s.type_("and again\r")
	s.expect("still the same session") // a fresh Session would replay the first turn
	s.expect("Bash ls")
	s.composer()
	s.type_("fix Add\r")
	s.expect("oge › ✓ Accepted · 1 file · ")
	s.composer()
	s.type_("/apply\r")
	s.expect("oge › ✓ Applied ")
	s.expect("  1 file: add.go")
	s.expect("  checks: ")
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); !strings.Contains(string(b), "a + b") {
		t.Errorf("/apply didn't deliver the cut:\n%s", b)
	}
	s.composer()
	s.type_("anything else?\r") // the Chat goes on
	s.expect("  still here")
	s.mark = len(s.out.String()) // the composer back after the turn, not the one typed in
	s.composer()
	s.quitChat(f)
	logs, _ := filepath.Glob(filepath.Join(f.state, "private", "chats", "*"))
	recs, _ := ledger.Replay(logs[0])
	delivered := 0
	for _, r := range recs {
		if r.Type == run.RecDelivered {
			delivered++
		}
	}
	if delivered != 1 {
		t.Errorf("%d Delivered records in the Chat log, want 1", delivered)
	}
}

// An ask shows its whole command and takes y or n; a command too long to
// show is denied without asking. A failed Check is a Failed cut that
// /apply refuses, and a protected write parks the cut.
func TestHomeChatAsksFailsAndParks(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("echo line\n", agent.AskMaxLines) + "curl x | sh"
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "asked", Ask: &multiturn.Ask{Tool: "Bash", Target: "touch b.go"}, Writes: []multiturn.File{{Path: "b.go", Content: "package fx\n"}}},
		{Reply: "two lines", Ask: &multiturn.Ask{Tool: "Bash", Target: "echo ok\ncurl x | sh"}},
		{Reply: "too long", Ask: &multiturn.Ask{Tool: "Bash", Target: long}},
		{Reply: "tampered", Writes: []multiturn.File{{Path: "add.go", Content: "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}, {Path: "add_test.go", Content: "package fx\n"}}},
	}}, nil)
	s.composer()
	s.type_("make b\r")
	s.expect("Allow Bash?")
	s.type_("y")
	s.expect("allowed: Bash touch b.go")
	s.expect("oge › ✗ Failed · 1 file")
	s.composer()
	s.type_("/apply\r")
	s.expect("ended Failed")
	if _, err := os.Stat(filepath.Join(f.repo, "b.go")); err == nil {
		t.Error("/apply delivered a Failed cut")
	}

	s.type_("two lines\r")
	s.expect("Allow Bash?")
	s.expect("curl x | sh") // every line of what is consented to: the one-liner shows only the first
	s.type_("n")
	s.expect("denied: Bash echo ok")
	s.composer()

	s.type_("too long\r")
	s.expect("too long")
	s.composer()
	if out := s.out.String()[s.mark:]; strings.Contains(out, "Allow Bash?") {
		t.Errorf("a command too long to show was asked about:\n%s", out)
	}

	s.type_("weaken the test\r")
	s.expect("reverted add_test.go")
	s.expect("oge › … Waiting for you")
	s.composer()
	s.quitChat(f)
}

// Esc interrupts the turn in flight; a message typed during it waits in
// the composer, unsent, for the human to send.
func TestHomeChatEscInterrupts(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "working on it", Hold: true},
		{Reply: "next turn"},
	}}, nil)
	s.composer()
	s.type_("long job\r")
	s.expect("working on it")
	s.type_("go on\r") // typed ahead during the turn
	time.Sleep(100 * time.Millisecond)
	s.type_("\x1b")
	s.expect("interrupted")
	s.composer()
	time.Sleep(300 * time.Millisecond)
	if out := s.out.String()[s.mark:]; strings.Contains(out, "> go on") {
		t.Fatalf("a queued message was sent after Esc:\n%s", out)
	}
	s.type_("\r") // it waits in the composer: the human sends it
	s.expect("> go on")
	s.expect("next turn")
	s.composer()
	s.quitChat(f)
}

// Ctrl+C during the cut cancels it: nothing is Accepted, and the composer
// comes back.
func TestHomeChatCtrlCCancelsTheCut(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "fixed Add", Writes: []multiturn.File{{Path: "add.go", Content: "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}},
	}}, func(f *runFixture) {
		// A Check that holds: the cut waits on it.
		writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte("schema = 1\n[project]\ntest_globs = [\"**/*_test.go\"]\n[[check.commands]]\nrun = \"sleep 30\"\n"))
	})
	s.composer()
	s.type_("fix Add\r")
	s.expect("verifying…")
	s.type_("\x03")
	s.expect("verification cancelled")
	s.composer()
	if out := s.out.String(); strings.Contains(out, "Accepted") {
		t.Errorf("a cancelled cut was Accepted:\n%s", out)
	}
	s.quitChat(f)
}

// OGE_CHAT=0 runs each home message as a one-shot Run, even on claude.
func TestHomeChatEscapeHatch(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "a one-shot reply"}}},
		func(f *runFixture) { f.setenv(run.ChatFlag, "0") })
	s.composer()
	s.type_("yo\r")
	s.expect("ÖGE RECEIPT") // a one-shot Run's, never a Chat turn's
	s.composer()
	s.type_("\x03")
	s.wait() // the one-shot path keeps its last Run's code; fakeclaude's envelope stops it
	if logs, _ := filepath.Glob(filepath.Join(f.state, "private", "chats", "*")); len(logs) > 0 {
		t.Errorf("OGE_CHAT=0 opened a Chat: %v", logs)
	}
	f.onlyRun(t)
}

// AskUserQuestion shows the question and its options, and takes an
// option's number or typed text as the answer.
func TestHomeChatAnswersAQuestion(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first", Ask: &multiturn.Ask{Tool: "AskUserQuestion", Target: "Ship it?"}},
		{Reply: "second", Ask: &multiturn.Ask{Tool: "AskUserQuestion", Target: "Which name?"}},
	}}, nil)
	s.composer()
	s.type_("ask me\r")
	s.expect("Ship it?")
	s.expect("2. no")
	s.type_("2\r")
	s.expect("answered: no")
	s.composer()
	s.type_("again\r")
	s.expect("Which name?")
	s.type_("Ada\r")
	s.expect("answered: Ada")
	s.composer()
	s.quitChat(f)
}

// An ask shows hidden characters as escapes, never dropped.
func TestHomeChatAskShowsHiddenCharacters(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "tab", Ask: &multiturn.Ask{Tool: "Bash", Target: "rm\t-rf x"}},
		{Reply: "bidi", Ask: &multiturn.Ask{Tool: "Bash", Target: "echo a\u202eb"}},
	}}, nil)
	s.composer()
	s.type_("one\r")
	s.expect(`rm\t-rf x`)
	s.type_("n")
	s.composer()
	s.type_("two\r")
	s.expect(`echo a\u{202e}b`)
	s.type_("n")
	s.composer()
	s.quitChat(f)
}

// The cut verifies in the background: while its Check holds, the next
// turn is sent and replies, and its cut supersedes the held one, which
// shows no Verdict; the newer Verdict covers both turns. /apply while a
// newer cut verifies takes the last verified one and says so; Ctrl+C with
// no turn running cancels the verification, and the next one quits.
func TestHomeChatVerifiesInTheBackground(t *testing.T) {
	t.Parallel()
	mark := filepath.Join(t.TempDir(), "checking")
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "fixed Add", Writes: []multiturn.File{{Path: "add.go", Content: fixed}, {Path: "hold.go", Content: "package fx\n"}}},
		{Reply: "and more", Writes: []multiturn.File{{Path: "more.go", Content: "package fx\n"}}},
		{Reply: "waiting", Writes: []multiturn.File{{Path: "wait.go", Content: "package fx\n"}}},
	}}, func(f *runFixture) {
		// The first Check on hold.go holds, and every Check on wait.go.
		hold := `if [ -f hold.go ] && [ ! -f "` + mark + `" ]; then touch "` + mark + `"; while :; do sleep 0.1; done; fi; ` +
			`if [ -f wait.go ]; then while :; do sleep 0.1; done; fi; go test -json ./...`
		writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte("schema = 1\n[project]\ntest_globs = [\"**/*_test.go\"]\noutput_globs = [\"**/*.go\"]\n[[check.commands]]\nrun = '"+hold+"'\nreport = \"go-test-json\"\n"))
	})
	s.composer()
	s.type_("fix Add\r")
	s.expect("verifying…")
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		if time.Since(start) > time.Minute {
			t.Fatal("the first cut's Check never held")
		}
	}
	s.composer()
	s.type_("and more\r")
	s.expect("  and more") // replied while the first cut's Check holds
	s.expect("oge › ✓ Accepted · 3 files · ")
	s.composer()
	if out := s.out.String(); strings.Count(out, "Accepted") != 1 || strings.Contains(out, "cancelled") {
		t.Errorf("the superseded cut showed something:\n%s", out)
	}
	s.type_("wait\r")
	s.expect("  waiting")
	s.expect("verifying…")
	s.composer()
	s.type_("/apply\r")
	s.expect("newer edits, unchecked: [a] apply verified  [w] wait")
	s.type_("w")
	s.expect("waiting: nothing delivered")
	s.composer()
	if _, err := os.Stat(filepath.Join(f.repo, "more.go")); err == nil {
		t.Error("[w] applied something")
	}
	s.type_("/apply\r")
	s.expect("newer edits, unchecked: [a] apply verified  [w] wait")
	s.type_("a")
	s.expect("a newer cut is still verifying")
	s.expect("Applied")
	s.composer()
	s.type_("\x1b[B") // the action row says so too
	time.Sleep(actionsArm + 100*time.Millisecond)
	s.type_("r")
	s.expect("a newer cut is still verifying")
	if _, err := os.Stat(filepath.Join(f.repo, "more.go")); err != nil {
		t.Errorf("/apply didn't take the last verified cut: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "wait.go")); err == nil {
		t.Error("/apply took the cut still verifying")
	}
	s.type_("\x03")
	s.expect("verification cancelled")
	s.composer()
	s.quitChat(f)
}

// heldChatSession is a Chat whose Check on hold.go holds until it is
// killed, noting its shell's pid in the file it returns.
func heldChatSession(t *testing.T) (*homeSession, *runFixture, string) {
	t.Helper()
	pids := filepath.Join(t.TempDir(), "pids")
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "held", Writes: []multiturn.File{{Path: "hold.go", Content: "package fx\n"}}},
	}}, func(f *runFixture) {
		hold := `if [ -f hold.go ]; then echo $$ >> "` + pids + `"; while :; do sleep 0.1; done; fi; go test -json ./...`
		writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte("schema = 1\n[project]\ntest_globs = [\"**/*_test.go\"]\noutput_globs = [\"**/*.go\"]\n[[check.commands]]\nrun = '"+hold+"'\nreport = \"go-test-json\"\n"))
	})
	s.composer()
	s.type_("hold\r")
	s.expect("verifying…")
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(pids); len(b) > 0 {
			break
		}
		if time.Since(start) > time.Minute {
			t.Fatal("the Check never held")
		}
	}
	return s, f, pids
}

// checkGone fails unless every Check shell noted in pids has exited.
func checkGone(t *testing.T, pids string) {
	t.Helper()
	b, _ := os.ReadFile(pids)
	for _, l := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(l)
		if err != nil {
			t.Fatal(err)
		}
		for start := time.Now(); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
			if time.Since(start) > 30*time.Second {
				t.Errorf("the Check process %d is still running", pid)
				break
			}
		}
	}
}

// /new during a held Check doesn't hold the home up: the composer comes
// back while the old Chat closes, and its Check is killed.
func TestHomeChatNewDuringVerify(t *testing.T) {
	t.Parallel()
	s, f, pids := heldChatSession(t)
	s.type_("/new\r")
	s.composer()
	checkGone(t, pids)
	if out := s.out.String()[s.mark:]; strings.Contains(out, "Accepted") || strings.Contains(out, "cancelled") {
		t.Errorf("the old Chat's cut showed:\n%s", out)
	}
	s.quitChat(f)
}

// Quitting while a cut verifies leaves no Check process behind.
func TestHomeChatQuitDuringVerify(t *testing.T) {
	t.Parallel()
	s, f, pids := heldChatSession(t)
	s.type_("\x03") // cancels the verification; the next quits at once
	s.quitChat(f)
	checkGone(t, pids)
}

// A Verdict shows its diff inline when it is 20 lines or fewer, dim and
// indented; a longer one only behind /diff.
func TestHomeChatInlineDiff(t *testing.T) {
	t.Parallel()
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	var long strings.Builder
	long.WriteString(fixed)
	for i := range 25 {
		long.WriteString("// long line " + strconv.Itoa(i) + "\n")
	}
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "fixed Add", Writes: []multiturn.File{{Path: "add.go", Content: fixed}}},
		{Reply: "and long", Writes: []multiturn.File{{Path: "add.go", Content: long.String()}}},
	}}, nil)
	s.composer()
	s.type_("fix Add\r")
	s.expect("oge › ✓ Accepted · 1 file · ")
	s.expect("    +func Add(a, b int) int { return a + b }")
	s.composer()
	s.type_("and long\r")
	s.expect("oge › ✓ Accepted · 1 file · ")
	s.composer()
	if out := s.out.String(); strings.Contains(out, "long line 3") {
		t.Errorf("a diff over 20 lines showed inline:\n%s", out)
	}
	s.quitChat(f)
}

// A protected write's Tamper Gate never shows mid-turn: the cut waits
// for you, and /apply opens its Gate. Rejecting there applies nothing;
// the next cut, still gated by that Tamper event, is acknowledged at
// /apply and applied.
func TestHomeChatTamperGateAtApply(t *testing.T) {
	t.Parallel()
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "tampered", Writes: []multiturn.File{{Path: "add.go", Content: fixed}, {Path: "add_test.go", Content: "package fx\n"}}},
		{Reply: "again", Writes: []multiturn.File{{Path: "add.go", Content: fixed + "\n// again\n"}}},
	}}, nil)
	s.composer()
	s.type_("weaken the test\r")
	s.expect("reverted add_test.go")
	s.expect("oge › … Waiting for you: the tamper Gate · /apply")
	s.composer()
	s.type_("/apply\r")
	s.expect("acknowledge")
	s.type_("reject the tests stay\r")
	s.expect("oge › ✗ Rejected")
	s.composer()
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); strings.Contains(string(b), "a + b") {
		t.Fatal("a rejected Tamper Gate applied the cut")
	}
	s.type_("once more\r")
	s.expect("oge › … Waiting for you: the tamper Gate · /apply")
	s.composer()
	s.type_("/apply\r")
	s.expect("acknowledge")
	s.type_("acknowledge the revert is right\r")
	s.expect("oge › ✓ Accepted")
	s.expect("oge › ✓ Applied ")
	s.composer()
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); !strings.Contains(string(b), "// again") {
		t.Errorf("the acknowledged cut wasn't applied:\n%s", b)
	}
	s.quitChat(f)
}

// An Ambiguous file's Gate shows at /apply: promoting every file keeps
// the Verdict and applies it; dropping one re-cuts without it, which is
// checked again before it is applied.
func TestHomeChatAmbiguousGateAtApply(t *testing.T) {
	t.Parallel()
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	for _, c := range []struct {
		name, choice string
		kept         bool
	}{{"keep", "p", true}, {"drop", "d", false}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
				{Reply: "and notes", Writes: []multiturn.File{{Path: "add.go", Content: fixed}, {Path: "extra.go", Content: "package fx\n"}}},
			}}, nil)
			s.composer()
			s.type_("fix Add\r")
			s.expect("oge › … Waiting for you: the ")
			s.composer()
			s.type_("/apply\r")
			s.expect("extra.go")
			s.type_(c.choice + "\r")
			s.expect("oge › ✓ Accepted")
			s.expect("oge › ✓ Applied ")
			s.composer()
			if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); !strings.Contains(string(b), "a + b") {
				t.Errorf("the cut wasn't applied:\n%s", b)
			}
			if _, err := os.Stat(filepath.Join(f.repo, "extra.go")); (err == nil) != c.kept {
				t.Errorf("extra.go applied: %v, want %v", err == nil, c.kept)
			}
			s.quitChat(f)
		})
	}
}

// A second /apply merges on the cut last applied, not the Snapshot: a
// later cut undoing part of the first one's edit lands exactly.
func TestHomeChatSecondApplyMergesOnTheLast(t *testing.T) {
	t.Parallel()
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "fixed and noted", Writes: []multiturn.File{{Path: "add.go", Content: fixed + "\n// a note\n"}}},
		{Reply: "note gone", Writes: []multiturn.File{{Path: "add.go", Content: fixed}}},
	}}, nil)
	s.composer()
	s.type_("fix Add\r")
	s.expect("oge › ✓ Accepted")
	s.composer()
	s.type_("/apply\r")
	s.expect("oge › ✓ Applied ")
	s.composer()
	s.type_("drop the note\r")
	s.expect("oge › ✓ Accepted")
	s.composer()
	s.type_("/apply\r")
	s.expect("oge › ✓ Applied ")
	s.expect("  1 file: add.go")
	s.composer()
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); string(b) != fixed {
		t.Errorf("add.go is %q, want the second cut's exactly", b)
	}
	s.quitChat(f)
}

// A slash command typed during a turn never reaches the agent: it waits
// in the composer for the turn to end.
func TestHomeChatSlashWaitsForTheTurn(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "working on it", Hold: true},
	}}, nil)
	s.composer()
	s.type_("long job\r")
	s.expect("working on it")
	s.type_("/apply\r")
	s.expect("/apply: wait for the turn to finish")
	s.type_("\x1b")
	s.expect("interrupted")
	s.composer()
	if out := s.out.String(); strings.Contains(out, "message queued") {
		t.Errorf("/apply was sent as a message:\n%s", out)
	}
	s.type_("\x15") // clear the composer, for quitChat
	s.quitChat(f)
}

// A Gate decided at /apply whose cut a newer turn's cut supersedes while
// it is checked again delivers nothing.
func TestHomeChatSupersededGateDeliversNothing(t *testing.T) {
	t.Parallel()
	mark := filepath.Join(t.TempDir(), "armed")
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "with notes", Writes: []multiturn.File{{Path: "add.go", Content: fixed}, {Path: "notes.txt", Content: "notes\n"}}},
		{Reply: "and more", Writes: []multiturn.File{{Path: "more.go", Content: "package fx\n"}}},
	}}, func(f *runFixture) {
		hold := `if [ -f "` + mark + `" ] && [ ! -f more.go ]; then touch "` + mark + `.held"; while :; do sleep 0.1; done; fi; go test -json ./...`
		writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte("schema = 1\n[project]\ntest_globs = [\"**/*_test.go\"]\noutput_globs = [\"**/*.go\"]\n[[check.commands]]\nrun = '"+hold+"'\nreport = \"go-test-json\"\n"))
	})
	s.composer()
	s.type_("fix Add\r")
	s.expect("oge › … Waiting for you: the ")
	s.composer()
	s.type_("/apply\r")
	s.expect("notes.txt")
	writeFile(t, mark, nil)
	s.type_("d\r")
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(mark + ".held"); err == nil {
			break
		}
		if time.Since(start) > time.Minute {
			t.Fatal("the Recheck never held")
		}
	}
	s.composer()
	s.type_("and more\r")
	s.expect("  and more")
	s.expect("oge › … Waiting for you: the ")
	s.composer()
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); strings.Contains(string(b), "a + b") {
		t.Error("the superseded cut's Gate delivered it")
	}
	if out := s.out.String(); strings.Contains(out, "Applied") {
		t.Errorf("something was applied:\n%s", out)
	}
	s.quitChat(f)
}
