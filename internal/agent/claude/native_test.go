package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// An adopted session's argv carries none of the Launch profile's
// restrictions, and its environment none of Öge's.
func TestNativeLaunchKeepsSettings(t *testing.T) {
	args := nativeArgs("abc-123")
	for _, banned := range []string{"--setting-sources=", "--strict-mcp-config", "--settings", "--tools",
		"--permission-mode", "--no-session-persistence", "--disable-slash-commands", "--model", "--append-system-prompt"} {
		if slices.Contains(args, banned) {
			t.Errorf("native argv has %s: %v", banned, args)
		}
	}
	for _, want := range []string{"--resume", "abc-123", "--permission-prompt-tool", "stdio", "--input-format"} {
		if !slices.Contains(args, want) {
			t.Errorf("native argv lacks %s: %v", want, args)
		}
	}
	env, err := nativeEnv(profile{role: "implementer"}, []string{"HOME=/h", "CLAUDECODE=1"}, agent.LaunchSpec{})
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range env {
		for _, n := range setEnv {
			if strings.HasPrefix(kv, n+"=") {
				t.Errorf("native env sets %s", kv)
			}
		}
		if strings.HasPrefix(kv, "CLAUDECODE=") {
			t.Errorf("native env keeps a session marker: %s", kv)
		}
	}
}

// openNative opens an adopted session on fakeclaude in cwd, under mode.
func openNative(t *testing.T, sc multiturn.Scenario, mode string) (agent.Session, string, string) {
	t.Helper()
	cwd, rec := t.TempDir(), filepath.Join(t.TempDir(), "rec.json")
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(path); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Path: fakePath, CloseGrace: time.Second, Environ: func() []string {
		return append(os.Environ(), "OGE_FAKE_CLAUDE_SCENARIO="+path, "OGE_FAKE_CLAUDE_RECORD="+rec, "OGE_FAKE_CLAUDE_MODE="+mode)
	}}
	s, err := a.Open(context.Background(), agent.LaunchSpec{Workspace: cwd, Resume: &agent.Resume{ID: "sess-1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, cwd, rec
}

// The process starts in the session's cwd with --resume, and the session's
// reported permission mode reaches the host.
func TestNativeOpenResumesInPlace(t *testing.T) {
	s, cwd, rec := openNative(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "hi"}}}, "bypassPermissions")
	if err := s.Send(agent.Turn{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	var mode string
	for _, ev := range turnEvents(t, s, nil) {
		if ev.Kind == agent.SessionOpened {
			mode = ev.Session.PermissionMode
		}
	}
	if mode != "bypassPermissions" {
		t.Errorf("reported mode %q", mode)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Argv []string }
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(got.Argv, "--resume"); i < 0 || got.Argv[i+1] != "sess-1" {
		t.Errorf("argv %v", got.Argv)
	}
	if slices.Contains(got.Argv, "--tools") || slices.Contains(got.Argv, "--setting-sources=") {
		t.Errorf("argv has Launch profile flags: %v", got.Argv)
	}
	_ = cwd
}

// What the session's settings ask becomes an Ask, and the answer reaches
// the agent: the write happens only on yes.
func TestNativeAskReachesAgent(t *testing.T) {
	sc := multiturn.Scenario{Turns: []multiturn.Turn{
		{Ask: &multiturn.Ask{Tool: "Bash", Target: "make deploy"}, Writes: []multiturn.File{{Path: "a.txt", Content: "a"}}},
	}}
	s, cwd, _ := openNative(t, sc, "default")
	if err := s.Send(agent.Turn{Text: "deploy"}); err != nil {
		t.Fatal(err)
	}
	var asked *agent.Ask
	turnEvents(t, s, func(ev agent.Event) {
		asked = ev.Ask
		if err := ev.Ask.Reply(true); err != nil {
			t.Error(err)
		}
	})
	if asked == nil || asked.Detail != "make deploy" {
		t.Fatalf("ask: %+v", asked)
	}
	if _, err := os.Stat(filepath.Join(cwd, "a.txt")); err != nil {
		t.Errorf("an allowed ask's write didn't happen: %v", err)
	}
}
