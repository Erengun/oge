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
	s.expect("Applied")
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); !strings.Contains(string(b), "a + b") {
		t.Errorf("/apply didn't deliver the cut:\n%s", b)
	}
	s.quitChat(f)
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

// Esc interrupts the turn in flight; a message typed during it is sent
// next, and the composer comes back.
func TestHomeChatEscInterrupts(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "working on it", Hold: true},
		{Reply: "next turn"},
	}}, nil)
	s.composer()
	s.type_("long job\r")
	s.expect("working on it")
	s.type_("go on\r") // typed ahead: sent once the turn has ended
	time.Sleep(100 * time.Millisecond)
	s.type_("\x1b")
	s.expect("interrupted")
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
