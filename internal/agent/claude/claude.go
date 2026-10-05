// Package claude is the native Claude Code adapter (ADR-0004). It spawns
// the user's unmodified claude binary in -p stream-json mode under an
// Öge-owned Launch profile, speaks the control protocol directly, checks
// the startup envelope, answers every permission itself, and turns the
// stream into the seam's normalised events (ADR-0005). It never handles a
// credential (ADR-0006).
package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/proc"
	"github.com/erengun/oge/internal/redact"
)

// Name is the agent name the adapter binds as.
const Name = "claude"

// Adapter launches claude Sessions.
type Adapter struct {
	// Path is the claude executable; empty means "claude" on PATH.
	Path string
	// Environ is the parent environment the child's is built from;
	// nil means os.Environ.
	Environ func() []string
	// StartTimeout bounds the initialize handshake; zero means a minute.
	StartTimeout time.Duration
}

// New returns the adapter for the claude on PATH.
func New() *Adapter { return &Adapter{} }

// closeGrace is how long Close waits for claude to exit on its own once
// stdin is closed, before killing its process group.
const closeGrace = 5 * time.Second

// Open spawns claude and completes the initialize handshake, which spends
// no quota. The envelope is checked when the first turn's system/init
// arrives: claude reports it only after a user message.
func (a *Adapter) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	prof, err := profileFor(spec.Role)
	if err != nil {
		return nil, err
	}
	path := a.Path
	if path == "" {
		if path, err = exec.LookPath("claude"); err != nil {
			return nil, fmt.Errorf("claude isn't installed or isn't on PATH")
		}
	}
	id := newUUID()
	args, err := prof.args(spec, id)
	if err != nil {
		return nil, err
	}
	environ := a.Environ
	if environ == nil {
		environ = os.Environ
	}
	cmd := exec.Command(path, args...)
	cmd.Dir = spec.Workspace
	cmd.Env = prof.env(environ(), spec)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s := &session{
		cmd: cmd, stdin: stdin, events: make(chan agent.Event, 256),
		stop: make(chan struct{}), exited: make(chan struct{}),
		pending: map[string]chan json.RawMessage{}, decided: map[string]agent.HostDecision{},
		hash: prof.hash(),
		env:  envelope{role: spec.Role, cwd: spec.Workspace, tools: prof.tools},
	}
	s.policy = newPolicy(spec, s.hash)
	cmd.Stderr = &s.stderr
	if err := proc.Start(cmd); err != nil {
		return nil, fmt.Errorf("starting claude: %w", err)
	}
	s.process = agent.Process{PID: cmd.Process.Pid, PGID: proc.Group(cmd)}
	go s.read(stdout)

	timeout := a.StartTimeout
	if timeout == 0 {
		timeout = time.Minute
	}
	if err := s.initialize(ctx, timeout); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

type session struct {
	cmd     *exec.Cmd
	process agent.Process
	events  chan agent.Event
	stop    chan struct{} // closed by Close: the reader stops delivering
	exited  chan struct{} // closed once claude has exited and been waited for
	stderr  tail
	policy  *policy
	hash    string
	env     envelope

	wmu   sync.Mutex // serialises writes to stdin
	stdin io.WriteCloser

	emu      sync.Mutex // serialises event delivery with closing the stream
	evClosed bool

	mu        sync.Mutex
	nextID    int
	pending   map[string]chan json.RawMessage
	decided   map[string]agent.HostDecision // by tool_use_id
	inFlight  bool
	interrupt bool // an interrupt was sent for the turn in flight
	opened    bool // SessionOpened was emitted
	caps      []string
	hooksRan  bool
	apiError  string // the last structured API error this turn
	dead      error  // the Session can take no more turns
	closeOnce sync.Once
}

func (s *session) Process() agent.Process     { return s.process }
func (s *session) Events() <-chan agent.Event { return s.events }

// initialize registers Öge's PreToolUse hook, so every tool call reaches
// Öge, sandboxed Bash included (ADR-0004).
func (s *session) initialize(ctx context.Context, timeout time.Duration) error {
	hooks := map[string]any{"PreToolUse": []any{map[string]any{"matcher": nil, "hookCallbackIds": []string{hookID}}}}
	resp, err := s.request(map[string]any{"subtype": "initialize", "hooks": hooks})
	if err != nil {
		return err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r, ok := <-resp:
		if !ok {
			<-s.exited
			return fmt.Errorf("claude exited before the session started%s", s.stderr.reason())
		}
		var cr struct {
			Subtype string `json:"subtype"`
			Error   string `json:"error"`
		}
		// The response carries account details: only its outcome is read.
		_ = json.Unmarshal(r, &cr)
		if cr.Subtype != "success" {
			return fmt.Errorf("claude refused to start a session: %s", clip(cr.Error))
		}
		return nil
	case <-s.exited:
		return fmt.Errorf("claude exited before the session started%s", s.stderr.reason())
	case <-t.C:
		return fmt.Errorf("claude didn't start a session within %s", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

const hookID = "oge_pre_tool_use"

// request sends a control request and returns where its response arrives.
func (s *session) request(req map[string]any) (<-chan json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("oge_req_%d", s.nextID)
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	return ch, s.write(map[string]any{"type": "control_request", "request_id": id, "request": req})
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

// Send writes one user message; the turn is accepted once it is written.
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
	s.inFlight, s.interrupt, s.apiError = true, false, ""
	s.mu.Unlock()
	msg := map[string]any{
		"type": "user", "session_id": "", "parent_tool_use_id": nil, "uuid": newUUID(),
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": t.Text}}},
	}
	// Accepted goes out before anything the reader sees after the write.
	s.emu.Lock()
	defer s.emu.Unlock()
	if err := s.write(msg); err != nil {
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
		return err
	}
	s.deliver(agent.Event{Kind: agent.TurnAccepted})
	return nil
}

// ErrNoInterrupt means the agent didn't report a non-terminal interrupt.
var ErrNoInterrupt = errors.New("claude didn't report the interrupt capability")

// Interrupt asks claude to stop the turn in flight. The turn then settles,
// and the Session takes another turn.
func (s *session) Interrupt() error {
	s.mu.Lock()
	if !s.inFlight || s.interrupt {
		s.mu.Unlock()
		return nil
	}
	if s.opened && !oneOf(CapInterrupt, s.caps) {
		s.mu.Unlock()
		return ErrNoInterrupt
	}
	s.interrupt = true
	s.mu.Unlock()
	_, err := s.request(map[string]any{"subtype": "interrupt"})
	return err
}

// Close closes stdin, which cancels any pending prompt and lets claude
// exit, then kills its process group if it hasn't within the grace.
func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.dead == nil {
			s.dead = errors.New("the session is closed")
		}
		s.mu.Unlock()
		close(s.stop)
		s.wmu.Lock()
		_ = s.stdin.Close()
		s.wmu.Unlock()
		select {
		case <-s.exited:
		case <-time.After(closeGrace):
			proc.Kill(s.cmd)
			<-s.exited
		}
	})
	return nil
}

// kill ends the process now; the reader then sees EOF.
func (s *session) kill(why error) {
	s.mu.Lock()
	if s.dead == nil {
		s.dead = why
	}
	s.mu.Unlock()
	proc.Kill(s.cmd)
}

// emit delivers an event unless the Session is closed.
func (s *session) emit(ev agent.Event) {
	s.emu.Lock()
	defer s.emu.Unlock()
	s.deliver(ev)
}

// deliver is emit with emu held.
func (s *session) deliver(ev agent.Event) {
	if s.evClosed {
		return
	}
	select {
	case s.events <- ev:
	case <-s.stop:
	}
}

// settle ends the turn in flight, if any.
func (s *session) settle(ev agent.Event) {
	s.mu.Lock()
	was := s.inFlight
	s.inFlight = false
	s.mu.Unlock()
	if was {
		ev.Kind = agent.TurnSettled
		s.emit(ev)
	}
}

// read turns claude's stdout into events until EOF, then waits for the
// process and closes the event stream.
func (s *session) read(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if ferr := s.frame(line); ferr != nil {
				s.settle(agent.Event{Failure: "malformed_frame: " + ferr.Error()})
				s.kill(ferr)
			}
		}
		if err != nil {
			break
		}
	}
	werr := proc.Wait(s.cmd)
	why := "agent_crash: claude exited before the turn settled"
	if werr != nil {
		why += " (" + werr.Error() + ")"
	}
	s.settle(agent.Event{Failure: why + s.stderr.reason()})
	s.mu.Lock()
	if s.dead == nil {
		s.dead = errors.New("claude exited")
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

// frame is the envelope every stream-json line shares.
type frame struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Message   json.RawMessage `json:"message"`
	Parent    *string         `json:"parent_tool_use_id"`
	Error     string          `json:"error"`
	// result
	IsError        bool   `json:"is_error"`
	Result         string `json:"result"`
	TerminalReason string `json:"terminal_reason"`
	NumTurns       int    `json:"num_turns"`
	// rate_limit_event
	RateLimit *struct {
		Status string `json:"status"`
		Type   string `json:"rateLimitType"`
	} `json:"rate_limit_info"`
}

// ignored are message types and system subtypes Öge reads nothing from.
var ignored = map[string]bool{
	"user": true, "command_lifecycle": true, "stream_event": true, "keep_alive": true,
	"tool_progress": true, "tool_use_summary": true, "prompt_suggestion": true, "control_cancel_request": true,
	"system/thinking_tokens": true, "system/status": true, "system/compact_boundary": true,
	"system/informational": true, "system/notification": true, "system/files_persisted": true,
	"system/session_state_changed": true, "system/permission_denied": true, "system/task_started": true,
	"system/task_progress": true, "system/task_updated": true, "system/task_notification": true,
	"system/background_tasks_changed": true,
}

// stopErrors are structured API errors that end the Run: retrying can't
// help until the user acts (ADR-0006, ADR-0012).
var stopErrors = map[string]string{
	"authentication_failed": "Claude's authentication failed",
	"oauth_org_not_allowed": "Claude's organisation doesn't allow this login",
	"billing_error":         "Claude reported a billing problem",
}

// transientErrors end the Run only if the turn then fails.
var transientErrors = map[string]bool{"rate_limit": true, "server_error": true, "overloaded": true}

const authBelongs = "; authentication belongs to Claude: fix it in claude itself, then run oge again"

// frame handles one line. An error means the line can't be decoded.
func (s *session) frame(line []byte) error {
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		return errors.New("a line isn't JSON")
	}
	if f.Type == "" {
		return errors.New("a message has no type")
	}
	kind := f.Type
	if f.Type == "system" {
		kind = "system/" + f.Subtype
	}
	switch {
	case f.Type == "control_response":
		return s.controlResponse(f.Response)
	case f.Type == "control_request":
		return s.controlRequest(f.RequestID, f.Request)
	case kind == "system/init":
		return s.init(line)
	case kind == "system/hook_started" || kind == "system/hook_response":
		s.mu.Lock()
		if !s.opened {
			s.hooksRan = true
		}
		s.mu.Unlock()
	case kind == "system/api_retry":
		var r struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(line, &r)
		s.apiErr(r.Error)
	case f.Type == "auth_status":
		if f.Error != "" {
			s.stopNow("Claude reported an authentication problem" + authBelongs)
		}
	case f.Type == "rate_limit_event":
		if f.RateLimit == nil {
			return nil
		}
		if st := f.RateLimit.Status; st != "allowed" && st != "allowed_warning" {
			s.stopNow(fmt.Sprintf("Claude's usage limit is reached (%s, %s); run oge again once it resets", clip(st), clip(f.RateLimit.Type)))
			return nil
		}
		s.emit(agent.Event{Kind: agent.Usage, Text: "rate limit " + clip(f.RateLimit.Status)})
	case f.Type == "assistant":
		return s.assistant(f)
	case f.Type == "result":
		s.result(f)
	case ignored[kind]:
	default:
		// Only the type is kept (ADR-0005).
		s.emit(agent.Event{Kind: agent.Unknown, Text: clip(kind)})
	}
	return nil
}

func (s *session) apiErr(code string) {
	if code == "" {
		return
	}
	if why, ok := stopErrors[code]; ok {
		s.stopNow(why + authBelongs)
		return
	}
	s.mu.Lock()
	s.apiError = code
	s.mu.Unlock()
}

// stopNow ends the turn as an Infrastructure stop and the process with it.
func (s *session) stopNow(why string) {
	s.settle(agent.Event{Stop: why})
	s.kill(errors.New(why))
}

func (s *session) controlResponse(raw json.RawMessage) error {
	var r struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return errors.New("a control response can't be decoded")
	}
	s.mu.Lock()
	ch := s.pending[r.RequestID]
	delete(s.pending, r.RequestID)
	s.mu.Unlock()
	if ch != nil {
		ch <- raw
	}
	return nil
}

// controlRequest answers can_use_tool and hook_callback with one host
// decision per tool-use id: whichever arrives second gets the first's
// answer (#35).
func (s *session) controlRequest(id string, raw json.RawMessage) error {
	var r struct {
		Subtype   string          `json:"subtype"`
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return errors.New("a control request can't be decoded")
	}
	var tool, useID string
	var input json.RawMessage
	switch r.Subtype {
	case "can_use_tool":
		tool, input, useID = r.ToolName, r.Input, r.ToolUseID
	case "hook_callback":
		var h struct {
			Input struct {
				Event     string          `json:"hook_event_name"`
				ToolName  string          `json:"tool_name"`
				ToolInput json.RawMessage `json:"tool_input"`
				ToolUseID string          `json:"tool_use_id"`
			} `json:"input"`
		}
		_ = json.Unmarshal(raw, &h)
		if h.Input.Event != "PreToolUse" {
			return s.write(response(id, map[string]any{"continue": true}))
		}
		tool, input, useID = h.Input.ToolName, h.Input.ToolInput, h.Input.ToolUseID
	default:
		s.emit(agent.Event{Kind: agent.Unknown, Text: "control_request/" + clip(r.Subtype)})
		return s.write(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": id, "error": "Öge doesn't support " + r.Subtype}})
	}
	s.mu.Lock()
	d, seen := s.decided[useID]
	if !seen {
		d = s.policy.decide(tool, input)
		if useID != "" {
			s.decided[useID] = d
		}
	}
	s.mu.Unlock()
	if !seen {
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &d})
	}
	if r.Subtype == "hook_callback" {
		if d.Decision == "allow" {
			// No hook decision: the native rules and sandbox still apply,
			// and a prompt that follows gets the same answer.
			return s.write(response(id, map[string]any{"continue": true}))
		}
		return s.write(response(id, map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": d.Reason}}))
	}
	if d.Decision == "allow" {
		in := input
		if len(in) == 0 {
			in = json.RawMessage("{}")
		}
		return s.write(response(id, map[string]any{"behavior": "allow", "updatedInput": in}))
	}
	return s.write(response(id, map[string]any{"behavior": "deny", "message": d.Reason}))
}

func response(id string, body any) map[string]any {
	return map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": id, "response": body}}
}

// init checks the envelope claude reports at the start of every turn.
func (s *session) init(line []byte) error {
	var in initFrame
	if err := json.Unmarshal(line, &in); err != nil {
		return errors.New("system/init can't be decoded")
	}
	s.mu.Lock()
	first := !s.opened
	s.opened = true
	hooks := s.hooksRan
	s.caps = effective(in.Capabilities)
	caps := s.caps
	s.mu.Unlock()
	warnings, fatal := s.env.check(in, hooks)
	if first {
		status := "ok"
		if fatal != "" {
			status = "fail"
		} else if len(warnings) > 0 {
			status = "warn"
		}
		s.emit(agent.Event{Kind: agent.SessionOpened, Session: &agent.SessionInfo{
			AgentVersion: clip(in.Version), Capabilities: caps, Profile: s.hash, Envelope: status,
			AuthSource: "apiKeySource:" + clip(in.APIKeySource),
		}})
		for _, w := range warnings {
			s.emit(agent.Event{Kind: agent.Warning, Text: "envelope: " + w})
		}
	}
	if fatal != "" {
		s.stopNow("the agent's startup envelope breaks the Launch profile: " + fatal)
	}
	return nil
}

type content struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (s *session) assistant(f frame) error {
	if f.Error != "" {
		s.apiErr(f.Error)
	}
	if f.Parent != nil {
		return nil // a subagent's message; the implementer has no Task tool
	}
	var m struct {
		Content []content `json:"content"`
	}
	if err := json.Unmarshal(f.Message, &m); err != nil {
		return errors.New("an assistant message can't be decoded")
	}
	for _, c := range m.Content {
		switch c.Type {
		case "text":
			if t := strings.TrimSpace(c.Text); t != "" {
				s.emit(agent.Event{Kind: agent.Claim, Text: string(redact.Redact([]byte(t)))})
			}
		case "tool_use":
			var in map[string]any
			_ = json.Unmarshal(c.Input, &in)
			s.emit(agent.Event{Kind: agent.Claim, Tool: clip(c.Name), Target: target(c.Name, in, s.policy.ws)})
		}
	}
	return nil
}

// Exits the implementer may declare (ADR-0007).
var implementerExits = []string{"done", "infeasible"}

func (s *session) result(f frame) {
	s.emit(agent.Event{Kind: agent.Usage, Text: fmt.Sprintf("%d model turns", f.NumTurns)})
	s.mu.Lock()
	interrupted, apiErr := s.interrupt, s.apiError
	s.mu.Unlock()
	switch {
	case !f.IsError && f.Subtype == "success":
		s.settle(agent.Event{Exit: exitOf(f.Result)})
	case interrupted:
		s.settle(agent.Event{Failure: "interrupted"})
	case transientErrors[apiErr]:
		s.settle(agent.Event{Stop: "Claude's API failed (" + apiErr + "); run oge again later"})
	default:
		why := "agent_error: " + clip(f.Subtype)
		if f.TerminalReason != "" {
			why += " (" + clip(f.TerminalReason) + ")"
		}
		s.settle(agent.Event{Failure: why})
	}
}

// exitOf is the Exit a final message declares on its last line, as
// "Exit: <name>"; done when it declares none.
// TODO(#44-decision): Claude declares an Exit in prose, as the Briefing
// asks; an unknown name counts as none.
func exitOf(result string) string {
	lines := strings.Split(strings.TrimSpace(result), "\n")
	last := strings.ToLower(strings.TrimSpace(lines[len(lines)-1]))
	last = strings.Trim(last, "*`_ ")
	if name, ok := strings.CutPrefix(last, "exit:"); ok {
		name = strings.Trim(strings.TrimSpace(name), "*`_. ")
		if oneOf(name, implementerExits) {
			return name
		}
	}
	return "done"
}

// tail keeps the end of claude's stderr in memory, for a failure's reason.
// It is redacted before use and never persisted whole.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailMax = 4 << 10

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailMax {
		t.buf = t.buf[len(t.buf)-tailMax:]
	}
	return len(p), nil
}

// reason is the last line of stderr, redacted, as ": <line>".
func (t *tail) reason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return ""
	}
	return ": " + clip(string(redact.Redact([]byte(last))))
}

// clip bounds agent-provided text kept in events.
func clip(s string) string { return shorten(s, 200) }

// newUUID is a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
