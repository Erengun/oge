package claude

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// initFrame is the part of system/init the envelope check and the
// Capabilities read. Unknown fields are ignored.
type initFrame struct {
	Cwd        string   `json:"cwd"`
	Tools      []string `json:"tools"`
	MCPServers []struct {
		Name string `json:"name"`
	} `json:"mcp_servers"`
	PermissionMode string          `json:"permissionMode"`
	APIKeySource   string          `json:"apiKeySource"`
	Version        string          `json:"claude_code_version"`
	OutputStyle    string          `json:"output_style"`
	Skills         json.RawMessage `json:"skills"`
	Plugins        []struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	} `json:"plugins"`
	Capabilities []string        `json:"capabilities"`
	MemoryPaths  json.RawMessage `json:"memory_paths"`
}

// envelope is what a Launch profile expects system/init to report.
type envelope struct {
	role  string
	cwd   string
	tools []string
}

// check compares an observed system/init with the envelope. Unexpected
// instructions, memory or injected context fail a verifier closed and warn
// for the implementer; extra installed tooling warns; anything that breaks
// the profile's own guarantees (permission mode, working directory,
// version) fails every role (ADR-0005, #35). hooksRan reports that hooks
// ran before init, which is how a SessionStart hook injects context.
func (e envelope) check(in initFrame, hooksRan bool) (warnings []string, fatal string) {
	var fatals, injected []string
	if in.PermissionMode != "default" {
		fatals = append(fatals, fmt.Sprintf("permission mode is %q, not \"default\": Öge must answer every permission", in.PermissionMode))
	}
	if !sameDir(in.Cwd, e.cwd) {
		fatals = append(fatals, "the working directory isn't the Workspace")
	}
	switch {
	case in.Version == "":
		fatals = append(fatals, "no claude_code_version reported")
	case versionLess(in.Version, MinVersion):
		fatals = append(fatals, fmt.Sprintf("claude %s is older than the oldest supported, %s", in.Version, MinVersion))
	case versionLess(LastTested, in.Version):
		warnings = append(warnings, fmt.Sprintf("claude %s is newer than the last tested, %s", in.Version, LastTested))
	}

	if hooksRan {
		injected = append(injected, "hooks ran at startup")
	}
	if m := strings.TrimSpace(string(in.MemoryPaths)); m != "" && m != "null" && m != "{}" {
		injected = append(injected, "memory is loaded")
	}
	if in.OutputStyle != "" && in.OutputStyle != "default" {
		injected = append(injected, fmt.Sprintf("output style %q is on", in.OutputStyle))
	}
	if len(in.MCPServers) > 0 {
		var names []string
		for _, s := range in.MCPServers {
			names = append(names, s.Name)
		}
		injected = append(injected, "MCP servers: "+strings.Join(names, ", "))
	}
	if len(injected) > 0 {
		msg := "unexpected context in the agent's startup envelope: " + strings.Join(injected, "; ")
		if e.role == "verifier" {
			fatals = append(fatals, msg)
		} else {
			warnings = append(warnings, msg)
		}
	}

	if extra, missing := diff(in.Tools, e.tools); len(extra)+len(missing) > 0 {
		var parts []string
		if len(extra) > 0 {
			parts = append(parts, "extra tools "+strings.Join(extra, ", "))
		}
		if len(missing) > 0 {
			parts = append(parts, "missing tools "+strings.Join(missing, ", "))
		}
		warnings = append(warnings, "tools differ from the Launch profile: "+strings.Join(parts, "; "))
	}
	// TODO(#44-decision): plugins and skills a user can't remove from an
	// isolated launch (organisation-managed ones) are residue, not an
	// envelope failure; built-in plugins are expected, others warn.
	var plugins []string
	for _, p := range in.Plugins {
		if !strings.HasSuffix(p.Source, "@builtin") {
			plugins = append(plugins, p.Name)
		}
	}
	var skills []json.RawMessage
	_ = json.Unmarshal(in.Skills, &skills)
	var tooling []string
	if len(plugins) > 0 {
		tooling = append(tooling, fmt.Sprintf("%d plugins (%s)", len(plugins), strings.Join(plugins, ", ")))
	}
	if len(skills) > 0 {
		tooling = append(tooling, fmt.Sprintf("%d skills", len(skills)))
	}
	if len(tooling) > 0 {
		warnings = append(warnings, "extra installed tooling: "+strings.Join(tooling, ", "))
	}
	return warnings, strings.Join(fatals, "; ")
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

// diff is what got has beyond want, and what want has that got lacks.
func diff(got, want []string) (extra, missing []string) {
	in := func(s string, l []string) bool { return oneOf(s, l) }
	for _, g := range got {
		if !in(g, want) {
			extra = append(extra, g)
		}
	}
	for _, w := range want {
		if !in(w, got) {
			missing = append(missing, w)
		}
	}
	sort.Strings(extra)
	return extra, missing
}

// versionLess compares dotted numeric versions; a non-numeric part
// compares as 0.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// Capabilities this adapter declares (ADR-0005). The effective set is
// these, intersected with what system/init reports and what the Launch
// profile enables.
const (
	CapHostRequests = "host_requests"
	CapInterrupt    = "interrupt"
	CapDenyReason   = "deny_reason_reaches_model"
	CapResume       = "resume"
)

// effective is the Session's effective Capabilities. Host requests and the
// deny reason ride on --permission-prompt-tool stdio and the host hook,
// which the profile always enables; interrupt needs the receipt the agent
// reports; resume is declared but the profile turns it off
// (--no-session-persistence).
func effective(reported []string) []string {
	caps := []string{CapHostRequests, CapDenyReason}
	if oneOf("interrupt_receipt_v1", reported) {
		caps = append(caps, CapInterrupt)
	}
	return caps
}
