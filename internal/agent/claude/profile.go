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
const ProfileVersion = 6

// MinVersion is the oldest claude this adapter supports; LastTested is the
// newest it was tested against (#35). A newer one only warns.
const (
	MinVersion = "2.1.289"
	LastTested = "2.1.289"
)

// Efforts are the levels `claude --effort` accepts (2.1.289 --help). A
// Stage's level is part of its Launch profile (#179).
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// TODO(#179-decision): fast mode has no launch flag in claude 2.1.289 (the
// binary has a fastMode setting and fast_mode_state in system/init, but
// nothing in --help documents or verifies either), so it is not offered.

// implementerTools is the implementer's --tools whitelist. Glob and Grep
// keep the model from reaching for find and grep through Bash, which the
// policy doesn't pre-authorise.
var implementerTools = []string{"Read", "Edit", "Write", "Bash", "Glob", "Grep"}

// verifierTools is the verifier's: the same tools, which its policy
// confines to reading and to creating new test files (spec #35).
var verifierTools = []string{"Read", "Edit", "Write", "Bash", "Glob", "Grep"}

// builtinPlugins are the @builtin plugins claude LastTested ships inside
// its own binary and loads even isolated (observed live on #46). They are
// part of the pinned binary, so a verifier may start with exactly this
// set; any other set, or any other plugin, skill or subagent, fails it
// closed (envelope.go).
// TODO(#46-decision): an allowlist of the pinned version's builtin
// plugins, hashed into the Launch profile; skills are removed with
// --disable-slash-commands.
var builtinPlugins = []string{
	"cc-plugin-agents-md@builtin", "cc-plugin-plugin-authoring@builtin",
	"cc-plugin-sec-default@builtin", "cc-plugin-telemetry@builtin",
}

// stripEnv are the session markers removed from the child environment
// (ADR-0006): exact names, never a prefix, except CLAUDE_CODE_REMOTE*.
// Everything else passes through, including every auth or provider
// variable and, for the default profile, CLAUDE_CONFIG_DIR.
var stripEnv = []string{
	"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_SESSION_ATTENDED",
	"CLAUDE_PID", "CLAUDE_EFFORT", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH",
	"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_BRIDGE_SESSION_ID",
	"CLAUDE_CODE_SSE_PORT", "CLAUDE_PROJECT_DIR", "CLAUDE_ENV_FILE", "AI_AGENT", "TRACEPARENT",
}

const stripEnvPrefix = "CLAUDE_CODE_REMOTE"

// setEnv are the only variables Öge sets: the Launch profile's isolation
// knobs, the Run-private Go cache, and Öge's own markers. Öge also sets
// CLAUDE_CONFIG_DIR when a Stage is bound to a named profile (profiles.go).
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
	// extra are profiles kept outside $HOME, by name (see Profiles).
	extra map[string]string
	// effort is the Stage's --effort level, "" for claude's default.
	effort string
	// mode is claude's --permission-mode: "default", where Öge answers
	// every permission, or "auto" for a Chat Session (D2).
	mode string
}

// permissionMode is the --permission-mode a LaunchSpec's Permissions
// launch with. Auto mode is `claude --permission-mode auto`, listed in
// claude 2.1.296's --help (choices acceptEdits, auto, bypassPermissions,
// manual, dontAsk, plan); its asks arrive as can_use_tool requests over
// --permission-prompt-tool stdio, like any prompt (spike row 4).
// Slice 7 (#259) launches every bare-oge Chat this way. Auto mode's
// classifier was checked against --help only, not live: the scenario fake
// stands in for its asks, and the live check is #114's attended run.
func permissionMode(perms string) (string, error) {
	switch perms {
	case "":
		return "default", nil
	case agent.AutoPermissions:
		return "auto", nil
	}
	return "", fmt.Errorf("claude adapter: unknown permissions %q", perms)
}

func profileFor(role string) (profile, error) {
	switch role {
	case "implementer":
		return profile{role: role, tools: implementerTools}, nil
	case "verifier":
		// A one-shot, fresh Session like every MVP Session; its envelope
		// fails closed on injected context and residue (envelope.go).
		// TODO(#46-decision): the Write scope is Öge's policy (Write and
		// Edit only on new files matching the test globs) plus the
		// revert after the Attempt, not native Edit(<glob>) allow rules.
		return profile{role: role, tools: verifierTools}, nil
	}
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
	mode := p.mode
	if mode == "" {
		mode = "default"
	}
	a := []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--setting-sources=", "--strict-mcp-config",
		"--settings", settings,
		"--tools", strings.Join(p.tools, ","),
		"--permission-mode", mode, "--permission-prompt-tool", "stdio",
		"--session-id", sessionID,
		// No Session is ever resumed (vendor resume is deferred,
		// ADR-0016; a Chat keeps one process alive instead), so every
		// role runs without persistence, as decided on #44: nothing lands
		// in the user's Claude config dir.
		"--no-session-persistence",
	}
	if p.role == "verifier" {
		// A judged role loads no skills at all (ADR-0009).
		a = append(a, "--disable-slash-commands")
	}
	if spec.Model != "" {
		a = append(a, "--model", spec.Model)
	}
	if p.effort != "" {
		if !oneOf(p.effort, Efforts) {
			return nil, fmt.Errorf("claude adapter: unknown effort %q (use one of %s)", p.effort, strings.Join(Efforts, ", "))
		}
		a = append(a, "--effort", p.effort)
	}
	if spec.RepoInstructions != "" {
		if len(spec.RepoInstructions) > maxInstructions {
			return nil, fmt.Errorf("claude adapter: the Snapshot's CLAUDE.md is over %d KiB", maxInstructions>>10)
		}
		a = append(a, "--append-system-prompt", spec.RepoInstructions)
	}
	return a, nil
}

// parentGetenv reads a variable from a parent environment.
func parentGetenv(parent []string) func(string) string {
	return func(name string) string {
		for _, kv := range parent {
			if k, v, ok := strings.Cut(kv, "="); ok && k == name {
				return v
			}
		}
		return ""
	}
}

// profiles is every known Claude profile, found from the parent's $HOME
// and CLAUDE_CONFIG_DIR.
func (p profile) profiles(parent []string) []Profile {
	get := parentGetenv(parent)
	return Profiles(get("HOME"), get, p.extra)
}

// configDir is the dir a bound profile launches with, "" for the default,
// which keeps whatever CLAUDE_CONFIG_DIR the parent has.
func (p profile) configDir(spec agent.LaunchSpec, parent []string) (string, error) {
	if spec.Profile == "" {
		return "", nil
	}
	var names []string
	for _, k := range p.profiles(parent) {
		if k.Name == spec.Profile {
			return k.Dir, nil
		}
		if k.Name != "" {
			names = append(names, k.Name)
		}
	}
	return "", fmt.Errorf("claude adapter: no Claude profile %q (known: %s)", spec.Profile, strings.Join(names, ", "))
}

// denyRead is every absolute path the sandbox denies reading: Öge's
// private state, the home secrets, and every known profile's config dir,
// the bound one included, as CLAUDE_CONFIG_DIR always was. A Stage can
// therefore not read another profile's credentials.
func (p profile) denyRead(spec agent.LaunchSpec, parent []string) []string {
	get := parentGetenv(parent)
	out := append([]string{}, spec.DenyRead...)
	if home := get("HOME"); filepath.IsAbs(home) {
		for _, s := range homeSecrets {
			out = append(out, filepath.Join(home, filepath.FromSlash(s)))
		}
	}
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		if d := get(v); filepath.IsAbs(d) {
			for _, r := range withReal(d) {
				if !oneOf(r, out) {
					out = append(out, r)
				}
			}
		}
	}
	for _, d := range DenyDirs(get("HOME"), get, p.extra) {
		for _, r := range withReal(d) {
			if !oneOf(r, out) {
				out = append(out, r)
			}
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
	reads := p.denyRead(spec, parent)
	if p.mode == "auto" {
		// A Chat's commands run under auto mode, not Öge's shapes, so the
		// sandbox denies reading the whole home directory, other
		// repositories included, and reopens only the Workspace, the
		// cache and the toolchain (sandbox.filesystem.allowRead, claude
		// 2.1.296). Credential paths stay denied inside what it reopens.
		// Öge's state roots are denied wherever they live, so a state dir
		// outside $HOME still hides other Runs and Chats.
		get := parentGetenv(parent)
		for _, d := range spec.StateRoots {
			if filepath.IsAbs(d) {
				reads = append(reads, withReal(d)...)
			}
		}
		if home := get("HOME"); filepath.IsAbs(home) {
			reads = append(reads, withReal(home)...)
		}
		allow := withReal(spec.Workspace)
		if spec.Cache != "" {
			allow = append(allow, spec.Cache)
		}
		fs["allowRead"] = append(allow, toolchain(get)...)
	}
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
func (p profile) env(parent []string, spec agent.LaunchSpec) ([]string, error) {
	dir, err := p.configDir(spec, parent)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if stripped(name) || oneOf(name, setEnv) || (dir != "" && name == "CLAUDE_CONFIG_DIR") {
			continue
		}
		out = append(out, kv)
	}
	if dir != "" {
		out = append(out, "CLAUDE_CONFIG_DIR="+dir)
	}
	out = append(out, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1", "GIT_OPTIONAL_LOCKS=0")
	if spec.Cache != "" {
		out = append(out, "GOCACHE="+spec.Cache)
	}
	return append(out, "OGE_RUN_ID="+spec.RunID, "OGE_ROLE="+p.role), nil
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
		"deny_read_profiles": "every ~/.claude-* dir + extras, symlinks resolved", "config_dir": "from the bound profile",
		"builtin_plugins": builtinPlugins,
		"effort":          p.effort, "effort_levels": Efforts,
	}
	if p.mode != "" && p.mode != "default" {
		// Only a Chat's profile carries its mode, so every one-shot
		// profile keeps its hash.
		def["permission_mode"] = p.mode
	}
	b, _ := json.Marshal(def)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}
