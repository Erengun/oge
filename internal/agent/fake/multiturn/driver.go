package multiturn

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Drivers script a multi-turn session from a test: they build and
// start the fake process over real pipes, send turns, markers and answers,
// and read the events back. Reads fail the test after Timeout.

// Timeout bounds every read and wait.
var Timeout = 20 * time.Second

// Line is one event from the fake agent.
type Line struct {
	Kind      string // a Kind* constant
	Text      string // a claim's text, or an end's reason
	Tool      string // a tool Claim's or ask's tool
	Target    string
	CostUSD   float64
	Tokens    int
	OutTokens int
}

// build compiles the main package at pkg (relative to the repo's internal/agent) into dir.
func build(t testing.TB, pkg, name string) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	bin := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./"+pkg)
	cmd.Dir = filepath.Join(filepath.Dir(here), "..", "..") // internal/agent
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", pkg, err, out)
	}
	return bin
}

// proc is a started fake with line-by-line stdout.
type proc struct {
	t     testing.TB
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	done  chan error
}

func start(t testing.TB, bin, dir string, env ...string) *proc {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{t: t, cmd: cmd, stdin: stdin, lines: make(chan string, 256), done: make(chan error, 1)}
	go func() {
		r := bufio.NewReaderSize(stdout, 1<<20)
		for {
			l, err := r.ReadString('\n')
			if l != "" {
				p.lines <- strings.TrimRight(l, "\r\n")
			}
			if err != nil {
				break
			}
		}
		close(p.lines)
		p.done <- cmd.Wait()
	}()
	t.Cleanup(func() { cmd.Process.Kill() })
	return p
}

func (p *proc) write(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdin, line+"\n"); err != nil {
		p.t.Fatalf("writing to the fake: %v", err)
	}
}

func (p *proc) readLine() (string, bool) {
	p.t.Helper()
	select {
	case l, ok := <-p.lines:
		return l, ok
	case <-time.After(Timeout):
		p.t.Fatal("timed out waiting for the fake")
		return "", false
	}
}

// CloseStdin closes the fake's stdin, as the host does to end the session.
func (p *proc) CloseStdin() { p.stdin.Close() }

// Wait returns the process's exit error once it has exited; it fails the
// test if the process is still running after Timeout. Output not yet read
// is discarded.
func (p *proc) Wait() error {
	p.t.Helper()
	go func() {
		for range p.lines {
		}
	}()
	select {
	case err := <-p.done:
		return err
	case <-time.After(Timeout):
		p.t.Fatal("the fake did not exit")
		return nil
	}
}

// Agent drives the fake agent (./fakeagent).
type Agent struct{ *proc }

// StartAgent runs sc in the fake agent with dir as its Workspace.
func StartAgent(t testing.TB, sc Scenario, dir string) *Agent {
	t.Helper()
	bin := build(t, "fake/fakeagent", "fakeagent")
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(path); err != nil {
		t.Fatal(err)
	}
	return &Agent{start(t, bin, dir, "OGE_FAKE_SCENARIO="+path)}
}

// Send sends one user turn. Text must be one line and not start with "!".
func (a *Agent) Send(text string) {
	a.t.Helper()
	if strings.ContainsAny(text, "\r\n") || strings.HasPrefix(text, "!") {
		a.t.Fatalf("not a one-line turn: %q", text)
	}
	a.write(text)
}

// Interrupt ends the turn in flight early.
func (a *Agent) Interrupt() { a.write(MarkInterrupt) }

// Answer answers an ask.
func (a *Agent) Answer(allow bool) {
	if allow {
		a.write(MarkAllow)
	} else {
		a.write(MarkDeny)
	}
}

// Read returns the events up to and including the first for which stop is
// true; it fails the test if the process ends first.
func (a *Agent) Read(stop func(Line) bool) []Line {
	a.t.Helper()
	var got []Line
	for {
		raw, ok := a.readLine()
		if !ok {
			a.t.Fatalf("the fake ended before the expected event; got %+v", got)
		}
		l := parseLine(raw)
		got = append(got, l)
		if stop(l) {
			return got
		}
	}
}

// ReadTurn reads through the turn's end line.
func (a *Agent) ReadTurn() []Line {
	a.t.Helper()
	return a.Read(func(l Line) bool { return l.Kind == KindEnd })
}

// ReadAsk reads through the next ask.
func (a *Agent) ReadAsk() []Line {
	a.t.Helper()
	return a.Read(func(l Line) bool { return l.Kind == KindAsk })
}

func parseLine(raw string) Line {
	f := strings.Split(raw, "\t")
	l := Line{Kind: f[0]}
	arg := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	switch l.Kind {
	case KindClaim, KindEnd:
		l.Text = arg(1)
	case KindFile:
		l.Target = arg(1)
	case KindTool, KindAsk:
		l.Tool, l.Target = arg(1), arg(2)
	case KindUsage:
		l.CostUSD, _ = strconv.ParseFloat(arg(1), 64)
		l.Tokens, _ = strconv.Atoi(arg(2))
		l.OutTokens, _ = strconv.Atoi(arg(3))
	}
	return l
}

// Frame is one stream-json frame from fakeclaude.
type Frame map[string]any

func (f Frame) Type() string { s, _ := f["type"].(string); return s }

// Subtype is the frame's subtype, or its request's or response's.
func (f Frame) Subtype() string {
	if s, ok := f["subtype"].(string); ok {
		return s
	}
	for _, k := range []string{"request", "response"} {
		if m, ok := f[k].(map[string]any); ok {
			if s, ok := m["subtype"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// Texts are the text blocks of an assistant frame.
func (f Frame) Texts() []string { return f.blocks("text", "text") }

// ToolUses are the tool names of an assistant frame's tool_use blocks.
func (f Frame) ToolUses() []string { return f.blocks("tool_use", "name") }

func (f Frame) blocks(typ, key string) []string {
	msg, _ := f["message"].(map[string]any)
	parts, _ := msg["content"].([]any)
	var out []string
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok && m["type"] == typ {
			s, _ := m[key].(string)
			out = append(out, s)
		}
	}
	return out
}

// Claude drives fakeclaude in scenario mode, speaking stream-json.
type Claude struct {
	*proc
	askID string
}

// StartClaude runs sc in fakeclaude with dir as its Workspace.
func StartClaude(t testing.TB, sc Scenario, dir string) *Claude {
	t.Helper()
	bin := build(t, "claude/fakeclaude", "claude")
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(path); err != nil {
		t.Fatal(err)
	}
	return &Claude{proc: start(t, bin, dir, "OGE_FAKE_CLAUDE_SCENARIO="+path)}
}

func (c *Claude) send(v any) {
	b, _ := json.Marshal(v)
	c.write(string(b))
}

// Initialize sends the initialize control request and reads its response.
func (c *Claude) Initialize() Frame {
	c.t.Helper()
	c.send(map[string]any{"type": "control_request", "request_id": "req_init",
		"request": map[string]any{"subtype": "initialize", "hooks": nil}})
	return c.Read(func(f Frame) bool { return f.Type() == "control_response" })[0]
}

// Send sends one user message in the shape the real CLI takes.
func (c *Claude) Send(text string) {
	c.send(map[string]any{"type": "user", "uuid": "", "session_id": "", "parent_tool_use_id": nil,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}})
}

// Interrupt sends an interrupt control request.
func (c *Claude) Interrupt() {
	c.send(map[string]any{"type": "control_request", "request_id": "req_int",
		"request": map[string]any{"subtype": "interrupt"}})
}

// Answer answers the last ask (a can_use_tool request read by Read).
func (c *Claude) Answer(allow bool) {
	c.t.Helper()
	if c.askID == "" {
		c.t.Fatal("no ask to answer")
	}
	body := map[string]any{"behavior": "allow", "updatedInput": map[string]any{}}
	if !allow {
		body = map[string]any{"behavior": "deny", "message": "denied by the test"}
	}
	c.send(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": c.askID, "response": body}})
	c.askID = ""
}

// Read returns the frames up to and including the first for which stop is
// true; it fails the test if the process ends first.
func (c *Claude) Read(stop func(Frame) bool) []Frame {
	c.t.Helper()
	var got []Frame
	for {
		raw, ok := c.readLine()
		if !ok {
			c.t.Fatalf("fakeclaude ended before the expected frame; got %d frames", len(got))
		}
		var f Frame
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			c.t.Fatalf("not a JSON frame: %q", raw)
		}
		if f.Type() == "control_request" && f.Subtype() == "can_use_tool" {
			c.askID, _ = f["request_id"].(string)
		}
		got = append(got, f)
		if stop(f) {
			return got
		}
	}
}

// ReadTurn reads through the turn's result frame.
func (c *Claude) ReadTurn() []Frame {
	c.t.Helper()
	return c.Read(func(f Frame) bool { return f.Type() == "result" })
}

// ReadAsk reads through the next can_use_tool request.
func (c *Claude) ReadAsk() []Frame {
	c.t.Helper()
	return c.Read(func(f Frame) bool { return f.Type() == "control_request" && f.Subtype() == "can_use_tool" })
}

// BuildClaude compiles fakeclaude and returns its path, for a test that
// launches it through the claude adapter with OGE_FAKE_CLAUDE_SCENARIO.
func BuildClaude(t testing.TB) string { return build(t, "claude/fakeclaude", "claude") }

// BuildAgy compiles fakeagy and returns its path, for a test that launches
// it through the agy adapter with OGE_FAKE_AGY_FIXTURE.
func BuildAgy(t testing.TB) string { return build(t, "agy/fakeagy", "agy") }
