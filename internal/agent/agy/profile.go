package agy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/erengun/oge/internal/agent"
)

// ProfileVersion versions the Launch profile: its flags, environment
// lists and isolation recipe (ADR-0023).
const ProfileVersion = 1

// MinVersion is the oldest agy this adapter supports (since 1.2.6 AGY_ERROR
// is on stderr and exit code 3). LastTested is the newest it was run against.
const (
	MinVersion = "1.2.6"
	LastTested = "1.3.5"
)

// Efforts are the levels `agy --effort` accepts (1.3.5 --help).
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// stripEnv are the session markers removed from the child environment
// (ADR-0006): exact names. Everything else passes through.
var stripEnv = []string{
	"AGY_SESSION_ID", "AGY_LOG_FILE", "AGY_CONVERSATION_ID",
	"GEMINI_CLI_SESSION_ID", "GEMINI_CLI_REMOTE_CONTROL",
	"AI_AGENT", "TRACEPARENT",
}

// setEnv are the only variables Öge sets: the Run-private Go cache and
// Öge's own markers.
var setEnv = []string{"GOCACHE", "GIT_OPTIONAL_LOCKS", "OGE_RUN_ID", "OGE_ROLE"}

// profile is one role's Launch profile for agy.
type profile struct {
	role   string
	effort string
}

func profileFor(role string, effort string) (profile, error) {
	switch role {
	case "implementer":
		if effort != "" && !slices.Contains(Efforts, effort) {
			return profile{}, fmt.Errorf("agy adapter: unknown effort %q (use one of %s)", effort, strings.Join(Efforts, ", "))
		}
		return profile{role: role, effort: effort}, nil
	}
	return profile{}, fmt.Errorf("agy adapter: the %s role isn't supported yet", role)
}

func (p profile) args(spec agent.LaunchSpec) ([]string, error) {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--print-timeout", "0",
		"--dangerously-skip-permissions",
		"--disable-slash-commands",
	}
	if spec.Network != "on" {
		args = append(args, "--sandbox")
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if p.effort != "" {
		args = append(args, "--effort", p.effort)
	}
	return args, nil
}

func (p profile) env(parent []string, spec agent.LaunchSpec) ([]string, error) {
	var out []string
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(stripEnv, name) || slices.Contains(setEnv, name) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "GIT_OPTIONAL_LOCKS=0")
	if spec.Cache != "" {
		out = append(out, "GOCACHE="+spec.Cache)
	}
	return append(out, "OGE_RUN_ID="+spec.RunID, "OGE_ROLE="+p.role), nil
}

func (p profile) hash(denyRead []string) string {
	def := map[string]any{
		"version": ProfileVersion, "role": p.role,
		"strip": stripEnv, "set": setEnv,
		"deny_read": denyRead,
		"effort":    p.effort, "effort_levels": Efforts,
	}
	b, _ := json.Marshal(def)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// version is the dotted numeric version in s, such as "1.3.5".
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

// older reports whether version a is older than version b.
func older(a, b string) bool {
	va, vb := version(a), version(b)
	for i := 0; i < len(va) && i < len(vb); i++ {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return len(va) < len(vb)
}

// parseVersion extracts a dotted version from text like "agy 1.3.5\n" or "1.3.5".
func parseVersion(s string) string {
	fields := strings.Fields(s)
	for _, f := range fields {
		f = strings.TrimPrefix(f, "v")
		if version(f) != nil {
			return f
		}
	}
	return ""
}
