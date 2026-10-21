//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/agy"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// A bare oge on agy talks to a Chat, as on claude: "yo" gets a reply and
// no Run, the next message reaches the same Session, and a change is cut
// and verified (D2: agy chats under its Launch profile).
func TestHomeChatOnAgy(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	frames := []string{
		`{"dir": "out", "msg": {"event": "init", "conversation_id": "conv-1", "tools": ["run_command", "write_to_file", "view_file"], "model": "gemini-3.8-flash-high"}}`,
		`{"dir": "in"}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_type": "agent_response", "text_delta": "hello from agy"}}`,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": "hello from agy", "usage": {"total_tokens": 10}}}`,
		`{"dir": "in"}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_type": "agent_response", "text_delta": "still the same agy"}}`,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": "still the same agy", "usage": {"total_tokens": 20}}}`,
		`{"dir": "in"}`,
		`{"dir": "act", "write": {"path": "add.go", "content": "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_type": "agent_response", "text_delta": "fixed Add"}}`,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": "fixed Add", "usage": {"total_tokens": 30}}}`,
		`{"dir": "in"}`,
	}
	fixture := filepath.Join(t.TempDir(), "session.ndjson")
	if err := os.WriteFile(fixture, []byte(strings.Join(frames, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ad := &agy.Adapter{Path: multiturn.BuildAgy(t), CloseGrace: time.Second,
		Environ: func() []string { return append(os.Environ(), "OGE_FAKE_AGY_FIXTURE="+fixture) }}
	s := startHomeSessionWith(t, f, func(e *Env) { e.Agents = map[string]agent.Adapter{"agy": ad} }, "--fast", "--agent", "agy")
	s.composer()
	s.type_("yo\r")
	s.expect("  hello from agy")
	s.composer()
	if out := s.out.String()[s.mark:]; strings.Contains(out, "implement ") || strings.Contains(out, "Check passed") || strings.Contains(out, "verifying") {
		t.Errorf("a Replied turn shows stages or a Receipt:\n%s", out)
	}
	s.type_("and again\r")
	s.expect("still the same agy")
	s.composer()
	s.type_("fix Add\r")
	s.expect("oge › ✓ Accepted · 1 file · ")
	s.composer()
	s.quitChat(f)
	logs, _ := filepath.Glob(filepath.Join(f.state, "private", "chats", "*"))
	recs, _ := ledger.Replay(logs[0])
	started := 0
	for _, r := range recs {
		if r.Type == run.RecProcessStarted || r.Type == run.RecSessionReopened {
			started++
		}
	}
	if started != 1 {
		t.Errorf("%d agy processes started in the Chat, want 1", started)
	}
}
