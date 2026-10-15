package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withFakeAgy(t *testing.T, env []string, fixture string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.ndjson")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, kv := range env {
		if p, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + fakeAgyDir + string(os.PathListSeparator) + p
		}
	}
	return append(env, "OGE_FAKE_AGY_FIXTURE="+path)
}

func agySession(fix bool) string {
	frames := []string{
		`{"dir": "out", "msg": {"event": "init", "conversation_id": "conv-1", "tools": ["run_command", "write_to_file", "replace_file_content", "view_file"], "model": "gemini-3.8-flash-high"}}`,
		`{"dir": "in"}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_type": "agent_response", "text_delta": "Fixing Add function."}}`,
	}
	if fix {
		frames = append(frames,
			`{"dir": "act", "write": {"path": "add.go", "content": "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}`,
			`{"dir": "out", "msg": {"event": "step_update", "step_type": "tool", "tool_info": {"name": "write_to_file", "parameters": {"TargetFile": "add.go"}}}}`,
		)
	}
	frames = append(frames,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": "Fixed.\nExit: done", "num_turns": 1, "usage": {"total_tokens": 42}}}`,
		`{"dir": "exit", "code": 0}`,
	)
	return strings.Join(frames, "\n") + "\n"
}

func TestBinaryAgyRunAccepted(t *testing.T) {
	t.Parallel()
	repo, env := runFixture(t)
	env = withFakeAgy(t, env, agySession(true))
	code, out, errOut := runExe(t, testBinary, repo, env, "fix Add", "--fast", "--agent", "agy", "--unattended", "-v")
	if code != 0 || !strings.Contains(out, "✓ Accepted") || !strings.Contains(out, "1 ran · 0 failed") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"[implement #1 agy] started (cause: first) · fresh Session · Workspace from Snapshot · 1.3.5 · envelope ok",
		"Fixing Add function.",
		"[implement #1 agy] write_to_file add.go",
		"[implement #1 agy] Exit: done",
		"implement  agy · Exit done · Candidate",
		"implement (agy): headless runs operate under --dangerously-skip-permissions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}

	if b, _ := os.ReadFile(filepath.Join(repo, "add.go")); string(b) != brokenAdd {
		t.Errorf("the user's add.go was overwritten directly in the repo: %q", b)
	}

	ledger := findLedger(t, env)
	for _, want := range []string{`"kind":"session"`, `"envelope":"ok"`, `"agent":"agy"`} {
		if !strings.Contains(ledger, want) {
			t.Errorf("the Ledger lacks %s", want)
		}
	}
}
