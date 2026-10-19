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
	// CloseGrace is how long Close waits at each step (stdin closed,
	// SIGTERM, SIGKILL); zero means closeGrace.
	CloseGrace time.Duration
	// Profiles are extra Claude profiles kept outside $HOME, by name: the
	// seam for the user-level Öge config (#129). See Profiles.
	Profiles map[string]string
}

// New returns the adapter for the claude on PATH.
func New() *Adapter { return &Adapter{} }

// closeGrace is how long Close waits for claude to exit on its own once
// stdin is closed, and again after SIGTERM, before SIGKILL.
const closeGrace = 5 * time.Second

// AutoMode is true: claude's --permission-mode auto runs a Chat (D2).
func (a *Adapter) AutoMode() bool { return true }

// StrongestModel is the model an Advisor runs by default (D-A1): claude's
// alias for its newest Opus.
func (a *Adapter) StrongestModel() string { return "opus" }

// AdvisorReadOnly is true: the advisor's Launch profile has only the
// tools that read, and its policy denies every other (profile.go).
func (a *Adapter) AdvisorReadOnly() bool { return true }

// Open spawns claude and completes the initialize handshake, which spends
// no quota. The envelope is checked when the first turn's system/init
// arrives: claude reports it only after a user message.
func (a *Adapter) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	prof, err := profileFor(spec.Role)
	if err != nil {
		return nil, err
	}
	prof.extra = a.Profiles
	prof.effort = spec.Effort
	if prof.mode, err = permissionMode(spec.Permissions); err != nil {
		return nil, err
	}
	if prof.mode != "default" && spec.Role != "implementer" {
		// Only the implementer may run under its own permission mode (D1).
		return nil, fmt.Errorf("claude adapter: the %s can't run in %s mode", spec.Role, prof.mode)
	}
	path := a.Path
	if path == "" {
		if path, err = exec.LookPath("claude"); err != nil {
			return nil, fmt.Errorf("claude isn't installed or isn't on PATH")
		}
	}
	id := newUUID()
	environ := a.Environ
	if environ == nil {
		environ = os.Environ
	}
	parent := environ()
	args, err := prof.args(spec, id, parent)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	cmd.Dir = spec.Workspace
	if cmd.Env, err = prof.env(parent, spec); err != nil {
		return nil, err
	}
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
		env:  envelope{role: spec.Role, cwd: spec.Workspace, tools: prof.tools, mode: prof.mode},
		auto: prof.mode == "auto", asks: map[string]*pendingAsk{},
	}
	s.policy = newPolicy(spec, s.hash)
	if s.auto {
		s.policy.readOK = toolchain(parentGetenv(parent))
	}
	if s.grace = a.CloseGrace; s.grace == 0 {
		s.grace = closeGrace
	}
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
	grace   time.Duration
	policy  *policy
	hash    string
	env     envelope
	// auto is a Chat Session in claude's auto mode: Öge's policy only
	// hard-denies, and what the mode asks about goes to the host.
	auto bool
	// asks are the Asks waiting for the human, by control request id,
	// under mu; a cancel or the turn settling withdraws them.
	asks map[string]*pendingAsk

	wmu   sync.Mutex // serialises writes to stdin
	stdin io.WriteCloser

	emu      sync.Mutex // serialises event delivery with closing the stream
	evClosed bool
	// accepting holds the reader's events while Send writes a turn, so
	// TurnAccepted comes first without holding emu during the write.
	accepting bool
	held      []agent.Event

	mu        sync.Mutex
	nextID    int
	pending   map[string]chan json.RawMessage
	decided   map[string]agent.HostDecision // by tool_use_id
	inFlight  bool
	interrupt bool // an interrupt was sent for the turn in flight
	opened    bool // the envelope check passed and SessionOpened was emitted
	fatal     bool // the envelope check failed
	caps      []string
	hooksRan  bool
	apiError  string // the last structured API error this turn
	dead      error  // the Session can take no more turns
	closeOnce sync.Once
	ctxUsed   int           // the last top-level assistant message's input tokens, under mu
	model     string        // system/init's model id, under mu
	friction  frictionMeter // the turn's policy friction, under mu
}

func (s *session) Process() agent.Process     { return s.process }
func (s *session) Events() <-chan agent.Event { return s.events }

// initialize registers Öge's PreToolUse hook, so every tool call reaches
// Öge, sandboxed Bash included (ADR-0004).
func (s *session) initialize(ctx context.Context, timeout time.Duration) error {
	hooks := map[string]any{"PreToolUse": []any{map[string]any{"matcher": nil, "hookCallbackIds": []string{hookID}}}}
	resp, err := s.request(map[string]any{"subtype": "initialize", "hooks": hooks})
	if err != nil {
		// A write can fail (e.g. a broken pipe) because claude has already
		// exited; report that, not the pipe error.
		select {
		case <-s.exited:
			return fmt.Errorf("claude exited before the session started%s", s.stderr.reason())
		case <-time.After(2 * time.Second):
			return err
		}
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
	// Accepted goes out before anything the reader sees after the write;
	// the write itself holds no event lock, so a blocked write can't stop
	// the reader.
	s.emu.Lock()
	s.accepting = true
	s.emu.Unlock()
	err := s.write(msg)
	s.emu.Lock()
	defer s.emu.Unlock()
	s.accepting = false
	if err != nil {
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
	} else {
		s.deliver(agent.Event{Kind: agent.TurnAccepted})
	}
	for _, ev := range s.held {
		s.deliver(ev)
	}
	s.held = nil
	return err
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
	// The Capability is known only from system/init (ADR-0005); before it
	// arrives an interrupt isn't sent, and Close ends the process instead.
	if !s.opened || !oneOf(CapInterrupt, s.caps) {
		s.mu.Unlock()
		return ErrNoInterrupt
	}
	s.interrupt = true
	s.mu.Unlock()
	_, err := s.request(map[string]any{"subtype": "interrupt"})
	return err
}

// Close closes stdin, which cancels any pending prompt and lets claude
// exit. If it hasn't within the grace, its process group gets SIGTERM,
// then SIGKILL. Close takes no lock a blocked write could hold, and
// returns within its deadlines whatever the process does.
func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.dead == nil {
			s.dead = errors.New("the session is closed")
		}
		s.mu.Unlock()
		close(s.stop)
		// Closing the pipe unblocks a write in progress.
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
	if s.accepting {
		s.held = append(s.held, ev)
		return
	}
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
	friction := s.friction.take()
	for id, a := range s.asks {
		// An answer after the turn ended can't land in the next one.
		a.done = true
		delete(s.asks, id)
	}
	s.mu.Unlock()
	if was {
		ev.Kind = agent.TurnSettled
		ev.Friction = friction
		s.emit(ev)
	}
}

// read turns claude's stdout into events until EOF, then waits for the
// process and closes the event stream.
func (s *session) read(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 1<<16)
	broken := false
	for {
		line, err := readFrame(r)
		if errors.Is(err, errFrameTooLarge) || (err == io.EOF && len(bytes.TrimSpace(line)) > 0) {
			why := errFrameTooLarge
			if err == io.EOF {
				why = errors.New("the last line has no newline")
			}
			s.settle(agent.Event{Failure: "malformed_frame: " + why.Error()})
			s.kill(why)
			broken = true
		} else if len(bytes.TrimSpace(line)) > 0 && !broken {
			if ferr := s.frame(line); ferr != nil {
				s.settle(agent.Event{Failure: "malformed_frame: " + ferr.Error()})
				s.kill(ferr)
				broken = true
			}
		}
		if err != nil && !errors.Is(err, errFrameTooLarge) {
			break
		}
	}
	// claude has closed its stdout; end whatever its tools left running
	// in its group. The leader isn't reaped yet, so the group id can't
	// have been reused.
	proc.Kill(s.cmd)
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

// maxFrame bounds one stdout line, so a runaway frame can't take
// unbounded memory.
const maxFrame = 16 << 20

var errFrameTooLarge = fmt.Errorf("a line is over %d MiB", maxFrame>>20)

// readFrame reads one line of at most maxFrame bytes. Past that it
// discards the rest of the line and returns errFrameTooLarge.
func readFrame(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxFrame {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			if err != nil && err != io.EOF {
				return nil, err
			}
			return nil, errFrameTooLarge
		}
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
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
	IsError        bool     `json:"is_error"`
	Result         string   `json:"result"`
	TerminalReason string   `json:"terminal_reason"`
	NumTurns       int      `json:"num_turns"`
	CostUSD        *float64 `json:"total_cost_usd"`
	TokenUsage     *struct {
		In  int `json:"input_tokens"`
		Out int `json:"output_tokens"`
	} `json:"usage"`
	// ModelUsage is per model; the context window is reported only here.
	ModelUsage map[string]struct {
		ContextWindow int `json:"contextWindow"`
	} `json:"modelUsage"`
	// rate_limit_event
	RateLimit *struct {
		Status string `json:"status"`
		Type   string `json:"rateLimitType"`
	} `json:"rate_limit_info"`
}

// ignored are message types and system subtypes Öge reads nothing from.
var ignored = map[string]bool{
	"command_lifecycle": true, "stream_event": true, "keep_alive": true,
	"tool_progress": true, "tool_use_summary": true, "prompt_suggestion": true,
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
	case f.Type == "control_cancel_request":
		s.withdraw(f.RequestID)
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
	case f.Type == "user":
		s.toolResults(f)
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
	gate := ""
	switch {
	case s.fatal || s.dead != nil:
		gate = "the agent's startup envelope failed, or the session has ended"
	case !s.opened:
		gate = "the agent's startup envelope hasn't been checked yet"
	}
	if gate != "" {
		d := s.policy.refuse(tool, input, gate)
		s.friction.refused()
		sofar := s.friction.sofar()
		s.mu.Unlock()
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &d, Friction: sofar})
		return s.answer(id, r.Subtype, d, input)
	}
	if s.auto {
		return s.autoRequest(id, r.Subtype, tool, useID, input)
	}
	d, seen := s.decided[useID]
	if !seen {
		d = s.policy.decide(tool, input)
		if useID != "" {
			s.decided[useID] = d
		}
		s.friction.decided(d)
	}
	sofar := s.friction.sofar()
	s.mu.Unlock()
	if !seen {
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &d, Friction: sofar})
	}
	return s.answer(id, r.Subtype, d, input)
}

// autoRequest is controlRequest in auto mode, with s.mu held: Öge's hard
// denies (outside the Workspace, Öge's private state, the Oracle's
// held-out side, .git) answer at once and never reach the human; a call
// the policy pre-authorises is allowed; anything else is left to claude's
// auto mode, and what that mode asks about becomes an Ask (D2).
func (s *session) autoRequest(id, subtype, tool, useID string, input json.RawMessage) error {
	d, seen := s.decided[useID]
	if !seen {
		d = s.policy.decide(tool, input)
	}
	hard := d.Rule != ruleNoInteractive && d.Rule != rulePreAuthorised
	if !hard && subtype == "hook_callback" {
		s.mu.Unlock()
		// No hook decision: the auto mode decides, and may ask.
		return s.write(response(id, map[string]any{"continue": true}))
	}
	if hard || d.Rule == rulePreAuthorised {
		if !seen && useID != "" {
			s.decided[useID] = d
		}
		if !seen {
			s.friction.decided(d)
		}
		sofar := s.friction.sofar()
		s.mu.Unlock()
		if !seen {
			s.emit(agent.Event{Kind: agent.HostRequest, Host: &d, Friction: sofar})
		}
		return s.answer(id, subtype, d, input)
	}
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	detail := askDetail(tool, in, input, s.policy.ws)
	if strings.TrimSpace(detail) == "" || strings.Count(detail, "\n")+1 > agent.AskMaxLines {
		// The human can't consent to what the prompt can't show: denied
		// without asking.
		d.Decision, d.Rule = "deny", ruleAskTooLong
		d.Reason = fmt.Sprintf("Öge denied this: it is too long to show the user for approval (more than %d lines). Run it as shorter commands.", agent.AskMaxLines)
		if strings.TrimSpace(detail) == "" {
			d.Reason = "Öge denied this: there is nothing to show the user for approval."
		}
		if useID != "" {
			s.decided[useID] = d
		}
		s.friction.decided(d)
		sofar := s.friction.sofar()
		s.mu.Unlock()
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &d, Friction: sofar})
		return s.answer(id, subtype, d, input)
	}
	a := &agent.Ask{} // filled below; a withdrawal names it by this pointer
	pa := &pendingAsk{ask: a}
	s.asks[id] = pa
	s.mu.Unlock()
	ask := d
	ask.Decision, ask.Rule, ask.By, ask.Reason, ask.Hint = "ask", ruleAutoAsk, "agent_auto_mode", "", ""
	reply := func(ans agent.Answer) error {
		s.mu.Lock()
		if pa.done {
			s.mu.Unlock()
			return ErrAskClosed
		}
		pa.done = true
		delete(s.asks, id)
		s.mu.Unlock()
		got := ask
		got.By, got.Rule, got.Decision = "human", ruleHumanAnswer, "deny"
		got.AdvisedBy = ans.AdvisedBy
		got.Reason = "The user denied this."
		if ans.Reason != "" {
			got.Reason = ans.Reason
		}
		updated := input
		if ans.Allow {
			got.Decision, got.Reason = "allow", ""
			if len(ans.Answers) > 0 && tool == "AskUserQuestion" {
				updated = withAnswer(in, ans.Answers)
			}
		}
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &got})
		return s.answer(id, subtype, got, updated)
	}
	*a = *agent.NewAsk(tool, ask.Target, detail, reply)
	if tool == "AskUserQuestion" {
		a.Questions = questions(in)
	}
	s.emit(agent.Event{Kind: agent.HostRequest, Host: &ask, Ask: a})
	return nil
}

// withAnswer is an AskUserQuestion's input with the human's answers, one
// per question in order, as claude takes them back.
func withAnswer(in map[string]any, texts []string) json.RawMessage {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	answers := map[string]any{}
	qs, _ := in["questions"].([]any)
	for i, q := range qs {
		m, _ := q.(map[string]any)
		text, _ := m["question"].(string)
		if i < len(texts) {
			answers[text] = texts[i]
		}
	}
	out["answers"] = answers
	b, _ := json.Marshal(out)
	return b
}

// pendingAsk is an Ask not yet answered or withdrawn.
type pendingAsk struct {
	done bool
	ask  *agent.Ask // what the host was shown
}

// ErrAskClosed is a Reply to an Ask that was answered or withdrawn.
var ErrAskClosed = errors.New("the ask was already answered or withdrawn")

// withdraw closes an Ask claude cancelled, as it does on an interrupt.
func (s *session) withdraw(id string) {
	s.mu.Lock()
	pa := s.asks[id]
	if pa != nil {
		pa.done = true
		delete(s.asks, id)
	}
	s.mu.Unlock()
	if pa != nil {
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &agent.HostDecision{Family: agent.Approval,
			Decision: "cancel", By: "agent", Rule: ruleWithdrawn}, Ask: pa.ask})
	}
}

// answer writes a decision in the form the request's subtype takes.
func (s *session) answer(id, subtype string, d agent.HostDecision, input json.RawMessage) error {
	if subtype == "hook_callback" {
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
	hooks := s.hooksRan
	s.model = in.Model
	s.mu.Unlock()
	warnings, fatal := s.env.check(in, hooks)
	res := residueOf(in)
	// No tool request is authorised until this check passes, nor after it
	// fails: the reader handles frames in order, so a request buffered
	// behind this init sees the outcome.
	s.mu.Lock()
	first := !s.opened && !s.fatal
	if fatal != "" {
		s.fatal, s.opened = true, false
	} else if !s.fatal {
		s.opened = true
	}
	s.caps = effective(in)
	caps := s.caps
	s.mu.Unlock()
	if first {
		status := "ok"
		if fatal != "" {
			status = "fail"
		} else if len(warnings) > 0 {
			status = "warn"
		}
		s.emit(agent.Event{Kind: agent.SessionOpened, Session: &agent.SessionInfo{
			AgentVersion: clip(in.Version), Capabilities: caps, Profile: s.hash, Envelope: status,
			AuthSource: "apiKeySource:" + clip(in.APIKeySource), Residue: res,
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
	ID    string          `json:"id"`
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
		ID       string    `json:"id"`
		Content  []content `json:"content"`
		RawUsage *struct {
			In     int `json:"input_tokens"`
			Read   int `json:"cache_read_input_tokens"`
			Create int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(f.Message, &m); err != nil {
		return errors.New("an assistant message can't be decoded")
	}
	s.mu.Lock()
	if u := m.RawUsage; u != nil {
		s.ctxUsed = u.In + u.Read + u.Create
	}
	s.friction.message(m.ID)
	s.mu.Unlock()
	for _, c := range m.Content {
		switch c.Type {
		case "text":
			if t := strings.TrimSpace(c.Text); t != "" {
				s.emit(agent.Event{Kind: agent.Claim, Text: string(redact.Redact([]byte(t)))})
			}
		case "tool_use":
			var in map[string]any
			_ = json.Unmarshal(c.Input, &in)
			ev := agent.Event{Kind: agent.Claim, Tool: clip(c.Name), Target: target(c.Name, in, s.policy.ws), ToolID: c.ID}
			if c.Name == "TodoWrite" {
				ev.Todos = todos(c.Input)
			}
			s.emit(ev)
		}
	}
	return nil
}

// toolResults emits what the tools of a user message returned, for the
// view only: redacted, clipped, never written to a stream.
func (s *session) toolResults(f frame) {
	if f.Parent != nil {
		return
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(f.Message, &m) != nil {
		return
	}
	var parts []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return // a plain-string user message
	}
	for _, p := range parts {
		if p.Type != "tool_result" {
			continue
		}
		var text string
		if json.Unmarshal(p.Content, &text) != nil {
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(p.Content, &blocks) != nil {
				continue
			}
			var sb []string
			for _, b := range blocks {
				if b.Type == "text" {
					sb = append(sb, b.Text)
				}
			}
			text = strings.Join(sb, "\n")
		}
		s.emit(agent.Event{Kind: agent.ToolResult, ToolID: p.ToolUseID, Failed: p.IsError,
			Output: agent.ClipOutput(string(redact.Redact([]byte(text))))})
	}
}

// todos is the plan a TodoWrite call carries, redacted; nil when it
// carries none (or can't be read).
func todos(input json.RawMessage) []agent.Todo {
	var in struct {
		Todos []struct {
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var out []agent.Todo
	for _, t := range in.Todos {
		status := t.Status
		if status != "in_progress" && status != "completed" {
			status = "pending"
		}
		r := func(s string) string { return string(redact.Redact([]byte(strings.TrimSpace(s)))) }
		out = append(out, agent.Todo{Text: r(t.Content), Status: status, Active: r(t.ActiveForm)})
	}
	return out
}

// contextWindow is the window result reports for the session's model, or
// for its only model; 0 when it reports none (never guessed).
func contextWindow(f frame, model string) int {
	if u, ok := f.ModelUsage[model]; ok {
		return u.ContextWindow
	}
	if len(f.ModelUsage) == 1 {
		for _, u := range f.ModelUsage {
			return u.ContextWindow
		}
	}
	return 0
}

func (s *session) result(f frame) {
	use := agent.Event{Kind: agent.Usage, Text: fmt.Sprintf("%d model turns", f.NumTurns), CostUSD: f.CostUSD}
	if u := f.TokenUsage; u != nil && u.In+u.Out > 0 {
		n := u.In + u.Out
		use.Tokens = &n
	}
	s.mu.Lock()
	used, model := s.ctxUsed, s.model
	s.mu.Unlock()
	if w := contextWindow(f, model); w > 0 && used > 0 {
		use.Context = &agent.ContextUse{Used: used, Window: w}
	}
	s.emit(use)
	s.mu.Lock()
	interrupted, apiErr := s.interrupt, s.apiError
	s.mu.Unlock()
	switch {
	case !f.IsError && f.Subtype == "success":
		s.settle(agent.Event{Exit: agent.ExitOf(f.Result, s.env.role)})
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
