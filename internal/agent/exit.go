package agent

import (
	"slices"
	"strings"
)

// Exits each role may declare (ADR-0007, ADR-0008).
var (
	implementerExits = []string{"done", "infeasible"}
	verifierExits    = []string{"extended", "no_additions", "conflicts_with_oracle"}
)

// ExitOf is the Exit a final message declares on its last line, as
// "Exit: <name>": for the implementer done when it declares none, for the
// verifier "" (Öge then decides from what it added).
// TODO(#44-decision): Claude declares an Exit in prose, as the Briefing
// asks; an unknown name counts as none.
func ExitOf(result, role string) string {
	exits, none := implementerExits, "done"
	if role == "verifier" {
		exits, none = verifierExits, ""
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	last := strings.ToLower(strings.TrimSpace(lines[len(lines)-1]))
	last = strings.Trim(last, "*`_ ")
	if name, ok := strings.CutPrefix(last, "exit:"); ok {
		name = strings.Trim(strings.TrimSpace(name), "*`_. ")
		if slices.Contains(exits, name) {
			return name
		}
	}
	return none
}
