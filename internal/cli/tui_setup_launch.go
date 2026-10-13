package cli

import (
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/pipeline"
)

// launchLines is the setup panel's read-only "Agent launched with" section
// (#180, ADR-0004): what each bound Stage's agent starts with. It only
// displays; the Claude facts come from claude.LaunchInfoFor, the same
// definition the adapter launches with and hashes. Paths are summarised as
// categories, never listed.
func (p *setupPanel) launchLines() []string {
	st := p.st
	out := []string{"", " " + st.bold("Agent launched with") + st.dim("   read-only · ADR-0004")}
	type stage struct {
		name, role string
		sel        stageSel
	}
	stages := []stage{{"implement", "implementer", p.sel.impl}}
	if p.sel.mode != pipeline.Fast {
		stages = append(stages, stage{"verify", "verifier", p.sel.ver})
	}
	for _, s := range stages {
		agent, profile := s.sel.agent, s.sel.profile
		if agent == "" {
			agent, profile = p.sel.impl.agent, p.sel.impl.profile
		}
		head := "   " + fmt.Sprintf("%-10s", s.name) + " " + clean(agent)
		if agent != claude.Name {
			out = append(out, head+st.dim("   no Launch profile is described for this agent"))
			continue
		}
		info, err := claude.LaunchInfoFor(s.role)
		if err != nil {
			out = append(out, head+st.dim("   "+clean(err.Error())))
			continue
		}
		out = append(out, head+" · profile "+clean(claude.Profile{Name: profile}.Label())+
			st.dim(fmt.Sprintf(" · Launch profile v%d %s", info.Version, clean(info.Hash))))
		for _, l := range launchFacts(info) {
			out = append(out, "      "+st.dim(clean(l)))
		}
	}
	return out
}

// launchFacts are the one-line statements about a Claude launch.
func launchFacts(i claude.LaunchInfo) []string {
	plugins := fmt.Sprintf("plugins: only the %d pinned builtins (part of the pinned binary)", len(i.BuiltinPlugins))
	facts := []string{
		"MCP servers: none from your config (--strict-mcp-config)",
		"settings, hooks: none loaded from your user or project config",
		plugins,
		"memory: auto-memory off, nothing persisted to your Claude config",
		"tools: " + strings.Join(i.Tools, ", "),
	}
	if i.NetworkOff {
		facts = append(facts, "network: off in the sandbox; web tools denied")
	}
	return append(facts, fmt.Sprintf("no reads of: Öge state, other profiles, %d credential locations", i.HomeSecrets))
}
