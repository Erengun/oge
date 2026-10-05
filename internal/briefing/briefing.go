// Package briefing builds what each Role kind is told at the start of an
// Attempt, from the Task and the Run's frozen config (ADR-0009): the
// implementer's and the verifier's.
package briefing

import (
	"fmt"
	"sort"
	"strconv"
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

// Verifier is the verifier's Briefing: the Task with its Acceptance
// criteria, then how QA works in this Run. It never carries the
// implementer's transcript, Exit, Claims, authorship or earlier Verdicts
// (ADR-0009); those aren't inputs here. checks are the Run's Check
// commands and testGlobs the globs a new test file must match.
func Verifier(t task.Task, checks, testGlobs []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(t.Text))
	b.WriteString("\n\n---\nHow this Run works:\n")
	b.WriteString("- You are QA. The current directory is a disposable copy of the repository with a proposed change for this Task on top of it; `git diff` and `git status` show the change.\n")
	b.WriteString("- Your job is to write new tests that check the Task, its acceptance criteria and the change, and catch what is wrong with it. A failing test is how you report a problem: don't fix the code.\n")
	fmt.Fprintf(&b, "- Only add new test files matching %s; don't change or delete any existing file. Anything else you write is discarded.\n", quoted(testGlobs))
	if len(checks) > 0 {
		fmt.Fprintf(&b, "- Öge keeps your tests private and runs them with the project's Checks: %s. Each test must pass on a correct solution of the Task.\n", quoted(checks))
	}
	if len(t.Criteria) > 0 {
		fmt.Fprintf(&b, "- Name the criterion each test checks in its doc comment, e.g. `// %s: …` above `func Test…`.\n", t.Criteria[0].ID)
	}
	b.WriteString("- Give each test a descriptive name and failure message: they are shown to the user.\n")
	b.WriteString("- Prefer black-box tests in the external test package (`package <name>_test`) where practical, and don't change package state from a test.\n")
	b.WriteString("- " + Assumptions + "\n")
	b.WriteString("- No one approves requests during this Run. Reading files, writing new test files, the Check commands, and simple build, vet, test, list, find and git diff/status commands are allowed; anything else is denied.\n")
	b.WriteString("- Run each command on its own from the current directory: no cd, &&, pipes, redirection or shell globs.\n")
	b.WriteString("- End your final message with the line `Exit: extended` if you added tests, or `Exit: no_additions` if none are needed. If the Task contradicts the existing tests, end it with `Exit: conflicts_with_oracle` and say why.\n")
	return b.String()
}

func quoted(list []string) string {
	q := make([]string, len(list))
	for i, c := range list {
		q[i] = "`" + c + "`"
	}
	return strings.Join(q, ", ")
}

// HeldOutFeedback is what the implementer learns about failing held-out
// tests: a count plus Acceptance criterion ids, never a file name, test
// name, source, assertion text or output (ADR-0009). criteria holds each
// failing test's criterion ids; empty means unmapped.
// TODO(#46-decision): count + criterion ids only, as ADR-0009 and spec
// story 48 say; test names and messages reach the human's terminal, never
// the implementer.
func HeldOutFeedback(criteria [][]string) string {
	n := len(criteria)
	count := map[string]int{}
	var ids []string
	unmapped := 0
	for _, cs := range criteria {
		if len(cs) == 0 {
			unmapped++
		}
		for _, c := range cs {
			if count[c] == 0 {
				ids = append(ids, c)
			}
			count[c]++
		}
	}
	sortIDs(ids)
	tests := "tests"
	if n == 1 {
		tests = "test"
	}
	var b strings.Builder
	if len(ids) == 0 {
		fmt.Fprintf(&b, "%d held-out %s failed (", n, tests)
		if n == 1 {
			b.WriteString("it names no acceptance criterion).")
		} else {
			b.WriteString("they name no acceptance criterion).")
		}
	} else {
		var parts []string
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf("%s ×%d", id, count[id]))
		}
		if unmapped > 0 {
			parts = append(parts, fmt.Sprintf("unmapped ×%d", unmapped))
		}
		fmt.Fprintf(&b, "%d held-out %s failed: %s.", n, tests, strings.Join(parts, ", "))
	}
	b.WriteString(" Öge doesn't show held-out tests. Re-read the Task and its acceptance criteria, and fix the behaviour they describe.")
	return b.String()
}

// sortIDs orders criterion ids by number: AC-2 before AC-10.
func sortIDs(ids []string) {
	num := func(s string) int {
		n, _ := strconv.Atoi(strings.TrimPrefix(s, "AC-"))
		return n
	}
	sort.Slice(ids, func(i, j int) bool { return num(ids[i]) < num(ids[j]) })
}
