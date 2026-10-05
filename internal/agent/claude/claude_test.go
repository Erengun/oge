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
	settings := regexp.MustCompile(`--settings\x00([^\x00]*)\x00`)
	m := settings.FindStringSubmatch(argv)
	if m == nil {
		t.Fatalf("no --settings in %q", rec.Argv)
	}
	argv = settings.ReplaceAllString(argv, "--settings\x00<settings>\x00")
	want := strings.Join([]string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--setting-sources=", "--strict-mcp-config", "--settings", "<settings>",
		"--tools", "Read,Edit,Write,Bash,Glob,Grep",
		"--permission-mode", "default", "--permission-prompt-tool", "stdio",
		"--session-id", "<uuid>", "--no-session-persistence",
		"--model", "haiku", "--append-system-prompt", "Use tabs.",
	}, "\x00")
	if argv != want {
		t.Errorf("argv\n got %q\nwant %q", strings.Split(argv, "\x00"), strings.Split(want, "\x00"))
	}

	// The policy JSON: sandbox on and unescapable, the cache the only
	// extra writable path, private state and home secrets unreadable,
	// no network domain allowed and the network tools denied.
	private, cache, home := h.spec.DenyRead[0], h.spec.Cache, "/synthetic/home"
	var got, wantS any
	if err := json.Unmarshal([]byte(m[1]), &got); err != nil {
		t.Fatal(err)
	}
	reads := []string{private}
	for _, s := range homeSecrets {
		reads = append(reads, home+"/"+s)
	}
	reads = append(reads, "/synthetic/claude-config")
	ws, _ := json.Marshal(map[string]any{
		"permissions": map[string]any{"deny": []string{"Read(/" + private + "/**)", "Edit(/" + private + "/**)", "WebFetch", "WebSearch"}},
		"sandbox": map[string]any{
			"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false,
			"filesystem": map[string]any{"allowWrite": []string{cache}, "denyRead": reads},
			"network":    map[string]any{"allowedDomains": []string{}, "allowLocalBinding": false},
		},
	})
	json.Unmarshal(ws, &wantS)
	if gb, _ := json.Marshal(got); string(gb) != func() string { b, _ := json.Marshal(wantS); return string(b) }() {
		t.Errorf("settings\n got %s\nwant %s", gb, ws)
	}
	for _, s := range []string{".ssh", ".aws", ".gnupg", ".config/gh", ".netrc", ".docker/config.json", ".kube",
		".git-credentials", ".config/gcloud", ".azure", ".npmrc", ".pypirc", ".cargo/credentials", ".terraform.d"} {
		if !oneOf(s, homeSecrets) {
			t.Errorf("%s isn't denied", s)
		}
	}

	// Names and values of the synthetic environment only: no real one.
	env := map[string]string{}
	for _, kv := range rec.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, name := range append(append([]string{}, stripEnv...), "CLAUDE_CODE_REMOTE", "CLAUDE_CODE_REMOTE_SESSION_ID") {
		if _, ok := env[name]; ok {
			t.Errorf("session marker %s reached claude", name)
		}
	}
	keep := map[string]string{
		"CLAUDE_CONFIG_DIR": "/synthetic/claude-config", "CLAUDE_CODE_USE_BEDROCK": "1",
		"ANTHROPIC_BASE_URL": "https://synthetic.example", "AWS_REGION": "eu-west-1",
		"CLAUDE_CODE_SUBAGENT_MODEL": "haiku", "HOME": home,
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1", "GIT_OPTIONAL_LOCKS": "0", "GOCACHE": cache,
		"OGE_RUN_ID": "run-1", "OGE_ROLE": "implementer",
	}
	for k, v := range keep {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	// Öge adds nothing else: the child's names are the parent's, minus the
	// strip list, plus Öge's own and the fake's test knobs.
	for k := range env {
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

// answers are the host's control responses the fake read, by request id.
func answers(t *testing.T, h *harness) map[string]string {
	t.Helper()
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(readFile(t, h.stdin)))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m struct {
			Type     string
			Response struct {
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
			}
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Type == "control_response" {
			out[m.Response.RequestID] = string(m.Response.Response)
		}
	}
	return out
}

// expect sets, in order, the decision each "in" control response of
// frames must carry; the fake fails the replay on any other.
func expect(frames []string, decisions ...string) []string {
	out := append([]string{}, frames...)
	for i, l := range out {
		if len(decisions) == 0 {
			break
		}
		if strings.Contains(l, `"dir": "in"`) && strings.Contains(l, `"type": "control_response"`) {
			out[i] = strings.Replace(l, `{"dir": "in",`, `{"dir": "in", "expect": "`+decisions[0]+`",`, 1)
			decisions = decisions[1:]
		}
	}
	return out
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
	h := open(t, expect(fixture(t, "turns.ndjson"), "allow", "allow"))
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
	// The recording's residue: one plugin, no skills, a built-in agent.
	if r := opened.Residue; r == nil || strings.Join(r.Plugins, ",") != "example-plugin@builtin" || len(r.Skills)+len(r.Agents) != 0 || r.Fingerprint == "" {
		t.Errorf("Residue = %+v", opened.Residue)
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
	a := answers(t, h)
	if got := a["ff052cc2-c70c-496a-ab3e-85f90ec23913"]; got != `{"behavior":"allow","updatedInput":{"file_path":"`+h.ws+`/hello.txt","old_string":"hi","new_string":"hello","replace_all":false}}` {
		t.Errorf("the can_use_tool answer = %s", got)
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

// realMemoryPaths is memory_paths as claude 2.1.289 reports it when
// memory loads (spike log leak_default.ndjson at ab056605bbb3; isolated
// launches leave the field out, as in testdata).
const realMemoryPaths = `{"auto": "/home/user/.claude/projects/-home-user-project/memory/"}`

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
		"memory warns implementer": {role: "implementer", edit: func(f *initFrame) { f.MemoryPaths = json.RawMessage(realMemoryPaths) }, warn: "memory is loaded"},
		"memory fails verifier":    {role: "verifier", edit: func(f *initFrame) { f.MemoryPaths = json.RawMessage(realMemoryPaths) }, fatal: "memory is loaded"},
		"hooks fail verifier":      {role: "verifier", edit: func(*initFrame) {}, hooks: true, fatal: "hooks ran at startup"},
		"output style":             {role: "verifier", edit: func(f *initFrame) { f.OutputStyle = "Learning" }, fatal: `output style "Learning"`},
		"mcp warns implementer": {role: "implementer", edit: func(f *initFrame) {
			f.MCPServers = append(f.MCPServers, struct {
				Name string `json:"name"`
			}{"gh"})
		}, warn: "MCP servers: gh"},
		"residue isn't a warning": {role: "implementer", edit: func(f *initFrame) {
			f.Skills, f.Agents = []string{"deep-research"}, []string{"reviewer"}
			f.Plugins = append(f.Plugins, initPlugin{Name: "org", Path: "/home/user/.claude/plugins/org", Source: "org@corp"})
		}},
		"residue fails verifier": {role: "verifier", edit: func(f *initFrame) {
			f.Skills, f.Agents = []string{"deep-research"}, []string{"reviewer"}
			f.Plugins = append(f.Plugins, initPlugin{Name: "org", Path: "/home/user/.claude/plugins/org", Source: "org@corp"})
		}, fatal: "the verifier would load 1 plugins, 1 skills, 1 subagents that Öge can't remove"},
		"built-in subagents don't fail verifier":         {role: "verifier", edit: func(f *initFrame) { f.Agents = builtinAgents }},
		"the pinned builtin plugins don't fail verifier": {role: "verifier", edit: func(f *initFrame) { f.Plugins = pluginsOf(builtinPlugins...) }},
		"fewer builtin plugins fail verifier": {role: "verifier", edit: func(f *initFrame) { f.Plugins = pluginsOf(builtinPlugins[1:]...) },
			fatal: "the verifier would load 3 plugins"},
		"a repeated builtin plugin fails verifier": {role: "verifier", edit: func(f *initFrame) {
			f.Plugins = pluginsOf(builtinPlugins[0], builtinPlugins[0], builtinPlugins[1], builtinPlugins[2])
		}, fatal: "the verifier would load 4 plugins"},
		"a user marketplace named builtin fails verifier": {role: "verifier", edit: func(f *initFrame) {
			f.Plugins = pluginsOf(builtinPlugins...)
			f.Plugins[0].Path = "/home/user/.claude/plugins/marketplaces/builtin/cc-plugin-agents-md"
		}, fatal: "the verifier would load 4 plugins"},
		"another builtin plugin fails verifier": {role: "verifier", edit: func(f *initFrame) {
			f.Plugins = pluginsOf(append(append([]string{}, builtinPlugins...), "cc-plugin-new@builtin")...)
		}, fatal: "the verifier would load 5 plugins"},
		"builtin plugins with a skill fail verifier": {role: "verifier", edit: func(f *initFrame) {
			f.Plugins, f.Skills = pluginsOf(builtinPlugins...), []string{"debug"}
		}, fatal: "4 plugins, 1 skills"},
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
	// The interrupt Capability is known once system/init arrives.
	for e := range h.sess.Events() {
		if e.Kind == agent.SessionOpened {
			break
		}
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

// Before system/init reports the Capability, no interrupt is sent.
func TestNoInterruptBeforeTheEnvelope(t *testing.T) {
	frames := fixture(t, "interrupt.ndjson")[:4] // initialize, its response, the user turn
	h := open(t, append(frames, `{"dir": "in", "msg": {"type": "user"}}`))
	if err := h.sess.Send(agent.Turn{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := h.sess.Interrupt(); err != ErrNoInterrupt {
		t.Errorf("Interrupt before system/init = %v, want ErrNoInterrupt", err)
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
// hook, and Öge's policy answers each one; the fake checks each answer.
func TestPermissionRequestsFollowThePolicy(t *testing.T) {
	h := open(t, expect(fixture(t, "hook_decider.ndjson"), "allow", "allow", "allow", "allow", "deny", "deny", "allow"))
	evs := h.turn("do the steps")
	s := settled(t, evs)
	if s.Exit != "done" {
		t.Fatalf("settled %+v (the fake rejects a wrong answer)", s)
	}
	// Recorded wire order: two denied Bash calls, each in a turn of its
	// own with nothing allowed (#90).
	if want := (agent.Friction{Denied: 2, LostTurns: 2}); s.Friction == nil || *s.Friction != want {
		t.Errorf("friction %+v, want %+v", s.Friction, want)
	}
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
	a := answers(t, h)
	if got := a["8f353bbd-b898-492d-81fc-8ae5f166ef75"]; got != `{"continue":true}` {
		t.Errorf("hook allow = %s", got)
	}
	deny := a["b5740a7a-af7a-4c29-b960-b6d5f29ba72e"]
	if !strings.HasPrefix(deny, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Öge denied this: this command isn't pre-authorised.`) {
		t.Errorf("hook deny = %s", deny)
	}
}

// A can_use_tool for a call the hook already decided gets the same answer,
// and isn't a second Host request.
func TestOneDecisionPerToolUse(t *testing.T) {
	for _, c := range []struct {
		hookReq, useID, path, decision, answer string
	}{
		// The allowed Read of tests/test_calc.py.
		{"8f353bbd", "toolu_01CwqJqvrFHcrWFnmVysSd1T", "tests/test_calc.py", "allow", `{"behavior":"allow","updatedInput":{"file_path":"WS/tests/test_calc.py"}}`},
		// The denied Bash write to src/bash.txt.
		{"b5740a7a", "toolu_01EyGiytsEL1yqhMuoew4oT4", "src/bash.txt", "deny", `{"behavior":"deny","message":"Öge denied this: this command isn't pre-authorised.`},
	} {
		var frames []string
		for _, l := range expect(fixture(t, "hook_decider.ndjson"), "allow", "allow", "allow", "allow", "deny", "deny", "allow") {
			frames = append(frames, l)
			if strings.Contains(l, `"request_id": "`+c.hookReq) && strings.Contains(l, `"dir": "in"`) {
				frames = append(frames,
					`{"dir": "out", "msg": {"type": "control_request", "request_id": "cut-1", "request": {"subtype": "can_use_tool", "tool_name": "Read", "input": {"file_path": "/home/user/project/`+c.path+`"}, "tool_use_id": "`+c.useID+`"}}}`,
					`{"dir": "in", "expect": "`+c.decision+`", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "cut-1", "response": {}}}}`)
			}
		}
		h := open(t, frames)
		n := 0
		for _, e := range h.turn("go") {
			if e.Kind == agent.HostRequest {
				n++
			}
		}
		if n != 7 {
			t.Errorf("%d Host requests, want 7", n)
		}
		h.sess.Close()
		want := strings.ReplaceAll(c.answer, "WS", h.ws)
		if got := answers(t, h)["cut-1"]; !strings.HasPrefix(got, want) {
			t.Errorf("can_use_tool after the hook = %s, want %s", got, want)
		}
	}
}

// No tool request is authorised before the envelope check passes
// (ADR-0005 as amended).
func TestNoAuthorisationBeforeTheEnvelopePasses(t *testing.T) {
	early := `{"dir": "out", "msg": {"type": "control_request", "request_id": "early", "request": {"subtype": "hook_callback", "callback_id": "oge_pre_tool_use", "input": {"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": {"file_path": "/home/user/project/hello.txt"}, "tool_use_id": "toolu_early"}}}}`
	frames := insertAfter(firstTurn(t, "turns.ndjson"), `"type": "command_lifecycle", "command_uuid": "e09658db-c6ad-4d29-9b9f-ca79ca4484da", "state": "started"`,
		early, `{"dir": "in", "expect": "deny", "msg": {"type": "control_response", "response": {"subtype": "success", "request_id": "early"}}}`)
	h := open(t, frames)
	evs := h.turn("hi")
	var first *agent.HostDecision
	for _, e := range evs {
		if e.Kind == agent.HostRequest && first == nil {
			first = e.Host
		}
	}
	if first == nil || first.Decision != "deny" || first.Rule != ruleUnchecked {
		t.Errorf("early request = %+v", first)
	}
	s := settled(t, evs)
	if s.Exit != "done" {
		t.Errorf("settled %+v", s)
	}
	// A refusal for timing isn't policy friction (#90).
	if want := (agent.Friction{EnvelopeRefusals: 1}); s.Friction == nil || *s.Friction != want {
		t.Errorf("friction %+v, want %+v", s.Friction, want)
	}
}

// A request buffered right behind a failing init is refused too: the
// envelope fails, and nothing is authorised after it.
func TestNoAuthorisationAfterAFailedEnvelope(t *testing.T) {
	frames := editInit(t, firstTurn(t, "turns.ndjson"), func(m map[string]any) { m["permissionMode"] = "bypassPermissions" })
	var initLine string
	var out []string
	for _, l := range frames {
		if strings.Contains(l, `"subtype": "init"`) || strings.Contains(l, `"subtype":"init"`) {
			var e struct {
				Msg json.RawMessage `json:"msg"`
			}
			json.Unmarshal([]byte(l), &e)
			initLine = string(e.Msg)
			// init and a tool request in one write, so both are buffered.
			hook := `{"type": "control_request", "request_id": "late", "request": {"subtype": "hook_callback", "callback_id": "oge_pre_tool_use", "input": {"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": {"file_path": "/home/user/project/hello.txt"}, "tool_use_id": "toolu_late"}}}`
			b, _ := json.Marshal(map[string]any{"dir": "raw", "line": initLine + "\n" + hook})
			out = append(out, string(b))
			continue
		}
		out = append(out, l)
	}
	h := open(t, out)
	if err := h.sess.Send(agent.Turn{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	var hosts []agent.HostDecision
	var stop string
	timeout := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case e, ok := <-h.sess.Events():
			if !ok {
				done = true
				break
			}
			if e.Kind == agent.HostRequest {
				hosts = append(hosts, *e.Host)
			}
			if e.Kind == agent.TurnSettled {
				stop = e.Stop
			}
		case <-timeout:
			t.Fatal("the stream never ended")
		}
	}
	if !strings.Contains(stop, "permission mode") {
		t.Errorf("stop = %q", stop)
	}
	if len(hosts) == 0 || hosts[0].Target != "/home/user/project/hello.txt" {
		t.Errorf("the buffered request wasn't answered: %+v", hosts)
	}
	for _, d := range hosts {
		if d.Decision != "deny" || d.Rule != ruleUnchecked {
			t.Errorf("a request after the failed envelope got %+v", d)
		}
	}
}

func TestPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the policy reasons about POSIX paths; Runs are refused on Windows (ADR-0017)")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The default state dir on macOS has a space in it.
	ws, private := filepath.Join(dir, "Application Support", "ws"), filepath.Join(dir, "private")
	esc := strings.ReplaceAll(ws, " ", `\ `)
	os.MkdirAll(filepath.Join(ws, "pkg", ".git"), 0o755)
	os.MkdirAll(filepath.Join(ws, ".git"), 0o755)
	os.MkdirAll(private, 0o700)
	os.Symlink("/etc", filepath.Join(ws, "escape"))
	os.Symlink("/etc/hosts", filepath.Join(ws, "hosts"))
	os.Symlink(filepath.Join(ws, "pkg"), filepath.Join(ws, "inner"))
	os.Symlink("/etc", filepath.Join(ws, "etcdir"))
	os.Symlink(private, filepath.Join(ws, "priv"))
	p := newPolicy(agent.LaunchSpec{Workspace: ws, DenyRead: []string{private}, CheckCommands: []string{"make check"}}, "abc")
	const (
		allow   = "allow"
		outside = ruleOutside
		grey    = ruleNoInteractive
	)
	bash := func(cmd string) string { b, _ := json.Marshal(map[string]string{"command": cmd}); return string(b) }
	fp := func(k, v string) string { b, _ := json.Marshal(map[string]string{k: v}); return string(b) }
	cases := []struct{ tool, input, want string }{
		// File tools.
		{"Read", fp("file_path", ws+"/pkg/a.go"), allow},
		{"Read", fp("file_path", "pkg/a.go"), allow},
		{"Read", fp("file_path", "/etc/passwd"), outside},
		{"Read", fp("file_path", "~/.ssh/id_ed25519"), outside},
		{"Read", fp("file_path", "$HOME/.netrc"), outside},
		{"Read", fp("file_path", "pkg/../../x"), outside},
		{"Read", fp("file_path", "pkg/../a.go"), outside}, // no relative ".." at all
		{"Read", fp("file_path", private+"/runs/x/ledger.jsonl"), outside},
		{"Read", fp("file_path", ws+"/escape/passwd"), outside},
		{"Read", fp("file_path", "C:\\Windows\\win.ini"), outside},
		{"Read", fp("file_path", `\\\\server\\share`), outside},
		{"Read", `{}`, outside},
		{"Glob", `{"pattern":"**/*.go"}`, allow},
		{"Glob", `{"pattern":"../**"}`, outside},
		{"Glob", `{"pattern":"pkg/{..,x}/*"}`, outside},
		{"Glob", `{"pattern":"/etc/*"}`, outside},
		{"Glob", fp("pattern", ws+"/pkg/*.go"), allow},
		{"Glob", `{"pattern":"*.go","path":"/"}`, outside},
		// A relative pattern's fixed prefix, or its first wildcard part,
		// can be a symlink out.
		{"Glob", `{"pattern":"etcdir/*"}`, outside},
		{"Glob", `{"pattern":"etc?ir/*"}`, outside},
		{"Glob", `{"pattern":"pri?/*"}`, outside},
		{"Glob", `{"pattern":"pkg/*.go"}`, allow},
		{"Grep", `{"pattern":"x","glob":"etcdir/**"}`, outside},
		{"Grep", `{"pattern":"x","glob":"pri?/**"}`, outside},
		{"Grep", `{"pattern":"x","glob":"*.go"}`, allow},
		{"Grep", `{"pattern":"Add"}`, allow},
		{"Grep", `{"pattern":"Add","path":"/"}`, outside},
		{"Grep", `{"pattern":"Add","glob":"../**/*.go"}`, outside},
		{"Grep", `{"pattern":"Add","path":"escape"}`, outside},
		{"Edit", fp("file_path", ws+"/add.go"), allow},
		{"Write", fp("file_path", ws+"/new/dir/x.go"), allow},
		{"Write", fp("file_path", ws+"/../x.go"), outside},
		{"Write", fp("file_path", ws+"/hosts"), outside},
		{"Edit", fp("file_path", ws+"/.git/config"), outside},
		{"Edit", fp("file_path", ws+"/.GIT/config"), outside},
		{"Edit", fp("file_path", ws+"/.Git/hooks/pre-commit"), outside},
		{"Write", fp("file_path", ws+"/pkg/.git/config"), outside},
		{"Write", fp("file_path", ws+"/inner/.git/HEAD"), outside},
		{"WebFetch", `{"url":"https://example.com"}`, outside},
		{"AskUserQuestion", `{"questions":[]}`, ruleQuestion},

		// Bash: the Check command, exactly.
		{"Bash", bash("make check"), allow},
		{"Bash", bash("make install"), grey},
		// go test, build, vet.
		{"Bash", bash("go test ./..."), allow},
		{"Bash", bash("go test -run TestAdd -v -count=1 -race -short -timeout 30s -json -cover ./pkg"), allow},
		{"Bash", bash("go test -exec=/bin/evil ./..."), grey},
		{"Bash", bash("go test -toolexec x ./..."), grey},
		{"Bash", bash("go test -c ./pkg"), grey},
		{"Bash", bash("go test -o x ./pkg"), grey},
		{"Bash", bash("go test -ldflags=-extld=/bin/evil ./..."), grey},
		{"Bash", bash("go test -gcflags=all=-N ./..."), grey},
		{"Bash", bash("go test -pkgdir=/tmp ./..."), grey},
		{"Bash", bash("go test ../.."), outside},
		{"Bash", bash("go build ./..."), allow},
		{"Bash", bash("go build -o bin/x ."), allow},
		{"Bash", bash("go build -o /usr/local/bin/x ."), outside},
		{"Bash", bash("go build -o .git/x ."), outside},
		{"Bash", bash("go build -ldflags=-extldflags=-x ."), grey},
		{"Bash", bash("go vet ./..."), allow},
		{"Bash", bash("go vet -vettool=/bin/evil ./..."), grey},
		{"Bash", bash("go run ."), grey},
		// gofmt.
		{"Bash", bash("gofmt -l ."), allow},
		{"Bash", bash("gofmt -s -w pkg/a.go"), allow},
		{"Bash", bash("gofmt -w /etc/x.go"), outside},
		{"Bash", bash("gofmt -w .git/x.go"), outside},
		{"Bash", bash("gofmt -r a->b ."), grey},
		// ls and cat.
		{"Bash", bash("ls"), allow},
		{"Bash", bash("ls -la pkg"), allow},
		{"Bash", bash("ls " + esc + "/pkg"), allow},
		{"Bash", bash(`ls "` + ws + `/pkg"`), allow},
		{"Bash", bash("ls .*/"), grey},
		{"Bash", bash(".[.]/"), grey},
		{"Bash", bash("ls .[.]/"), grey},
		{"Bash", bash("cat .*/.*/etc/hosts"), grey},
		{"Bash", bash("cat -n pkg/a.go"), allow},
		{"Bash", bash("cat /etc/hosts"), outside},
		{"Bash", bash("cat ../secret"), outside},
		{"Bash", bash("cat escape"), outside}, // a bare-word symlink out
		{"Bash", bash("cat hosts"), outside},
		{"Bash", bash("ls " + private), outside},
		{"Bash", bash("cat ~/.ssh/id_ed25519"), grey},
		{"Bash", bash("cat $HOME/.netrc"), grey},
		// find, read and search only.
		{"Bash", bash(`find . -name "*.go"`), allow},
		{"Bash", bash(`find pkg -type f -iname '*_test.go' -maxdepth 2`), allow},
		{"Bash", bash(`find . -newer pkg/a.go -print`), allow},
		{"Bash", bash("find . -name *.go"), grey}, // an unquoted glob
		{"Bash", bash("find . -exec rm {} ;"), grey},
		{"Bash", bash("find . -execdir x"), grey},
		{"Bash", bash("find . -ok rm"), grey},
		{"Bash", bash("find . -delete"), grey},
		{"Bash", bash("find . -fprint x"), grey},
		{"Bash", bash(`find . "-delete"`), grey},
		{"Bash", bash("find / -name x"), outside},
		{"Bash", bash("find -L . -name x"), grey},
		{"Bash", bash("find . -type c"), grey},
		// git, read-only.
		{"Bash", bash("git status"), allow},
		{"Bash", bash("git status --short"), allow},
		{"Bash", bash("git diff"), allow},
		{"Bash", bash("git diff --stat -- pkg"), allow},
		{"Bash", bash("git diff -U3 --no-color"), allow},
		{"Bash", bash("git diff --output=x"), grey},
		// Optional-argument flags only in their attached form: git reads a
		// separate next word as its own option.
		{"Bash", bash("git diff --unified=3"), allow},
		{"Bash", bash("git diff --unified --output=.git/config"), grey},
		{"Bash", bash("git diff --unified --output=add_test.go"), grey},
		{"Bash", bash("git diff --unified --ext-diff"), grey},
		{"Bash", bash("git diff --unified --textconv"), grey},
		{"Bash", bash("git diff --unified 3"), grey},
		{"Bash", bash("git diff --color --output=x"), grey},
		{"Bash", bash("git diff --color=never"), allow},
		{"Bash", bash("git diff -U --output=x"), grey},
		{"Bash", bash("git diff -U 3"), grey},
		{"Bash", bash("git status --untracked-files --output=x"), grey},
		{"Bash", bash("git status --untracked-files=no"), allow},
		{"Bash", bash("git diff --ext-diff"), grey},
		{"Bash", bash("git diff --no-index /etc/hosts pkg/a.go"), grey},
		{"Bash", bash("git -c core.pager=evil diff"), grey},
		{"Bash", bash("git add -A"), grey},
		{"Bash", bash("git commit -m x"), grey},
		{"Bash", bash("git checkout ."), grey},
		{"Bash", bash("git reset --hard"), grey},
		{"Bash", bash("git clean -fd"), grey},
		{"Bash", bash("git push"), grey},
		{"Bash", bash("git fetch"), grey},
		// Shells, chaining, substitution, redirection.
		{"Bash", bash("sh -c 'go test'"), grey},
		{"Bash", bash("bash -c ls"), grey},
		{"Bash", bash("cd " + esc + " && go test ./..."), grey},
		{"Bash", bash("go test ./... && rm -rf ."), grey},
		{"Bash", bash("go test ./...; rm x"), grey},
		{"Bash", bash("go test ./... | tail"), grey},
		{"Bash", bash("go test ./... 2>&1"), grey},
		{"Bash", bash("go test ./... > out"), grey},
		{"Bash", bash("go test $(evil)"), grey},
		{"Bash", bash("go test `evil`"), grey},
		{"Bash", bash("ls {a,b}"), grey},
		{"Bash", bash("ls !x"), grey},
		{"Bash", bash("go test ./...\nrm x"), grey},
		{"Bash", bash("ls 'unbalanced"), grey},
		{"Bash", bash("rm -rf ."), grey},
		{"Bash", bash("curl https://example.com"), grey},
		{"Bash", bash(""), grey},
	}
	for _, c := range cases {
		d := p.decide(c.tool, json.RawMessage(c.input))
		got := d.Rule
		if d.Decision == "allow" {
			got = allow
		}
		if got != c.want {
			t.Errorf("%s %s = %s (%s), want %s", c.tool, c.input, d.Decision, d.Rule, c.want)
		}
		if d.By != "launch_profile:abc" {
			t.Errorf("By = %q", d.By)
		}
		if d.Decision == "deny" && !strings.HasPrefix(d.Reason, "Öge denied this: ") {
			t.Errorf("deny reason %q", d.Reason)
		}
	}
	if d := p.decide("AskUserQuestion", nil); d.Family != agent.Question || d.Decision != "cancel" || strings.Contains(d.Reason, "Öge denied") {
		t.Errorf("question = %+v", d)
	}
}

func TestResidueAndCapabilities(t *testing.T) {
	in := initFrame{
		Skills: []string{"design", "deep-research"}, Agents: append([]string{"reviewer"}, builtinAgents...),
		Version: "2.1.289", Capabilities: []string{"interrupt_receipt_v1"},
	}
	in.Plugins = append(in.Plugins, initPlugin{Name: "cc-plugin-agents-md", Path: "builtin", Source: "cc-plugin-agents-md@builtin"})
	r := residueOf(in)
	if r == nil || strings.Join(r.Plugins, ",") != "cc-plugin-agents-md@builtin" || strings.Join(r.Skills, ",") != "deep-research,design" ||
		strings.Join(r.Agents, ",") != "reviewer" {
		t.Fatalf("residue = %+v", r)
	}
	in.Skills = in.Skills[:1]
	if r2 := residueOf(in); r2.Fingerprint == r.Fingerprint {
		t.Error("a changed residue kept its fingerprint")
	}
	if residueOf(initFrame{Agents: builtinAgents}) != nil {
		t.Error("built-in agents alone are residue")
	}
	// Capabilities come only from system/init.
	if got := strings.Join(effective(in), ","); got != "host_requests,deny_reason_reaches_model,interrupt" {
		t.Errorf("effective = %s", got)
	}
	if got := effective(initFrame{}); len(got) != 0 {
		t.Errorf("an init with no version and no capabilities gives %v", got)
	}
}

func TestExitOf(t *testing.T) {
	for in, want := range map[string]string{
		"": "done", "Fixed it.": "done", "Fixed.\nExit: done": "done", "Can't.\n**Exit: infeasible**": "infeasible",
		"exit: infeasible.": "infeasible", "Exit: bogus": "done",
	} {
		if got := exitOf(in, "implementer"); got != want {
			t.Errorf("exitOf(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"": "", "Added tests.\nExit: extended": "extended", "Exit: no_additions": "no_additions",
		"Exit: conflicts_with_oracle": "conflicts_with_oracle", "Exit: done": "",
	} {
		if got := exitOf(in, "verifier"); got != want {
			t.Errorf("verifier exitOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVerifierPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws, "add.go"), []byte("package fx\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "add_test.go"), []byte("package fx\n"), 0o644)
	p := newPolicy(agent.LaunchSpec{Role: "verifier", Workspace: ws, CheckCommands: []string{"go test -json ./..."},
		WriteGlobs: []string{"**/*_test.go"}}, "abc")
	fp := func(k, v string) string { b, _ := json.Marshal(map[string]string{k: v}); return string(b) }
	bash := func(cmd string) string { b, _ := json.Marshal(map[string]string{"command": cmd}); return string(b) }
	for _, c := range []struct{ tool, input, want string }{
		{"Read", fp("file_path", "add.go"), "allow"},
		{"Write", fp("file_path", "add.go"), "deny"},       // production code
		{"Edit", fp("file_path", "add_test.go"), "deny"},   // an existing test
		{"Write", fp("file_path", "add_test.go"), "deny"},  // overwriting it
		{"Write", fp("file_path", "neg_test.go"), "allow"}, // a new test
		{"Edit", fp("file_path", "neg_test.go"), "allow"},  // the test it created
		{"Edit", fp("file_path", "other_test.go"), "deny"}, // Edit can't create
		{"Write", fp("file_path", "NOTES.md"), "deny"},     // not a test
		{"NotebookEdit", fp("notebook_path", "x_test.go"), "deny"},
		{"Bash", bash("go test -json ./..."), "allow"},
		{"Bash", bash("gofmt -l ."), "allow"},
		{"Bash", bash("gofmt -w add.go"), "deny"},
		{"Bash", bash("go build -o out ."), "deny"},
	} {
		in := json.RawMessage(c.input)
		if c.input == "" {
			in = nil
		}
		// A Write the policy allows is then made, as claude would.
		d := p.decide(c.tool, in)
		if d.Decision != c.want {
			t.Errorf("%s %s: %s (%s), want %s", c.tool, c.input, d.Decision, d.Reason, c.want)
		}
		if c.tool == "Write" && d.Decision == "allow" {
			var m map[string]string
			json.Unmarshal(in, &m)
			os.WriteFile(filepath.Join(ws, m["file_path"]), []byte("package fx\n"), 0o644)
		}
	}
	if d := p.decide("WebFetch", nil); !strings.Contains(d.Reason, "verifier's tools") {
		t.Errorf("reason %q", d.Reason)
	}
}

func TestToolTargetsAreShortAndRedacted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the targets are POSIX paths; Runs are refused on Windows (ADR-0017)")
	}
	ws := "/w"
	// Built at run time, so the source holds no key-shaped string.
	key := "sk" + "-ant-" + strings.Repeat("q", 24)
	in := map[string]any{"command": "export ANTHROPIC_API_KEY=" + key + " && go test\nsecond line"}
	got := target("Bash", in, []string{ws})
	if strings.Contains(got, key) || strings.Contains(got, "second line") {
		t.Errorf("target = %q", got)
	}
	if got := target("Edit", map[string]any{"file_path": "/w/a/b.go", "old_string": "secret body"}, []string{ws}); got != "a/b.go" {
		t.Errorf("Edit target = %q", got)
	}
	sp := "/srv/Application Support/w"
	// The cd prefix stays, so a denied "cd <ws> && go test" reads apart
	// from an allowed "go test"; the Workspace itself shortens to ".".
	for cmd, want := range map[string]string{
		`cd /srv/Application\ Support/w && go test ./...`:  "cd . && go test ./...",
		`cd "/srv/Application Support/w" && go test ./...`: "cd . && go test ./...",
		`cd /srv/Application\ Support/w/pkg && go test`:    "cd pkg && go test",
		`go test /srv/Application\ Support/w/pkg`:          "go test pkg",
		`ls /srv/Application\ Support/wx`:                  `ls /srv/Application\ Support/wx`,
	} {
		if got := target("Bash", map[string]any{"command": cmd}, []string{sp}); got != want {
			t.Errorf("target(%q) = %q, want %q", cmd, got, want)
		}
	}
	// No control character reaches a target: no terminal escapes.
	if got := target("Bash", map[string]any{"command": "ls \x1b[31mred\x07\u009b"}, []string{ws}); got != "ls [31mred" {
		t.Errorf("control target = %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := target("Bash", map[string]any{"command": long}, []string{ws}); len([]rune(got)) != 80 {
		t.Errorf("long target has %d runes", len([]rune(got)))
	}
}

func pluginsOf(sources ...string) []initPlugin {
	var out []initPlugin
	for _, src := range sources {
		out = append(out, initPlugin{Name: src, Path: "builtin", Source: src})
	}
	return out
}

func TestVerifierLaunchDisablesSkills(t *testing.T) {
	for role, want := range map[string]bool{"verifier": true, "implementer": false} {
		p, err := profileFor(role)
		if err != nil {
			t.Fatal(err)
		}
		args, err := p.args(agent.LaunchSpec{Role: role, Workspace: t.TempDir()}, "id", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(strings.Join(args, " "), "--disable-slash-commands"); got != want {
			t.Errorf("%s: --disable-slash-commands = %v", role, got)
		}
	}
}
