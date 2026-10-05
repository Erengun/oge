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
	// Seed, when set, is the Run's warm cache seed: each Check's caches
	// start as private copies of it (ADR-0021).
	Seed *Seed
	// SeedTemplate, when set, is a build cache each new seed starts as a
	// private clone or copy of, before setup and the warm step run. Only
	// tests set it, to skip compiling the standard library every Run
	// (see cli.Env.CacheSeedTemplate).
	SeedTemplate string
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
	for _, name := range r.PassEnv {
		if v := r.Getenv(name); v != "" {
			vars[name] = v
		}
	}
	for k, v := range env { // the fixed variables always win
		vars[k] = v
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
	err := proc.Wait(cmd)
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
	Pass bool `json:"pass"`
	// Why is set when the Check failed for a reason no single command
	// shows: an Oracle path the Candidate blocked, or Oracle tests no
	// report shows passing (named in Missing).
	Why     string   `json:"why,omitempty"`
	Missing []string `json:"missing_tests,omitempty"`
	// Cache is how the Check-local caches were made (CacheClone, CacheCopy
	// or CacheCold), and CacheMs how long that took.
	Cache    string      `json:"cache"`
	CacheWhy string      `json:"cache_why,omitempty"` // why it fell back
	CacheMs  int64       `json:"cache_materialise_ms"`
	Setup    *Execution  `json:"setup,omitempty"`
	Commands []Execution `json:"commands"`
}

// Check runs the Oracle on candidate in a fresh Check directory under
// root: the Candidate, then the setup command, then the Oracle's tests
// laid over its test paths, then every Check command. The Verdict is pass
// only if every command passes. root is removed afterwards.
func (r *Runner) Check(ctx context.Context, repo Repo, m *Manifest, candidate, setup, root string) (*Result, error) {
	defer RemoveAll(root)
	dir, env, cache, err := r.prepareCheck(root)
	if err != nil {
		return nil, err
	}
	if err := repo.Checkout(candidate, dir); err != nil {
		return nil, err
	}
	res := &Result{Pass: true, Cache: cache.strategy, CacheWhy: cache.why, CacheMs: cache.ms}
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
	if blocked, err := r.overlay(m, dir); err != nil {
		return nil, err
	} else if blocked != "" {
		res.Pass, res.Why = false, blocked
		return res, nil
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
	// Every Oracle test must pass in some go-test-json report: a package
	// that silently dropped out of the run (a nested go.mod, a build
	// constraint) is not a pass.
	var reports []*Report
	for _, e := range res.Commands {
		if e.Report != nil {
			reports = append(reports, e.Report)
		}
	}
	if len(reports) > 0 {
		if res.Missing = missingTests(m.Expected, reports); len(res.Missing) > 0 {
			names := res.Missing
			if len(names) > 5 {
				names = append(names[:5:5], "…")
			}
			res.Pass = false
			res.Why = fmt.Sprintf("Oracle tests that never passed (%d): %s", len(res.Missing), strings.Join(names, ", "))
		}
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
		"GOTOOLCHAIN": "local", "GOWORK": "off", "XDG_CACHE_HOME": filepath.Join(dirs["home"], ".cache"),
	}
	return dirs["tree"], env, nil
}

// overlay lays the Oracle version over the Check directory's test paths:
// files matching the test globs or the test-config globs are the Oracle's,
// never the Candidate's.
// It never writes outside dir: an Oracle path the Candidate blocks with a
// symlink (or a non-directory) anywhere along it is returned as a reason
// the Check fails, and nothing is written for it.
func (r *Runner) overlay(m *Manifest, dir string) (blocked string, err error) {
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel := filepath.ToSlash(rel); MatchAny(m.TestGlobs, rel) || MatchAny(m.TestConfigGlobs, rel) {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, f := range append(append([]File(nil), m.Tests...), m.Config...) {
		if why, err := blockedPath(dir, f.Path); err != nil || why != "" {
			return why, err
		}
		b, err := r.Blobs.Get(f.Blob)
		if err != nil {
			return "", err
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", err
		}
		// O_EXCL: the removal pass cleared every test path, so anything
		// here now is not ours to write through.
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return "", err
		}
		_, err = out.Write(b)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", err
		}
	}
	return "", nil
}

// blockedPath Lstats every existing component of rel under dir and says
// why the Oracle can't be written there, or "" when it can.
func blockedPath(dir, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	p := dir
	for i, part := range parts {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", nil // the rest is created fresh
		case err != nil:
			return "", err
		case fi.Mode()&os.ModeSymlink != 0:
			return "Oracle path " + rel + " is blocked by a symlink in the Candidate", nil
		case i < len(parts)-1 && !fi.IsDir(), i == len(parts)-1:
			return "Oracle path " + rel + " is blocked by a file in the Candidate", nil
		}
	}
	return "", nil
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

	outcomes map[TestID]string // top-level tests' terminal actions, by package
}

// outcome is id's terminal action in the report ("" when it never ran);
// an id with no package matches the test in any package, a pass first.
func (r *Report) outcome(id TestID) string {
	if id.Package != "" {
		return r.outcomes[id]
	}
	got := ""
	for p, o := range r.outcomes {
		if p.Name == id.Name && (got == "" || o == "pass") {
			got = o
		}
	}
	return got
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
		if (ev.Action == "pass" || ev.Action == "fail" || ev.Action == "skip") && !strings.Contains(ev.Test, "/") {
			if rep.outcomes == nil {
				rep.outcomes = map[TestID]string{}
			}
			rep.outcomes[TestID{Package: ev.Package, Name: ev.Test}] = ev.Action
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
