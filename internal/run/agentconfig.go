package run

import (
	"strings"

	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/workspace"
)

// DeclaredAgentConfig reports whether an Excluded agent-config path p is
// declared as output (#106, #107): an output glob matches it and names
// the agent-config entry that makes it Excluded literally, as a path
// segment (CLAUDE.md, **/AGENTS.md, .claude/**). A declared file is an
// ordinary deliverable: in the Promoted view QA sees, checked and
// delivered. Any other Excluded change is held back from delivery.
// Intent is never inferred from the Task's text.
//
// TODO(#107-decision): a broad glob that only happens to match (**,
// **/*.md, docs/**) doesn't declare agent configuration; the glob must
// name it. The declaration may come from --output or the project's
// output_globs alike.
func DeclaredAgentConfig(outputGlobs []string, p string) bool {
	if !workspace.Excluded(p) {
		return false
	}
	entry := workspace.ExcludedEntry(p)
	for _, g := range outputGlobs {
		if !oracle.Match(g, p) {
			continue
		}
		for _, seg := range strings.Split(g, "/") {
			if seg == entry {
				return true
			}
		}
	}
	return false
}
