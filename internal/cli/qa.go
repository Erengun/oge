package cli

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// QA is the user-facing label for the verifier Stage (docs/positioning.md):
// a fresh agent writing held-out tests, then Öge's Check. It never claims
// more than the verifier and the Check did.
const qaLabel = "QA"

// maxIssues bounds the "QA found N issues" lines.
const maxIssues = 5

// stageLabel is how a stage is named on screen.
func stageLabel(name string, f *pipeline.Frozen) string {
	if f != nil {
		for _, s := range f.Stages {
			if s.Name == name && s.Role == "verifier" {
				return qaLabel
			}
		}
	}
	return name
}

// freshAgent is "Fresh Claude": the verifier is a distinct actor, a new
// Session of the same agent, never "the same agent" (ADR-0009).
func freshAgent(name string) string {
	r, n := utf8.DecodeRuneInString(name)
	return "Fresh " + string(unicode.ToUpper(r)) + name[n:]
}

// reviewingText is QA's line while it runs.
func reviewingText(agentName string) string { return freshAgent(agentName) + " · reviewing" }

// qaText is a finished verifier Attempt's line.
func qaText(a *run.Attempt) string {
	who := freshAgent(a.Agent)
	switch {
	case a.Stop != "":
		return who + " · Infrastructure stop"
	case a.Failure != "":
		return fmt.Sprintf("%s · Attempt failed: %s", who, clean(a.Failure))
	case a.QA == nil:
		return who
	}
	q := a.QA
	parts := []string{who, "Exit " + clean(q.Exit)}
	if q.HeldOut > 0 {
		s := fmt.Sprintf("+%d held-out", q.HeldOut)
		if q.Unmapped > 0 {
			s += fmt.Sprintf(" (%d unmapped)", q.Unmapped)
		}
		parts = append(parts, s)
	}
	if n := len(q.Dropped); n > 0 {
		parts = append(parts, pluralOf(n, "addition", "additions")+" left out")
	}
	if n := len(q.Withheld); n > 0 {
		parts = append(parts, pluralOf(n, "file", "files")+" withheld")
	}
	return strings.Join(parts, " · ")
}

// qaScopeText is the line for QA's discarded writes, or "".
func qaScopeText(a *run.Attempt) string {
	var paths []string
	for _, r := range a.Reverted {
		if r.Class == run.ClassOutsideScope {
			paths = append(paths, pathText(r.Path))
		}
	}
	if len(paths) == 0 {
		return ""
	}
	names, more := paths, ""
	if len(names) > 3 {
		names, more = names[:3], fmt.Sprintf(" and %d more", len(paths)-3)
	}
	return clean(fmt.Sprintf("%s outside QA's scope: %s%s", pluralOf(len(paths), "discarded write", "discarded writes"), strings.Join(names, ", "), more))
}

func pluralOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// issueLines are "QA found N issues" and a line per issue, from the
// failing held-out tests' names and first messages, then a neutral line
// per package whose held-out tests no longer build: that is no QA finding.
func issueLines(issues, conflicts []string) []string {
	var lines []string
	if len(issues) > 0 {
		lines = append(lines, "QA found "+pluralOf(len(issues), "issue", "issues"))
		for i, s := range issues {
			if i == maxIssues {
				lines = append(lines, fmt.Sprintf("· and %d more", len(issues)-maxIssues))
				break
			}
			lines = append(lines, "· "+shortLine(clean(s), 100))
		}
	}
	for _, p := range conflicts {
		lines = append(lines, clean("held-out test no longer builds against the Candidate (package "+p+")"))
	}
	return lines
}

func shortLine(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// qaStep is what a verifier event shows as activity: its tool steps, but
// not what it says, which may describe its held-out tests.
// TODO(#46-decision): QA's own words stay off the screen in both views;
// they are in the Ledger.
func qaStep(e agent.Event) bool {
	return !(e.Kind == agent.Claim && e.Tool == "")
}

// notCovered is the summary's "Not covered" text for the Run's mode.
// TODO(#63): the Receipt replaces it.
func notCovered(f *pipeline.Frozen, res *run.Result) string {
	const unconfined = "Checks run Candidate code uncontained: no isolation against deliberately hostile code running with your privileges"
	var parts []string
	if s := unresolvedText(res); s != "" {
		parts = append(parts, s)
	}
	if f == nil || f.Mode == pipeline.Fast {
		return strings.Join(append(parts, "an independent verifier and held-out tests (Fast mode)", unconfined), " · ")
	}
	if res.Oracle == 0 {
		// QA never claims more than it did (docs/positioning.md).
		parts = append(parts, "QA added no held-out tests")
	}
	if q := res.QA; q != nil {
		if q.UnmappedTotal > 0 {
			parts = append(parts, pluralOf(q.UnmappedTotal, "held-out test names", "held-out tests name")+" no acceptance criterion")
		}
		// An Accepted Run has no Ambiguous files left: the review resolved
		// each one (#97). Any other outcome lists its unresolved ones
		// above; delivering them is #105.
	}
	return strings.Join(append(parts, unconfined), " · ")
}
