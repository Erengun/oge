package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
)

// These contract tests run the adapter against the fake claude
// (./fakeclaude) replaying the re-redacted recordings in testdata over
// real pipes (ADR-0017). They spend no quota.

var fakePath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oge-fakeclaude-")
	if err != nil {
		panic(err)
	}
	fakePath = filepath.Join(dir, "claude")
	if runtime.GOOS == "windows" {
		fakePath += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", fakePath, "./fakeclaude").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// synthetic is the parent environment the tests launch from: session
// markers to strip, and config and provider variables to keep. Nothing in
// it is a real credential.
var synthetic = []string{
	"PATH=/usr/bin:/bin", "HOME=/synthetic/home",
	"CLAUDE_CONFIG_DIR=/synthetic/claude-config",
	"CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_SESSION_ID=parent-session",
	"CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_SESSION_ATTENDED=1", "CLAUDE_PID=4242", "CLAUDE_EFFORT=high",
	"CLAUDE_CODE_EXECPATH=/synthetic/claude", "CLAUDE_CODE_MESSAGING_SOCKET=/synthetic/sock",
	"CLAUDE_CODE_BRIDGE_SESSION_ID=b", "CLAUDE_CODE_SSE_PORT=1234", "CLAUDE_PROJECT_DIR=/synthetic/project",
	"CLAUDE_ENV_FILE=/synthetic/env", "CLAUDE_CODE_REMOTE=1", "CLAUDE_CODE_REMOTE_SESSION_ID=r",
	"AI_AGENT=claude-code", "TRACEPARENT=00-abc-def-01",
	"CLAUDE_CODE_USE_BEDROCK=1", "ANTHROPIC_BASE_URL=https://synthetic.example", "AWS_REGION=eu-west-1",
	"CLAUDE_CODE_SUBAGENT_MODEL=haiku", "GOCACHE=/synthetic/gocache", "OGE_ROLE=verifier",
}

type harness struct {
	t      *testing.T
	ws     string
	record string
	stdin  string
	spec   agent.LaunchSpec
	sess   agent.Session
}

// open starts a Session replaying fixture (a testdata name, or frames).
func open(t *testing.T, frames []string) *harness {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the adapter runs only where Runs do (ADR-0017)")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ws: filepath.Join(dir, "ws"), record: filepath.Join(dir, "record.json"), stdin: filepath.Join(dir, "stdin.ndjson")}
	for _, d := range []string{h.ws, filepath.Join(h.ws, "src"), filepath.Join(h.ws, "tests"), filepath.Join(dir, "private"), filepath.Join(dir, "cache")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fixture := filepath.Join(dir, "fixture.ndjson")
	if err := os.WriteFile(fixture, []byte(strings.Join(frames, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(append([]string{}, synthetic...),
		"OGE_FAKE_CLAUDE_FIXTURE="+fixture, "OGE_FAKE_CLAUDE_RECORD="+h.record, "OGE_FAKE_CLAUDE_STDIN="+h.stdin)
	h.spec = agent.LaunchSpec{
		Role: "implementer", Model: "haiku", Workspace: h.ws, Network: "off", RunID: "run-1",
		RepoInstructions: "Use tabs.", CheckCommands: []string{"go test -json ./..."},
		DenyRead: []string{filepath.Join(dir, "private")}, Cache: filepath.Join(dir, "cache"),
	}
	a := &Adapter{Path: fakePath, Environ: func() []string { return env }, StartTimeout: 10 * time.Second}
	s, err := a.Open(context.Background(), h.spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.sess = s
	t.Cleanup(func() { s.Close() })
	return h
}

// turn sends text and collects events until the turn settles.
func (h *harness) turn(text string) []agent.Event {
	h.t.Helper()
	if err := h.sess.Send(agent.Turn{Text: text}); err != nil {
		h.t.Fatalf("Send: %v", err)
	}
	return h.until()
}

func (h *harness) until() []agent.Event {
	h.t.Helper()
	var evs []agent.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-h.sess.Events():
			if !ok {
				h.t.Fatalf("events closed before the turn settled: %+v", evs)
			}
			evs = append(evs, ev)
			if ev.Kind == agent.TurnSettled {
				return evs
			}
		case <-timeout:
			h.t.Fatalf("the turn never settled: %+v", evs)
		}
	}
}

// fixture reads a recording's lines.
func fixture(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// firstTurn is a recording up to its first result, then a clean exit.
func firstTurn(t *testing.T, name string) []string {
	var out []string
	for _, l := range fixture(t, name) {
		out = append(out, l)
		if strings.Contains(l, `"type": "result"`) {
			break
		}
	}
	return append(out, `{"dir": "meta", "exit": 0}`)
}

// editInit rewrites every system/init frame.
func editInit(t *testing.T, frames []string, edit func(m map[string]any)) []string {
	t.Helper()
	for i, l := range frames {
		if !strings.Contains(l, `"subtype": "init"`) {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatal(err)
		}
		edit(e["msg"].(map[string]any))
		b, _ := json.Marshal(e)
		frames[i] = string(b)
	}
	return frames
}

// exactTools gives init the implementer's tool list, so the envelope passes.
func exactTools(m map[string]any) {
	m["tools"] = []string{"Bash", "Edit", "Glob", "Grep", "Read", "Write"}
}

// insertAfter adds frames after the first line containing marker.
func insertAfter(frames []string, marker string, add ...string) []string {
	for i, l := range frames {
		if strings.Contains(l, marker) {
			return append(append(append([]string{}, frames[:i+1]...), add...), frames[i+1:]...)
		}
	}
	panic("no " + marker)
}

func kinds(evs []agent.Event) []agent.EventKind {
	var k []agent.EventKind
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func settled(t *testing.T, evs []agent.Event) agent.Event {
	t.Helper()
	last := evs[len(evs)-1]
	if last.Kind != agent.TurnSettled {
		t.Fatalf("last event %v, want settled", last.Kind)
	}
	return last
}

func TestLaunchProfileArgvAndEnv(t *testing.T) {
	h := open(t, firstTurn(t, "turns.ndjson"))
	h.turn("hi")
	h.sess.Close()
	b, err := os.ReadFile(h.record)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Argv []string
		Env  []string
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}

	argv := strings.Join(rec.Argv, "\x00")
	uuid := regexp.MustCompile(`--session-id\x00[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\x00`)
	if !uuid.MatchString(argv) {
		t.Errorf("no Öge-chosen --session-id UUID in %q", rec.Argv)
	}
	argv = uuid.ReplaceAllString(argv, "--session-id\x00<uuid>\x00")
	private, cache := h.spec.DenyRead[0], h.spec.Cache
	settings := `{"permissions":{"deny":["Read(/` + private + `/**)","Edit(/` + private + `/**)"]},` +
		`"sandbox":{"allowUnsandboxedCommands":false,"enabled":true,"failIfUnavailable":true,` +
		`"filesystem":{"allowWrite":["` + cache + `"],"denyRead":["` + private + `"]}}}`
	want := strings.Join([]string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--setting-sources=", "--strict-mcp-config", "--settings", settings,
		"--tools", "Read,Edit,Write,Bash,Glob,Grep",
		"--permission-mode", "default", "--permission-prompt-tool", "stdio",
		"--session-id", "<uuid>", "--no-session-persistence",
		"--model", "haiku", "--append-system-prompt", "Use tabs.",
	}, "\x00")
	if argv != want {
		t.Errorf("argv\n got %q\nwant %q", strings.Split(argv, "\x00"), strings.Split(want, "\x00"))
	}

	// Names and values of the synthetic environment only: no real one.
	got := map[string]string{}
	for _, kv := range rec.Env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for _, name := range append(append([]string{}, stripEnv...), "CLAUDE_CODE_REMOTE", "CLAUDE_CODE_REMOTE_SESSION_ID") {
		if _, ok := got[name]; ok {
			t.Errorf("session marker %s reached claude", name)
		}
	}
	keep := map[string]string{
		"CLAUDE_CONFIG_DIR": "/synthetic/claude-config", "CLAUDE_CODE_USE_BEDROCK": "1",
		"ANTHROPIC_BASE_URL": "https://synthetic.example", "AWS_REGION": "eu-west-1",
		"CLAUDE_CODE_SUBAGENT_MODEL": "haiku", "HOME": "/synthetic/home",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1", "GOCACHE": cache, "OGE_RUN_ID": "run-1", "OGE_ROLE": "implementer",
	}
	for k, v := range keep {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// Öge adds nothing else: the child's names are the parent's, minus the
	// strip list, plus Öge's own and the fake's test knobs.
	for k := range got {
		if strings.HasPrefix(k, "OGE_FAKE_CLAUDE_") || k == "PATH" || k == "PWD" {
			continue
		}
		if _, ok := keep[k]; !ok {
			t.Errorf("unexpected variable %s in the child environment", k)
		}
	}

	// The initialize request registers Öge's PreToolUse hook.
	first := strings.SplitN(readFile(t, h.stdin), "\n", 2)[0]
	if !strings.Contains(first, `"subtype":"initialize"`) || !strings.Contains(first, `"PreToolUse":[{"hookCallbackIds":["`+hookID+`"],"matcher":null}]`) {
		t.Errorf("initialize didn't register the hook: %s", first)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// turns.ndjson: two turns in one process, tool use, can_use_tool, and a
// recorded envelope whose tool list is narrower than the profile's (warn).
func TestTwoTurnsWithToolActivityAndAWarnedEnvelope(t *testing.T) {
	h := open(t, fixture(t, "turns.ndjson"))
	evs := h.turn("edit hello.txt")
	if evs[0].Kind != agent.TurnAccepted {
		t.Errorf("first event %v, want accepted before anything else", evs[0].Kind)
	}
	var opened *agent.SessionInfo
	var warnings, tools []string
	var hosts []agent.HostDecision
	for _, e := range evs {
		switch e.Kind {
		case agent.SessionOpened:
			opened = e.Session
		case agent.Warning:
			warnings = append(warnings, e.Text)
		case agent.Claim:
			if e.Tool != "" {
				tools = append(tools, e.Tool+" "+e.Target)
			}
		case agent.HostRequest:
			hosts = append(hosts, *e.Host)
		}
	}
	if opened == nil || opened.AgentVersion != "2.1.289" || opened.Envelope != "warn" || opened.AuthSource != "apiKeySource:none" {
		t.Fatalf("SessionOpened = %+v", opened)
	}
	if strings.Join(opened.Capabilities, ",") != "host_requests,deny_reason_reaches_model,interrupt" {
		t.Errorf("Capabilities = %v", opened.Capabilities)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "missing tools Bash, Glob, Grep") {
		t.Errorf("warnings = %q", warnings)
	}
	if strings.Join(tools, "|") != "Read hello.txt|Edit hello.txt" {
		t.Errorf("tool activity = %q", tools)
	}
	if len(hosts) != 1 || hosts[0].Decision != "allow" || hosts[0].Rule != rulePreAuthorised ||
		hosts[0].Reason != "pre-authorised by Launch profile "+(profile{role: "implementer", tools: implementerTools}).hash() {
		t.Errorf("Host requests = %+v", hosts)
	}
	if s := settled(t, evs); s.Exit != "done" || s.Failure != "" {
		t.Errorf("settled %+v", s)
	}

	// The Session takes a second turn; system/init comes again and isn't
	// a second SessionOpened.
	evs = h.turn("edit calc")
	for _, e := range evs {
		if e.Kind == agent.SessionOpened {
			t.Error("a second SessionOpened")
		}
	}
	if s := settled(t, evs); s.Exit != "done" {
		t.Errorf("second turn settled %+v", s)
	}
	h.sess.Close()
	if !strings.Contains(readFile(t, h.stdin), `"behavior":"allow","updatedInput":{"file_path":"`+h.ws+`/hello.txt"`) {
		t.Errorf("the can_use_tool allow didn't carry the tool input:\n%s", readFile(t, h.stdin))
	}
	if _, ok := <-h.sess.Events(); ok {
		t.Error("events still open after Close")
	}
}

func TestEnvelopePasses(t *testing.T) {
	h := open(t, editInit(t, firstTurn(t, "turns.ndjson"), exactTools))
	evs := h.turn("hi")
	for _, e := range evs {
		if e.Kind == agent.SessionOpened && e.Session.Envelope != "ok" {
			t.Errorf("envelope %q", e.Session.Envelope)
		}
		if e.Kind == agent.Warning {
			t.Errorf("warning: %s", e.Text)
		}
	}
	if s := settled(t, evs); s.Exit != "done" {
		t.Errorf("settled %+v", s)
	}
}

// A permission mode Öge didn't ask for breaks the profile for every role:
// the turn stops before the model's work counts.
func TestEnvelopeFailsClosed(t *testing.T) {
	h := open(t, editInit(t, firstTurn(t, "turns.ndjson"), func(m map[string]any) {
		exactTools(m)
		m["permissionMode"] = "bypassPermissions"
	}))
	evs := h.turn("hi")
	s := settled(t, evs)
	if s.Stop == "" || !strings.Contains(s.Stop, `permission mode is "bypassPermissions"`) || s.Exit != "" {
		t.Errorf("settled %+v", s)
	}
	for _, e := range evs {
		if e.Kind == agent.HostRequest {
			t.Error("a Host request was answered after the envelope failed")
		}
	}
}

func TestEnvelopeRules(t *testing.T) {
	ws := t.TempDir()
	base := initFrame{Cwd: ws, Tools: implementerTools, PermissionMode: "default", Version: "2.1.289", OutputStyle: "default"}
	cases := map[string]struct {
		role  string
		edit  func(*initFrame)
		hooks bool
		warn  string
		fatal string
	}{
		"pass":                     {role: "implementer", edit: func(*initFrame) {}},
		"memory warns implementer": {role: "implementer", edit: func(f *initFrame) { f.MemoryPaths = json.RawMessage(`{"auto":"/x"}`) }, warn: "memory is loaded"},
		"memory fails verifier":    {role: "verifier", edit: func(f *initFrame) { f.MemoryPaths = json.RawMessage(`{"auto":"/x"}`) }, fatal: "memory is loaded"},
		"hooks fail verifier":      {role: "verifier", edit: func(*initFrame) {}, hooks: true, fatal: "hooks ran at startup"},
		"output style":             {role: "verifier", edit: func(f *initFrame) { f.OutputStyle = "Learning" }, fatal: `output style "Learning"`},
		"mcp warns implementer": {role: "implementer", edit: func(f *initFrame) {
			f.MCPServers = append(f.MCPServers, struct {
				Name string `json:"name"`
			}{"gh"})
		}, warn: "MCP servers: gh"},
		"plugin warns": {role: "verifier", edit: func(f *initFrame) {
			f.Plugins = append(f.Plugins, struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			}{"org", "org@corp"})
		}, warn: "1 plugins (org)"},
		"builtin plugin is fine": {role: "verifier", edit: func(f *initFrame) {
			f.Plugins = append(f.Plugins, struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			}{"x", "x@builtin"})
		}},
		"wrong cwd":   {role: "implementer", edit: func(f *initFrame) { f.Cwd = "/elsewhere" }, fatal: "working directory"},
		"too old":     {role: "implementer", edit: func(f *initFrame) { f.Version = "2.1.200" }, fatal: "older than the oldest supported"},
		"newer warns": {role: "implementer", edit: func(f *initFrame) { f.Version = "2.2.0" }, warn: "newer than the last tested"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := base
			c.edit(&in)
			w, f := envelope{role: c.role, cwd: ws, tools: implementerTools}.check(in, c.hooks)
			if c.fatal == "" && f != "" || c.fatal != "" && !strings.Contains(f, c.fatal) {
				t.Errorf("fatal = %q, want %q", f, c.fatal)
			}
			ws := strings.Join(w, "; ")
			if c.warn == "" && ws != "" || c.warn != "" && !strings.Contains(ws, c.warn) {
				t.Errorf("warnings = %q, want %q", ws, c.warn)
			}
		})
	}
}

// interrupt.ndjson: the interrupt settles the turn, and the Session takes
// the next one.
func TestInterruptLeavesTheSessionUsable(t *testing.T) {
	h := open(t, fixture(t, "interrupt.ndjson"))
	if err := h.sess.Send(agent.Turn{Text: "count slowly"}); err != nil {
		t.Fatal(err)
	}
	if err := h.sess.Send(agent.Turn{Text: "again"}); err != agent.ErrTurnInFlight {
		t.Errorf("a second Send in flight = %v, want ErrTurnInFlight", err)
	}
	if err := h.sess.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if s := settled(t, h.until()); s.Failure != "interrupted" {
		t.Errorf("interrupted turn settled %+v", s)
	}
	if s := settled(t, h.turn("say alive")); s.Exit != "done" {
		t.Errorf("turn after the interrupt settled %+v", s)
	}
}

func TestUnknownMessagesAreNotFatal(t *testing.T) {
	frames := insertAfter(firstTurn(t, "turns.ndjson"), `"subtype": "init"`,
		`{"dir": "out", "msg": {"type": "brand_new_message", "payload": {"x": 1}}}`,
		`{"dir": "out", "msg": {"type": "system", "subtype": "brand_new_subtype"}}`)
	evs := replay(t, frames).turn("hi")
	var unknown []string
	for _, e := range evs {
		if e.Kind == agent.Unknown {
			unknown = append(unknown, e.Text)
		}
	}
	if strings.Join(unknown, ",") != "brand_new_message,system/brand_new_subtype" {
		t.Errorf("unknown = %q", unknown)
	}
	if s := settled(t, evs); s.Exit != "done" {
		t.Errorf("settled %+v", s)
	}
}

func replay(t *testing.T, frames []string) *harness { return open(t, frames) }

func TestMalformedFrameIsAnAttemptFailure(t *testing.T) {
	frames := insertAfter(firstTurn(t, "turns.ndjson"), `"subtype": "init"`, `{"dir": "raw", "line": "{not json"}`)
	s := settled(t, replay(t, frames).turn("hi"))
	if !strings.HasPrefix(s.Failure, "malformed_frame") || s.Stop != "" {
		t.Errorf("settled %+v", s)
	}
}

func TestCrashIsAnAttemptFailure(t *testing.T) {
	frames := insertAfter(firstTurn(t, "turns.ndjson"), `"subtype": "init"`, `{"dir": "die"}`)
	s := settled(t, replay(t, frames).turn("hi"))
	if !strings.HasPrefix(s.Failure, "agent_crash") {
		t.Errorf("settled %+v", s)
	}
}

func TestOpenFailsWhenClaudeExitsEarly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	fx := filepath.Join(dir, "f.ndjson")
	os.WriteFile(fx, []byte(`{"dir": "die"}`+"\n"), 0o644)
	a := &Adapter{Path: fakePath, Environ: func() []string { return []string{"OGE_FAKE_CLAUDE_FIXTURE=" + fx} }}
	_, err := a.Open(context.Background(), agent.LaunchSpec{Role: "implementer", Workspace: dir})
	if err == nil || !strings.Contains(err.Error(), "claude exited before the session started") {
		t.Errorf("Open = %v", err)
	}
	if _, err := a.Open(context.Background(), agent.LaunchSpec{Role: "verifier", Workspace: dir}); err == nil {
		t.Error("Open for a role without a profile succeeded")
	}
}

func TestAuthAndQuotaSignalsAreInfrastructureStops(t *testing.T) {
	for name, frame := range map[string]string{
		"auth":  `{"dir": "out", "msg": {"type": "system", "subtype": "api_retry", "attempt": 1, "max_retries": 10, "retry_delay_ms": 500, "error_status": 401, "error": "authentication_failed"}}`,
		"login": `{"dir": "out", "msg": {"type": "assistant", "message": {"content": [{"type": "text", "text": "Login expired"}]}, "parent_tool_use_id": null, "error": "authentication_failed"}}`,
		"quota": `{"dir": "out", "msg": {"type": "rate_limit_event", "rate_limit_info": {"status": "rejected", "rateLimitType": "five_hour"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			frames := insertAfter(firstTurn(t, "turns.ndjson"), `"subtype": "init"`, frame)
			s := settled(t, replay(t, frames).turn("hi"))
			if s.Stop == "" || s.Failure != "" {
				t.Errorf("settled %+v", s)
			}
		})
	}
	// A transient API error that ends the turn in an error is a stop too;
	// one the turn recovers from isn't.
	frames := firstTurn(t, "turns.ndjson")
	frames = insertAfter(frames, `"subtype": "init"`, `{"dir": "out", "msg": {"type": "system", "subtype": "api_retry", "error": "rate_limit"}}`)
	if s := settled(t, replay(t, frames).turn("hi")); s.Exit != "done" {
		t.Errorf("recovered turn settled %+v", s)
	}
	for i, l := range frames {
		if strings.Contains(l, `"type": "result"`) {
			frames[i] = `{"dir": "out", "msg": {"type": "result", "subtype": "error_during_execution", "is_error": true, "num_turns": 1}}`
		}
	}
	if s := settled(t, replay(t, frames).turn("hi")); !strings.Contains(s.Stop, "rate_limit") {
		t.Errorf("failed turn settled %+v", s)
	}
}

// hook_decider.ndjson: every tool call reaches Öge through the PreToolUse
// hook, and Öge's policy answers each one, as recorded in the events.
func TestPermissionRequestsFollowThePolicy(t *testing.T) {
	h := open(t, fixture(t, "hook_decider.ndjson"))
	evs := h.turn("do the steps")
	var got []string
	for _, e := range evs {
		if e.Kind == agent.HostRequest {
			got = append(got, e.Host.Tool+" "+e.Host.Target+" → "+e.Host.Decision+" ("+e.Host.Rule+")")
		}
	}
	want := []string{
		"Read tests/test_calc.py → allow (pre_authorised)",
		"Edit tests/test_calc.py → allow (pre_authorised)",
		"Read src/calc.py → allow (pre_authorised)",
		"Write src/new.py → allow (pre_authorised)",
		"Bash echo y > src/bash.txt; echo rc=$? → deny (no_interactive_approval)",
		"Bash echo y > tests/bash.txt; echo rc=$? → deny (no_interactive_approval)",
		"Write tests/test_new.py → allow (pre_authorised)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("decisions\n got %q\nwant %q", got, want)
	}
	h.sess.Close()
	var allows, denies int
	sc := bufio.NewScanner(strings.NewReader(readFile(t, h.stdin)))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		l := sc.Text()
		if strings.Contains(l, `"continue":true`) {
			allows++
		}
		if strings.Contains(l, `"permissionDecision":"deny","permissionDecisionReason":"Öge denied this: this command isn't pre-authorised.`) {
			denies++
		}
	}
	if allows != 5 || denies != 2 {
		t.Errorf("hook answers: %d allow, %d deny", allows, denies)
	}
}

// A can_use_tool for a call the hook already decided gets the same answer,
// and isn't a second Host request.
func TestOneDecisionPerToolUse(t *testing.T) {
	frames := fixture(t, "hook_decider.ndjson")
	var out []string
	for _, l := range frames {
		out = append(out, l)
		if strings.Contains(l, `"request_id": "8f353bbd`) && strings.Contains(l, `"dir": "in"`) {
			out = append(out,
				`{"dir": "out", "msg": {"type": "control_request", "request_id": "cut-1", "request": {"subtype": "can_use_tool", "tool_name": "Read", "input": {"file_path": "/home/user/project/tests/test_calc.py"}, "tool_use_id": "toolu_01CwqJqvrFHcrWFnmVysSd1T"}}}`,
				`{"dir": "in", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "cut-1", "response": {}}}}`)
		}
	}
	h := open(t, out)
	n := 0
	for _, e := range h.turn("go") {
		if e.Kind == agent.HostRequest {
			n++
		}
	}
	if n != 7 {
		t.Errorf("%d Host requests, want 7", n)
	}
}

func TestPolicy(t *testing.T) {
	dir := t.TempDir()
	// The default state dir on macOS has a space in it.
	ws, private := filepath.Join(dir, "Application Support", "ws"), filepath.Join(dir, "private")
	esc := strings.ReplaceAll(ws, " ", `\\ `)
	os.MkdirAll(filepath.Join(ws, "pkg"), 0o755)
	os.MkdirAll(private, 0o700)
	os.Symlink("/etc", filepath.Join(ws, "escape"))
	p := newPolicy(agent.LaunchSpec{Workspace: ws, DenyRead: []string{private}, CheckCommands: []string{"make check"}}, "abc")
	cases := []struct {
		tool, input, decision, rule string
	}{
		{"Read", `{"file_path":"` + ws + `/pkg/a.go"}`, "allow", rulePreAuthorised},
		{"Read", `{"file_path":"/etc/passwd"}`, "deny", ruleOutside},
		{"Read", `{"file_path":"` + private + `/runs/x/ledger.jsonl"}`, "deny", ruleOutside},
		{"Read", `{"file_path":"` + ws + `/escape/passwd"}`, "deny", ruleOutside},
		{"Glob", `{"pattern":"**/*.go"}`, "allow", rulePreAuthorised},
		{"Grep", `{"pattern":"Add","path":"/"}`, "deny", ruleOutside},
		{"Edit", `{"file_path":"` + ws + `/add.go"}`, "allow", rulePreAuthorised},
		{"Write", `{"file_path":"` + ws + `/new/dir/x.go"}`, "allow", rulePreAuthorised},
		{"Write", `{"file_path":"` + ws + `/../x.go"}`, "deny", ruleOutside},
		{"Edit", `{"file_path":"` + ws + `/.git/config"}`, "deny", ruleOutside},
		{"Bash", `{"command":"go test ./..."}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"go test -json ./... 2>&1 | tail -30"}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"cd ` + ws + ` && go vet ./pkg"}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"cd /tmp && go vet ./pkg"}`, "deny", ruleOutside},
		{"Bash", `{"command":"cd ` + esc + ` && go test -json ./..."}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"cd \"` + ws + `\" && go test ./pkg"}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"ls ` + esc + `/pkg"}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"find . -name '*.go'"}`, "deny", ruleNoInteractive},
		{"Bash", `{"command":"gofmt -l ."}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"make check"}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"git diff"}`, "allow", rulePreAuthorised},
		{"Bash", `{"command":"cat /etc/hosts"}`, "deny", ruleOutside},
		{"Bash", `{"command":"cat ../secret"}`, "deny", ruleOutside},
		{"Bash", `{"command":"ls ` + private + `"}`, "deny", ruleOutside},
		{"Bash", `{"command":"go test -exec=/bin/evil ./..."}`, "deny", ruleNoInteractive},
		{"Bash", `{"command":"go build -o /usr/local/bin/x ."}`, "deny", ruleOutside},
		{"Bash", `{"command":"git diff --output=x"}`, "deny", ruleNoInteractive},
		{"Bash", `{"command":"rm -rf ."}`, "deny", ruleNoInteractive},
		{"Bash", `{"command":"curl https://example.com"}`, "deny", ruleNoInteractive},
		{"Bash", `{"command":"go test ./... && rm -rf ."}`, "deny", ruleNoInteractive},
		{"Bash", `{"command":"go test $(evil)"}`, "deny", ruleNoInteractive},
		{"Bash", `{"command":"go test ./...\nrm x"}`, "deny", ruleNoInteractive},
		{"WebFetch", `{"url":"https://example.com"}`, "deny", ruleOutside},
		{"AskUserQuestion", `{"questions":[]}`, "cancel", ruleQuestion},
	}
	for _, c := range cases {
		d := p.decide(c.tool, json.RawMessage(c.input))
		if d.Decision != c.decision || d.Rule != c.rule {
			t.Errorf("%s %s = %s (%s), want %s (%s)", c.tool, c.input, d.Decision, d.Rule, c.decision, c.rule)
		}
		if d.By != "launch_profile:abc" {
			t.Errorf("By = %q", d.By)
		}
		if c.decision == "deny" && !strings.HasPrefix(d.Reason, "Öge denied this: ") {
			t.Errorf("deny reason %q", d.Reason)
		}
	}
	if d := p.decide("AskUserQuestion", nil); d.Family != agent.Question || strings.Contains(d.Reason, "Öge denied") {
		t.Errorf("question = %+v", d)
	}
}

func TestExitOf(t *testing.T) {
	for in, want := range map[string]string{
		"": "done", "Fixed it.": "done", "Fixed.\nExit: done": "done", "Can't.\n**Exit: infeasible**": "infeasible",
		"exit: infeasible.": "infeasible", "Exit: bogus": "done",
	} {
		if got := exitOf(in); got != want {
			t.Errorf("exitOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolTargetsAreShortAndRedacted(t *testing.T) {
	ws := "/w"
	in := map[string]any{"command": "export ANTHROPIC_API_KEY=sk-ant-abcdefghijklmnopqrstuvwx && go test\nsecond line"}
	got := target("Bash", in, []string{ws})
	if strings.Contains(got, "abcdefghij") || strings.Contains(got, "second line") {
		t.Errorf("target = %q", got)
	}
	if got := target("Edit", map[string]any{"file_path": "/w/a/b.go", "old_string": "secret body"}, []string{ws}); got != "a/b.go" {
		t.Errorf("Edit target = %q", got)
	}
	sp := "/Users/u/Application Support/w"
	if got := target("Bash", map[string]any{"command": `cd /Users/u/Application\ Support/w && go test ./...`}, []string{sp}); got != "go test ./..." {
		t.Errorf("cd target = %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := target("Bash", map[string]any{"command": long}, []string{ws}); len([]rune(got)) != 80 {
		t.Errorf("long target has %d runes", len([]rune(got)))
	}
}
