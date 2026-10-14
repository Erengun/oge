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
	Dir string         `json:"dir"`
	Msg map[string]any `json:"msg"`
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
	rest := load(t, name)
	for i, r := range rest {
		if r.Msg["method"] == "initialized" {
			return append(out, rest[i+1:]...)
		}
	}
	t.Fatal("no initialized in " + name)
	return nil
}

// asImplementer rewrites the recorded verifier envelope into the one an
// implementer thread reports.
func asImplementer(script []rec) {
	for _, r := range script {
		res, _ := r.Msg["result"].(map[string]any)
		if res == nil || res["activePermissionProfile"] == nil {
			continue
		}
		res["activePermissionProfile"] = map[string]any{"id": profileName, "extends": ":workspace"}
		res["sandbox"].(map[string]any)["writableRoots"] = []any{}
	}
}

type harness struct {
	t     *testing.T
	log   string
	ws    string
	tests int
	// selfTest is what the faked self-test returns.
	selfTest error
}

func (h *harness) open(script []rec, spec agent.LaunchSpec) (agent.Session, error) {
	dir := h.t.TempDir()
	h.ws = filepath.Join(dir, "ws")
	if err := os.Mkdir(h.ws, 0o700); err != nil {
		h.t.Fatal(err)
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
		SelfTest: func(context.Context, string, []string, *profile) error { h.tests++; return h.selfTest },
	}
	if spec.Role == "" {
		spec.Role = "implementer"
	}
	spec.Workspace = h.ws
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
	asImplementer(script)
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
	asImplementer(script)
	_, err := h.open(script, agent.LaunchSpec{})
	if err == nil || !strings.Contains(err.Error(), "self-test") {
		t.Fatalf("Open = %v", err)
	}
	if len(h.sent()["thread/start"]) != 0 {
		t.Error("a thread started after the self-test failed")
	}
}

func TestDeathDuringTurnFailsIt(t *testing.T) {
	h := &harness{t: t}
	script := probeThen(t, "command_approval.jsonl")
	asImplementer(script)
	for i, r := range script {
		if r.Msg["method"] == "turn/started" {
			cut := append([]rec{}, script[:i+1]...)
			cut = append(cut, rec{Dir: "s2c", Msg: map[string]any{"method": "thread/brandNew", "params": map[string]any{"secret": "x"}}}, rec{Dir: "exit"})
			script = cut
			break
		}
	}
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

// bare is a session with no process, for frame-level tests.
func bare(spec agent.LaunchSpec) (*session, *bytes.Buffer) {
	var out bytes.Buffer
	s := &session{stdin: nopCloser{&out}, events: make(chan agent.Event, 64), stop: make(chan struct{}), exited: make(chan struct{}),
		pending: map[int]chan reply{}, items: map[string][]string{}, prof: newProfile(spec), ws: withReal(spec.Workspace), opened: true}
	for _, d := range spec.DenyRead {
		s.private = append(s.private, withReal(d)...)
	}
	return s, &out
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func TestFileChangeApprovalJoinsItsItem(t *testing.T) {
	recs := load(t, "held_approval.jsonl")
	var started, request []byte
	for _, r := range recs {
		b, _ := json.Marshal(r.Msg)
		switch r.Msg["method"] {
		case "item/started":
			if it := r.Msg["params"].(map[string]any)["item"].(map[string]any); it["type"] == "fileChange" {
				started = b
			}
		case "item/fileChange/requestApproval":
			request = b
		}
	}
	// The recorded Workspace becomes a real path on this platform.
	dir := filepath.Join(t.TempDir(), "ws")
	quoted, _ := json.Marshal(dir)
	started = bytes.ReplaceAll(started, []byte(ws), quoted[1:len(quoted)-1])
	for _, c := range []struct {
		name    string
		spec    agent.LaunchSpec
		started bool
		want    string
	}{
		{"contained", agent.LaunchSpec{Workspace: dir}, true, "accept"},
		{"private", agent.LaunchSpec{Workspace: dir, DenyRead: []string{filepath.Join(dir, "README.md")}}, true, "decline"},
		{"outside", agent.LaunchSpec{Workspace: filepath.Join(t.TempDir(), "elsewhere")}, true, "decline"},
		{"unknown item", agent.LaunchSpec{Workspace: dir}, false, "decline"},
	} {
		s, out := bare(c.spec)
		s.thread = "01a1082c-8e7a-7500-862d-a89838d92417"
		if c.started {
			if err := s.frame(started); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.frame(request); err != nil {
			t.Fatal(err)
		}
		var ans struct {
			Result struct{ Decision string } `json:"result"`
		}
		_ = json.Unmarshal(out.Bytes(), &ans)
		if ans.Result.Decision != c.want {
			t.Errorf("%s: answered %s, want %s", c.name, out.String(), c.want)
		}
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
	got := toml(p.perm)
	for _, want := range []string{`"extends"=":workspace"`, strconv.Quote(priv) + `="deny"`, `":workspace_roots"={".git"="read"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("toml = %s, missing %s", got, want)
		}
	}
}
