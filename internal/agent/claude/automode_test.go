package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// A Chat implementer launches in claude's auto mode, under its own
// profile hash; the one-shot profile keeps "default" and its hash.
func TestAutoModeLaunch(t *testing.T) {
	spec := agent.LaunchSpec{Role: "implementer", Workspace: t.TempDir()}
	one, _ := profileFor("implementer")
	args, err := one.args(spec, "id", nil)
	if err != nil || !strings.Contains(strings.Join(args, " "), "--permission-mode default") {
		t.Fatalf("one-shot args %v, %v", args, err)
	}
	auto := one
	if auto.mode, err = permissionMode(agent.AutoPermissions); err != nil {
		t.Fatal(err)
	}
	args, _ = auto.args(spec, "id", nil)
	if !strings.Contains(strings.Join(args, " "), "--permission-mode auto") || !strings.Contains(strings.Join(args, " "), "--no-session-persistence") {
		t.Errorf("auto args %v", args)
	}
	if auto.hash() == one.hash() {
		t.Error("auto mode has the one-shot profile's hash")
	}
	if w, fatal := (envelope{mode: "auto"}).check(initFrame{PermissionMode: "default"}, false); !strings.Contains(fatal, "permission mode") {
		t.Errorf("an auto Session reporting default mode: %v %q", w, fatal)
	}
}

// Only the implementer may run in auto mode (D1).
func TestAutoModeImplementerOnly(t *testing.T) {
	_, err := (&Adapter{Path: "no-such-claude"}).Open(context.Background(),
		agent.LaunchSpec{Role: "verifier", Permissions: agent.AutoPermissions})
	if err == nil || !strings.Contains(err.Error(), "verifier can't run in auto mode") {
		t.Errorf("verifier in auto mode: %v", err)
	}
}

// In a Chat, a Bash command auto mode would otherwise decide is hard-denied
// when it names anything outside the Workspace (D2).
func TestChatBashEscapes(t *testing.T) {
	ws := t.TempDir()
	p := newPolicy(agent.LaunchSpec{Role: "implementer", Workspace: ws, Permissions: agent.AutoPermissions,
		DenyRead: []string{filepath.Join(t.TempDir(), "private")}}, "h")
	tool := t.TempDir()
	p.readOK = withReal(tool)
	decide := func(cmd string) agent.HostDecision {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return p.decide("Bash", in)
	}
	for _, cmd := range []string{
		"git -C /other log",
		"sh -c 'cat /etc/hosts'",
		`python3 -c "print(open('/etc/hosts').read())"`,
		"cd / && head etc/hosts",
		"cat ~/notes.txt",
		"cat $HOME/notes.txt",
		"head ../../outside.txt",
		`bash -c "$CMD"`,
		"eval ls",
		"cat < /etc/hosts",
		"make -f /etc/Makefile",
	} {
		if d := decide(cmd); d.Decision != "deny" || d.Rule != ruleOutside || !strings.Contains(d.Reason, "Chat") {
			t.Errorf("%s: %+v", cmd, d)
		}
	}
	grey := []string{"make deploy", "python3 -c 'print(1)'", "cd sub && make", "npm run build", "cat /dev/null"}
	if runtime.GOOS != "windows" {
		grey = append(grey, "ls "+filepath.Join(tool, "src"))
	}
	for _, cmd := range grey {
		if d := decide(cmd); d.Rule != ruleNoInteractive {
			t.Errorf("%s: %+v, want it left to auto mode", cmd, d)
		}
	}
	// A question goes to the human in a Chat.
	if d := p.decide("AskUserQuestion", json.RawMessage(`{}`)); d.Family != agent.Question || d.Rule != ruleNoInteractive {
		t.Errorf("AskUserQuestion: %+v", d)
	}
}

// openAuto opens a Chat Session on fakeclaude serving sc.
func openAuto(t *testing.T, sc multiturn.Scenario) (agent.Session, string) {
	t.Helper()
	ws := t.TempDir()
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(path); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Path: fakePath, CloseGrace: time.Second,
		Environ: func() []string { return append(os.Environ(), "OGE_FAKE_CLAUDE_SCENARIO="+path) }}
	s, err := a.Open(context.Background(), agent.LaunchSpec{Role: "implementer", Workspace: ws, Permissions: agent.AutoPermissions})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, ws
}

func turnEvents(t *testing.T, s agent.Session, onAsk func(agent.Event)) []agent.Event {
	t.Helper()
	var evs []agent.Event
	limit := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("the Session ended mid-turn: %+v", evs)
			}
			evs = append(evs, ev)
			if ev.Ask != nil && onAsk != nil {
				onAsk(ev)
			}
			if ev.Kind == agent.TurnSettled {
				return evs
			}
		case <-limit:
			t.Fatalf("the turn never settled: %+v", evs)
		}
	}
}

// The PreToolUse hook is the hard-deny point when auto mode would approve
// silently: an outside path is denied, a grey command continues to auto
// mode, and a pre-authorised call is allowed to run.
func TestAutoModeHook(t *testing.T) {
	s, _ := openAuto(t, multiturn.Scenario{Turns: []multiturn.Turn{{Tools: []multiturn.Tool{
		{Name: "Bash", Target: "cat /etc/hosts"}, {Name: "Bash", Target: "make deploy"}, {Name: "Read", Target: "x.go"},
	}}}})
	if err := s.Send(agent.Turn{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	evs := turnEvents(t, s, nil)
	var said []string
	var hosts []string
	for _, ev := range evs {
		if ev.Kind == agent.Claim && strings.HasPrefix(ev.Text, "hook ") {
			said = append(said, ev.Text)
		}
		if ev.Kind == agent.HostRequest {
			hosts = append(hosts, ev.Host.Tool+":"+ev.Host.Decision+":"+ev.Host.Rule)
		}
	}
	if want := "hook Bash: deny|hook Bash: continue|hook Read: continue"; strings.Join(said, "|") != want {
		t.Errorf("hook answers %q, want %q", said, want)
	}
	// A pre-authorised call and a grey one both continue at the hook:
	// only the hard deny is a Host decision.
	if want := "Bash:deny:outside_role"; strings.Join(hosts, "|") != want {
		t.Errorf("Host requests %q, want %q", hosts, want)
	}
}

// An interrupt withdraws a pending Ask: claude cancels it, and a late
// answer can't land in the next turn.
func TestAutoModeAskWithdrawn(t *testing.T) {
	s, ws := openAuto(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Ask: &multiturn.Ask{Tool: "Bash", Target: "make deploy"}, Writes: []multiturn.File{{Path: "a.txt", Content: "a"}}},
		{Reply: "next"},
	}})
	if err := s.Send(agent.Turn{Text: "deploy"}); err != nil {
		t.Fatal(err)
	}
	var ask *agent.Ask
	evs := turnEvents(t, s, func(ev agent.Event) {
		ask = ev.Ask
		if err := s.Interrupt(); err != nil {
			t.Error(err)
		}
	})
	if ask == nil || evs[len(evs)-1].Failure != "interrupted" {
		t.Fatalf("the asked turn: %+v", evs)
	}
	var withdrawn bool
	for _, ev := range evs {
		withdrawn = withdrawn || (ev.Host != nil && ev.Host.Rule == ruleWithdrawn)
	}
	if !withdrawn {
		t.Errorf("no withdrawal in %+v", evs)
	}
	if err := ask.Reply(true); !errors.Is(err, ErrAskClosed) {
		t.Errorf("a late Reply: %v", err)
	}
	if err := s.Send(agent.Turn{Text: "next"}); err != nil {
		t.Fatal(err)
	}
	if evs := turnEvents(t, s, nil); evs[len(evs)-1].Failure != "" {
		t.Errorf("the next turn: %+v", evs)
	}
	if _, err := os.Stat(filepath.Join(ws, "a.txt")); err == nil {
		t.Error("the withdrawn ask's write happened")
	}
}
