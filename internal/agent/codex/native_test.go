package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
)

// These tests resume a thread against the fake app-server. The script is
// hand-written to the shape thread/resume answered in
// testdata/steer_interrupt_resume.jsonl (codex-cli 0.155.1), with the
// approval policy and sandbox the test sets: what codex says of the
// thread is all the coverage line may use (ADR-0025).

const nativeThread = "01a10828-3767-7d41-9f73-229cb7968756"

func c2s(m map[string]any) rec { return rec{Dir: "c2s", Msg: m} }
func s2c(m map[string]any) rec { return rec{Dir: "s2c", Msg: m} }

// nativeScript is a resume, a turn that asks to run a command and, when
// asked is set, the human's answer and the rest of the turn.
func nativeScript(t *testing.T, approval, sandbox any, mcp map[string]any) []rec {
	return nativeScriptReviewed(t, approval, sandbox, "user", mcp)
}

func nativeScriptReviewed(t *testing.T, approval, sandbox any, reviewer string, mcp map[string]any) []rec {
	init := load(t, "init_account.jsonl")[:2]
	out := []rec{init[0], init[1], c2s(map[string]any{"method": "initialized", "params": map[string]any{}})}
	cfg := map[string]any{"mcp_servers": mcp}
	out = append(out,
		c2s(map[string]any{"id": 2, "method": "config/read", "params": map[string]any{}}),
		s2c(map[string]any{"id": 2, "result": map[string]any{"config": cfg}}),
		c2s(map[string]any{"id": 3, "method": "thread/resume", "params": map[string]any{}}),
		s2c(map[string]any{"id": 3, "result": map[string]any{"thread": map[string]any{"id": nativeThread}, "cwd": ws,
			"approvalPolicy": approval, "sandbox": sandbox, "approvalsReviewer": reviewer}}),
		c2s(map[string]any{"id": 4, "method": "turn/start", "params": map[string]any{}}),
		s2c(map[string]any{"id": 4, "result": map[string]any{"turn": map[string]any{"id": "t1"}}}),
		s2c(map[string]any{"method": "item/started", "params": map[string]any{"threadId": nativeThread,
			"item": map[string]any{"type": "commandExecution", "id": "x1", "command": "/bin/zsh -lc 'make deploy'"}}}),
		s2c(map[string]any{"id": 0, "method": "item/commandExecution/requestApproval", "params": map[string]any{
			"threadId": nativeThread, "turnId": "t1", "itemId": "x1", "command": "/bin/zsh -lc 'make deploy'", "cwd": ws,
			"commandActions": []any{map[string]any{"type": "unknown", "command": "make deploy"}}}}),
		c2s(map[string]any{"id": 0, "result": map[string]any{}}),
		s2c(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": nativeThread,
			"item": map[string]any{"type": "agentMessage", "id": "m1", "text": "deployed"}}}),
		s2c(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": nativeThread,
			"turn": map[string]any{"id": "t1", "status": "completed"}}}),
	)
	return out
}

func openNativeFor(t *testing.T, h *harness, script []rec) agent.Session {
	t.Helper()
	t.Setenv("OGE_FAKECODEX_ARGV", filepath.Join(t.TempDir(), "argv.json"))
	s, err := h.open(script, agent.LaunchSpec{Resume: &agent.Resume{ID: nativeThread}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNativeResumeCarriesNoOgeSettings(t *testing.T) {
	h := &harness{t: t}
	s := openNativeFor(t, h, nativeScript(t, "on-request", map[string]any{"type": "workspaceWrite"}, nil))
	defer s.Close()
	sent := h.sent()
	if len(sent["thread/start"]) != 0 || len(sent["account/read"]) != 0 || len(sent["account/rateLimits/read"]) != 0 {
		t.Errorf("an adopted thread is resumed, not started or probed: %v", slices.Collect(func(yield func(string) bool) {
			for k := range sent {
				yield(k)
			}
		}))
	}
	if h.tests != 0 {
		t.Error("the sandbox self-test ran for an adopted thread")
	}
	r := sent["thread/resume"]
	if len(r) != 1 {
		t.Fatalf("thread/resume sent %d times", len(r))
	}
	params, _ := r[0]["params"].(map[string]any)
	if len(params) != 1 || params["threadId"] != nativeThread {
		t.Errorf("thread/resume params %v: want the thread id and nothing of Öge's", params)
	}
	// The app-server starts with no `-c notify=[]` pin, in the thread's cwd.
	b, err := os.ReadFile(os.Getenv("OGE_FAKECODEX_ARGV"))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		Argv []string
		Cwd  string
	}
	if err := json.Unmarshal(b, &started); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(started.Argv, []string{"app-server"}) {
		t.Errorf("argv %v", started.Argv)
	}
	if real, _ := filepath.EvalSymlinks(h.ws); started.Cwd != h.ws && started.Cwd != real {
		t.Errorf("cwd %s, want %s", started.Cwd, h.ws)
	}
}

func TestNativeApprovalBecomesAnAsk(t *testing.T) {
	for _, tc := range []struct {
		allow    bool
		decision string
	}{{true, "accept"}, {false, "decline"}} {
		t.Run(tc.decision, func(t *testing.T) {
			h := &harness{t: t}
			s := openNativeFor(t, h, nativeScript(t, "on-request", map[string]any{"type": "workspaceWrite"}, nil))
			defer s.Close()
			if err := s.Send(agent.Turn{Text: "go"}); err != nil {
				t.Fatal(err)
			}
			var opened *agent.SessionInfo
			var ask *agent.Ask
			var host *agent.HostDecision
			var evs []agent.Event
			timeout := time.After(20 * time.Second)
		loop:
			for {
				select {
				case ev := <-s.Events():
					evs = append(evs, ev)
					switch {
					case ev.Kind == agent.SessionOpened:
						opened = ev.Session
					case ev.Kind == agent.HostRequest && ev.Ask != nil:
						ask, host = ev.Ask, ev.Host
						if err := ask.Answer(agent.Answer{Allow: tc.allow}); err != nil {
							t.Fatal(err)
						}
					case ev.Kind == agent.TurnSettled:
						break loop
					}
				case <-timeout:
					t.Fatalf("no settle: %+v", evs)
				}
			}
			if opened == nil || opened.ApprovalPolicy != "on-request" || opened.Sandbox != "workspace-write" || opened.Profile != "native" {
				t.Errorf("session info %+v", opened)
			}
			if ask == nil || host.Decision != "ask" || host.Rule != "session_settings_ask" || host.By != "session_settings" {
				t.Fatalf("ask %+v host %+v", ask, host)
			}
			if ask.Tool != "Bash" || ask.Target != "make deploy" || !strings.Contains(ask.Detail, "make deploy") {
				t.Errorf("ask shows %q %q %q", ask.Tool, ask.Target, ask.Detail)
			}
			if err := ask.Reply(true); err == nil {
				t.Error("an answered Ask took a second answer")
			}
			// The answer reached codex: a one-shot decision, never a
			// session-wide amendment.
			var got map[string]any
			for _, m := range h.sent()["response"] {
				if m["id"] == float64(0) {
					got, _ = m["result"].(map[string]any)
				}
			}
			if got["decision"] != tc.decision || len(got) != 1 {
				t.Errorf("codex was answered %v, want only %s", got, tc.decision)
			}
			var human *agent.HostDecision
			for _, ev := range evs {
				if ev.Kind == agent.HostRequest && ev.Ask == nil && ev.Host.By == "human" {
					human = ev.Host
				}
			}
			if human == nil || (human.Decision == "allow") != tc.allow {
				t.Errorf("human decision %+v", human)
			}
		})
	}
}

func TestNativeReportsWhatCodexSaidOfItself(t *testing.T) {
	h := &harness{t: t}
	mcp := map[string]any{"dummy": map[string]any{"command": "/bin/cat", "enabled": true}, "off": map[string]any{"enabled": false}}
	s := openNativeFor(t, h, nativeScript(t, "never", map[string]any{"type": "dangerFullAccess"}, mcp))
	defer s.Close()
	if err := s.Send(agent.Turn{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	var opened *agent.SessionInfo
	for ev := range s.Events() {
		if ev.Kind == agent.SessionOpened {
			opened = ev.Session
			break
		}
	}
	if opened.ApprovalPolicy != "never" || opened.Sandbox != "danger-full-access" || !slices.Equal(opened.Unsupervised, []string{"dummy"}) {
		t.Errorf("session info %+v", opened)
	}
}

func TestNativeReportsWhoReviewsApprovals(t *testing.T) {
	for reviewer, want := range map[string]string{"user": "user", "auto_review": "auto_review", "": "unreadable"} {
		h := &harness{t: t}
		s := openNativeFor(t, h, nativeScriptReviewed(t, "on-request", map[string]any{"type": "workspaceWrite"}, reviewer, nil))
		if err := s.Send(agent.Turn{Text: "go"}); err != nil {
			t.Fatal(err)
		}
		for ev := range s.Events() {
			if ev.Kind == agent.SessionOpened {
				if ev.Session.ApprovalsReviewer != want || !slices.Equal(ev.Session.Refused, refusedRequests) {
					t.Errorf("reviewer %q: session info %+v", reviewer, ev.Session)
				}
				break
			}
		}
		s.Close()
	}
}

func TestNativeNamesOfWhatCodexReported(t *testing.T) {
	for in, want := range map[string]string{
		`"never"`: "never", `{"granular":{"sandbox_approval":true}}`: "granular", `null`: "", `{}`: "", `{"type":"never"}`: "",
	} {
		if got := approvalName(json.RawMessage(in)); got != want {
			t.Errorf("approvalName(%s) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		`{"type":"dangerFullAccess"}`: "danger-full-access", `{"type":"workspaceWrite","writableRoots":[]}`: "workspace-write",
		`{"type":"readOnly"}`: "read-only", `"danger-full-access"`: "danger-full-access", `null`: "",
	} {
		if got := sandboxName(json.RawMessage(in)); got != want {
			t.Errorf("sandboxName(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestOgeLaunchPathIsUnchanged(t *testing.T) {
	// A Launch without Resume still takes the Launch profile's path: it
	// refuses a role it doesn't support before any app-server starts.
	_, err := (&Adapter{Path: fakePath}).Open(t.Context(), agent.LaunchSpec{Role: "verifier", Workspace: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "isn't supported") {
		t.Errorf("err = %v", err)
	}
}
