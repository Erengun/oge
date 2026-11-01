package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
)

// These contract tests run the adapter against the fake app-server
// (./fakecodex) replaying the re-redacted recordings in testdata (#60).

var fakePath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oge-fakecodex-")
	if err != nil {
		panic(err)
	}
	fakePath = filepath.Join(dir, "codex")
	if runtime.GOOS == "windows" {
		fakePath += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", fakePath, "./fakecodex").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ws is the recordings' Workspace.
const ws = "/home/user/scratch/ws-b"

type rec struct {
	Proc string         `json:"proc,omitempty"`
	Dir  string         `json:"dir"`
	Msg  map[string]any `json:"msg"`
}

func load(t *testing.T, name string) []rec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []rec
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var r rec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// probeThen is the readiness probe's recording followed by name's frames
// after its initialize handshake.
func probeThen(t *testing.T, name string) []rec {
	out := load(t, "init_account.jsonl")
	// config/read, from a throwaway CODEX_HOME declaring one MCP server.
	for _, r := range load(t, "mcp_servers.jsonl") {
		if r.Proc == "M" {
			out = append(out, r)
		}
	}
	rest := load(t, name)
	for i, r := range rest {
		if r.Msg["method"] == "initialized" {
			return append(out, rest[i+1:]...)
		}
	}
	t.Fatal("no initialized in " + name)
	return nil
}

// recordedCache is the tool cache in implementer_thread_start.jsonl.
const recordedCache = "/home/user/scratch/cache"

// asImplementer gives the recorded verifier thread the envelope a live
// implementer thread reported (implementer_thread_start.jsonl, 0.162.1).
func asImplementer(t *testing.T, script []rec) {
	t.Helper()
	var live map[string]any
	for _, r := range load(t, "implementer_thread_start.jsonl") {
		if res, _ := r.Msg["result"].(map[string]any); res != nil && res["activePermissionProfile"] != nil {
			live = res
		}
	}
	for _, r := range script {
		res, _ := r.Msg["result"].(map[string]any)
		if res == nil || res["activePermissionProfile"] == nil {
			continue
		}
		res["activePermissionProfile"], res["sandbox"] = live["activePermissionProfile"], live["sandbox"]
	}
}

func TestRecordedImplementerEnvelopePasses(t *testing.T) {
	var e envelope
	for _, r := range load(t, "implementer_thread_start.jsonl") {
		if res, _ := r.Msg["result"].(map[string]any); res != nil && res["activePermissionProfile"] != nil {
			b, _ := json.Marshal(res)
			_ = json.Unmarshal(b, &e)
		}
	}
	p := newProfile(agent.LaunchSpec{Role: "implementer", Workspace: "/home/user/scratch/ws", Cache: recordedCache, Network: "off"})
	if err := p.check(e); err != nil {
		t.Fatal(err)
	}
	p = newProfile(agent.LaunchSpec{Role: "implementer", Workspace: "/home/user/scratch/ws", Network: "off"})
	if err := p.check(e); err == nil {
		t.Error("a write root Öge didn't ask for passed")
	}
}

type harness struct {
	t     *testing.T
	log   string
	ws    string
	tests int
	// selfTest is what the faked self-test returns.
	selfTest error
	// prepare, when set, fills the Workspace before Open.
	prepare func(ws string)
}

func (h *harness) open(script []rec, spec agent.LaunchSpec) (agent.Session, error) {
	dir := h.t.TempDir()
	h.ws = filepath.Join(dir, "ws")
	if err := os.Mkdir(h.ws, 0o700); err != nil {
		h.t.Fatal(err)
	}
	if h.prepare != nil {
		h.prepare(h.ws)
	}
	// The recorded Workspace becomes a real one, as JSON escapes it.
	quoted, _ := json.Marshal(h.ws)
	var b bytes.Buffer
	for _, r := range script {
		line, _ := json.Marshal(r)
		line = bytes.ReplaceAll(line, []byte(ws), quoted[1:len(quoted)-1])
		b.Write(append(line, '\n'))
	}
	path := filepath.Join(dir, "script.jsonl")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.log = filepath.Join(dir, "client.jsonl")
	a := &Adapter{Path: fakePath, Timeout: 10 * time.Second, CloseGrace: time.Second,
		Environ: func() []string {
			return append(os.Environ(), "OGE_FAKECODEX_SCRIPT="+path, "OGE_FAKECODEX_LOG="+h.log)
		},
		SelfTest: func(context.Context, string, []string, *profile, []string) error { h.tests++; return h.selfTest },
	}
	if spec.Role == "" {
		spec.Role = "implementer"
	}
	spec.Workspace = h.ws
	if spec.Cache == "" {
		spec.Cache = recordedCache
	}
	return a.Open(context.Background(), spec)
}

// sent is every client message the fake received, by method.
func (h *harness) sent() map[string][]map[string]any {
	b, _ := os.ReadFile(h.log)
	out := map[string][]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			k, _ := m["method"].(string)
			if k == "" {
				k = "response"
			}
			out[k] = append(out[k], m)
		}
	}
	return out
}

// untilSettled collects events through TurnSettled.
func untilSettled(t *testing.T, s agent.Session) []agent.Event {
	t.Helper()
	var evs []agent.Event
	timeout := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("events closed before the turn settled: %+v", evs)
			}
			evs = append(evs, ev)
			if ev.Kind == agent.TurnSettled {
				return evs
			}
		case <-timeout:
			t.Fatalf("the turn didn't settle: %+v", evs)
		}
	}
}

func TestCommandApprovalIsDeclinedOneShot(t *testing.T) {
	h := &harness{t: t}
	script := probeThen(t, "command_approval.jsonl")
	asImplementer(t, script)
	s, err := h.open(script, agent.LaunchSpec{RepoInstructions: "snapshot rules"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send(agent.Turn{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	evs := untilSettled(t, s)
	if evs[0].Kind != agent.TurnAccepted || evs[1].Kind != agent.SessionOpened {
		t.Fatalf("first events = %+v", evs[:2])
	}
	info := evs[1].Session
	if info.AgentVersion != "0.155.1" || info.Envelope != "ok" || info.AuthSource != "account:chatgpt" {
		t.Errorf("session info = %+v", info)
	}
	for _, c := range info.Capabilities {
		if c == "deny_reason_reaches_model" {
			t.Error("a decline's reason never reaches the model")
		}
	}
	var host *agent.HostDecision
	var claims []string
	for _, ev := range evs {
		if ev.Kind == agent.HostRequest {
			host = ev.Host
		}
		if ev.Kind == agent.Claim && ev.Tool != "" {
			claims = append(claims, ev.Tool+":"+ev.Target)
		}
	}
	if host == nil || host.Decision != "deny" || host.Tool != "Bash" || host.Target != "echo x >> src/calc.py" {
		t.Fatalf("host request = %+v", host)
	}
	if len(claims) == 0 || !strings.HasPrefix(claims[0], "Bash:printenv") {
		t.Errorf("claims = %q", claims)
	}
	last := evs[len(evs)-1]
	if last.Failure != "" || last.Exit != "done" {
		t.Errorf("settled = %+v", last)
	}
	if evs[len(evs)-2].Kind != agent.Usage || evs[len(evs)-2].Tokens == nil {
		t.Errorf("no usage before settling: %+v", evs[len(evs)-2])
	}

	sent := h.sent()
	init := sent["initialize"][0]["params"].(map[string]any)["clientInfo"].(map[string]any)
	if init["name"] != "oge" {
		t.Errorf("clientInfo = %v", init)
	}
	if p := sent["account/read"][0]["params"].(map[string]any); p["refreshToken"] != false {
		t.Errorf("account/read params = %v, want refreshToken false", p)
	}
	if len(sent["account/rateLimits/read"]) != 1 {
		t.Error("the readiness probe didn't read rate limits")
	}
	ts := sent["thread/start"][0]["params"].(map[string]any)
	if ts["approvalPolicy"] != "on-request" || ts["approvalsReviewer"] != "user" || ts["developerInstructions"] != "snapshot rules" || ts["cwd"] != h.ws {
		t.Errorf("thread/start params = %v", ts)
	}
	cfg := ts["config"].(map[string]any)
	if cfg["default_permissions"] != profileName || cfg["project_doc_max_bytes"] != float64(0) {
		t.Errorf("config = %v", cfg)
	}
	// The MCP server config/read reported is turned off for the thread.
	if mcp, _ := json.Marshal(cfg["mcp_servers"]); string(mcp) != `{"dummy":{"enabled":false}}` {
		t.Errorf("mcp_servers = %s", mcp)
	}
	// auth.json is denied in the codexHome initialize reported. The
	// recording's codexHome is a Unix path: on Windows it isn't absolute,
	// so it is ignored there.
	fs := cfg["permissions"].(map[string]any)[profileName].(map[string]any)["filesystem"].(map[string]any)
	if runtime.GOOS != "windows" && fs[filepath.Join("/home/user/.codex", "auth.json")] != "deny" {
		t.Errorf("filesystem = %v", fs)
	}
	resp := sent["response"]
	if len(resp) != 1 || resp[0]["result"].(map[string]any)["decision"] != "decline" {
		t.Errorf("approval answer = %v, want a one-shot decline", resp)
	}
	if h.tests != 1 {
		t.Errorf("self-test ran %d times", h.tests)
	}
}

func TestEnvelopeMismatchFailsClosed(t *testing.T) {
	h := &harness{t: t}
	// The recorded envelope is the verifier's read-only profile.
	_, err := h.open(probeThen(t, "command_approval.jsonl"), agent.LaunchSpec{})
	if err == nil || !strings.Contains(err.Error(), "startup envelope") {
		t.Fatalf("Open = %v, want an envelope failure", err)
	}
}

func TestUnloadableProfileFailsClosed(t *testing.T) {
	h := &harness{t: t}
	_, err := h.open(probeThen(t, "threads_file_approval.jsonl"), agent.LaunchSpec{})
	if err == nil || !strings.Contains(err.Error(), "-32600") {
		t.Fatalf("Open = %v, want thread/start's -32600", err)
	}
}

func TestOldCodexIsRefused(t *testing.T) {
	h := &harness{t: t}
	script := probeThen(t, "command_approval.jsonl")
	script[1].Msg["result"].(map[string]any)["userAgent"] = "oge/0.150.0 (Mac OS 15; arm64) terminal (oge; 0)"
	_, err := h.open(script, agent.LaunchSpec{})
	if err == nil || !strings.Contains(err.Error(), "older than") {
		t.Fatalf("Open = %v", err)
	}
}

func TestSelfTestFailureFailsClosed(t *testing.T) {
	h := &harness{t: t, selfTest: errors.New("read private")}
	script := probeThen(t, "command_approval.jsonl")
	asImplementer(t, script)
	_, err := h.open(script, agent.LaunchSpec{})
	if err == nil || !strings.Contains(err.Error(), "self-test") {
		t.Fatalf("Open = %v", err)
	}
	if len(h.sent()["thread/start"]) != 0 {
		t.Error("a thread started after the self-test failed")
	}
}

// lines are a recording's lines from and to (1-based, inclusive), with
// their thread id made the one thread/start reported in base.
func lines(t *testing.T, base []rec, name string, from, to int) []rec {
	t.Helper()
	var thread string
	for _, r := range base {
		if res, _ := r.Msg["result"].(map[string]any); res != nil && res["activePermissionProfile"] != nil {
			thread = res["thread"].(map[string]any)["id"].(string)
		}
	}
	recs := load(t, name)[from-1 : to]
	old := recs[0].Msg["params"].(map[string]any)["threadId"].(string)
	var out []rec
	for _, r := range recs {
		b, _ := json.Marshal(r)
		var c rec
		_ = json.Unmarshal(bytes.ReplaceAll(b, []byte(old), []byte(thread)), &c)
		out = append(out, c)
	}
	return out
}

// upToThread is the probe and command_approval's implementer thread/start.
func upToThread(t *testing.T) []rec {
	script := probeThen(t, "command_approval.jsonl")
	asImplementer(t, script)
	for i, r := range script {
		if r.Msg["method"] == "thread/start" {
			return script[:i+2]
		}
	}
	t.Fatal("no thread/start")
	return nil
}

func TestDeathDuringTurnFailsIt(t *testing.T) {
	h := &harness{t: t}
	base := upToThread(t)
	// kill_resume's first process was killed mid-command (lines 11-28).
	script := append(base, lines(t, base, "kill_resume.jsonl", 11, 28)...)
	script = append(script, rec{Dir: "s2c", Msg: map[string]any{"method": "thread/brandNew", "params": map[string]any{"secret": "x"}}}, rec{Dir: "exit"})
	s, err := h.open(script, agent.LaunchSpec{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.Send(agent.Turn{Text: "go"})
	evs := untilSettled(t, s)
	last := evs[len(evs)-1]
	if !strings.HasPrefix(last.Failure, "agent_crash") {
		t.Errorf("settled = %+v, want an agent_crash failure", last)
	}
	var unknown []string
	for _, ev := range evs {
		if ev.Kind == agent.Unknown {
			unknown = append(unknown, ev.Text)
		}
	}
	if len(unknown) != 1 || unknown[0] != "thread/brandNew" {
		t.Errorf("unknown events = %q, want the method only", unknown)
	}
}

func TestMCPServerStartFailsClosed(t *testing.T) {
	h := &harness{t: t}
	script := upToThread(t)
	for _, r := range load(t, "mcp_servers.jsonl") {
		if r.Proc == "S" {
			script = append(script, r)
		}
	}
	s, err := h.open(script, agent.LaunchSpec{})
	if err == nil {
		defer s.Close()
		timeout := time.After(10 * time.Second)
	drain:
		for {
			select {
			case _, ok := <-s.Events():
				if !ok {
					break drain
				}
			case <-timeout:
				t.Fatal("the session didn't end")
			}
		}
		err = s.Send(agent.Turn{Text: "go"})
	}
	if err == nil || !strings.Contains(err.Error(), "MCP server (dummy)") {
		t.Fatalf("err = %v, want the MCP server to fail the Session closed", err)
	}
}

func TestInterruptIsAcknowledgedByTurnCompleted(t *testing.T) {
	h := &harness{t: t}
	base := upToThread(t)
	// steer_interrupt_resume lines 72-83: a turn, turn/interrupt, and
	// turn/completed{interrupted}.
	s, err := h.open(append(base, lines(t, base, "steer_interrupt_resume.jsonl", 72, 83)...), agent.LaunchSpec{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send(agent.Turn{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	evs := untilSettled(t, s)
	if last := evs[len(evs)-1]; last.Failure != "interrupted" {
		t.Errorf("settled = %+v, want interrupted", last)
	}
	// The turn has ended: no second interrupt goes out.
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(h.sent()["turn/interrupt"]); n != 1 {
		t.Errorf("sent %d interrupts, want 1", n)
	}
}

// bare is a session with no process, for frame-level tests.
func bare(spec agent.LaunchSpec) (*session, *bytes.Buffer) {
	var out bytes.Buffer
	s := &session{stdin: nopCloser{&out}, events: make(chan agent.Event, 64), stop: make(chan struct{}), exited: make(chan struct{}),
		pending: map[int]chan reply{}, items: map[string][]string{}, prof: newProfile(spec), ws: withReal(spec.Workspace), opened: true}
	return s, &out
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func TestFileChangeApprovalsAreDeclined(t *testing.T) {
	var started, request map[string]any
	for _, r := range load(t, "held_approval.jsonl") {
		switch r.Msg["method"] {
		case "item/started":
			if it := r.Msg["params"].(map[string]any)["item"].(map[string]any); it["type"] == "fileChange" {
				started = r.Msg
			}
		case "item/fileChange/requestApproval":
			request = r.Msg
		}
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	work := filepath.Join(dir, "ws")
	for _, d := range []string{home, work} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(work, "link")
	symlinked := os.Symlink(home, link) == nil
	thread := started["params"].(map[string]any)["threadId"].(string)
	itemID := request["params"].(map[string]any)["itemId"].(string)
	change := func(path string, kind map[string]any) map[string]any {
		return map[string]any{"method": "item/started", "params": map[string]any{"threadId": thread,
			"item": map[string]any{"type": "fileChange", "id": itemID, "changes": []any{map[string]any{"path": path, "kind": kind}}}}}
	}
	withGrant := func(root string) map[string]any {
		b, _ := json.Marshal(request)
		var r map[string]any
		_ = json.Unmarshal(b, &r)
		r["params"].(map[string]any)["grantRoot"] = root
		return r
	}
	cases := []struct {
		name             string
		started, request map[string]any
	}{
		{"recorded, inside the Workspace", change(filepath.Join(work, "README.md"), map[string]any{"type": "update"}), request},
		{"move_path out of the Workspace", change(filepath.Join(work, "a.txt"), map[string]any{"type": "update", "move_path": filepath.Join(home, ".zshrc")}), request},
		{"grantRoot", change(filepath.Join(work, "a.txt"), map[string]any{"type": "add"}), withGrant(work)},
		{"unknown item", nil, request},
	}
	if symlinked {
		cases = append(cases, struct {
			name             string
			started, request map[string]any
		}{"through a symlink", change(filepath.Join(link, ".zshrc"), map[string]any{"type": "update"}), request})
	}
	for _, c := range cases {
		s, out := bare(agent.LaunchSpec{Workspace: work})
		s.thread = thread
		for _, m := range []map[string]any{c.started, c.request} {
			if m == nil {
				continue
			}
			b, _ := json.Marshal(m)
			if err := s.frame(b); err != nil {
				t.Fatal(err)
			}
		}
		var ans struct {
			Result struct{ Decision string } `json:"result"`
		}
		_ = json.Unmarshal(out.Bytes(), &ans)
		if ans.Result.Decision != "decline" {
			t.Errorf("%s: answered %s, want decline", c.name, out.String())
		}
		var host *agent.HostDecision
		for len(s.events) > 0 {
			if ev := <-s.events; ev.Kind == agent.HostRequest {
				host = ev.Host
			}
		}
		if host == nil || host.Decision != "deny" || !strings.HasSuffix(host.Reason, noReasonToModel) {
			t.Errorf("%s: host decision = %+v", c.name, host)
		}
	}
}

func TestSandboxSelfTestExitCodes(t *testing.T) {
	ws := t.TempDir()
	p := newProfile(agent.LaunchSpec{Workspace: ws})
	secret := "sk-" + strings.Repeat("x", 24)
	for _, c := range []struct {
		code, out, sleep, want string
	}{
		{"0", "", "", ""},
		{"3", "", "", "couldn't read the Workspace"},
		{"4", "/private/dir", "", "let a command read /private/dir"},
		{"5", ws + "/.git/HEAD", "", "let a command write"},
		{"6", ws + "/.codex", "", "already exists"},
		{"1", "boom " + secret, "", "codex sandbox failed"},
		{"0", "", "5s", "didn't finish"},
	} {
		old := selfTestTimeout
		if c.sleep != "" {
			selfTestTimeout = 200 * time.Millisecond
		}
		env := append(os.Environ(), "OGE_FAKECODEX_SANDBOX_EXIT="+c.code, "OGE_FAKECODEX_SANDBOX_OUT="+c.out, "OGE_FAKECODEX_SANDBOX_SLEEP="+c.sleep)
		err := sandboxSelfTest(context.Background(), fakePath, env, p, nil)
		selfTestTimeout = old
		switch {
		case c.want == "" && err != nil:
			t.Errorf("exit %s: %v", c.code, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("exit %s: %v, want %q", c.code, err, c.want)
		case err != nil && strings.Contains(err.Error(), secret):
			t.Errorf("exit %s: the output wasn't redacted: %v", c.code, err)
		}
	}
}

func TestSelfTestCacheKeysOnProbedPaths(t *testing.T) {
	ws := t.TempDir()
	qa := filepath.Join(t.TempDir(), "qa")
	p := newProfile(agent.LaunchSpec{Workspace: ws, DenyRead: []string{qa}})
	before := p.probes()
	if err := os.Mkdir(qa, 0o700); err != nil {
		t.Fatal(err)
	}
	after := p.probes()
	if !slices.Contains(after, filepath.Clean(qa)) || slices.Contains(before, filepath.Clean(qa)) {
		t.Fatalf("probes before %q, after %q", before, after)
	}
	if selfTestKey("codex", "0.160.1", p, before) == selfTestKey("codex", "0.160.1", p, after) {
		t.Error("a newly existing denied path reuses the old self-test result")
	}
}

func TestInterruptOnlyAKnownRunningTurn(t *testing.T) {
	s, out := bare(agent.LaunchSpec{Workspace: ws})
	s.thread = "th"
	if err := s.Interrupt(); err != nil || out.Len() > 0 {
		t.Fatalf("idle Interrupt = %v, sent %q", err, out.String())
	}
	s.inFlight = true
	if err := s.Interrupt(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Interrupt before the turn is known = %v", err)
	}
	s.turn = "t1"
	s.timeout = time.Millisecond
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	s.wmu.Lock()
	sent := out.String()
	s.wmu.Unlock()
	if !strings.Contains(sent, `"method":"turn/interrupt"`) || !strings.Contains(sent, `"turnId":"t1"`) {
		t.Errorf("sent %q", sent)
	}
}

func TestProfileTOML(t *testing.T) {
	priv := filepath.Join(t.TempDir(), "private")
	p := newProfile(agent.LaunchSpec{Workspace: ws, DenyRead: []string{priv}, Network: "off"})
	got := inlineTOML(p.perm)
	for _, want := range []string{`"extends"=":workspace"`, strconv.Quote(priv) + `="deny"`, `":workspace_roots"={".codex"="read", ".git"="read"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("toml = %s, missing %s", got, want)
		}
	}
}

func TestProjectMCPConfigFailsClosed(t *testing.T) {
	h := &harness{t: t, prepare: func(ws string) {
		_ = os.Mkdir(filepath.Join(ws, ".codex"), 0o700)
		_ = os.WriteFile(filepath.Join(ws, ".codex", "config.toml"), []byte("[mcp_servers.proj]\ncommand = \"/bin/cat\"\n"), 0o600)
	}}
	script := probeThen(t, "command_approval.jsonl")
	asImplementer(t, script)
	_, err := h.open(script, agent.LaunchSpec{})
	if err == nil || !strings.Contains(err.Error(), "sets mcp_servers") {
		t.Fatalf("Open = %v", err)
	}
	if len(h.sent()["thread/start"]) != 0 {
		t.Error("a thread started despite the project's MCP servers")
	}
	for body, bad := range map[string]bool{
		"":                                   false,
		"# only a comment\n":                 false,
		"notify = [\"sh\", \"-c\", \"x\"]\n": true,
		"[model_providers.p]\nbase_url = \"x\"\n":       true,
		"[profiles.x.mcp_servers.a]\ncommand = \"x\"\n": true,
		"model = \"x\"\n": true,
		"not toml [":      true,
	} {
		dir := t.TempDir()
		_ = os.Mkdir(filepath.Join(dir, ".codex"), 0o700)
		_ = os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(body), 0o600)
		if err := projectConfig(dir); (err != nil) != bad {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestMCPToolCallFailsClosed(t *testing.T) {
	h := &harness{t: t}
	base := upToThread(t)
	script := append(base, lines(t, base, "command_approval.jsonl", 8, 9)...)
	thread := script[len(script)-2].Msg["params"].(map[string]any)["threadId"]
	script = append(script, rec{Dir: "s2c", Msg: map[string]any{"method": "item/started", "params": map[string]any{
		"threadId": thread, "item": map[string]any{"type": "mcpToolCall", "id": "m1", "server": "x", "tool": "read"}}}})
	s, err := h.open(script, agent.LaunchSpec{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.Send(agent.Turn{Text: "go"})
	evs := untilSettled(t, s)
	if last := evs[len(evs)-1]; !strings.Contains(last.Failure, "envelope_failed: the agent called an MCP tool") {
		t.Errorf("settled = %+v", last)
	}
}

func TestOpenReportsWhyItFailedClosed(t *testing.T) {
	h := &harness{t: t}
	script := probeThen(t, "command_approval.jsonl")
	var start []rec
	for _, r := range load(t, "mcp_servers.jsonl") {
		if r.Proc == "S" {
			start = append(start, r)
		}
	}
	// An MCP server reports starting during the probe.
	script = append(append(append([]rec{}, script[:2]...), start...), script[2:]...)
	_, err := h.open(script, agent.LaunchSpec{})
	if err == nil || !strings.Contains(err.Error(), "MCP server (dummy)") {
		t.Fatalf("Open = %v, want the fail-closed reason", err)
	}
}

func TestAuthDenyNeedsAnAbsoluteHome(t *testing.T) {
	p := newProfile(agent.LaunchSpec{Workspace: ws})
	before := len(p.perm["filesystem"].(map[string]any))
	p.denyAuth("relative/codex")
	p.denyAuth("")
	if after := len(p.perm["filesystem"].(map[string]any)); after != before {
		t.Errorf("a relative codex home was denied: %v", p.perm)
	}
	if home, err := os.UserHomeDir(); err == nil && p.perm["filesystem"].(map[string]any)[filepath.Join(home, ".codex", "auth.json")] != "deny" {
		t.Error("~/.codex/auth.json isn't denied")
	}
}

func TestCodexProbe(t *testing.T) {
	dir := t.TempDir()
	p := newProfile(agent.LaunchSpec{Workspace: dir})
	if got, err := p.codexProbe(); err != nil || got != filepath.Join(dir, ".codex") {
		t.Errorf("no .codex: probe %s, %v", got, err)
	}
	_ = os.Mkdir(filepath.Join(dir, ".codex"), 0o700)
	a, _ := p.codexProbe()
	b, _ := p.codexProbe()
	if filepath.Dir(a) != filepath.Join(dir, ".codex") || a == b {
		t.Errorf("with .codex: probes %s and %s, want unique names inside it", a, b)
	}
	file := t.TempDir()
	_ = os.WriteFile(filepath.Join(file, ".codex"), nil, 0o600)
	if _, err := newProfile(agent.LaunchSpec{Workspace: file}).codexProbe(); err == nil {
		t.Error("a .codex file was probed")
	}
	if err := sandboxSelfTest(context.Background(), fakePath, os.Environ(), newProfile(agent.LaunchSpec{Workspace: file}), nil); err == nil || !strings.Contains(err.Error(), "plain directory") {
		t.Errorf("self-test with a .codex file = %v", err)
	}
}

// A Chat opens codex under AutoPermissions on its Launch profile (D2);
// any other mode is refused.
func TestChatOpensUnderLaunchProfile(t *testing.T) {
	if !agent.HasAutoMode(New()) {
		t.Fatal("codex can't run a Chat")
	}
	h := &harness{t: t}
	script := probeThen(t, "command_approval.jsonl")
	asImplementer(t, script)
	s, err := h.open(script, agent.LaunchSpec{Permissions: agent.AutoPermissions})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := h.open(script, agent.LaunchSpec{Permissions: "plan"}); err == nil || !strings.Contains(err.Error(), "no plan permission mode") {
		t.Errorf("plan mode: %v", err)
	}
}

// thread/tokenUsage/updated carries the thread's running total: each
// turn's Usage is the change since the last turn settled.
func TestUsageIsPerTurnNotTheRunningTotal(t *testing.T) {
	s, _ := bare(agent.LaunchSpec{})
	for _, total := range []int{210371, 225571, 241021} {
		s.mu.Lock()
		s.inFlight = true
		out := total / 100 // output is a small share; cached input is in total
		ev := agent.Event{Kind: agent.Usage, Tokens: &total, OutTokens: &out}
		s.usage = &ev
		s.mu.Unlock()
		s.settle(agent.Event{})
		u := <-s.events
		if u.Kind != agent.Usage || u.Tokens == nil {
			t.Fatalf("usage event %+v", u)
		} else if want := map[int]int{210371: 210371, 225571: 15200, 241021: 15450}[total]; *u.Tokens != want {
			t.Errorf("total %d: turn tokens %d, want %d", total, *u.Tokens, want)
		}
		wantOut := map[int]int{210371: 2103, 225571: 2255 - 2103, 241021: 2410 - 2255}[total]
		if u.OutTokens == nil || *u.OutTokens != wantOut {
			t.Errorf("total %d: turn out tokens %v, want %d", total, u.OutTokens, wantOut)
		}
		<-s.events // TurnSettled
	}
}

// held_approval.jsonl: Codex does not expire a pending approval. The
// fileChange request waited about 690 s for an answer; nothing from the
// server touched it in between, only Öge's decline resolved it, and the
// turn completed (docs/research/held-requests.md). Öge's adapter answers
// every approval at once today, so it never holds one itself.
func TestCodexDoesNotExpireAHeldApproval(t *testing.T) {
	var asked, answered, resolved, completed float64
	var between []string
	var declinedAt float64
	raw := loadTimed(t, "held_approval.jsonl")
	for _, r := range raw {
		m := r.Msg
		method, _ := m["method"].(string)
		switch {
		case method == "item/fileChange/requestApproval":
			asked = r.T
		case r.Dir == "c2s" && m["id"] == float64(0) && m["result"] != nil:
			answered = r.T
		case method == "serverRequest/resolved" && resolved == 0:
			resolved = r.T
		case method == "turn/completed":
			completed = r.T
		case method == "item/completed" && declinedAt == 0:
			if item, _ := m["params"].(map[string]any)["item"].(map[string]any); item["type"] == "fileChange" && item["status"] == "declined" {
				declinedAt = r.T
			}
		}
		if asked != 0 && answered == 0 && r.T > asked && method != "" {
			between = append(between, method)
		}
	}
	if asked == 0 || answered-asked < 600 {
		t.Fatalf("the recording held the approval %.0f s, want at least 600", answered-asked)
	}
	if len(between) != 0 {
		t.Errorf("the server sent %v while the approval was held", between)
	}
	if declinedAt < answered {
		t.Errorf("the fileChange item declined at %.1f, before the answer at %.1f", declinedAt, answered)
	}
	if resolved < answered || completed < resolved {
		t.Errorf("order: answered %.1f, resolved %.1f, turn completed %.1f", answered, resolved, completed)
	}
}

type timedRec struct {
	T   float64        `json:"t"`
	Dir string         `json:"dir"`
	Msg map[string]any `json:"msg"`
}

func loadTimed(t *testing.T, name string) []timedRec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []timedRec
	for _, l := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var r timedRec
		if err := json.Unmarshal(l, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}
