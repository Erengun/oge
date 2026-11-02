package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/proc"
)

// An adopted thread is resumed with the settings it had (ADR-0025):
// `thread/resume` by id, in its own cwd and CODEX_HOME, and none of the
// Launch profile's restrictions. This path is separate from Open's on
// purpose: nothing here weakens the Launch profile. Öge sends no
// approval policy, sandbox, permission profile or config override, runs
// no sandbox self-test, does not turn the user's MCP servers off and
// does not read the repository's .codex/config.toml. What the session's
// own settings would ask the human about becomes an Ask.

// ruleNativeAsk and the rules below match the claude adapter's.
const (
	ruleNativeAsk   = "session_settings_ask"
	ruleHumanAnswer = "human_answer"
	ruleAskTooLong  = "ask_too_long"
	// ruleNativeUnsupported: a request Öge has no prompt for.
	ruleNativeUnsupported = "native_request_unsupported"
	ruleWithdrawn         = "ask_withdrawn"
)

// ErrAskClosed is a Reply to an Ask that was answered or withdrawn.
var ErrAskClosed = errors.New("the ask was already answered or withdrawn")

type pendingAsk struct {
	done bool
	ask  *agent.Ask
}

// openNative spawns an app-server for the thread's own cwd, with the
// parent's environment (so its CODEX_HOME), and resumes the thread.
func (a *Adapter) openNative(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
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
	spec.Role = "implementer"
	// No `-c notify=[]`: the thread's own notify setting stays.
	cmd := exec.Command(path, "app-server")
	cmd.Dir, cmd.Env = spec.Workspace, environ()
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
		pending: map[int]chan reply{}, items: map[string][]string{}, prof: newProfile(spec),
		ws: withReal(spec.Workspace), timeout: a.Timeout, grace: a.CloseGrace,
		native: true, asks: map[string]*pendingAsk{},
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
	if err := s.resume(ctx, spec.Resume.ID, spec.Workspace); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// resume is the handshake and thread/resume of an adopted thread. The
// request carries the thread id only: with no overrides the thread keeps
// the approval policy, sandbox and config it had, and the response says
// what they are.
func (s *session) resume(ctx context.Context, id, cwd string) error {
	var init struct {
		UserAgent string `json:"userAgent"`
	}
	if err := s.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "oge", "title": "Öge", "version": "0"}}, &init); err != nil {
		return err
	}
	ver, _, _ := strings.Cut(strings.TrimPrefix(init.UserAgent, "oge/"), " ")
	if version(ver) == nil || older(ver, MinVersion) {
		return fmt.Errorf("codex %s is older than %s, the oldest Öge supports; update codex", clip(ver), MinVersion)
	}
	s.version = ver
	if err := s.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	// What codex itself reports of its MCP servers: they run outside the
	// sandbox, so the coverage line names them. Only names are read, and
	// a failed read is not fatal: the line then claims nothing of them.
	var cfg struct {
		Config struct {
			MCP map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := s.call(ctx, "config/read", map[string]any{"includeLayers": false, "cwd": cwd}, &cfg); err != nil {
		s.mcpUnread = true
	} else {
		for name, raw := range cfg.Config.MCP {
			var on struct {
				Enabled *bool `json:"enabled"`
			}
			if json.Unmarshal(raw, &on) == nil && on.Enabled != nil && !*on.Enabled {
				continue
			}
			s.mcp = append(s.mcp, clip(name))
		}
		sort.Strings(s.mcp)
	}
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ApprovalPolicy    json.RawMessage `json:"approvalPolicy"`
		Sandbox           json.RawMessage `json:"sandbox"`
		ApprovalsReviewer json.RawMessage `json:"approvalsReviewer"`
	}
	if err := s.call(ctx, "thread/resume", map[string]any{"threadId": id}, &res); err != nil {
		return fmt.Errorf("codex couldn't resume the session: %w", err)
	}
	s.mu.Lock()
	s.thread, s.opened = res.Thread.ID, true
	if s.thread == "" {
		s.thread = id
	}
	s.approval, s.sandbox = approvalName(res.ApprovalPolicy), sandboxName(res.Sandbox)
	// Anything but "user" sends approvals to codex's own reviewer, so none
	// reach Öge; a response that doesn't say is not taken for "user".
	s.reviewer = "unreadable"
	var rv string
	if json.Unmarshal(res.ApprovalsReviewer, &rv) == nil && rv != "" {
		s.reviewer = clip(rv)
	}
	s.mu.Unlock()
	return nil
}

// approvalName is the approval policy codex reported: a string such as
// "never" or "on-request", or an object (a granular policy).
func approvalName(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return clip(str)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil && len(obj) == 1 && obj["granular"] != nil {
		return "granular"
	}
	return "" // a form Öge doesn't know: the coverage line says so
}

// sandboxName is the sandbox codex reported, as the kebab-case mode the
// coverage line and thread/start use: the response is a tagged object
// ({"type":"dangerFullAccess"}), the request a string.
func sandboxName(raw json.RawMessage) string {
	var typ string
	var obj struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &typ) != nil {
		if json.Unmarshal(raw, &obj) != nil {
			return ""
		}
		typ = obj.Type
	}
	var b strings.Builder
	for i, r := range clip(typ) {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sessionInfo is SessionOpened's payload: the Launch profile's for an
// Öge thread, what the thread reported of itself for an adopted one.
func (s *session) sessionInfo() *agent.SessionInfo {
	if !s.native {
		return &agent.SessionInfo{
			AgentVersion: clip(s.version), Capabilities: []string{CapHostRequests, CapInterrupt},
			Profile: s.hash, Envelope: "ok", AuthSource: "account:" + s.auth,
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return &agent.SessionInfo{
		AgentVersion: clip(s.version), Capabilities: []string{CapHostRequests, CapInterrupt},
		Profile: "native", Envelope: "native", ApprovalPolicy: s.approval, Sandbox: s.sandbox,
		Unsupervised: append([]string(nil), s.mcp...), MCPUnread: s.mcpUnread,
		ApprovalsReviewer: s.reviewer, Refused: refusedRequests,
	}
}

// refusedRequests are what nativeRequest answers with an error: codex
// takes it as a refusal, and Öge has no prompt for them.
var refusedRequests = []string{"permission requests", "MCP elicitations", "user-input prompts"}

// nativeNotification handles what an adopted thread's notifications mean
// differently, and reports whether it consumed m. MCP servers are the
// thread's own: a start is noted, never a failure.
func (s *session) nativeNotification(m message, thread string) bool {
	switch m.Method {
	case "mcpServer/startupStatus/updated":
		var mp struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &mp)
		if n := clip(mp.Name); n != "" {
			s.mu.Lock()
			found := false
			for _, have := range s.mcp {
				found = found || have == n
			}
			late := !found && s.announced // the coverage line was already printed
			if !found {
				s.mcp = append(s.mcp, n)
				sort.Strings(s.mcp)
			}
			s.mu.Unlock()
			if late {
				s.emit(agent.Event{Kind: agent.Warning, Text: "MCP server " + n + " started after the coverage line: it runs outside the sandbox, and Öge doesn't gate it"})
			}
		}
		return true
	case "serverRequest/resolved":
		// Codex settled a request itself (an interrupt, say): the Ask
		// for it is withdrawn.
		var rp struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		_ = json.Unmarshal(m.Params, &rp)
		if len(rp.RequestID) > 0 {
			s.withdraw(string(rp.RequestID))
		}
		return true
	}
	return false
}

// nativeRequest is a server request in an adopted thread. Its own
// settings already allowed what they allow, so an approval request is one
// they would ask the human about: it becomes an Ask. Öge adds no allow,
// deny or hard-deny of its own, and sends a one-shot accept or decline,
// never a session-wide amendment.
func (s *session) nativeRequest(m message) error {
	var p struct {
		ItemID    string `json:"itemId"`
		GrantRoot string `json:"grantRoot"`
		item
	}
	_ = json.Unmarshal(m.Params, &p)
	d := agent.HostDecision{Family: agent.Approval}
	var detail string
	switch m.Method {
	case "item/commandExecution/requestApproval":
		d.Tool, d.Target = "Bash", s.target(p.command())
		detail = strings.TrimSpace(redacted(p.command()))
	case "item/fileChange/requestApproval":
		s.mu.Lock()
		paths := s.items[p.ItemID]
		s.mu.Unlock()
		d.Tool = "Edit"
		var shown []string
		for _, path := range paths {
			shown = append(shown, s.target(path))
		}
		if len(shown) > 0 {
			d.Target = shown[0]
		}
		detail = strings.Join(shown, "\n")
		if p.GrantRoot != "" {
			detail += "\nand asks to keep writing under " + s.target(p.GrantRoot)
		}
		detail = strings.TrimSpace(detail)
	default:
		refused := agent.HostDecision{Family: agent.Approval, Tool: clip(m.Method), Decision: "deny", Rule: ruleNativeUnsupported, By: "session_settings",
			Reason: "Öge refused this: it has no prompt for " + clip(m.Method) + ", so codex got an error."}
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &refused})
		return s.write(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "Öge doesn't support " + m.Method}})
	}
	key := string(m.ID)
	if detail == "" || strings.Count(detail, "\n")+1 > agent.AskMaxLines {
		// The human can't consent to what the prompt can't show.
		d.Decision, d.Rule, d.By = "deny", ruleAskTooLong, "session_settings"
		d.Reason = fmt.Sprintf("Öge denied this: it is too long to show the user for approval (more than %d lines).", agent.AskMaxLines)
		if detail == "" {
			d.Reason = "Öge denied this: there is nothing to show the user for approval."
		}
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &d})
		return s.write(map[string]any{"id": m.ID, "result": map[string]any{"decision": "decline"}})
	}
	a := &agent.Ask{} // filled below; a withdrawal names it by this pointer
	pa := &pendingAsk{ask: a}
	s.mu.Lock()
	s.asks[key] = pa
	s.mu.Unlock()
	ask := d
	ask.Decision, ask.Rule, ask.By = "ask", ruleNativeAsk, "session_settings"
	reply := func(ans agent.Answer) error {
		s.mu.Lock()
		if pa.done {
			s.mu.Unlock()
			return ErrAskClosed
		}
		pa.done = true
		delete(s.asks, key)
		s.mu.Unlock()
		got := ask
		got.By, got.Rule, got.Decision, got.AdvisedBy = "human", ruleHumanAnswer, "deny", ans.AdvisedBy
		got.Reason = "The user denied this." + noReasonToModel
		decision := "decline"
		if ans.Allow {
			got.Decision, got.Reason, decision = "allow", "", "accept"
		}
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &got})
		return s.write(map[string]any{"id": m.ID, "result": map[string]any{"decision": decision}})
	}
	*a = *agent.NewAsk(d.Tool, ask.Target, detail, reply)
	s.emit(agent.Event{Kind: agent.HostRequest, Host: &ask, Ask: a})
	return nil
}

// withdraw closes an Ask codex settled itself.
func (s *session) withdraw(key string) {
	s.mu.Lock()
	pa := s.asks[key]
	if pa != nil {
		pa.done = true
		delete(s.asks, key)
	}
	s.mu.Unlock()
	if pa != nil {
		s.emit(agent.Event{Kind: agent.HostRequest, Host: &agent.HostDecision{Family: agent.Approval,
			Decision: "cancel", By: "agent", Rule: ruleWithdrawn}, Ask: pa.ask})
	}
}

// withdrawAsks closes every Ask still open when a turn ends: codex no
// longer waits for them.
func (s *session) withdrawAsks() {
	if !s.native {
		return
	}
	s.mu.Lock()
	keys := make([]string, 0, len(s.asks))
	for k := range s.asks {
		keys = append(keys, k)
	}
	s.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		s.withdraw(k)
	}
}

// AgentVersion is the version of the codex on PATH (or Path), from
// `codex --version`.
func (a *Adapter) AgentVersion(ctx context.Context) (string, error) {
	path := a.Path
	if path == "" {
		var err error
		if path, err = exec.LookPath("codex"); err != nil {
			return "", err
		}
	}
	cmd := exec.CommandContext(ctx, path, "--version")
	if a.Environ != nil {
		cmd.Env = a.Environ()
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return agent.VersionOf(string(out)), nil
}
