// Package codex is the native Codex adapter (ADR-0018). It runs one
// `codex app-server` per Run over stdio, in its own process group, speaks
// a hand-written subset of the JSON-RPC protocol, starts each thread with
// Öge's inline permission profile and isolation recipe, asserts the
// envelope thread/start reports, proves what that can't show with a
// no-model sandbox self-test, and answers every approval itself, one shot
// at a time. It never handles a credential (ADR-0006).
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/proc"
	"github.com/erengun/oge/internal/redact"
)

// Name is the agent name the adapter binds as.
const Name = "codex"

// MinVersion is the oldest codex-cli the adapter supports: the spike's
// (ADR-0018). LastTested is the newest it was run against; a newer one
// works with a warning.
const (
	MinVersion = "0.155.1"
	LastTested = "0.160.1"
)

// Capabilities the adapter declares (ADR-0005). A decline's reason never
// reaches the model, so deny_reason_reaches_model is absent (ADR-0018).
const (
	CapHostRequests = "host_requests"
	CapInterrupt    = "interrupt"
)

// Adapter launches codex Sessions.
type Adapter struct {
	// Path is the codex executable; empty means "codex" on PATH.
	Path string
	// Environ is the parent environment; nil means os.Environ.
	Environ func() []string
	// Timeout bounds each of Öge's protocol requests; zero means
	// requestTimeout.
	Timeout time.Duration
	// CloseGrace is how long Close waits at each step; zero means
	// closeGrace.
	CloseGrace time.Duration
	// SelfTest proves the profile's denies; nil means sandboxSelfTest.
	SelfTest func(ctx context.Context, path string, env []string, p *profile, probes []string) error

	mu     sync.Mutex
	passed map[string]bool // self-test results that passed, by selfTestKey
}

// New returns the adapter for the codex on PATH.
func New() *Adapter { return &Adapter{} }

// AutoMode is true: a Chat runs codex under its Launch profile, which
// declines every approval itself and denies Öge's DenyRead paths in its
// sandbox (D2, ADR-0018).
func (a *Adapter) AutoMode() bool { return true }

const (
	requestTimeout = 30 * time.Second
	closeGrace     = 5 * time.Second
)

// Open spawns the Run's app-server, runs the readiness probe and the
// sandbox self-test, and starts the thread, failing closed on any
// envelope it can't assert.
func (a *Adapter) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	if spec.Role != "implementer" {
		// TODO(#60): the verifier's read-only profile, write scope and
		// shell_snapshot=false.
		return nil, fmt.Errorf("codex adapter: the %s role isn't supported yet", spec.Role)
	}
	if spec.Permissions != "" && spec.Permissions != agent.AutoPermissions {
		return nil, fmt.Errorf("codex adapter: codex has no %s permission mode", spec.Permissions)
	}
	path := a.Path
	if path == "" {
		var err error
		if path, err = exec.LookPath("codex"); err != nil {
			return nil, fmt.Errorf("codex isn't installed or isn't on PATH")
		}
	}
	environ := a.Environ
	if environ == nil {
		environ = os.Environ
	}
	env := append(environ(), "OGE_RUN_ID="+spec.RunID, "OGE_ROLE="+spec.Role)
	if spec.Cache != "" {
		env = append(env, "GOCACHE="+spec.Cache)
	}
	prof := newProfile(spec)
	// notify runs a command outside the sandbox at a turn's end: pinned
	// empty for the process and, in config, for the thread.
	cmd := exec.Command(path, "app-server", "-c", "notify=[]")
	cmd.Dir, cmd.Env = spec.Workspace, env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s := &session{
		cmd: cmd, stdin: stdin, events: make(chan agent.Event, 256), stop: make(chan struct{}), exited: make(chan struct{}),
		pending: map[int]chan reply{}, items: map[string][]string{}, prof: prof,
		ws: withReal(spec.Workspace), timeout: a.Timeout, grace: a.CloseGrace,
	}
	if s.timeout == 0 {
		s.timeout = requestTimeout
	}
	if s.grace == 0 {
		s.grace = closeGrace
	}
	cmd.Stderr = &s.stderr
	if err := proc.Start(cmd); err != nil {
		return nil, fmt.Errorf("starting codex: %w", err)
	}
	s.process = agent.Process{PID: cmd.Process.Pid, PGID: proc.Group(cmd)}
	go s.read(stdout)
	if err := s.start(ctx, a, path, env); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// start runs the readiness probe, which writes nothing (ADR-0006), then
// the self-test and thread/start.
func (s *session) start(ctx context.Context, a *Adapter, path string, env []string) error {
	var init struct {
		UserAgent string `json:"userAgent"`
		CodexHome string `json:"codexHome"`
	}
	if err := s.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "oge", "title": "Öge", "version": "0"}}, &init); err != nil {
		return err
	}
	// The user agent is "oge/<codex version> (…)".
	ver, _, _ := strings.Cut(strings.TrimPrefix(init.UserAgent, "oge/"), " ")
	if version(ver) == nil || older(ver, MinVersion) {
		return fmt.Errorf("codex %s is older than %s, the oldest Öge supports; update codex", clip(ver), MinVersion)
	}
	s.version = ver
	s.prof.denyAuth(init.CodexHome)
	s.prof.denyAuth(codexHome(env))
	if err := s.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	var acct struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	// Never refreshToken:true: the probe must not touch credentials.
	if err := s.call(ctx, "account/read", map[string]any{"refreshToken": false}, &acct); err != nil {
		return err
	}
	if acct.Account == nil {
		return errors.New("codex isn't logged in" + authBelongs)
	}
	s.auth = clip(acct.Account.Type)
	var limits struct {
		RateLimits *struct {
			Reached *string `json:"rateLimitReachedType"`
		} `json:"rateLimits"`
	}
	if err := s.call(ctx, "account/rateLimits/read", map[string]any{}, &limits); err != nil {
		return err
	}
	if l := limits.RateLimits; l != nil && l.Reached != nil {
		return fmt.Errorf("codex's usage limit is reached (%s); run oge again once it resets", clip(*l.Reached))
	}
	if err := projectConfig(s.prof.spec.Workspace); err != nil {
		return err
	}
	// Codex's MCP servers would run as its children, outside the sandbox
	// and without approvals: each one the effective config declares,
	// project layers included, is turned off for the thread.
	var cfg struct {
		Config struct {
			MCP map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := s.call(ctx, "config/read", map[string]any{"includeLayers": false, "cwd": s.prof.spec.Workspace}, &cfg); err != nil {
		return err
	}
	for name := range cfg.Config.MCP {
		s.prof.mcpOff = append(s.prof.mcpOff, name)
	}
	sort.Strings(s.prof.mcpOff)
	s.mu.Lock()
	s.hash = s.prof.hash()
	s.mu.Unlock()
	test := a.SelfTest
	if test == nil {
		test = sandboxSelfTest
	}
	// The key is the paths actually probed: one that appears later, such
	// as QA's directory on a send-back, forces a new test.
	probes := s.prof.probes()
	key := selfTestKey(path, ver, s.prof, probes)
	a.mu.Lock()
	cached := a.passed[key]
	a.mu.Unlock()
	if !cached {
		if err := test(ctx, path, env, s.prof, probes); err != nil {
			return fmt.Errorf("codex's sandbox self-test failed, so Öge can't prove its private files are hidden: %w", err)
		}
		a.mu.Lock()
		if a.passed == nil {
			a.passed = map[string]bool{}
		}
		a.passed[key] = true
		a.mu.Unlock()
	}
	var env0 envelope
	if err := s.call(ctx, "thread/start", s.prof.threadStart(), &env0); err != nil {
		// An unloadable profile is refused here: fail closed (ADR-0018).
		return fmt.Errorf("codex refused Öge's thread settings: %w", err)
	}
	if err := s.prof.check(env0); err != nil {
		return fmt.Errorf("the agent's startup envelope breaks the Launch profile: %w", err)
	}
	s.mu.Lock()
	s.thread, s.opened = env0.Thread.ID, true
	s.mu.Unlock()
	return nil
}

// projectConfig fails closed when the Workspace's project config sets
// anything. config/read leaves an untrusted project's layer out, and
// thread/start then trusts the Workspace and loads it (checked live on
// 0.162.1), too late to undo: mcp_servers would run servers outside the
// sandbox, notify a command, model_providers another host. No key is
// known to be harmless there, so none is allowed; profiles.* are keys
// too.
func projectConfig(ws string) error {
	b, err := os.ReadFile(filepath.Join(ws, ".codex", "config.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("the repository's .codex/config.toml can't be read: %w", err)
	}
	var cfg map[string]any
	if _, err := toml.Decode(string(b), &cfg); err != nil {
		return errors.New("the repository's .codex/config.toml can't be parsed, so Öge can't tell what it would change")
	}
	if len(cfg) > 0 {
		keys := make([]string, 0, len(cfg))
		for k := range cfg {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return fmt.Errorf("the repository's .codex/config.toml sets %s, which Codex would load outside Öge's control (MCP servers, notify commands or model providers run outside the sandbox); codex can't run here until it's emptied", clip(strings.Join(keys, ", ")))
	}
	return nil
}

// codexHome is $CODEX_HOME in env; ~/.codex is always denied too, and
// a relative one is ignored (denyAuth).
func codexHome(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "CODEX_HOME="); ok && v != "" {
			return v
		}
	}
	return ""
}

const authBelongs = "; authentication belongs to Codex: fix it in codex itself (codex login), then run oge again"

type session struct {
	cmd     *exec.Cmd
	process agent.Process
	events  chan agent.Event
	stop    chan struct{}
	exited  chan struct{}
	stderr  tail
	timeout time.Duration
	grace   time.Duration
	prof    *profile
	hash    string
	ws      []string // the Workspace, as given and with symlinks resolved
	version string
	auth    string

	wmu   sync.Mutex
	stdin io.WriteCloser

	emu      sync.Mutex
	evClosed bool

	mu        sync.Mutex
	nextID    int
	pending   map[int]chan reply
	thread    string
	opened    bool // the envelope passed
	announced bool // SessionOpened was emitted
	inFlight  bool
	turn      string // the turn known to be running, once turn/start answered
	interrupt bool
	items     map[string][]string // a turn's file-change paths, by item id
	last      string              // the turn's last agent message
	usage     *agent.Event
	dead      error
	why       error // why Öge ended the Session, failing closed
	closeOnce sync.Once
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type reply struct {
	result json.RawMessage
	err    *rpcError
}

func (s *session) Process() agent.Process     { return s.process }
func (s *session) Events() <-chan agent.Event { return s.events }

// call sends a request and waits for its response, at most s.timeout.
func (s *session) call(ctx context.Context, method string, params, out any) error {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan reply, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	drop := func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}
	if err := s.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		drop()
		return s.exitErr(err)
	}
	t := time.NewTimer(s.timeout)
	defer t.Stop()
	select {
	case r, ok := <-ch:
		if !ok {
			return s.exitErr(nil)
		}
		if r.err != nil {
			return fmt.Errorf("%s: %s (%d)", method, clip(redacted(r.err.Message)), r.err.Code)
		}
		if out != nil && json.Unmarshal(r.result, out) != nil {
			return fmt.Errorf("%s: the response can't be decoded", method)
		}
		return nil
	case <-t.C:
		drop()
		return fmt.Errorf("codex didn't answer %s within %s", method, s.timeout)
	case <-ctx.Done():
		drop()
		return ctx.Err()
	}
}

// exitErr reports codex having exited, if it has, rather than err.
func (s *session) exitErr(err error) error {
	select {
	case <-s.exited:
		s.mu.Lock()
		why := s.why
		s.mu.Unlock()
		if why != nil {
			return why
		}
		return fmt.Errorf("codex exited%s", s.stderr.reason())
	case <-time.After(2 * time.Second):
		if err == nil {
			err = errors.New("codex exited")
		}
		return err
	}
}

// notify sends a notification; a failed write reports codex having
// exited, and why Öge ended it, as call does.
func (s *session) notify(method string, params any) error {
	if err := s.write(map[string]any{"method": method, "params": params}); err != nil {
		return s.exitErr(err)
	}
	return nil
}

func (s *session) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

// Send starts a turn; it is accepted once turn/start is written, and
// known to be running once codex answers it.
func (s *session) Send(t agent.Turn) error {
	s.mu.Lock()
	if s.dead != nil {
		s.mu.Unlock()
		return s.dead
	}
	if s.inFlight {
		s.mu.Unlock()
		return agent.ErrTurnInFlight
	}
	s.inFlight, s.turn, s.interrupt, s.last, s.usage = true, "", false, "", nil
	clear(s.items)
	first := !s.announced
	s.announced = true
	s.mu.Unlock()
	s.emit(agent.Event{Kind: agent.TurnAccepted})
	if first {
		s.emit(agent.Event{Kind: agent.SessionOpened, Session: &agent.SessionInfo{
			AgentVersion: clip(s.version), Capabilities: []string{CapHostRequests, CapInterrupt},
			Profile: s.hash, Envelope: "ok", AuthSource: "account:" + s.auth,
		}})
		if older(LastTested, s.version) {
			s.emit(agent.Event{Kind: agent.Warning, Text: "envelope: codex " + clip(s.version) + " is newer than " + LastTested + ", the last version Öge was tested with"})
		}
	}
	var r struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err := s.call(context.Background(), "turn/start", map[string]any{"threadId": s.thread,
		"input": []any{map[string]any{"type": "text", "text": t.Text}}}, &r)
	if err != nil {
		s.settle(agent.Event{Failure: "agent_error: " + err.Error()})
		return nil
	}
	s.mu.Lock()
	if s.inFlight && s.turn == "" {
		s.turn = r.Turn.ID
	}
	s.mu.Unlock()
	return nil
}

// ErrNotRunning means no turn is known to be running yet, so there is
// nothing codex can interrupt; Close ends the process instead.
var ErrNotRunning = errors.New("codex hasn't confirmed the turn is running")

// Interrupt asks codex to stop the turn known to be running. Its
// acknowledgement is turn/completed with status interrupted; an
// interrupt of an ended turn would never be answered (ADR-0018).
func (s *session) Interrupt() error {
	s.mu.Lock()
	if !s.inFlight || s.interrupt {
		s.mu.Unlock()
		return nil
	}
	if s.turn == "" {
		s.mu.Unlock()
		return ErrNotRunning
	}
	s.interrupt = true
	params := map[string]any{"threadId": s.thread, "turnId": s.turn}
	s.mu.Unlock()
	go func() { _ = s.call(context.Background(), "turn/interrupt", params, nil) }()
	return nil
}

// Close closes stdin, so the app-server exits; if it hasn't within the
// grace, its process group gets SIGTERM, then SIGKILL.
func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.dead == nil {
			s.dead = errors.New("the session is closed")
		}
		s.mu.Unlock()
		close(s.stop)
		_ = s.stdin.Close()
		for _, step := range []func(){nil, func() { proc.Terminate(s.cmd) }, func() { proc.Kill(s.cmd) }} {
			if step != nil {
				step()
			}
			select {
			case <-s.exited:
				return
			case <-time.After(s.grace):
			}
		}
	})
	return nil
}

func (s *session) emit(ev agent.Event) {
	s.emu.Lock()
	defer s.emu.Unlock()
	if s.evClosed {
		return
	}
	select {
	case s.events <- ev:
	case <-s.stop:
	}
}

// settle ends the turn in flight, if any, after its usage.
func (s *session) settle(ev agent.Event) {
	s.mu.Lock()
	was, use := s.inFlight, s.usage
	s.inFlight, s.turn, s.usage = false, "", nil
	s.mu.Unlock()
	if !was {
		return
	}
	if use != nil {
		s.emit(*use)
	}
	ev.Kind = agent.TurnSettled
	s.emit(ev)
}

// read turns the app-server's stdout into events until EOF. Its death
// fails the turn in flight (ADR-0018).
func (s *session) read(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<16), maxFrame)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := s.frame(line); err != nil {
			s.settle(agent.Event{Failure: "malformed_frame: " + err.Error()})
			s.kill(err)
			break
		}
	}
	if sc.Err() != nil {
		s.settle(agent.Event{Failure: "malformed_frame: " + sc.Err().Error()})
	}
	// End whatever its tools left running in its group; escaped children
	// are residue, not contained (ADR-0012).
	proc.Kill(s.cmd)
	werr := proc.Wait(s.cmd)
	why := "agent_crash: codex exited before the turn settled"
	if werr != nil {
		why += " (" + werr.Error() + ")"
	}
	s.settle(agent.Event{Failure: why + s.stderr.reason()})
	s.mu.Lock()
	if s.dead == nil {
		s.dead = errors.New("codex exited")
	}
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.mu.Unlock()
	close(s.exited)
	s.emu.Lock()
	s.evClosed = true
	close(s.events)
	s.emu.Unlock()
}

const maxFrame = 16 << 20

func (s *session) kill(why error) {
	s.mu.Lock()
	if s.dead == nil {
		s.dead = why
	}
	s.mu.Unlock()
	proc.Kill(s.cmd)
}

// message is any JSON-RPC message: a request has a method and an id, a
// notification only a method, a response only an id.
type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// ignored are notifications Öge reads nothing from.
var ignored = map[string]bool{
	"turn/started": true, "thread/started": true, "thread/status/changed": true, "turn/diff/updated": true,
	"serverRequest/resolved": true, "remoteControl/status/changed": true, "deprecationNotice": true,
	"thread/goal/cleared": true, "account/rateLimits/updated": true, "account/updated": true, "turn/plan/updated": true,
}

func (s *session) frame(line []byte) error {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		return errors.New("a line isn't JSON")
	}
	switch {
	case m.Method != "" && len(m.ID) > 0:
		return s.request(m)
	case m.Method != "":
		s.notification(m)
	case len(m.ID) > 0:
		var id int
		if json.Unmarshal(m.ID, &id) != nil {
			return nil // not one of Öge's requests
		}
		s.mu.Lock()
		ch := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if ch != nil {
			ch <- reply{result: m.Result, err: m.Error}
		}
	default:
		return errors.New("a message has neither a method nor an id")
	}
	return nil
}

type item struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	Text           string `json:"text"`
	Command        string `json:"command"`
	CommandActions []struct {
		Command string `json:"command"`
	} `json:"commandActions"`
	Changes []struct {
		Path string `json:"path"`
	} `json:"changes"`
}

// command is the command as the model wrote it, without the shell
// wrapper codex runs it in.
func (it item) command() string {
	if len(it.CommandActions) == 1 && it.CommandActions[0].Command != "" {
		return it.CommandActions[0].Command
	}
	return it.Command
}

func (s *session) notification(m message) {
	var p struct {
		ThreadID string `json:"threadId"`
		Item     item   `json:"item"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		TokenUsage struct {
			Total struct {
				Total int `json:"totalTokens"`
			} `json:"total"`
			Last struct {
				Input int `json:"inputTokens"`
			} `json:"last"`
			Window int `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	_ = json.Unmarshal(m.Params, &p)
	if m.Method == "mcpServer/startupStatus/updated" {
		// An MCP server started despite being turned off: it runs outside
		// the sandbox, so the Session can't go on (fail closed).
		var mp struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &mp)
		s.failClosed("an MCP server (" + clip(mp.Name) + ") started outside Öge's sandbox")
		return
	}
	if m.Method == "item/started" && p.Item.Type == "mcpToolCall" {
		// A tripwire: an MCP tool from anywhere (apps, plugins) runs
		// outside the sandbox and never asks for approval.
		s.failClosed("the agent called an MCP tool, which runs outside Öge's sandbox")
		return
	}
	s.mu.Lock()
	ours := p.ThreadID == "" || p.ThreadID == s.thread
	s.mu.Unlock()
	switch {
	case !ours:
		s.emit(agent.Event{Kind: agent.Unknown, Text: clip(m.Method)})
	case m.Method == "item/started":
		switch p.Item.Type {
		case "commandExecution":
			s.emit(agent.Event{Kind: agent.Claim, Tool: "Bash", Target: s.target(p.Item.command())})
		case "fileChange":
			var paths []string
			for _, c := range p.Item.Changes {
				paths = append(paths, c.Path)
			}
			s.mu.Lock()
			s.items[p.Item.ID] = paths
			s.mu.Unlock()
			if len(paths) > 0 {
				s.emit(agent.Event{Kind: agent.Claim, Tool: "Edit", Target: s.target(paths[0])})
			}
		}
	case m.Method == "item/completed":
		if p.Item.Type == "agentMessage" {
			if t := strings.TrimSpace(p.Item.Text); t != "" {
				s.mu.Lock()
				s.last = t
				s.mu.Unlock()
				s.emit(agent.Event{Kind: agent.Claim, Text: redacted(t)})
			}
		}
	case m.Method == "thread/tokenUsage/updated":
		n := p.TokenUsage.Total.Total
		ev := agent.Event{Kind: agent.Usage, Text: "codex token usage", Tokens: &n}
		if p.TokenUsage.Window > 0 && p.TokenUsage.Last.Input > 0 {
			ev.Context = &agent.ContextUse{Used: p.TokenUsage.Last.Input, Window: p.TokenUsage.Window}
		}
		s.mu.Lock()
		s.usage = &ev
		s.mu.Unlock()
	case m.Method == "turn/completed":
		s.mu.Lock()
		mine := s.inFlight && (s.turn == "" || s.turn == p.Turn.ID)
		last := s.last
		s.mu.Unlock()
		if !mine {
			return
		}
		switch p.Turn.Status {
		case "completed":
			s.settle(agent.Event{Exit: agent.ExitOf(last, s.prof.spec.Role)})
		case "interrupted":
			s.settle(agent.Event{Failure: "interrupted"})
		default:
			why := "agent_error: turn " + clip(p.Turn.Status)
			if p.Turn.Error != nil {
				why += " (" + clip(redacted(p.Turn.Error.Message)) + ")"
			}
			s.settle(agent.Event{Failure: why})
		}
	case ignored[m.Method] || strings.HasSuffix(m.Method, "/delta") || strings.HasSuffix(m.Method, "Delta"):
	default:
		// Only the method is kept (ADR-0005, ADR-0018).
		s.emit(agent.Event{Kind: agent.Unknown, Text: clip(m.Method)})
	}
}

// failClosed ends the Session: the turn in flight fails, and Open and
// Send report why.
func (s *session) failClosed(what string) {
	why := errors.New("envelope_failed: " + what)
	s.mu.Lock()
	if s.why == nil {
		s.why = why
	}
	s.mu.Unlock()
	s.kill(why)
	s.settle(agent.Event{Failure: why.Error()})
}

// Policy rules, as recorded on each HostDecision; the same names as the
// claude adapter's.
const (
	ruleNoInteractive = "no_interactive_approval"
	ruleUnchecked     = "envelope_not_passed"
)

// noReasonToModel ends every decline's recorded reason.
const noReasonToModel = " (Codex doesn't pass this reason to the model.)"

// request answers a server request. Approvals are one-shot and, in C1,
// always declined: codex asks only for what its sandbox refused, and Öge
// never pre-authorises leaving the sandbox. No session-wide amendment is
// ever sent (ADR-0018). Any other request is refused.
func (s *session) request(m message) error {
	var p struct {
		ItemID string `json:"itemId"`
		item
	}
	_ = json.Unmarshal(m.Params, &p)
	s.mu.Lock()
	d := agent.HostDecision{Family: agent.Approval, By: "launch_profile:" + s.hash, Decision: "deny"}
	s.mu.Unlock()
	switch m.Method {
	case "item/commandExecution/requestApproval":
		d.Tool, d.Target = "Bash", s.target(p.command())
		// Codex asks only for what its sandbox refused: accepting would
		// run the command outside the sandbox, which no Launch profile
		// pre-authorises.
		d.Rule, d.Reason = ruleNoInteractive, "Öge denied this: running a command outside Codex's sandbox isn't pre-authorised."
	case "item/fileChange/requestApproval":
		// The request names only the item; its paths came with
		// item/started (ADR-0018), and are shown, never trusted: codex
		// asks only for a write its sandbox refused, so accepting would
		// write outside the sandbox, and a grantRoot would keep that
		// open for the rest of the thread.
		s.mu.Lock()
		paths := s.items[p.ItemID]
		s.mu.Unlock()
		d.Tool = "Edit"
		if len(paths) > 0 {
			d.Target = s.target(paths[0])
		}
		d.Rule, d.Reason = ruleNoInteractive, "Öge denied this: writing outside Codex's sandbox isn't pre-authorised."
	default:
		s.emit(agent.Event{Kind: agent.Unknown, Text: "server_request/" + clip(m.Method)})
		return s.write(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "Öge doesn't support " + m.Method}})
	}
	s.mu.Lock()
	open := s.opened && s.dead == nil
	s.mu.Unlock()
	if !open {
		d.Decision, d.Rule, d.Reason = "deny", ruleUnchecked, "Öge denied this: the agent's startup envelope hasn't passed."
	}
	// Codex takes no decline reason: the model never sees it (ADR-0018).
	d.Reason += noReasonToModel
	s.emit(agent.Event{Kind: agent.HostRequest, Host: &d})
	return s.write(map[string]any{"id": m.ID, "result": map[string]any{"decision": "decline"}})
}

// target is a short, redacted target: a command's first line, or a path
// relative to the Workspace.
func (s *session) target(t string) string {
	t, _, _ = strings.Cut(strings.TrimSpace(t), "\n")
	for _, w := range s.ws {
		if rel, err := filepath.Rel(w, t); err == nil && filepath.IsAbs(t) && !strings.HasPrefix(rel, "..") {
			t = filepath.ToSlash(rel)
			break
		}
	}
	return shorten(redacted(t), 120)
}

func withReal(dir string) []string {
	dir = filepath.Clean(dir)
	out := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r != dir {
		out = append(out, r)
	}
	return out
}

func oneOf(s string, list []string) bool {
	for _, l := range list {
		if s == l {
			return true
		}
	}
	return false
}

func redacted(s string) string { return string(redact.Redact([]byte(s))) }

// clip bounds agent-provided text kept in events.
func clip(s string) string { return shorten(s, 200) }

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// tail keeps the end of codex's stderr, for a failure's reason. It is
// redacted before use and never persisted whole.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4<<10 {
		t.buf = t.buf[len(t.buf)-4<<10:]
	}
	return len(p), nil
}

func (t *tail) reason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	last := strings.TrimSpace(lastLine(string(t.buf)))
	if last == "" {
		return ""
	}
	return ": " + clip(redacted(last))
}
