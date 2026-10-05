package cli

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

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

// testConfigWarning is the warning for a Go project that leaves
// project.test_config empty, or "": the Oracle then doesn't hold go.mod or
// go.sum, so setup and Candidate code can change what the Check reads.
//
// TODO(#41-decision): a warning, not a refusal; Go only, from the
// Snapshot's root go.mod or go.sum.
func testConfigWarning(root string, f *pipeline.Frozen) string {
	if len(f.Project.TestConfig) > 0 {
		return ""
	}
	for _, p := range []string{"go.mod", "go.sum"} {
		if sf, err := workspace.ReadSnapshotFile(root, p); err == nil && sf.InSnapshot {
			return "test_config is empty: setup and Candidate code can change go.mod/go.sum that the Check reads"
		}
	}
	return ""
}

// withWarning has v's summary show warn.
func withWarning(v view, warn string) {
	switch v := v.(type) {
	case *renderer:
		v.warn = warn
	case *tui:
		v.plain.warn = warn
	}
}
