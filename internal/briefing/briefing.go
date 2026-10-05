// Package briefing builds what each Role kind is told at the start of an
// Attempt, from the Task and the Run's frozen config (ADR-0009). So far it
// builds the implementer's.
package briefing

import (
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/task"
)

// Assumptions is the instruction every role gets (ADR-0019 #5).
const Assumptions = "Make reasonable, minimal and reversible assumptions where possible. " +
	"Do not ask the user unless missing information genuinely blocks progress or would materially change the Task, acceptance criteria or required authority."

// Implementer is the implementer's Briefing: the Task, then how Öge runs
// it. checks are the Run's Check commands.
func Implementer(t task.Task, checks []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(t.Text))
	b.WriteString("\n\n---\nHow this Run works:\n")
	fmt.Fprintf(&b, "- You are the implementer. The current directory is a disposable copy of the repository; work only inside it, and don't commit.\n")
	if len(checks) > 0 {
		q := make([]string, len(checks))
		for i, c := range checks {
			q[i] = "`" + c + "`"
		}
		fmt.Fprintf(&b, "- When you finish, Öge runs the project's Checks on your changes: %s. Their result decides the outcome, not your report.\n", strings.Join(q, ", "))
	}
	b.WriteString("- " + Assumptions + "\n")
	b.WriteString("- No one approves requests during this Run. Reading, editing and writing files here, the Check commands, and simple build, vet, test, format, list, find and git diff/status commands are allowed; anything else is denied.\n")
	b.WriteString("- Run each command on its own from the current directory: no cd, &&, pipes, redirection or shell globs.\n")
	b.WriteString("- End your final message with the line `Exit: done`. If the Task can't be done as asked, end it with `Exit: infeasible` and say why.\n")
	return b.String()
}
