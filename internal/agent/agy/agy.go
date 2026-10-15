// Package agy is the native Google Antigravity adapter (ADR-0023). It
// spawns the user's unmodified agy binary in -p stream-json mode under an
// Öge-owned Launch profile, speaks the NDJSON protocol over stdio, normalizes
// tool claims and text deltas, detects soft-denials, and maps turn outcomes.
// It never handles a credential (ADR-0006).
package agy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/proc"
	"github.com/erengun/oge/internal/redact"
)

// Name is the agent name the adapter binds as.
const Name = "agy"

// Capabilities the adapter declares (ADR-0005, ADR-0023).
const (
	CapInterrupt = "interrupt"
)

// Adapter launches agy Sessions.
type Adapter struct {
	// Path is the agy executable; empty means "agy" on PATH.
	Path string
	// Environ is the parent environment; nil means os.Environ.
	Environ func() []string
	// Timeout bounds initialization; zero means startTimeout.
	Timeout time.Duration
	// CloseGrace is how long Close waits at each step; zero means closeGrace.
	CloseGrace time.Duration
}

// New returns the adapter for the agy on PATH.
func New() *Adapter { return &Adapter{} }

const (
	startTimeout = 30 * time.Second
	closeGrace   = 5 * time.Second
)

// Open spawns agy, reads the initial init NDJSON frame, asserts the
// envelope, and begins session event normalization.
func (a *Adapter) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	prof, err := profileFor(spec.Role, spec.Effort)
	if err != nil {
		return nil, err
	}
	if spec.Permissions != "" {
		return nil, fmt.Errorf("agy adapter: agy has no %s permission mode", spec.Permissions)
	}
	path := a.Path
	if path == "" {
		if path, err = exec.LookPath("agy"); err != nil {
			return nil, fmt.Errorf("agy isn't installed or isn't on PATH")
		}
	}
	environ := a.Environ
	if environ == nil {
		environ = os.Environ
	}
	parent := environ()
	ver, err := probeVersion(ctx, path, parent)
	if err != nil {
		return nil, fmt.Errorf("agy adapter: %w", err)
	}
	args, err := prof.args(spec)
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
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := proc.Start(cmd); err != nil {
		return nil, fmt.Errorf("agy adapter: %w", err)
	}

	s := &session{
		cmd:        cmd,
		stdin:      stdin,
		process:    agent.Process{PID: cmd.Process.Pid, PGID: proc.Group(cmd)},
		events:     make(chan agent.Event, 256),
		ws:         spec.Workspace,
		role:       spec.Role,
		version:    ver,
		exited:     make(chan struct{}),
		closeGrace: a.CloseGrace,
	}
	if s.closeGrace == 0 {
		s.closeGrace = closeGrace
	}

	// Read init line from stdout
	initTimeout := a.Timeout
	if initTimeout == 0 {
		initTimeout = startTimeout
	}
	initCh := make(chan error, 1)
	rStdout := bufio.NewReaderSize(stdout, 16<<20)

	var initFrame map[string]any
	go func() {
		line, err := rStdout.ReadBytes('\n')
		if err != nil {
			initCh <- fmt.Errorf("failed to read init frame: %w", err)
			return
		}
		if err := json.Unmarshal(line, &initFrame); err != nil {
			initCh <- fmt.Errorf("invalid init JSON: %w", err)
			return
		}
		ev, _ := initFrame["event"].(string)
		if ev != "init" {
			initCh <- fmt.Errorf("expected init event, got %q", ev)
			return
		}
		initCh <- nil
	}()

	cleanupInit := func() {
		_ = stdin.Close()
		proc.Kill(cmd)
		_ = proc.Wait(cmd)
		close(s.exited)
	}

	select {
	case err := <-initCh:
		if err != nil {
			cleanupInit()
			return nil, fmt.Errorf("agy adapter handshake: %w", err)
		}
	case <-time.After(initTimeout):
		cleanupInit()
		return nil, errors.New("agy adapter: timed out waiting for init frame")
	case <-ctx.Done():
		cleanupInit()
		return nil, ctx.Err()
	}

	var warnings []string
	if older(LastTested, ver) {
		warnings = append(warnings, fmt.Sprintf("envelope: agy %s is newer than %s, the last version Öge was tested with", ver, LastTested))
	}
	initData := initFrame
	if sub, ok := initFrame["init"].(map[string]any); ok {
		initData = sub
	}
	cwd, _ := initData["cwd"].(string)
	if cwd == "" {
		cwd, _ = initFrame["cwd"].(string)
	}
	if cwd != "" && !sameDir(cwd, spec.Workspace) {
		warnings = append(warnings, fmt.Sprintf("envelope: working directory is %q, not Workspace %q", cwd, spec.Workspace))
	}
	model, _ := initData["model"].(string)
	if model == "" {
		model, _ = initFrame["model"].(string)
	}
	if spec.Model != "" && model != "" && model != spec.Model {
		warnings = append(warnings, fmt.Sprintf("envelope: model is %q, not requested %q", model, spec.Model))
	}

	convID, _ := initFrame["conversation_id"].(string)
	if convID == "" {
		convID, _ = initData["conversation_id"].(string)
	}
	if convID == "" {
		convID, _ = initFrame["session_id"].(string)
	}
	s.convID = convID

	envStatus := "ok"
	if len(warnings) > 0 {
		envStatus = "warn"
	}

	// Emit SessionOpened
	s.events <- agent.Event{
		Kind: agent.SessionOpened,
		Session: &agent.SessionInfo{
			AgentVersion: ver,
			Capabilities: []string{CapInterrupt},
			Profile:      prof.hash(spec.DenyRead),
			Envelope:     envStatus,
			AuthSource:   "keyring",
		},
		Text: model,
	}
	for _, w := range warnings {
		s.events <- agent.Event{Kind: agent.Warning, Text: w}
	}

	go s.readStdout(rStdout)
	go s.readStderr(stderr)

	return s, nil
}

type session struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	process    agent.Process
	events     chan agent.Event
	ws         string
	role       string
	closeGrace time.Duration

	mu          sync.Mutex
	inFlight    bool
	interrupted bool
	stopped     bool
	exited      chan struct{}
	closeOnce   sync.Once
	tokens      int
	lastTokens  int
	version     string
	convID      string
}

func (s *session) Process() agent.Process     { return s.process }
func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) Send(t agent.Turn) error {
	s.mu.Lock()
	if s.inFlight {
		s.mu.Unlock()
		return agent.ErrTurnInFlight
	}
	s.inFlight = true
	s.interrupted = false
	s.mu.Unlock()

	msg := map[string]any{
		"event": "user",
		"message": map[string]any{
			"content": t.Text,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
		return err
	}
	b = append(b, '\n')
	_, err = s.stdin.Write(b)
	if err != nil {
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
		return err
	}
	s.events <- agent.Event{Kind: agent.TurnAccepted}
	return nil
}

func (s *session) Interrupt() error {
	s.mu.Lock()
	if !s.inFlight {
		s.mu.Unlock()
		return nil
	}
	s.interrupted = true
	s.mu.Unlock()
	return proc.Interrupt(s.cmd)
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		for _, step := range []func(){nil, func() { proc.Terminate(s.cmd) }, func() { proc.Kill(s.cmd) }} {
			if step != nil {
				step()
			}
			select {
			case <-s.exited:
				return
			case <-time.After(s.closeGrace):
			}
		}
	})
	return nil
}

func (s *session) stop(reason string) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.inFlight = false
	s.mu.Unlock()
	s.events <- agent.Event{Kind: agent.TurnSettled, Stop: reason}
	proc.Kill(s.cmd)
}

func (s *session) settle(ev agent.Event) {
	s.mu.Lock()
	if s.stopped || !s.inFlight {
		s.mu.Unlock()
		return
	}
	s.inFlight = false
	s.mu.Unlock()
	ev.Kind = agent.TurnSettled
	s.events <- ev
}

func (s *session) readStdout(r *bufio.Reader) {
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var frame map[string]any
			if json.Unmarshal(line, &frame) == nil {
				s.handleFrame(frame)
			}
		}
		if err != nil {
			break
		}
	}
	s.mu.Lock()
	if !s.stopped {
		interrupted := s.interrupted
		s.mu.Unlock()
		if interrupted {
			s.settle(agent.Event{Failure: "interrupted"})
		} else {
			s.settle(agent.Event{Failure: "process exited prematurely"})
		}
	} else {
		s.mu.Unlock()
	}
	proc.Kill(s.cmd)
	_ = proc.Wait(s.cmd)
	close(s.exited)
}

func (s *session) handleFrame(f map[string]any) {
	ev, _ := f["event"].(string)
	switch ev {
	case "step_update":
		s.handleStepUpdate(f)
	case "result":
		s.handleResult(f)
	}
}

func (s *session) handleStepUpdate(f map[string]any) {
	data := f
	if sub, ok := f["step_update"].(map[string]any); ok {
		data = sub
	}
	toolInfo, _ := data["tool_info"].(map[string]any)
	toolName, _ := data["tool_name"].(string)
	if toolInfo != nil {
		if name, ok := toolInfo["name"].(string); ok && name != "" {
			toolName = name
		}
		params, _ := toolInfo["parameters"].(map[string]any)
		targetStr := targetForTool(toolName, params, s.ws)
		s.events <- agent.Event{
			Kind:   agent.Claim,
			Tool:   shorten(toolName, 32),
			Target: targetStr,
		}
	}

	if textDelta, ok := data["text_delta"].(string); ok && textDelta != "" {
		s.events <- agent.Event{
			Kind: agent.Claim,
			Text: string(redact.Redact([]byte(textDelta))),
		}
	}

	if u, ok := data["usage"].(map[string]any); ok {
		if tot, ok := u["total_tokens"].(float64); ok {
			s.mu.Lock()
			s.tokens = int(tot)
			s.mu.Unlock()
		}
	}
}

func (s *session) handleResult(f map[string]any) {
	data := f
	if sub, ok := f["result"].(map[string]any); ok {
		data = sub
	}
	s.mu.Lock()
	interrupted := s.interrupted
	totalTokens := s.tokens
	lastTokens := s.lastTokens
	s.mu.Unlock()

	if u, ok := data["usage"].(map[string]any); ok {
		if tot, ok := u["total_tokens"].(float64); ok && int(tot) > 0 {
			totalTokens = int(tot)
		}
	}

	status, _ := data["status"].(string)
	response, _ := data["response"].(string)

	deniedActions, _ := data["denied_actions"].([]any)
	if len(deniedActions) > 0 && strings.TrimSpace(response) == "" {
		s.settle(agent.Event{
			Failure: "permission check failed: action soft-denied by agy policy",
		})
		return
	}

	if interrupted || status == "INTERRUPTED" || status == "CANCELED" {
		s.settle(agent.Event{
			Failure: "interrupted",
			Text:    response,
		})
		return
	}

	if status == "ERROR" {
		s.settle(agent.Event{
			Failure: "error",
			Text:    response,
		})
		return
	}

	exit := agent.ExitOf(response, s.role)
	var tokPtr *int
	delta := totalTokens - lastTokens
	if delta > 0 {
		tokPtr = &delta
	}
	s.mu.Lock()
	s.lastTokens = totalTokens
	s.mu.Unlock()

	s.settle(agent.Event{
		Exit:   exit,
		Text:   response,
		Tokens: tokPtr,
	})
}

func (s *session) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		text := sc.Text()
		s.handleStderrLine(text)
	}
}

func (s *session) handleStderrLine(line string) {
	lower := strings.ToLower(line)
	if strings.Contains(line, "AGY_ERROR:") {
		var errData struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		jsonStr := strings.TrimSpace(line[strings.Index(line, "AGY_ERROR:")+len("AGY_ERROR:"):])
		if json.Unmarshal([]byte(jsonStr), &errData) == nil {
			if strings.EqualFold(errData.Status, "UNAUTHENTICATED") || strings.Contains(strings.ToLower(errData.Message), "auth") {
				s.stop("Antigravity authentication failed; please sign in with agy")
				return
			}
			if strings.EqualFold(errData.Status, "RESOURCE_EXHAUSTED") || strings.Contains(strings.ToLower(errData.Message), "quota") {
				s.stop("Antigravity quota exhausted")
				return
			}
		}
	}
	if strings.Contains(lower, "authentication required") || strings.Contains(lower, "secret keyring is locked") {
		s.stop("Antigravity authentication failed; please sign in with agy")
	}
}

func targetForTool(tool string, in map[string]any, ws string) string {
	if in == nil {
		return ""
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := in[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	var t string
	switch tool {
	case "run_command":
		cmd := str("CommandLine", "command")
		cmd, _, _ = strings.Cut(cmd, "\n")
		t = strings.TrimSpace(cmd)
	case "write_to_file", "replace_file_content", "view_file":
		path := str("TargetFile", "AbsolutePath", "path")
		t = relPath(path, ws)
	default:
		t = str("TargetFile", "AbsolutePath", "CommandLine", "command")
		if t != "" && filepath.IsAbs(t) {
			t = relPath(t, ws)
		}
	}
	return shorten(noControl(string(redact.Redact([]byte(t)))), 80)
}

func relPath(path, ws string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) && ws != "" {
		if rel, err := filepath.Rel(ws, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func noControl(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func shorten(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func probeVersion(ctx context.Context, path string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("probe agy version: %w", err)
	}
	ver := parseVersion(string(out))
	if ver == "" {
		return "", fmt.Errorf("could not parse agy version from %q", strings.TrimSpace(string(out)))
	}
	if older(ver, MinVersion) {
		return "", fmt.Errorf("agy %s is older than %s, the oldest Öge supports; update agy", ver, MinVersion)
	}
	return ver, nil
}
