//go:build unix

package claude

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// Chat is Unix-only (ADR-0017): these name Unix paths.

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
	grey := []string{"make deploy", "python3 -c 'print(1)'", "cd sub && make", "npm run build", "cat /dev/null",
		"ls " + filepath.Join(tool, "src")}
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

// The Chat sandbox denies reading Öge's state roots wherever they are,
// not only under $HOME, and reopens only the Chat's own Workspace.
func TestChatSandboxDeniesStateRoots(t *testing.T) {
	home, state := t.TempDir(), t.TempDir() // the state dir outside $HOME
	ws := filepath.Join(state, "work", "chats", "c1", "implement")
	spec := agent.LaunchSpec{Role: "implementer", Workspace: ws, Permissions: agent.AutoPermissions,
		StateRoots: []string{state, filepath.Join(state, "private"), filepath.Join(state, "work")}}
	p, _ := profileFor("implementer")
	p.mode = "auto"
	js, err := p.settings(spec, []string{"HOME=" + home})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Sandbox struct {
			Filesystem struct {
				DenyRead, AllowRead []string
			}
		}
	}
	if err := json.Unmarshal([]byte(js), &got); err != nil {
		t.Fatal(err)
	}
	fs := got.Sandbox.Filesystem
	for _, want := range []string{home, state, filepath.Join(state, "work")} {
		if !under(want, fs.DenyRead) && !under(resolve(want), fs.DenyRead) {
			t.Errorf("denyRead %v lacks %s", fs.DenyRead, want)
		}
	}
	if !oneOf(ws, fs.AllowRead) {
		t.Errorf("allowRead %v lacks the Workspace", fs.AllowRead)
	}
	if strings.Contains(strings.Join(fs.AllowRead, " "), filepath.Join(state, "private")) {
		t.Errorf("allowRead %v reopens private state", fs.AllowRead)
	}
}
