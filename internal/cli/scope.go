package cli

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

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
		p := pathText(r.Path)
		switch {
		case r.Tamper:
			prot = append(prot, p)
			allTests = allTests && r.Class == run.ClassOracleTest
		case r.Class == workspace.ClassSymlinkEscape:
			links = append(links, p)
		default:
			inWay = append(inWay, p)
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

// pathText is a path for the terminal: quoted with Go escapes when it holds
// a control character or invalid UTF-8, so clean has nothing to drop and
// two distinct paths never print alike.
func pathText(p string) string {
	if !utf8.ValidString(p) || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) }) {
		return strconv.Quote(p)
	}
	return p
}
