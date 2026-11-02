package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
)

// RecTreeChecks is Checks run on an in-place Chat's working tree, when the
// human asked for them.
const RecTreeChecks = "TreeChecksRan"

// TreeChecksParams is what Checks on demand run from.
type TreeChecksParams struct {
	// Frozen holds the Check commands, as the repo resolves them now.
	Frozen *pipeline.Frozen
	// Getenv reads the environment names the Checks pass through; os.Getenv
	// when nil.
	Getenv func(string) string
}

// TreeChecks is what ran, on the user's working tree.
type TreeChecks struct {
	Dir      string
	Commands []oracle.Execution
	// Tails are the last lines of each command's redacted output, for a
	// command that did not pass; empty for one that did. Same order as
	// Commands.
	Tails [][]string
	// Pass is every command passing; false when there were none.
	Pass bool
}

// ChecksOnTree runs the Pipeline's Check commands in the in-place Chat's
// working tree, as the user asked, and records what ran. It is not a cut:
// there is no Snapshot, Oracle version, Candidate or Receipt, no overlay
// of held-out tests and no attestation, because the tree was never
// isolated, and nothing here says otherwise. The commands run as the
// repo's own, at the real path, in Öge's private home and caches.
func (c *Chat) ChecksOnTree(ctx context.Context, p TreeChecksParams) (*TreeChecks, error) {
	if !c.inPlace {
		return nil, errors.New("checks on demand are for an adopted session: this Chat's changes are checked in isolation")
	}
	if p.Frozen == nil || len(p.Frozen.Checks) == 0 {
		return nil, errors.New("no Checks are configured for this repo")
	}
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	runner := &oracle.Runner{Blobs: c.blobs, PassEnv: p.Frozen.Project.PassEnv, Getenv: getenv}
	// The caches persist across runs of the same Chat, so a second run is warm.
	_, env, err := runner.Prepare(filepath.Join(c.Dir, "tree-checks"))
	if err != nil {
		return nil, err
	}
	res := &TreeChecks{Dir: c.Workspace, Pass: true}
	for _, ch := range p.Frozen.Checks {
		timeout, limit := ch.Timeout, ch.OutputCap
		if timeout <= 0 {
			timeout = pipeline.DefaultCheckTimeout
		}
		if limit <= 0 {
			limit = pipeline.DefaultOutputCap
		}
		e, _, err := runner.Exec(ctx, ch.Run, c.Workspace, env, timeout, limit)
		if err != nil {
			return nil, err
		}
		// The command's own exit decides: no report parsing, no expected
		// tests, no attestation (those need the isolated Check's overlay).
		e.Pass = e.ExitCode == 0 && !e.TimedOut && e.Signal == ""
		if !e.Pass && e.Why == "" {
			e.Why = "exit " + strconv.Itoa(e.ExitCode)
		}
		res.Commands = append(res.Commands, e)
		var tail []string
		if !e.Pass {
			tail = c.outputTail(e, 8)
		}
		res.Tails = append(res.Tails, tail)
		res.Pass = res.Pass && e.Pass
	}
	rows := make([]map[string]any, len(res.Commands))
	for i, e := range res.Commands {
		rows[i] = map[string]any{"run": e.Run, "pass": e.Pass, "exit_code": e.ExitCode, "duration_ms": e.DurationMs,
			"stdout": e.Stdout.Blob, "stderr": e.Stderr.Blob, "why": e.Why}
	}
	if err := c.log(RecTreeChecks, map[string]any{"chat": c.ID, "at": time.Now().UTC(), "in_place": true,
		"isolated": false, "dir": c.Workspace, "pass": res.Pass, "commands": rows}); err != nil {
		return nil, err
	}
	return res, nil
}

// outputTail is the last n non-empty lines of a command's redacted output,
// stderr then stdout.
func (c *Chat) outputTail(e oracle.Execution, n int) []string {
	var lines []string
	for _, id := range []string{e.Stdout.Blob, e.Stderr.Blob} {
		if id == "" {
			continue
		}
		b, err := c.blobs.Get(id)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
