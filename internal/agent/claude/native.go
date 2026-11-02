package claude

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"github.com/erengun/oge/internal/agent"
)

// An adopted session is resumed with the settings it had (ADR-0025):
// `--resume <id>` in its own cwd and profile, and none of the Launch
// profile's restrictions. Öge adds only what it needs to drive the
// stream and to receive the prompts the session's own settings raise.
// This path is separate from profile.args on purpose: nothing here
// weakens the Launch profile.

// nativeArgs is the claude argv after the program name for an adopted
// session. Absent on purpose: --setting-sources=, --strict-mcp-config,
// --settings, --tools, --permission-mode and --no-session-persistence.
func nativeArgs(id string) []string {
	return []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--permission-prompt-tool", "stdio",
		"--resume", id,
	}
}

// nativeEnv is the parent's environment minus the markers of the session
// that started Öge (ADR-0006), with CLAUDE_CONFIG_DIR set to the
// session's profile. Auto memory stays as the session had it.
func nativeEnv(p profile, parent []string, spec agent.LaunchSpec) ([]string, error) {
	dir, err := p.configDir(spec, parent)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if stripped(name) || (dir != "" && name == "CLAUDE_CONFIG_DIR") {
			continue
		}
		out = append(out, kv)
	}
	if dir != "" {
		out = append(out, "CLAUDE_CONFIG_DIR="+dir)
	}
	return out, nil
}

// nativeInit reports what the resumed session says about itself. It
// checks nothing: there is no Launch profile to hold it to. Coverage
// comes from the reported permission mode, never from a guess.
func (s *session) nativeInit(in initFrame) {
	s.mu.Lock()
	first := !s.opened
	s.opened = true
	s.mu.Unlock()
	if !first {
		return
	}
	s.emit(agent.Event{Kind: agent.SessionOpened, Session: &agent.SessionInfo{
		AgentVersion: clip(in.Version), Profile: "native", Envelope: "native",
		AuthSource: "apiKeySource:" + clip(in.APIKeySource), PermissionMode: clip(in.PermissionMode),
	}})
}

// nativeRequest is a request in an adopted session, with s.mu held. The
// session's own settings already answered what they allow or deny, so a
// can_use_tool request is one they would ask the human about: it becomes
// an Ask. Öge adds no allow, deny or hard-deny of its own here.
func (s *session) nativeRequest(id, subtype, tool, useID string, input json.RawMessage) error {
	if subtype != "can_use_tool" {
		s.mu.Unlock()
		return s.write(response(id, map[string]any{"continue": true}))
	}
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	d := agent.HostDecision{Family: agent.Approval, Tool: tool, Target: target(tool, in, s.policy.ws), By: s.policy.by}
	return s.askHuman(id, subtype, tool, useID, input, d)
}

// AgentVersion is the version of the claude on PATH (or Path), from
// `claude --version`: what a restored Chat's trust check compares with
// the version its last Session reported.
func (a *Adapter) AgentVersion(ctx context.Context) (string, error) {
	path := a.Path
	if path == "" {
		var err error
		if path, err = exec.LookPath("claude"); err != nil {
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
