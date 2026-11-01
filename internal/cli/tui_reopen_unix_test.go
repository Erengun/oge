//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/run"
)

// Lazy reopen (docs/ux/away.md, build slice 5), on the fake agent: the
// first oge keeps a Chat, the second restores it.

func fakeChatAdapter(t *testing.T, sc multiturn.Scenario) agent.Adapter {
	t.Helper()
	scen := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(scen); err != nil {
		t.Fatal(err)
	}
	return &claude.Adapter{Path: multiturn.BuildClaude(t), CloseGrace: time.Second,
		Environ: func() []string { return append(os.Environ(), "OGE_FAKE_CLAUDE_SCENARIO="+scen) }}
}

// keptChat runs an oge that sends one message and quits keeping the Chat.
func keptChat(t *testing.T, f *runFixture, ad agent.Adapter) {
	t.Helper()
	s := startHomeSessionWith(t, f, func(e *Env) { e.Agents = map[string]agent.Adapter{"claude": ad} }, "--fast", "--agent", "claude")
	s.composer()
	s.type_("yo\r")
	s.expect("  hello from the chat")
	s.composer()
	s.type_("/quit\r")
	s.expectScreen("Keep 1 Chat for next time? k keep · c close · esc cancel")
	s.type_("k")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}

func quitKeeping(s *homeSession) {
	s.t.Helper()
	s.composer()
	s.type_("/quit\r")
	s.expectScreen("Keep 1 Chat for next time? k keep · c close · esc cancel")
	s.type_("k")
	if code := s.wait(); code != ExitOK {
		s.t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}

// Focus alone starts nothing. A message reopens exactly one Session, with
// the re-brief, and the turn settles; a Workspace that was gone is rebuilt
// first.
func TestRestoredChatReopensOnTheFirstMessage(t *testing.T) {
	t.Parallel()
	ad := fakeChatAdapter(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "hello from the chat"}}})
	f := newRunFixture(t)
	keptChat(t, f, ad)
	if n := len(chatRecords(t, f, run.RecProcessStarted)); n != 1 {
		t.Fatalf("%d ProcessStarted", n)
	}
	// The reboot took the Workspace.
	ws, _ := filepath.Glob(filepath.Join(f.state, "work", "chats", "*", "implement"))
	if len(ws) != 1 {
		t.Fatalf("Workspaces %v", ws)
	}
	if err := os.RemoveAll(ws[0]); err != nil {
		t.Fatal(err)
	}
	s := startHomeSessionWith(t, f, func(e *Env) { e.Agents = map[string]agent.Adapter{"claude": ad} }, "--fast", "--agent", "claude")
	s.composer()
	s.expectScreen("yo")
	if len(chatRecords(t, f, run.RecSessionRestored)) != 0 || len(chatRecords(t, f, run.RecSessionReopened)) != 0 {
		t.Fatal("restoring and focusing started something")
	}
	if _, err := os.Stat(ws[0]); err == nil {
		t.Fatal("focus rebuilt the Workspace: only a message may")
	}
	s.type_("again\r")
	s.expect("  hello from the chat")
	s.expectScreen("rebuilt this Chat's Workspace")
	if _, err := os.Stat(ws[0]); err != nil {
		t.Errorf("the Workspace wasn't rebuilt: %v", err)
	}
	quitKeeping(s)
	if n := len(chatRecords(t, f, run.RecSessionRestored)); n != 1 {
		t.Errorf("%d SessionRestored", n)
	}
	if n := len(chatRecords(t, f, run.RecSessionReopened)); n != 1 {
		t.Errorf("%d Sessions reopened for one message", n)
	}
}

// A changed Launch profile shows the card; Enter alone decides nothing;
// only the typed reopen goes on, and n leaves the Chat read-only.
func TestRestoredChatSettingsChangedNeedsTypedReopen(t *testing.T) {
	t.Parallel()
	ad := fakeChatAdapter(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "hello from the chat"}}})
	f := newRunFixture(t)
	keptChat(t, f, ad)
	s := startHomeSessionWith(t, f, func(e *Env) { e.Agents = map[string]agent.Adapter{"claude": ad} }, "--fast", "--agent", "claude:other-model")
	s.composer()
	s.type_("again\r")
	s.expectScreen("settings changed since this Chat's Session last ran")
	s.composer()
	s.type_("\r") // Enter alone: the screen doesn't change
	if len(chatRecords(t, f, run.RecSessionRestored)) != 0 {
		t.Fatal("Enter alone reopened the Chat")
	}
	s.type_("reopen\r")
	s.expect("  hello from the chat")
	quitKeeping(s)
	r := chatRecords(t, f, run.RecSessionRestored)
	if len(r) != 1 || r[0]["accepted"] != true {
		t.Errorf("SessionRestored %v", r)
	}
}

func TestRestoredChatSettingsChangedNLeavesItReadOnly(t *testing.T) {
	t.Parallel()
	ad := fakeChatAdapter(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "hello from the chat"}}})
	f := newRunFixture(t)
	keptChat(t, f, ad)
	s := startHomeSessionWith(t, f, func(e *Env) { e.Agents = map[string]agent.Adapter{"claude": ad} }, "--fast", "--agent", "claude:other-model")
	s.composer()
	s.type_("again\r")
	s.expectScreen("settings changed since this Chat's Session last ran")
	s.composer()
	s.type_("n\r")
	s.expectScreen("not reopened")
	s.type_("\x03") // clears the draft that came back
	if len(chatRecords(t, f, run.RecSessionRestored)) != 0 || len(chatRecords(t, f, run.RecSessionReopened)) != 0 {
		t.Fatal("n reopened the Chat")
	}
	quitKeeping(s)
}
