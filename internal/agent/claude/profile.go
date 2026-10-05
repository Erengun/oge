package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/erengun/oge/internal/agent"
)

// ProfileVersion versions the Launch profile: its flags, environment
// lists, policy JSON and pre-authorised operations. Bump it with any change
// to them, so the profile hash recorded with each decision changes too.
const ProfileVersion = 2

// MinVersion is the oldest claude this adapter supports; LastTested is the
// newest it was tested against (#35). A newer one only warns.
const (
	MinVersion = "2.1.289"
	LastTested = "2.1.289"
)

// implementerTools is the implementer's --tools whitelist. Glob and Grep
// keep the model from reaching for find and grep through Bash, which the
// policy doesn't pre-authorise.
var implementerTools = []string{"Read", "Edit", "Write", "Bash", "Glob", "Grep"}

// stripEnv are the session markers removed from the child environment
// (ADR-0006): exact names, never a prefix, except CLAUDE_CODE_REMOTE*.
// Everything else passes through, including CLAUDE_CONFIG_DIR and every
// auth or provider variable.
var stripEnv = []string{
	"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_SESSION_ATTENDED",
	"CLAUDE_PID", "CLAUDE_EFFORT", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH",
	"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_BRIDGE_SESSION_ID",
	"CLAUDE_CODE_SSE_PORT", "CLAUDE_PROJECT_DIR", "CLAUDE_ENV_FILE", "AI_AGENT", "TRACEPARENT",
}

const stripEnvPrefix = "CLAUDE_CODE_REMOTE"

// setEnv are the only variables Öge sets: the Launch profile's isolation
// knobs, the Run-private Go cache, and Öge's own markers.
var setEnv = []string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY", "GOCACHE", "GIT_OPTIONAL_LOCKS", "OGE_RUN_ID", "OGE_ROLE"}

// homeSecrets are well-known credential locations under $HOME that the
// sandbox denies the agent's shell commands (defense in depth, ADR-0020's
// threat model). The agent's Claude and Codex config dirs are added from
// CLAUDE_CONFIG_DIR and CODEX_HOME when set. All of $HOME isn't denied:
// toolchains read parts of it.
var homeSecrets = []string{
	".ssh", ".aws", ".gnupg", ".config/gh", ".netrc", ".docker/config.json", ".kube",
	".git-credentials", ".config/gcloud", ".azure", ".npmrc", ".pypirc", ".cargo/credentials", ".terraform.d",
	".claude", ".claude.json", ".codex",
}

// maxInstructions bounds the CLAUDE.md Öge passes on the command line.
const maxInstructions = 128 << 10

// profile is one role's Launch profile.
type profile struct {
	role  string
	tools []string
}

func profileFor(role string) (profile, error) {
	switch role {
	case "implementer":
		return profile{role: role, tools: implementerTools}, nil
	}
	// TODO(#46): the verifier's profile (test-glob allow rules, an absolute
	// deny-list) comes with the verifier Stage.
	return profile{}, fmt.Errorf("claude adapter: no Launch profile for the %s role yet", role)
}

// args is the claude argv after the program name. parent is the
// environment the child's is built from; only the locations of $HOME and
// the agents' config dirs are read from it, never a credential.
func (p profile) args(spec agent.LaunchSpec, sessionID string, parent []string) ([]string, error) {
	settings, err := p.settings(spec, parent)
	if err != nil {
		return nil, err
	}
	a := []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--setting-sources=", "--strict-mcp-config",
		"--settings", settings,
		"--tools", strings.Join(p.tools, ","),
		"--permission-mode", "default", "--permission-prompt-tool", "stdio",
		"--session-id", sessionID,
		// Every MVP Session is one-shot (vendor resume is deferred,
		// ADR-0016), so the implementer also runs without persistence, as
		// decided on #44: nothing lands in the user's Claude config dir.
		"--no-session-persistence",
	}
	if spec.Model != "" {
		a = append(a, "--model", spec.Model)
	}
	if spec.RepoInstructions != "" {
		if len(spec.RepoInstructions) > maxInstructions {
			return nil, fmt.Errorf("claude adapter: the Snapshot's CLAUDE.md is over %d KiB", maxInstructions>>10)
		}
		a = append(a, "--append-system-prompt", spec.RepoInstructions)
	}
	return a, nil
}

// denyRead is every absolute path the sandbox denies reading: Öge's
// private state and the home secrets.
func denyRead(spec agent.LaunchSpec, parent []string) []string {
	get := func(name string) string {
		for _, kv := range parent {
			if k, v, ok := strings.Cut(kv, "="); ok && k == name {
				return v
			}
		}
		return ""
	}
	out := append([]string{}, spec.DenyRead...)
	if home := get("HOME"); filepath.IsAbs(home) {
		for _, s := range homeSecrets {
			out = append(out, filepath.Join(home, filepath.FromSlash(s)))
		}
	}
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		if d := get(v); filepath.IsAbs(d) && !oneOf(filepath.Clean(d), out) {
			out = append(out, filepath.Clean(d))
		}
	}
	return out
}

// settings is the --settings policy JSON. Sandbox paths are absolute:
// relative ones fail silently (ADR-0004).
func (p profile) settings(spec agent.LaunchSpec, parent []string) (string, error) {
	fs := map[string]any{}
	if spec.Cache != "" {
		fs["allowWrite"] = []string{spec.Cache}
	}
	reads := denyRead(spec, parent)
	if len(reads) > 0 {
		fs["denyRead"] = reads
	}
	var deny []string
	for _, d := range spec.DenyRead {
		// "//" starts an absolute path in a permission rule.
		r := "/" + strings.TrimSuffix(d, "/") + "/**"
		deny = append(deny, "Read("+r+")", "Edit("+r+")")
	}
	sandbox := map[string]any{
		"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false,
		"filesystem": fs,
	}
	if spec.Network != "on" {
		// No domain is allowed, so a shell command's request goes to a
		// permission prompt, which Öge denies; the network tools are
		// denied outright. sandbox.network.allowedDomains is in claude
		// 2.1.289's settings schema.
		// TODO(#44-decision): that the prompt path is denied is unverified
		// live; Evidence records network as "sandbox, no domains allowed".
		sandbox["network"] = map[string]any{"allowedDomains": []string{}, "allowLocalBinding": false}
		deny = append(deny, "WebFetch", "WebSearch")
	}
	s := map[string]any{"sandbox": sandbox, "permissions": map[string]any{"deny": deny}}
	b, err := json.Marshal(s)
	return string(b), err
}

// env is the child environment: the parent's, minus the session markers
// and anything Öge sets, plus what Öge sets.
func (p profile) env(parent []string, spec agent.LaunchSpec) []string {
	var out []string
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if stripped(name) || oneOf(name, setEnv) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1", "GIT_OPTIONAL_LOCKS=0")
	if spec.Cache != "" {
		out = append(out, "GOCACHE="+spec.Cache)
	}
	return append(out, "OGE_RUN_ID="+spec.RunID, "OGE_ROLE="+p.role)
}

func stripped(name string) bool {
	return oneOf(name, stripEnv) || strings.HasPrefix(name, stripEnvPrefix)
}

func oneOf(s string, list []string) bool {
	for _, l := range list {
		if s == l {
			return true
		}
	}
	return false
}

// hash identifies the profile's definition, not one launch's paths, so
// the same profile has the same hash in every Run.
func (p profile) hash() string {
	def := map[string]any{
		"version": ProfileVersion, "role": p.role, "tools": p.tools,
		"strip": stripEnv, "strip_prefix": stripEnvPrefix, "set": setEnv,
		"bash_shapes": describeShapes(), "deny_read_home": homeSecrets,
	}
	b, _ := json.Marshal(def)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}
