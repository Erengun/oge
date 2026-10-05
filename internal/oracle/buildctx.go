package oracle

import (
	"bufio"
	"bytes"
	"fmt"
	"go/build"
	"io"
	"path"
	"regexp"
	"strings"
)

// Baseline-relative buildability (ADR-0020): which protected tests count
// is never the Candidate's to decide. A test that starts on the Snapshot
// control is required; one the Snapshot's fixed build context excludes
// (GOOS/GOARCH, filename platform suffixes, //go:build lines, the tags the
// Check commands name) is not covered, with the reason. The static view
// below gives those reasons, and the required set when the Snapshot
// doesn't compile.

// tagsFlag finds -tags in a Check command line or GOFLAGS.
var tagsFlag = regexp.MustCompile(`(?:^|\s)-{1,2}tags[= ]+["']?([^\s"']+)`)

// buildContext is the fixed build context a Check builds under: the
// host's platform, with the build tags its commands and GOFLAGS name.
func buildContext(m *Manifest, goflags string) build.Context {
	ctx := build.Default
	ctx.BuildTags = nil
	lines := []string{goflags}
	for _, c := range m.Commands {
		lines = append(lines, c.Run)
	}
	for _, l := range lines {
		for _, sub := range tagsFlag.FindAllStringSubmatch(l, -1) {
			ctx.BuildTags = append(ctx.BuildTags, strings.Split(sub[1], ",")...)
		}
	}
	return ctx
}

// excluded says why ctx never builds the Oracle test file p, or "" when
// it does.
func excluded(ctx build.Context, p string, src []byte) string {
	open := func(content []byte) func(string) (io.ReadCloser, error) {
		return func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(content)), nil }
	}
	dir, name := path.Split(p)
	ctx.OpenFile = open([]byte("package p\n"))
	if ok, err := ctx.MatchFile(dir, name); err == nil && !ok {
		return fmt.Sprintf("the Check platform is %s/%s", ctx.GOOS, ctx.GOARCH)
	}
	ctx.OpenFile = open(src)
	if ok, err := ctx.MatchFile(dir, name); err == nil && !ok {
		if c := constraint(src); c != "" {
			return fmt.Sprintf("requires build constraint %q", c)
		}
		return "excluded by its build constraints"
	}
	return ""
}

// constraint is src's //go:build expression, or its first // +build line.
func constraint(src []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(src))
	plus := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if e, ok := strings.CutPrefix(line, "//go:build "); ok {
			return strings.TrimSpace(e)
		}
		if e, ok := strings.CutPrefix(line, "// +build "); ok && plus == "" {
			plus = strings.TrimSpace(e)
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
	}
	return plus
}
