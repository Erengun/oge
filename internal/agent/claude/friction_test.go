package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

// step is one model turn of a synthetic session: an assistant message
// with that id, using a tool (or only saying something when tool is
// empty), and the decision the host must give it.
type step struct {
	msg, tool, input, decision string
}

func bashStep(msg, cmd, decision string) step {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return step{msg, "Bash", string(b), decision}
}

// synthetic builds a one-turn session that passes the envelope, takes
// the steps, and ends with Exit: done.
func syntheticSession(steps ...step) []string {
	frames := []string{
		`{"dir": "meta", "argv": ["claude"]}`,
		`{"dir": "in", "msg": {"type": "control_request", "request_id": "req_1", "request": {"subtype": "initialize"}}}`,
		`{"dir": "out", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "req_1", "response": {"account": "<redacted>"}}}}`,
		`{"dir": "in", "msg": {"type": "user"}}`,
		`{"dir": "out", "msg": {"type": "system", "subtype": "init", "cwd": "/home/user/project", "session_id": "s-1", "tools": ["Bash", "Edit", "Glob", "Grep", "Read", "Write"], "mcp_servers": [], "model": "claude-haiku-4-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.289", "output_style": "default", "plugins": [], "skills": [], "capabilities": ["interrupt_receipt_v1"]}}`,
	}
	for i, s := range steps {
		if s.tool == "" {
			frames = append(frames, fmt.Sprintf(`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"id": %q, "content": [{"type": "text", "text": "thinking it over"}]}}}`, s.msg))
			continue
		}
		use := fmt.Sprintf("toolu_%d", i)
		frames = append(frames,
			fmt.Sprintf(`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"id": %q, "content": [{"type": "tool_use", "id": %q, "name": %q, "input": %s}]}}}`, s.msg, use, s.tool, s.input),
			fmt.Sprintf(`{"dir": "out", "msg": {"type": "control_request", "request_id": "h%d", "request": {"subtype": "hook_callback", "callback_id": "oge_pre_tool_use", "input": {"hook_event_name": "PreToolUse", "tool_name": %q, "tool_input": %s, "tool_use_id": %q}}}}`, i, s.tool, s.input, use),
			fmt.Sprintf(`{"dir": "in", "expect": %q, "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "h%d"}}}`, s.decision, i),
		)
	}
	return append(frames,
		`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"id": "msg_last", "content": [{"type": "text", "text": "Done.\nExit: done"}]}}}`,
		`{"dir": "out", "msg": {"type": "result", "subtype": "success", "is_error": false, "result": "Done.\nExit: done", "num_turns": 2}}`,
		`{"dir": "meta", "exit": 0}`,
	)
}

func hosts(evs []agent.Event) []agent.HostDecision {
	var out []agent.HostDecision
	for _, e := range evs {
		if e.Kind == agent.HostRequest {
			out = append(out, *e.Host)
		}
	}
	return out
}

// A denied common idiom says, in one line, what to do instead (#90). The
// decision itself doesn't change.
func TestRecoveryHints(t *testing.T) {
	const tail = "Use the Grep tool."
	for _, c := range []struct{ cmd, hint string }{
		{`find . -name "*.go" | grep Add`, tail},
		{`find . -name "*.go" | xargs grep -l Add`, tail},
		{`find . -type f | grep -v vendor | head -20`, tail},
		{`find . -type f | head -20`, "Use the Glob tool."},
		{`find src -name "*.go" | wc -l`, "Use the Glob tool."},
		{`find . -name "*.go" | sort`, "Use the Glob tool."},
		{`cd /home/user/project && go test ./...`, "Run go test ./... directly; the working directory is already the Workspace."},
		{`cd "/home/user/project" && go test -run TestAdd ./... 2>&1 | tail -30`, "Run go test -run TestAdd ./... 2>&1 | tail -30 directly; the working directory is already the Workspace."},
		{`cd /home/user/project/src && ls`, "Run ls directly; the working directory is already the Workspace."},
	} {
		h := open(t, syntheticSession(bashStep("m1", c.cmd, "deny")))
		evs := h.turn("go")
		if s := settled(t, evs); s.Exit != "done" {
			t.Fatalf("%s: settled %+v (the fake rejects a wrong answer)", c.cmd, s)
		}
		d := hosts(evs)[0]
		want := "Öge denied this: this command isn't pre-authorised. " + c.hint
		if d.Decision != "deny" || d.Rule != ruleNoInteractive || d.Reason != want {
			t.Errorf("%s:\n got %s %s %q\nwant deny %s %q", c.cmd, d.Decision, d.Rule, d.Reason, ruleNoInteractive, want)
		}
		h.sess.Close()
		esc, _ := json.Marshal(c.hint)
		if a := answers(t, h)["h0"]; !strings.Contains(a, strings.Trim(string(esc), `"`)) {
			t.Errorf("%s: claude was told %s", c.cmd, a)
		}
	}
}

// Every other denial keeps its reason, and an idiom whose remedy would
// still be denied gets no hint.
func TestNoHintElsewhere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the policy reasons about POSIX paths")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "ws")
	os.MkdirAll(filepath.Join(ws, "src"), 0o755)
	p := newPolicy(agent.LaunchSpec{Workspace: ws}, "abc")
	grey := "Öge denied this: this command isn't pre-authorised." + denyTail
	outside := "Öge denied this: the command reaches outside the Workspace." + denyTail
	for cmd, want := range map[string]string{
		"curl https://example.com":           grey,
		"ls | grep x":                        grey,
		"find . -name x | sh":                grey,
		"find . -name x | grep y; rm -rf .":  grey,
		"find . -name x || grep y z":         grey,
		"find . -name x && grep y z":         grey,
		"cd /tmp && ls":                      grey,
		"cd " + dir + " && ls":               grey,
		"cd " + ws + " && cat /etc/passwd":   grey, // its remedy reaches outside
		"cd " + ws + " && go test\nrm -rf .": grey,
		"cd " + ws + "; go test":             grey,
		"cat /etc/passwd":                    outside,
	} {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		if d := p.decide("Bash", b); d.Decision != "deny" || d.Reason != want {
			t.Errorf("%q: %s %q", cmd, d.Decision, d.Reason)
		}
	}
}

// Policy friction (#90): denied requests, and the model turns from a
// denial up to and including the next tool use that is allowed or that
// differs from the denied one.
func TestPolicyFriction(t *testing.T) {
	cd := "cd /home/user/project && go test ./..."
	text := func(msg string) step { return step{msg: msg} }
	for _, c := range []struct {
		name  string
		steps []step
		want  agent.Friction
	}{
		{"none", []step{bashStep("m1", "go test ./...", "allow")}, agent.Friction{}},
		{"recovered at once", []step{bashStep("m1", cd, "deny"), bashStep("m2", "go test ./...", "allow")}, agent.Friction{Denied: 1, RecoveryTurns: 1}},
		{"identical retry", []step{bashStep("m1", "curl x", "deny"), bashStep("m2", "curl x", "deny"), bashStep("m3", "ls", "allow")}, agent.Friction{Denied: 2, RecoveryTurns: 2}},
		{"a turn spent talking", []step{bashStep("m1", "curl x", "deny"), text("m2"), bashStep("m3", "ls", "allow")}, agent.Friction{Denied: 1, RecoveryTurns: 2}},
		{"a different denied use", []step{bashStep("m1", "curl x", "deny"), bashStep("m2", "curl y", "deny"), bashStep("m3", "ls", "allow")}, agent.Friction{Denied: 2, RecoveryTurns: 2}},
		// The final answer is a turn after the denial, with no tool use.
		{"gave up", []step{bashStep("m1", "curl x", "deny")}, agent.Friction{Denied: 1, RecoveryTurns: 1}},
		// Frames of one message share its id: one model turn.
		{"one message, many frames", []step{bashStep("m1", "curl x", "deny"), text("m2"), bashStep("m2", "ls", "allow")}, agent.Friction{Denied: 1, RecoveryTurns: 1}},
	} {
		h := open(t, syntheticSession(c.steps...))
		s := settled(t, h.turn("go"))
		if s.Exit != "done" {
			t.Fatalf("%s: settled %+v", c.name, s)
		}
		if s.Friction == nil || *s.Friction != c.want {
			t.Errorf("%s: friction %+v, want %+v", c.name, s.Friction, c.want)
		}
		h.sess.Close()
	}
}
