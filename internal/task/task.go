// Package task parses a Task: the user's Markdown statement of what a Run
// must change, with its Acceptance criteria.
package task

import (
	"regexp"
	"strconv"
	"strings"
)

// Task is the resolved input of a Run. It never changes inside a Run.
type Task struct {
	Title    string
	Text     string
	Criteria []Criterion
}

// Criterion is one Acceptance criterion with its stable id (AC-1, AC-2, …).
type Criterion struct {
	ID   string
	Text string
}

// EditorTemplate is what $EDITOR opens when no Task was given. Everything in
// it is a comment, so an untouched template is an empty Task.
const EditorTemplate = `<!--
Write the Task in Markdown: a "# Title" line, then what should change.
Optionally list testable statements under a heading:

## Acceptance criteria
- After 5 failed logins, further attempts get HTTP 429.

Comments like this one are ignored. Save an empty file to cancel.
-->
`

var (
	listItem = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?(.*)$`)
	heading  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	comment  = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// Parse resolves a Markdown Task. Top-level list items under an
// "## Acceptance criteria" heading become Criteria, numbered in order;
// indented lines continue the previous item. CRLF line endings are read as LF.
func Parse(markdown string) Task {
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	t := Task{Text: strings.TrimSpace(markdown)}
	inCriteria, inFence := false, false
	fence := ""
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence, fence = true, trimmed[:3]
			continue
		}
		if m := heading.FindStringSubmatch(trimmed); m != nil && line == trimmed {
			if t.Title == "" && len(m[1]) == 1 {
				t.Title = m[2]
			}
			inCriteria = strings.EqualFold(m[2], "acceptance criteria")
			continue
		}
		if t.Title == "" && trimmed != "" {
			t.Title = trimmed
		}
		if !inCriteria || trimmed == "" {
			continue
		}
		if m := listItem.FindStringSubmatch(line); m != nil {
			t.Criteria = append(t.Criteria, Criterion{
				ID:   "AC-" + strconv.Itoa(len(t.Criteria)+1),
				Text: strings.TrimSpace(m[1]),
			})
		} else if n := len(t.Criteria); n > 0 && line != trimmed {
			t.Criteria[n-1].Text += " " + trimmed
		}
	}
	return t
}

// StripComments removes HTML comments, as left by EditorTemplate.
func StripComments(markdown string) string {
	return comment.ReplaceAllString(markdown, "")
}

// IsEmpty reports whether a Task has no content.
func IsEmpty(markdown string) bool {
	return strings.TrimSpace(markdown) == ""
}
