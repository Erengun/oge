package main

import (
	"strings"
	"testing"
)

// A denied "cd <Workspace> && go test" reads apart from the allowed
// "go test" under -v, carries its recovery hint, and the Run records and
// shows its policy friction (#90).
func TestBinaryClaudePolicyFriction(t *testing.T) {
	t.Parallel()
	repo, env := runFixture(t)
	env = withFakeClaude(t, env, frictionSession(true))
	code, out, errOut := runExe(t, testBinary, repo, env, "fix Add", "--fast", "--agent", "claude", "--unattended", "-v")
	if code != 0 || !strings.Contains(out, "ACCEPTED") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"[implement #1 claude] Bash cd . && go test ./...\n",
		"[implement #1 claude] denied: Bash cd . && go test ./... (Run go test ./... directly; the working directory is already the Workspace.)\n",
		"[implement #1 claude] Bash go test ./...\n",
		"[implement #1 claude] allow Bash go test ./... · pre-authorised by Launch profile ",
		"[implement #1 claude] policy friction 1 turn (1 denied)\n",
		"\nfriction   policy friction 1 turn (1 denied)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if ledger := findLedger(t, env); !strings.Contains(ledger, `"policy_friction":{"denied":1,"envelope_refusals":0,"lost_turns":1}`) {
		t.Errorf("the Ledger lacks the policy friction:\n%s", ledger)
	}
}

// With no friction, the summary says nothing about it; -v still does.
func TestBinaryClaudeNoFrictionLine(t *testing.T) {
	t.Parallel()
	repo, env := runFixture(t)
	env = withFakeClaude(t, env, frictionSession(false))
	code, out, errOut := runExe(t, testBinary, repo, env, "fix Add", "--fast", "--agent", "claude", "--unattended", "-v")
	if code != 0 || !strings.Contains(out, "ACCEPTED") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if strings.Contains(out, "\nfriction ") {
		t.Errorf("a friction line with no friction:\n%s", out)
	}
	if !strings.Contains(out, "[implement #1 claude] policy friction 0 turns (0 denied)\n") {
		t.Errorf("-v lacks the friction:\n%s", out)
	}
}

// frictionSession fixes Add, runs go test, and first tries it as
// "cd <Workspace> && go test" if deny is set.
func frictionSession(deny bool) string {
	use := func(msg, id, cmd string) []string {
		return []string{
			`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"id": "` + msg + `", "content": [{"type": "tool_use", "id": "` + id + `", "name": "Bash", "input": {"command": "` + cmd + `"}}]}}}`,
			`{"dir": "out", "msg": {"type": "control_request", "request_id": "h` + id + `", "request": {"subtype": "hook_callback", "callback_id": "oge_pre_tool_use", "input": {"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": {"command": "` + cmd + `"}, "tool_use_id": "` + id + `"}}}}`,
		}
	}
	frames := []string{
		`{"dir": "meta", "argv": ["claude"]}`,
		`{"dir": "in", "msg": {"type": "control_request", "request_id": "req_1", "request": {"subtype": "initialize"}}}`,
		`{"dir": "out", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "req_1", "response": {"account": "<redacted>"}}}}`,
		`{"dir": "in", "msg": {"type": "user"}}`,
		`{"dir": "out", "msg": {"type": "system", "subtype": "init", "cwd": "/home/user/project", "session_id": "s-1", "tools": ["Bash", "Edit", "Glob", "Grep", "Read", "Write"], "mcp_servers": [], "model": "claude-haiku-4-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.289", "output_style": "default", "plugins": [], "skills": [], "capabilities": ["interrupt_receipt_v1"]}}`,
		`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"id": "m1", "content": [{"type": "tool_use", "id": "t1", "name": "Edit", "input": {"file_path": "/home/user/project/add.go", "old_string": "return 0", "new_string": "return a + b"}}]}}}`,
		`{"dir": "out", "msg": {"type": "control_request", "request_id": "h1", "request": {"subtype": "hook_callback", "callback_id": "oge_pre_tool_use", "input": {"hook_event_name": "PreToolUse", "tool_name": "Edit", "tool_input": {"file_path": "/home/user/project/add.go", "old_string": "return 0", "new_string": "return a + b"}, "tool_use_id": "t1"}}}}`,
		`{"dir": "in", "expect": "allow", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "h1"}}}`,
		`{"dir": "act", "write": {"path": "add.go", "content": "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}`,
	}
	// Quoted: the Workspace path has a space in it ("Application Support").
	if deny {
		frames = append(frames, use("m2", "t2", `cd \"/home/user/project\" && go test ./...`)...)
		frames = append(frames, `{"dir": "in", "expect": "deny", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "ht2"}}}`)
	}
	frames = append(frames, use("m3", "t3", "go test ./...")...)
	frames = append(frames,
		`{"dir": "in", "expect": "allow", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "ht3"}}}`,
		`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"id": "m4", "content": [{"type": "text", "text": "Fixed.\nExit: done"}]}}}`,
		`{"dir": "out", "msg": {"type": "result", "subtype": "success", "is_error": false, "result": "Fixed.\nExit: done", "num_turns": 4, "terminal_reason": "completed"}}`,
		`{"dir": "meta", "exit": 0}`,
	)
	return strings.Join(frames, "\n") + "\n"
}
