package cli

import (
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// ExitParked is a Run waiting for a human decision (ADR-0015).
const ExitParked = 10

// scopeText is the compact line for an Attempt's reverts, or "" when
// there were none. It says what changed and what Öge undid, never why.
func scopeText(a *run.Attempt) string {
	var prot, links, inWay []string
	allTests := true
	for _, r := range a.Reverted {
		switch {
		case r.Tamper:
			prot = append(prot, r.Path)
			allTests = allTests && r.Class == run.ClassOracleTest
		case r.Class == workspace.ClassSymlinkEscape:
			links = append(links, r.Path)
		default:
			inWay = append(inWay, r.Path)
		}
	}
	var parts []string
	if len(prot) > 0 {
		one, many := "protected change", "protected changes"
		if allTests {
			one, many = "protected test change", "protected test changes"
		}
		parts = append(parts, counted(prot, one, many, "reverted"))
	}
	if len(links) > 0 {
		parts = append(parts, counted(links, "symlink out of the Workspace", "symlinks out of the Workspace", "removed"))
	}
	if len(inWay) > 0 {
		parts = append(parts, counted(inWay, "file in the way of a protected path", "files in the way of protected paths", "removed"))
	}
	return clean(strings.Join(parts, " · "))
}

// counted is "2 <many> <verb>: a, b", naming at most three paths.
func counted(paths []string, one, many, verb string) string {
	noun := many
	if len(paths) == 1 {
		noun = one
	}
	names, more := paths, ""
	if len(names) > 3 {
		names, more = names[:3], fmt.Sprintf(" and %d more", len(paths)-3)
	}
	return fmt.Sprintf("%d %s %s: %s%s", len(paths), noun, verb, strings.Join(names, ", "), more)
}
