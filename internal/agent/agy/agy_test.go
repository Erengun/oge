package agy

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
)

var fakePath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oge-fakeagy-")
	if err != nil {
		panic(err)
	}
	fakePath = filepath.Join(dir, "agy")
	if runtime.GOOS == "windows" {
		fakePath += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", fakePath, "./fakeagy").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func testAdapter(t *testing.T, extraEnv ...string) *Adapter {
	t.Helper()
	return &Adapter{
		Path:       fakePath,
		Timeout:    5 * time.Second,
		CloseGrace: time.Second,
		Environ: func() []string {
			return append(os.Environ(), extraEnv...)
		},
	}
}

func TestSessionOpened(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	ad := testAdapter(t)
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ev := <-s.Events()
	if ev.Kind != agent.SessionOpened {
		t.Fatalf("event = %v, want SessionOpened", ev.Kind)
	}
	if ev.Session == nil {
		t.Fatal("ev.Session is nil")
	}
	if ev.Session.Envelope != "ok" {
		t.Errorf("envelope = %q, want ok", ev.Session.Envelope)
	}
	if ev.Session.AgentVersion != LastTested {
		t.Errorf("agent version = %q, want %q", ev.Session.AgentVersion, LastTested)
	}
}

func TestTurnLifecycle(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	ad := testAdapter(t)
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// Consume SessionOpened
	<-s.Events()

	if err := s.Send(agent.Turn{Text: "fix the bug"}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	var claims []agent.Event
	var settled *agent.Event

	timeout := time.After(5 * time.Second)
	for settled == nil {
		select {
		case ev := <-s.Events():
			switch ev.Kind {
			case agent.Claim:
				claims = append(claims, ev)
			case agent.TurnSettled:
				settled = &ev
			}
		case <-timeout:
			t.Fatal("timed out waiting for turn to settle")
		}
	}

	if settled.Exit != "done" {
		t.Errorf("exit = %q, want done", settled.Exit)
	}
	if settled.Tokens == nil || *settled.Tokens != 42 {
		t.Errorf("tokens = %v, want 42", settled.Tokens)
	}
	if len(claims) == 0 || claims[0].Text != "Fixed." {
		t.Errorf("claims = %v, want text 'Fixed.'", claims)
	}
}

func TestSoftDeniedActionFails(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	ad := testAdapter(t, "OGE_FAKE_AGY_DENIED_ACTIONS=1")
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	<-s.Events()

	if err := s.Send(agent.Turn{Text: "run command"}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == agent.TurnSettled {
				if !strings.Contains(ev.Failure, "soft-denied") {
					t.Errorf("failure = %q, want soft-denied error", ev.Failure)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for turn settlement")
		}
	}
}

func TestInterruptTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals/interrupts not supported on Windows (ADR-0017)")
	}
	t.Parallel()
	ws := t.TempDir()
	ad := testAdapter(t, "OGE_FAKE_AGY_HANG=1")
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	<-s.Events()

	if err := s.Send(agent.Turn{Text: "start long task"}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Give fake time to start hanging
	time.Sleep(50 * time.Millisecond)

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt failed: %v", err)
	}

	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == agent.TurnSettled {
				if ev.Failure != "interrupted" {
					t.Errorf("failure = %q, want interrupted", ev.Failure)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for interrupt settlement")
		}
	}
}

func TestAuthFailureStderr(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	ad := testAdapter(t, `OGE_FAKE_AGY_STDERR=AGY_ERROR: {"status": "UNAUTHENTICATED", "message": "Authentication required"}`)
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	<-s.Events()

	select {
	case ev := <-s.Events():
		if ev.Kind != agent.TurnSettled {
			t.Fatalf("ev = %v, want TurnSettled", ev.Kind)
		}
		if !strings.Contains(ev.Stop, "authentication failed") {
			t.Errorf("stop = %q, want authentication failed", ev.Stop)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth failure Stop")
	}

	// Verify that premature exit doesn't follow and clobber the Stop
	select {
	case ev := <-s.Events():
		t.Fatalf("unexpected extra event after Stop: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestVersionFloorRejection(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	ad := testAdapter(t, "OGE_FAKE_AGY_VERSION=1.2.0")
	_, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err == nil || !strings.Contains(err.Error(), "older than 1.2.6") {
		t.Fatalf("expected version error, got %v", err)
	}
}

func TestEnvelopeWarnOnWorkingDirMismatch(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	otherDir := t.TempDir()
	otherDirJSON, _ := json.Marshal(otherDir)
	fixture := `{"dir": "out", "msg": {"event": "init", "cwd": ` + string(otherDirJSON) + `, "conversation_id": "conv-1", "tools": ["run_command"], "model": "gemini-3.8-flash-high"}}` + "\n"
	ad := testAdapter(t, "OGE_FAKE_AGY_FIXTURE="+writeFixture(t, fixture))
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ev := <-s.Events()
	if ev.Kind != agent.SessionOpened {
		t.Fatalf("ev = %v, want SessionOpened", ev.Kind)
	}
	if ev.Session.Envelope != "warn" {
		t.Errorf("envelope = %q, want warn", ev.Session.Envelope)
	}
	warn := <-s.Events()
	if warn.Kind != agent.Warning || !strings.Contains(warn.Text, "working directory is") {
		t.Errorf("expected working directory warning, got %+v", warn)
	}
}

func TestTokenDeltaCumulative(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	wsJSON, _ := json.Marshal(ws)
	fixture := strings.Join([]string{
		`{"dir": "out", "msg": {"event": "init", "cwd": ` + string(wsJSON) + `, "conversation_id": "conv-1", "tools": ["run_command"], "model": "gemini-3.8-flash-high"}}`,
		`{"dir": "in"}`,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": "Turn 1\nExit: continue", "usage": {"total_tokens": 100}}}`,
		`{"dir": "in"}`,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": "Turn 2\nExit: done", "usage": {"total_tokens": 250}}}`,
	}, "\n") + "\n"
	ad := testAdapter(t, "OGE_FAKE_AGY_FIXTURE="+writeFixture(t, fixture))
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()
	<-s.Events() // SessionOpened

	// Turn 1
	if err := s.Send(agent.Turn{Text: "turn 1"}); err != nil {
		t.Fatal(err)
	}
	ev1 := <-s.Events()
	if ev1.Kind != agent.TurnAccepted {
		t.Fatalf("expected TurnAccepted, got %+v", ev1)
	}
	ev2 := <-s.Events()
	if ev2.Kind != agent.TurnSettled || ev2.Tokens == nil || *ev2.Tokens != 100 {
		t.Fatalf("turn 1: expected 100 tokens, got %+v", ev2)
	}

	// Turn 2
	if err := s.Send(agent.Turn{Text: "turn 2"}); err != nil {
		t.Fatal(err)
	}
	ev3 := <-s.Events()
	if ev3.Kind != agent.TurnAccepted {
		t.Fatalf("expected TurnAccepted, got %+v", ev3)
	}
	ev4 := <-s.Events()
	if ev4.Kind != agent.TurnSettled || ev4.Tokens == nil || *ev4.Tokens != 150 {
		t.Fatalf("turn 2: expected 150 tokens (250-100), got %+v", ev4)
	}
}

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.ndjson")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTurnInFlight(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	ad := testAdapter(t, "OGE_FAKE_AGY_HANG=1")
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-123",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	<-s.Events()

	if err := s.Send(agent.Turn{Text: "first"}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if err := s.Send(agent.Turn{Text: "second"}); err != agent.ErrTurnInFlight {
		t.Errorf("Send second turn err = %v, want ErrTurnInFlight", err)
	}
}

func TestProfileArgsAndEnv(t *testing.T) {
	t.Parallel()
	prof, err := profileFor("implementer", "high")
	if err != nil {
		t.Fatal(err)
	}
	args, err := prof.args(agent.LaunchSpec{
		Role:    "implementer",
		Network: "off",
		Model:   "gemini-3.8-flash-high",
		Effort:  "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if slices.Contains(args, "-p") {
		t.Errorf("args must not contain -p in stream-json mode: %s", joined)
	}
	for _, want := range []string{"--input-format stream-json", "--output-format stream-json", "--dangerously-skip-permissions", "--sandbox", "--model gemini-3.8-flash-high", "--effort high"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q: %s", want, joined)
		}
	}

	env, err := prof.env([]string{"USER=test", "AGY_SESSION_ID=drop-me", "PATH=/bin"}, agent.LaunchSpec{
		Role:  "implementer",
		RunID: "run-99",
		Cache: "/tmp/cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	envJoined := strings.Join(env, " ")
	if strings.Contains(envJoined, "AGY_SESSION_ID") {
		t.Errorf("env kept stripped var: %s", envJoined)
	}
	for _, want := range []string{"USER=test", "PATH=/bin", "OGE_RUN_ID=run-99", "OGE_ROLE=implementer", "GOCACHE=/tmp/cache"} {
		if !strings.Contains(envJoined, want) {
			t.Errorf("env lacks %q: %s", want, envJoined)
		}
	}
}

func TestNestedPayloadParsing(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	wsJSON, _ := json.Marshal(ws)
	fixture := strings.Join([]string{
		`{"event": "init", "conversation_id": "conv-nested", "init": {"cwd": ` + string(wsJSON) + `, "tools": ["write_to_file"], "model": "gemini-3.8-flash-high"}}`,
		`{"dir": "in"}`,
		`{"event": "step_update", "step_update": {"conversation_id": "conv-nested", "step_index": 1, "state": "ACTIVE", "step_type": "agent_response", "text_delta": "Nested update."}}`,
		`{"event": "result", "result": {"conversation_id": "conv-nested", "status": "SUCCESS", "response": "Done with nested.\nExit: done", "num_turns": 1, "usage": {"total_tokens": 88}}}`,
	}, "\n") + "\n"
	ad := testAdapter(t, "OGE_FAKE_AGY_FIXTURE="+writeFixture(t, fixture))
	s, err := ad.Open(context.Background(), agent.LaunchSpec{
		Role:      "implementer",
		Workspace: ws,
		RunID:     "run-nested",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ev1 := <-s.Events()
	if ev1.Kind != agent.SessionOpened {
		t.Fatalf("expected SessionOpened, got %+v", ev1)
	}
	if err := s.Send(agent.Turn{Text: "do work"}); err != nil {
		t.Fatal(err)
	}
	ev2 := <-s.Events()
	if ev2.Kind != agent.TurnAccepted {
		t.Fatalf("expected TurnAccepted, got %+v", ev2)
	}
	claim := <-s.Events()
	if claim.Kind != agent.Claim || claim.Text != "Nested update." {
		t.Fatalf("expected claim with text 'Nested update.', got %+v", claim)
	}
	settled := <-s.Events()
	if settled.Kind != agent.TurnSettled || settled.Tokens == nil || *settled.Tokens != 88 {
		t.Fatalf("expected TurnSettled with 88 tokens, got %+v", settled)
	}
	if settled.Exit != "done" {
		t.Fatalf("expected Exit 'done', got %v", settled.Exit)
	}
}

// A result that arrives while Send is still writing must not overtake
// TurnAccepted (flaked under -race, #307).
func TestSettleHeldBehindTurnAccepted(t *testing.T) {
	t.Parallel()
	s := &session{events: make(chan agent.Event, 8), inFlight: true, accepting: true}
	s.settle(agent.Event{})
	select {
	case ev := <-s.events:
		t.Fatalf("event before TurnAccepted: %+v", ev)
	default:
	}
	s.endAccept(nil)
	if k := (<-s.events).Kind; k != agent.TurnAccepted {
		t.Fatalf("first = %v, want TurnAccepted", k)
	}
	if k := (<-s.events).Kind; k != agent.TurnSettled {
		t.Fatalf("second = %v, want TurnSettled", k)
	}
}
