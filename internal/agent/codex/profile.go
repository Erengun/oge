package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/erengun/oge/internal/agent"
)

// profileName is the inline permission profile every implementer thread
// runs under (ADR-0018).
const profileName = "oge_implementer"

// profile is a thread's settings: the inline permission profile and the
// isolation recipe (ADR-0009, ADR-0018), sent in thread/start and, for
// the self-test, to codex sandbox -P.
type profile struct {
	spec agent.LaunchSpec
	// perm is the permission profile, as thread/start's config takes it.
	perm map[string]any
}

func newProfile(spec agent.LaunchSpec) *profile {
	fs := map[string]any{
		// Writes under .git are not the implementer's, as in the claude
		// policy: a read entry inside the writable root is enforced.
		":workspace_roots": map[string]any{".git": "read"},
	}
	for _, d := range spec.DenyRead {
		fs[filepath.Clean(d)] = "deny"
	}
	if home, err := os.UserHomeDir(); err == nil {
		fs[filepath.Join(home, ".ssh")] = "deny"
		fs[filepath.Join(home, ".codex", "auth.json")] = "deny"
	}
	if spec.Cache != "" {
		fs[filepath.Clean(spec.Cache)] = "write"
	}
	perm := map[string]any{"extends": ":workspace", "filesystem": fs}
	if spec.Network == "on" {
		perm["network"] = map[string]any{"enabled": true}
	}
	return &profile{spec: spec, perm: perm}
}

// config is thread/start's config: the isolation recipe, which leaves
// instructionSources empty so only the Snapshot's instructions, sent as
// developerInstructions, reach the model (ADR-0009). The implementer
// keeps Codex's shell snapshot (ADR-0018).
func (p *profile) config() map[string]any {
	c := map[string]any{
		"project_doc_max_bytes":    0,
		"features":                 map[string]any{"memories": false, "hooks": false, "plugins": false, "apps": false},
		"memories":                 map[string]any{"use_memories": false, "generate_memories": false},
		"skills":                   map[string]any{"include_instructions": false},
		"permissions":              map[string]any{profileName: p.perm},
		"default_permissions":      profileName,
		"shell_environment_policy": map[string]any{"ignore_default_excludes": false},
	}
	if p.spec.Effort != "" {
		c["model_reasoning_effort"] = p.spec.Effort
	}
	return c
}

// threadStart is the thread/start request's params.
func (p *profile) threadStart() map[string]any {
	params := map[string]any{
		"cwd": p.spec.Workspace, "approvalPolicy": "on-request", "approvalsReviewer": "user",
		"config": p.config(), "developerInstructions": p.spec.RepoInstructions, "ephemeral": true,
	}
	if p.spec.Model != "" {
		params["model"] = p.spec.Model
	}
	return params
}

// hash identifies the Launch profile: everything thread/start is sent.
func (p *profile) hash() string {
	b, _ := json.Marshal(p.threadStart())
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

// toml is v as an inline TOML value, every key quoted, for codex's -c.
func toml(v any) string {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = strconv.Quote(k) + "=" + toml(v[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case string:
		return strconv.Quote(v)
	case bool:
		return strconv.FormatBool(v)
	}
	return fmt.Sprint(v)
}

// envelope is what thread/start reports about the thread it started.
type envelope struct {
	Model              string                        `json:"model"`
	Cwd                string                        `json:"cwd"`
	InstructionSources *[]any                        `json:"instructionSources"`
	ApprovalPolicy     any                           `json:"approvalPolicy"`
	ApprovalsReviewer  string                        `json:"approvalsReviewer"`
	Active             *struct{ ID, Extends string } `json:"activePermissionProfile"`
	Sandbox            *struct {
		Type          string   `json:"type"`
		WritableRoots []string `json:"writableRoots"`
		NetworkAccess bool     `json:"networkAccess"`
	} `json:"sandbox"`
	Thread struct {
		ID         string `json:"id"`
		CliVersion string `json:"cliVersion"`
	} `json:"thread"`
}

// check asserts what the envelope proves: the approval policy, reviewer,
// cwd, model, instruction sources and write roots (ADR-0018). Anything
// else fails closed; what it can't prove is the self-test's.
func (p *profile) check(e envelope) error {
	ws := withReal(p.spec.Workspace)
	switch {
	case e.ApprovalPolicy != "on-request":
		return fmt.Errorf("approval policy is %v, not on-request", e.ApprovalPolicy)
	case e.ApprovalsReviewer != "user":
		return fmt.Errorf("approvals go to %q, not to Öge", clip(e.ApprovalsReviewer))
	case !oneOf(filepath.Clean(e.Cwd), ws):
		return fmt.Errorf("the thread runs in %s, not the Workspace", clip(e.Cwd))
	case e.InstructionSources == nil:
		return errors.New("codex didn't report its instruction sources")
	case len(*e.InstructionSources) > 0:
		return fmt.Errorf("codex loaded %d instruction files besides the Snapshot's (such as a global AGENTS.md)", len(*e.InstructionSources))
	case e.Active == nil || e.Active.ID != profileName || e.Active.Extends != ":workspace":
		return errors.New("the thread isn't running Öge's permission profile")
	case e.Sandbox == nil || e.Sandbox.Type != "workspaceWrite":
		return errors.New("the thread's sandbox isn't workspace-write")
	case e.Sandbox.NetworkAccess != (p.spec.Network == "on"):
		return fmt.Errorf("network access is %v, but the Stage asks for %q", e.Sandbox.NetworkAccess, p.spec.Network)
	case p.spec.Model != "" && e.Model != p.spec.Model:
		return fmt.Errorf("the model is %s, not %s", clip(e.Model), clip(p.spec.Model))
	}
	var allowed []string
	if p.spec.Cache != "" {
		allowed = withReal(p.spec.Cache)
	}
	for _, r := range e.Sandbox.WritableRoots {
		if !oneOf(filepath.Clean(r), allowed) && !oneOf(filepath.Clean(r), ws) {
			return fmt.Errorf("the sandbox may write %s, outside the Workspace and cache", clip(r))
		}
	}
	return nil
}

// selfTestScript lists $1 (the Workspace, the control: the sandbox ran
// and can read), then must fail to read every other argument.
const selfTestScript = `ls "$1" >/dev/null 2>&1 || exit 3
shift
for p in "$@"; do
  if [ -d "$p" ]; then ls "$p" >/dev/null 2>&1 && { echo "$p"; exit 4; }
  else head -c1 "$p" >/dev/null 2>&1 && { echo "$p"; exit 4; }; fi
done
exit 0`

// sandboxSelfTest proves, without a model, what thread/start can't show:
// that the same inline profile denies reading Öge's private paths
// (ADR-0018). It runs codex sandbox -P with the profile thread/start is
// sent.
func sandboxSelfTest(ctx context.Context, path string, env []string, p *profile) error {
	args := []string{"sandbox", "-c", "permissions." + profileName + "=" + toml(p.perm), "-P", profileName,
		"-C", p.spec.Workspace, "--", "/bin/sh", "-c", selfTestScript, "oge-self-test", p.spec.Workspace}
	for _, d := range p.spec.DenyRead {
		if _, err := os.Stat(d); err == nil {
			args = append(args, d)
		}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir, cmd.Env = p.spec.Workspace, env
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ee) && ee.ExitCode() == 4:
		return fmt.Errorf("the sandbox let a command read %s", clip(strings.TrimSpace(lastLine(string(out)))))
	case errors.As(err, &ee) && ee.ExitCode() == 3:
		return errors.New("the sandbox couldn't read the Workspace")
	}
	return fmt.Errorf("codex sandbox failed (%v): %s", err, clip(redacted(lastLine(string(out)))))
}

// selfTestKey is what a self-test result may be reused for: the same
// binary and version, platform and profile (ADR-0018).
func selfTestKey(path, version string, p *profile) string {
	b, _ := json.Marshal([]any{path, version, runtime.GOOS, runtime.GOARCH, p.perm, p.spec.Workspace, p.spec.DenyRead})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// version is the dotted version in s, such as "0.160.1", as numbers.
func version(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// older reports whether version a is older than b.
func older(a, b string) bool {
	va, vb := version(a), version(b)
	for i := 0; i < len(va) && i < len(vb); i++ {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return len(va) < len(vb)
}
