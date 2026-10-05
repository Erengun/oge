package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These run oge --fast with the fake claude (internal/agent/claude/
// fakeclaude) first on PATH: the real Claude adapter, its process and
// protocol boundary, and a replayed session. They spend no quota.

// claudeSession is a synthetic recording of one implementer turn: it
// reads add.go, edits it through Öge's PreToolUse hook (the edit is applied
// only if Öge allows it, and only when fix is set), tries a command Öge
// doesn't pre-authorise, and finishes with Exit: done.
func claudeSession(fix bool) string { return claudeSessionWith(fix, `[]`) }

// claudeSessionWith is claudeSession with system/init reporting skills.
func claudeSessionWith(fix bool, skills string) string {
	frames := []string{
		`{"dir": "meta", "argv": ["claude"]}`,
		`{"dir": "in", "msg": {"type": "control_request", "request_id": "req_1", "request": {"subtype": "initialize"}}}`,
		`{"dir": "out", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "req_1", "response": {"account": "<redacted>"}}}}`,
		`{"dir": "in", "msg": {"type": "user"}}`,
		`{"dir": "out", "msg": {"type": "system", "subtype": "init", "cwd": "/home/user/project", "session_id": "s-1", "tools": ["Bash", "Edit", "Glob", "Grep", "Read", "Write"], "mcp_servers": [], "model": "claude-haiku-4-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.289", "output_style": "default", "plugins": [], "skills": ` + skills + `, "capabilities": ["interrupt_receipt_v1"]}}`,
		`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"content": [{"type": "text", "text": "Add ignores its arguments.\nFixing it."}]}}}`,
		`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"content": [{"type": "tool_use", "id": "toolu_1", "name": "Edit", "input": {"file_path": "/home/user/project/add.go", "old_string": "return 0", "new_string": "return a + b"}}]}}}`,
		`{"dir": "out", "msg": {"type": "control_request", "request_id": "h1", "request": {"subtype": "hook_callback", "callback_id": "oge_pre_tool_use", "input": {"hook_event_name": "PreToolUse", "tool_name": "Edit", "tool_input": {"file_path": "/home/user/project/add.go", "old_string": "return 0", "new_string": "return a + b"}, "tool_use_id": "toolu_1"}}}}`,
		`{"dir": "in", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "h1"}}}`,
	}
	if fix {
		frames = append(frames, `{"dir": "act", "write": {"path": "add.go", "content": "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}`)
	}
	frames = append(frames,
		`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"content": [{"type": "tool_use", "id": "toolu_2", "name": "Bash", "input": {"command": "curl https://example.com"}}]}}}`,
		`{"dir": "out", "msg": {"type": "control_request", "request_id": "h2", "request": {"subtype": "hook_callback", "callback_id": "oge_pre_tool_use", "input": {"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": {"command": "curl https://example.com"}, "tool_use_id": "toolu_2"}}}}`,
		`{"dir": "in", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "h2"}}}`,
		`{"dir": "out", "msg": {"type": "brand_new_message"}}`,
		`{"dir": "out", "msg": {"type": "assistant", "parent_tool_use_id": null, "message": {"content": [{"type": "text", "text": "Fixed.\nExit: done"}]}}}`,
		`{"dir": "out", "msg": {"type": "result", "subtype": "success", "is_error": false, "result": "Fixed.\nExit: done", "num_turns": 3, "terminal_reason": "completed"}}`,
		`{"dir": "meta", "exit": 0}`,
	)
	return strings.Join(frames, "\n") + "\n"
}

func withFakeClaude(t *testing.T, env []string, session string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.ndjson")
	if err := os.WriteFile(path, []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, kv := range env {
		if p, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + fakeClaudeDir + ":" + p
		}
	}
	return append(env, "OGE_FAKE_CLAUDE_FIXTURE="+path)
}

func TestBinaryClaudeRunAccepted(t *testing.T) {
	repo, env := runFixture(t)
	env = withFakeClaude(t, env, claudeSession(true))
	code, out, errOut := runExe(t, testBinary, repo, env, "fix Add", "--fast", "--agent", "claude", "--unattended", "-v")
	if code != 0 || !strings.Contains(out, "ACCEPTED") || !strings.Contains(out, "1 ran · 0 failed") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"[implement #1 claude] started (cause: first) · fresh Session · Workspace from Snapshot · 2.1.289 · envelope ok",
		`[implement #1 claude] "Add ignores its arguments."`,
		"[implement #1 claude] Edit add.go",
		"[implement #1 claude] allow Edit add.go · pre-authorised by Launch profile ",
		"[implement #1 claude] Bash curl https://example.com",
		"[implement #1 claude] denied: Bash curl https://example.com (this command isn't pre-authorised)\n",
		"[implement #1 claude] Exit: done",
		"implement  claude · Exit done · Candidate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	// No raw frame reaches the terminal (ADR-0022).
	if strings.Contains(out+errOut, `"type"`) || strings.Contains(out+errOut, "brand_new_message") {
		t.Errorf("a raw frame reached the output:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "add.go")); string(b) != brokenAdd {
		t.Errorf("the user's add.go was written: %q", b)
	}
	// The time to first activity and the Host-request counts are recorded.
	ledger := findLedger(t, env)
	for _, want := range []string{`"kind":"responsiveness"`, `"first_activity_ms":`, `"no_interactive_approval":1`, `"pre_authorised":1`, `"kind":"session"`, `"envelope":"ok"`} {
		if !strings.Contains(ledger, want) {
			t.Errorf("the Ledger lacks %s", want)
		}
	}
}

func TestBinaryClaudeRunRejected(t *testing.T) {
	repo, env := runFixture(t)
	env = withFakeClaude(t, env, claudeSession(false))
	code, out, errOut := runExe(t, testBinary, repo, env, "fix Add", "--fast", "--agent", "claude", "--unattended")
	if code != 3 || !strings.Contains(out, "REJECTED") || !strings.Contains(out, "1 failed: TestAdd") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// The release binary runs claude: a claude that can't start a session is
// an Attempt failure, reported with its own last words.
func TestReleaseBinaryRunsClaude(t *testing.T) {
	repo, env := runFixture(t)
	env = withFakeClaude(t, env, `{"dir": "die"}`+"\n")
	code, out, errOut := runBinary(t, repo, env, "fix Add", "--fast", "--agent", "claude", "--unattended")
	if code != 11 || !strings.Contains(out, "claude exited before the session started") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func findLedger(t *testing.T, env []string) string {
	t.Helper()
	var home string
	for _, kv := range env {
		if h, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = h
		}
	}
	var found string
	filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == "ledger.jsonl" {
			b, _ := os.ReadFile(p)
			found = string(b)
		}
		return nil
	})
	if found == "" {
		t.Fatal("no Ledger under HOME")
	}
	return found
}

// Residue an isolated launch can't remove is shown once, when it first
// appears and when it changes, never on every Run (#44).
func TestBinaryClaudeResidueIsShownOnce(t *testing.T) {
	repo, env := runFixture(t)
	runWith := func(skills string) string {
		t.Helper()
		e := withFakeClaude(t, append([]string{}, env...), claudeSessionWith(true, skills))
		code, out, errOut := runExe(t, testBinary, repo, e, "fix Add", "--fast", "--agent", "claude", "--unattended")
		if code != 0 {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		return out
	}
	const note = "note       claude still loads"
	if out := runWith(`["deep-research", "design"]`); !strings.Contains(out, note+" 2 skills when isolated") {
		t.Errorf("first Run doesn't show the residue:\n%s", out)
	}
	if out := runWith(`["design", "deep-research"]`); strings.Contains(out, "note ") {
		t.Errorf("unchanged residue shown again:\n%s", out)
	}
	if out := runWith(`["design"]`); !strings.Contains(out, note+" 1 skills") {
		t.Errorf("changed residue not shown:\n%s", out)
	}
	if l := findLedger(t, env); !strings.Contains(l, `"residue":{"Plugins":[],"Skills":["`) {
		t.Error("the residue isn't in the session Observation")
	}
}
