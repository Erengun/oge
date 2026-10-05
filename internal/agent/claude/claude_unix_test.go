//go:build unix

package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
)

func TestOversizedFrameIsAnAttemptFailure(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"dir": "raw", "line": `{"type": "assistant", "x": "` + strings.Repeat("x", maxFrame) + `"}`})
	frames := insertAfter(firstTurn(t, "turns.ndjson"), `"subtype": "init"`, string(b))
	s := settled(t, replay(t, frames).turn("hi"))
	if s.Failure != "malformed_frame: "+errFrameTooLarge.Error() {
		t.Errorf("settled %+v", s)
	}
}

func TestUnterminatedFrameIsAnAttemptFailure(t *testing.T) {
	frames := insertAfter(firstTurn(t, "turns.ndjson"), `"subtype": "init"`,
		`{"dir": "raw", "no_newline": true, "line": "{\"type\": \"assistant\""}`, `{"dir": "die"}`)
	s := settled(t, replay(t, frames).turn("hi"))
	if s.Failure != "malformed_frame: the last line has no newline" {
		t.Errorf("settled %+v", s)
	}
}

// Close returns within its deadlines even when claude ignores stdin EOF
// and SIGTERM.
func TestCloseKillsAClaudeThatWontExit(t *testing.T) {
	dir := t.TempDir()
	fx := filepath.Join(dir, "f.ndjson")
	frames := fixture(t, "turns.ndjson")[:3]
	os.WriteFile(fx, []byte(strings.Join(append(frames, `{"dir": "hang"}`), "\n")+"\n"), 0o644)
	a := &Adapter{Path: fakePath, CloseGrace: 200 * time.Millisecond,
		Environ: func() []string { return []string{"OGE_FAKE_CLAUDE_FIXTURE=" + fx} }}
	s, err := a.Open(context.Background(), agent.LaunchSpec{Role: "implementer", Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s.Close()
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("Close took %s", d)
	}
	pid := s.Process().PID
	if err := syscall.Kill(pid, 0); err == nil {
		t.Errorf("claude (pid %d) is still running after Close", pid)
	}
}

// A tool's background process doesn't outlive the Session.
func TestCloseEndsBackgroundProcesses(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	frames := fixture(t, "turns.ndjson")[:3]
	h := open(t, append(frames, `{"dir": "act", "spawn": "`+pidFile+`"}`, `{"dir": "meta", "exit": 0}`))
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(string(b))
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the fake never spawned its child")
	}
	h.sess.Close()
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("background process %d survived Close", pid)
}
