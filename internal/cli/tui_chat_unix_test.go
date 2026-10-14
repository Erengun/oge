//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// startChatSession is a bare oge whose implementer is claude, played by
// fakeclaude serving sc: the home talks to a Chat (#259).
func startChatSession(t *testing.T, sc multiturn.Scenario) (*homeSession, *runFixture) {
	t.Helper()
	f := newRunFixture(t)
	scen := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(scen); err != nil {
		t.Fatal(err)
	}
	ad := &claude.Adapter{Path: multiturn.BuildClaude(t), CloseGrace: time.Second,
		Environ: func() []string { return append(os.Environ(), "OGE_FAKE_CLAUDE_SCENARIO="+scen) }}
	s := startHomeSessionWith(t, f, func(e *Env) { e.Agents = map[string]agent.Adapter{"claude": ad} }, "--fast", "--agent", "claude")
	return s, f
}

// quit ends the session on an empty composer: exit 0 (D8), and the Chat
// is closed: its Workspace is gone.
func (s *homeSession) quitChat(f *runFixture) {
	s.t.Helper()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		s.t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	chats, _ := filepath.Glob(filepath.Join(f.state, "*", "work", "chats", "*"))
	more, _ := filepath.Glob(filepath.Join(f.state, "*", "*", "work", "chats", "*"))
	if left := append(chats, more...); len(left) > 0 {
		s.t.Errorf("the Chat's Workspace outlived the session: %v", left)
	}
}

// A no-change turn prints only its reply; the next message reaches the
// same Session; a change is cut, verified, summed up on one Verdict line
// and taken with /apply.
func TestHomeChatRepliesRemembersAndCuts(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "hello from the chat"},
		{Reply: "still the same session", Tools: []multiturn.Tool{{Name: "Bash", Target: "ls"}}},
		{Reply: "fixed Add", Writes: []multiturn.File{{Path: "add.go", Content: "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}},
	}})
	s.composer()
	s.type_("yo\r")
	s.expect("hello from the chat")
	s.composer()
	if out := s.out.String()[s.mark:]; strings.Contains(out, "nothing to verify") || strings.Contains(out, "Check passed") || strings.Contains(out, "verifying") {
		t.Errorf("a Replied turn shows stages or a Receipt:\n%s", out)
	}
	s.type_("and again\r")
	s.expect("still the same session") // a fresh Session would replay the first turn
	s.expect("Bash ls")
	s.composer()
	s.type_("fix Add\r")
	s.expect("✓ Accepted · 1 file changed")
	s.composer()
	s.type_("/apply\r")
	s.expect("Applied")
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); !strings.Contains(string(b), "a + b") {
		t.Errorf("/apply didn't deliver the cut:\n%s", b)
	}
	s.quitChat(f)
}

// An auto-mode ask is a one-line prompt; its answer goes back and the
// turn goes on. A write to a protected path is reverted and the cut parks.
func TestHomeChatAskAndTamper(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "asked", Ask: &multiturn.Ask{Tool: "Bash", Target: "touch b.go"}, Writes: []multiturn.File{{Path: "b.go", Content: "package fx\n"}}},
		{Reply: "tampered", Writes: []multiturn.File{{Path: "add.go", Content: "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}, {Path: "add_test.go", Content: "package fx\n"}}},
	}})
	s.composer()
	s.type_("make b\r")
	s.expect("Allow Bash touch b.go")
	s.type_("n")
	s.expect("denied: Bash touch b.go")
	s.composer()
	if out := s.out.String()[s.mark:]; strings.Contains(out, "verifying") || strings.Contains(out, "Accepted") {
		t.Errorf("a denied write was cut:\n%s", out)
	}
	s.type_("weaken the test\r")
	s.expect("reverted add_test.go")
	s.expect("✗ Parked")
	s.composer()
	s.quitChat(f)
}

// Esc interrupts the turn in flight, and the composer comes back.
func TestHomeChatEscInterrupts(t *testing.T) {
	t.Parallel()
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "working on it", Hold: true},
		{Reply: "next turn"},
	}})
	s.composer()
	s.type_("long job\r")
	s.expect("working on it")
	s.type_("\x1b")
	s.expect("interrupted")
	s.composer()
	s.type_("go on\r")
	s.expect("next turn")
	s.composer()
	s.quitChat(f)
}
