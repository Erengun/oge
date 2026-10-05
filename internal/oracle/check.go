package oracle

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/proc"
	"github.com/erengun/oge/internal/redact"
)

// Repo is the Run repository as a Check needs it.
type Repo interface {
	Source
	Checkout(commit, dir string) error
}

// Runner runs commands the way a Check does: a fresh directory, private
// home, temp and caches, an allowlisted environment, a process-group
// timeout, and redacted, capped output stored as blobs. Checks are
// uncontained in the MVP (ADR-0010): network off is not enforced.
type Runner struct {
	Blobs   *ledger.Blobs
	PassEnv []string // variable names exposed to commands (project.pass_env)
	Getenv  func(string) string
}

// Execution is the command-execution Evidence Öge records (ADR-0011).
type Execution struct {
	Run        string    `json:"run"`
	Argv       []string  `json:"argv"`
	Cwd        string    `json:"cwd"`
	EnvNames   []string  `json:"env_names"`
	ExitCode   int       `json:"exit_code"`
	Signal     string    `json:"signal,omitempty"`
	TimedOut   bool      `json:"timed_out,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	DurationMs int64     `json:"duration_ms"`
	Stdout     Output    `json:"stdout"`
	Stderr     Output    `json:"stderr"`
	Redaction  []string  `json:"redaction_rules"`
	Report     *Report   `json:"report,omitempty"`
	Pass       bool      `json:"pass"`
	Why        string    `json:"why,omitempty"` // why it didn't pass
}

// Output is one captured stream: the redacted, capped copy's blob plus the
// raw stream's length and hash. No unredacted copy is kept.
type Output struct {
	Blob      string `json:"blob"`
	RawBytes  int64  `json:"raw_bytes"`
	RawHash   string `json:"raw_sha256"`
	Truncated bool   `json:"truncated,omitempty"`
}

// maxParsed bounds how much stdout is held for report parsing.
const maxParsed = 256 << 20

// Exec runs a shell command line in dir. env holds the fixed variables;
// the PassEnv names are added from the process environment. It returns an
// error only when Öge itself fails; a nonzero exit is data.
func (r *Runner) Exec(ctx context.Context, line, dir string, env map[string]string, timeout time.Duration, outputCap int64) (Execution, []byte, error) {
	argv := []string{"/bin/sh", "-c", line}
	e := Execution{Run: line, Argv: argv, Cwd: dir, Redaction: redact.Rules()}
	vars := map[string]string{}
	for k, v := range env {
		vars[k] = v
	}
	for _, name := range r.PassEnv {
		if v := r.Getenv(name); v != "" {
			vars[name] = v
		}
	}
	var envList []string
	for k, v := range vars {
		envList = append(envList, k+"="+v)
		e.EnvNames = append(e.EnvNames, k)
	}
	sort.Strings(e.EnvNames)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, envList
	cmd.Cancel = func() error { proc.Kill(cmd); return nil }
	cmd.WaitDelay = 5 * time.Second
	stdout, stderr := newCapture(maxParsed), newCapture(outputCap)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	e.StartedAt = time.Now().UTC()
	if err := proc.Start(cmd); err != nil {
		return e, nil, fmt.Errorf("starting %q: %w", line, err)
	}
	err := cmd.Wait()
	e.DurationMs = time.Since(e.StartedAt).Milliseconds()
	e.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		e.ExitCode = exitErr.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			e.Signal = ws.Signal().String()
		}
	case e.TimedOut:
		e.ExitCode = -1
	default:
		return e, nil, fmt.Errorf("running %q: %w", line, err)
	}

	full := stdout.buf.Bytes()
	var serr error
	if e.Stdout, serr = stdout.store(r.Blobs, outputCap); serr != nil {
		return e, nil, serr
	}
	if e.Stderr, serr = stderr.store(r.Blobs, outputCap); serr != nil {
		return e, nil, serr
	}
	e.Pass = e.ExitCode == 0 && !e.TimedOut && e.Signal == ""
	switch {
	case e.TimedOut:
		e.Why = "timed out after " + timeout.String()
	case e.Signal != "":
		e.Why = "killed by " + e.Signal
	case e.ExitCode != 0:
		e.Why = fmt.Sprintf("exit %d", e.ExitCode)
	}
	return e, full, nil
}

// capture keeps up to limit bytes and hashes and counts everything.
type capture struct {
	buf   bytes.Buffer
	limit int64
	n     int64
	h     hash.Hash
}

func newCapture(limit int64) *capture { return &capture{limit: limit, h: sha256.New()} }

func (c *capture) Write(p []byte) (int, error) {
	c.h.Write(p)
	c.n += int64(len(p))
	if room := c.limit - int64(c.buf.Len()); room > 0 {
		if int64(len(p)) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *capture) store(blobs *ledger.Blobs, outputCap int64) (Output, error) {
	b := c.buf.Bytes()
	o := Output{RawBytes: c.n, RawHash: hex.EncodeToString(c.h.Sum(nil))}
	if int64(len(b)) > outputCap {
		b = b[:outputCap]
	}
	o.Truncated = int64(len(b)) < c.n
	id, err := blobs.Put(redact.Redact(append([]byte(nil), b...)))
	o.Blob = id
	return o, err
}

// Result is a Check's outcome on one Candidate against one Oracle version.
type Result struct {
	Pass     bool        `json:"pass"`
	Setup    *Execution  `json:"setup,omitempty"`
	Commands []Execution `json:"commands"`
}

// Check runs the Oracle on candidate in a fresh Check directory under
// root: the Candidate, then the setup command, then the Oracle's tests
// laid over its test paths, then every Check command. The Verdict is pass
// only if every command passes. root is removed afterwards.
func (r *Runner) Check(ctx context.Context, repo Repo, m *Manifest, candidate, setup, root string) (*Result, error) {
	defer RemoveAll(root)
	dir, env, err := r.Prepare(root)
	if err != nil {
		return nil, err
	}
	if err := repo.Checkout(candidate, dir); err != nil {
		return nil, err
	}
	res := &Result{Pass: true}
	if setup != "" {
		e, _, err := r.Exec(ctx, setup, dir, env, 10*time.Minute, 1<<20)
		if err != nil {
			return nil, err
		}
		res.Setup = &e
		if !e.Pass {
			// It passed on the Snapshot in Preflight, so the Candidate broke
			// it: a fail Verdict (ADR-0011).
			res.Pass = false
			return res, nil
		}
	}
	// The overlay comes after setup, which runs Candidate code, so nothing
	// the Candidate controls runs between laying the Oracle down and the
	// Check commands.
	if err := r.overlay(m, dir); err != nil {
		return nil, err
	}
	for _, c := range m.Commands {
		e, out, err := r.Exec(ctx, c.Run, dir, env, time.Duration(c.TimeoutSec)*time.Second, c.OutputCap)
		if err != nil {
			return nil, err
		}
		if c.Report == ReportGoTestJSON {
			rep := ParseGoTestJSON(out)
			e.Report = &rep
			if e.Pass {
				switch {
				case rep.Error != "":
					e.Pass, e.Why = false, "report: "+rep.Error
				case rep.Failed > 0:
					e.Pass, e.Why = false, fmt.Sprintf("%d failed", rep.Failed)
				case rep.Ran < c.ExpectedTests:
					e.Pass, e.Why = false, fmt.Sprintf("%d ran, %d expected", rep.Ran, c.ExpectedTests)
				}
			}
		}
		res.Commands = append(res.Commands, e)
		res.Pass = res.Pass && e.Pass
	}
	return res, nil
}

// Prepare makes root's fresh tree/ plus private home, temp and caches, and
// returns the tree and the fixed environment for commands run there.
func (r *Runner) Prepare(root string) (string, map[string]string, error) {
	dirs := map[string]string{}
	for _, d := range []string{"tree", "home", "tmp", "gocache", "gopath"} {
		p := filepath.Join(root, d)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return "", nil, err
		}
		dirs[d] = p
	}
	env := map[string]string{
		"PATH": r.Getenv("PATH"), "HOME": dirs["home"], "TMPDIR": dirs["tmp"],
		"GOCACHE": dirs["gocache"], "GOPATH": dirs["gopath"], "GOMODCACHE": filepath.Join(dirs["gopath"], "pkg", "mod"),
		"GOTOOLCHAIN": "local", "XDG_CACHE_HOME": filepath.Join(dirs["home"], ".cache"),
	}
	return dirs["tree"], env, nil
}

// overlay lays the Oracle version over the Check directory's test paths:
// files matching the test globs are the Oracle's, never the Candidate's.
func (r *Runner) overlay(m *Manifest, dir string) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if MatchAny(m.TestGlobs, filepath.ToSlash(rel)) {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, f := range m.Tests {
		b, err := r.Blobs.Get(f.Blob)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RemoveAll removes a directory tree, first making read-only directories
// (such as Go's module cache) writable.
func RemoveAll(dir string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// Report formats Öge parses itself.
const ReportGoTestJSON = "go-test-json"

// Report is what Öge's parser read from a structured report.
type Report struct {
	Format      string   `json:"format"`
	Ran         int      `json:"ran"`
	Failed      int      `json:"failed"`
	Skipped     int      `json:"skipped"`
	FailedTests []string `json:"failed_tests,omitempty"`
	Error       string   `json:"error,omitempty"` // missing or malformed
}

// ParseGoTestJSON reads `go test -json` output. Every non-empty line must
// be a JSON event; tests count by their terminal pass, fail or skip
// action. Unknown actions are ignored.
func ParseGoTestJSON(b []byte) Report {
	rep := Report{Format: ReportGoTestJSON}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 16<<20)
	events := 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev struct{ Action, Package, Test string }
		if err := json.Unmarshal(line, &ev); err != nil || ev.Action == "" {
			rep.Error = fmt.Sprintf("line %q is not a go test -json event", truncate(string(line), 80))
			return rep
		}
		events++
		if ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "pass":
			rep.Ran++
		case "fail":
			rep.Ran++
			rep.Failed++
			rep.FailedTests = append(rep.FailedTests, ev.Test)
		case "skip":
			rep.Skipped++
		}
	}
	if err := sc.Err(); err != nil {
		rep.Error = err.Error()
	} else if events == 0 {
		rep.Error = "no report: the command printed no go test -json events"
	}
	return rep
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
