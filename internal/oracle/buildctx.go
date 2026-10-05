package oracle

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/build"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
)

// Baseline-relative buildability (ADR-0020): which protected tests count
// is never the Candidate's to decide. A test that starts on the Snapshot
// control is required; one the Snapshot's fixed build context excludes
// (GOOS/GOARCH, filename platform suffixes, //go:build lines, the tags the
// Check commands name) is not covered, with the reason. The static view
// below gives those reasons, and the required set when the Snapshot
// doesn't compile.

// tagsFlag finds -tags in a Check command line or GOFLAGS: -tags=a,b,
// -tags a,b, or a quoted list such as -tags 'a b'.
var tagsFlag = regexp.MustCompile(`(?:^|\s)-{1,2}tags(?:=|\s+)(?:"([^"]*)"|'([^']*)'|([^\s"']+))`)

// buildEnv is the go command's view of a Check's environment.
type buildEnv struct {
	GOOS, GOARCH, CGO_ENABLED, GOFLAGS string
}

// buildContext is the fixed build context a Check builds under: the
// platform and cgo setting of the Check's environment, with the build
// tags its commands and GOFLAGS name.
func buildContext(m *Manifest, be buildEnv) build.Context {
	ctx := build.Default
	ctx.BuildTags = nil
	if be.GOOS != "" {
		ctx.GOOS = be.GOOS
	}
	if be.GOARCH != "" {
		ctx.GOARCH = be.GOARCH
	}
	if be.CGO_ENABLED != "" {
		ctx.CgoEnabled = be.CGO_ENABLED == "1"
	}
	lines := []string{be.GOFLAGS}
	for _, c := range m.Commands {
		lines = append(lines, c.Run)
	}
	split := regexp.MustCompile(`[,\s]+`)
	for _, l := range lines {
		for _, sub := range tagsFlag.FindAllStringSubmatch(l, -1) {
			for _, tag := range split.Split(sub[1]+sub[2]+sub[3], -1) {
				if tag != "" {
					ctx.BuildTags = append(ctx.BuildTags, tag)
				}
			}
		}
	}
	return ctx
}

// checkBuildEnv asks the go command, in the Check's environment, which
// platform and cgo setting it builds for. It runs no Candidate code: only
// go env, outside the Check directory. Without a go command the Check's
// own variables, then Öge's defaults, stand in.
func (r *Runner) checkBuildEnv(ctx context.Context, env map[string]string) buildEnv {
	vars := map[string]string{}
	for _, name := range r.PassEnv {
		if v := r.Getenv(name); v != "" {
			vars[name] = v
		}
	}
	for k, v := range env {
		vars[k] = v
	}
	be := buildEnv{GOOS: vars["GOOS"], GOARCH: vars["GOARCH"], CGO_ENABLED: vars["CGO_ENABLED"], GOFLAGS: vars["GOFLAGS"]}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS")
	cmd.Dir = env["TMPDIR"]
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if out, err := cmd.Output(); err == nil {
		var got buildEnv
		if json.Unmarshal(out, &got) == nil && got.GOOS != "" {
			return got
		}
	}
	return be
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
