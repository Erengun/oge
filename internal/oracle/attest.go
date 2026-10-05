package oracle

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Protected-test execution attestation (ADR-0020). The report a test
// binary prints is diagnostic only: Candidate code runs in that binary and
// can print any frames it likes. What a Check requires instead is that
// every expected Oracle test proves it executed:
//
//   - After the Candidate is frozen, each Check draws a fresh random token
//     per expected test. Tokens exist only in the overlay written into the
//     Check directory, never in the Candidate, the Briefing or a Workspace.
//   - The overlay rewrites each expected test so its first statement
//     attests, through a helper the overlay adds to the test's package.
//   - The helper writes "<token> start" when the test starts and, from a
//     t.Cleanup registered first (so it runs last), "<token> pass|fail|skip"
//     once the test and its subtests have finished.
//   - The lines go over a pipe Öge owns: an inherited descriptor named by
//     AttestEnv, never stdout, stderr or the report.
//
// Candidate code in the same process can find that descriptor, and the
// tokens in the overlay's sources or the binary. ADR-0020 accepts this:
// the MVP claims no isolation against deliberately hostile code running
// with the user's privileges. What this closes is everything short of
// that: printing PASS frames, exiting 0 from init or TestMain, exiting
// part-way through a test, or skipping a test that runs on the Snapshot.

// AttestEnv names the attestation descriptor in a Check command's
// environment. The static tripwires flag Candidate changes that mention
// it.
const AttestEnv = "OGE_ATTEST_FD"

// attestFD is the descriptor the channel is inherited as: the first of
// exec.Cmd.ExtraFiles.
const attestFD = 3

// Attested dispositions of a protected test.
const (
	AttestPass   = "pass"
	AttestFail   = "fail"
	AttestSkip   = "skip"
	AttestExited = "exited" // started, never finished: the process left mid-test
	AttestAbsent = "absent" // never started
	// AttestUnattested is a Snapshot control's test that its report shows
	// running but that never attested: the mechanism failed, not the code.
	AttestUnattested = "unattested"
)

// TestResult is one expected Oracle test in a Check.
type TestResult struct {
	TestID
	// Attested is the disposition the attestation channel showed.
	Attested string `json:"attested"`
	// Report is what a structured report claimed: diagnostic only.
	Report string `json:"report,omitempty"`
	// Snapshot is the test's disposition on the Snapshot control, set
	// when the Check consulted it.
	Snapshot string `json:"snapshot,omitempty"`
}

// attestation is one Check's tokens and the helpers its overlay adds.
type attestation struct {
	suffix  string            // makes the helper's identifiers and files unguessable
	tokens  map[string]TestID // token → the test it attests
	helpers map[[2]string]bool
}

func newAttestation() (*attestation, error) {
	s, err := randomHex(4)
	if err != nil {
		return nil, err
	}
	return &attestation{suffix: s, tokens: map[string]TestID{}, helpers: map[[2]string]bool{}}, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// instrument returns an Oracle test file with each of its expected tests
// attesting first thing. The call is spliced in after the body's opening
// brace, on the same line, so every line number stays the Snapshot's.
// A file that doesn't parse is returned as it is: it fails to compile on
// its own, and its tests never attest.
func (a *attestation) instrument(f File, src []byte) ([]byte, error) {
	if len(f.Expected) == 0 {
		return src, nil
	}
	want := map[string]TestID{}
	for _, id := range f.Expected {
		want[id.Name] = id
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.Path, src, parser.SkipObjectResolution)
	if err != nil {
		return src, nil
	}
	type edit struct {
		at, end int // replace src[at:end]
		text    string
	}
	var edits []edit
	for _, fn := range testFuncs(file) {
		id, ok := want[fn.Name.Name]
		if !ok || fn.Body == nil {
			continue
		}
		tok, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		a.tokens[tok] = id
		param := fn.Type.Params.List[0]
		name := "ogeT" + a.suffix
		switch {
		case len(param.Names) == 0: // func TestX(*testing.T)
			at := fset.Position(param.Type.Pos()).Offset
			edits = append(edits, edit{at, at, name + " "})
		case param.Names[0].Name == "_":
			at := fset.Position(param.Names[0].Pos()).Offset
			edits = append(edits, edit{at, at + 1, name})
		default:
			name = param.Names[0].Name
		}
		at := fset.Position(fn.Body.Lbrace).Offset + 1
		edits = append(edits, edit{at, at, fmt.Sprintf(" ogeAttest%s(%s, %q);", a.suffix, name, tok)})
	}
	if len(edits) == 0 {
		return src, nil
	}
	a.helpers[[2]string{path.Dir(f.Path), file.Name.Name}] = true
	sort.Slice(edits, func(i, j int) bool { return edits[i].at > edits[j].at })
	out := append([]byte(nil), src...)
	for _, e := range edits {
		out = append(out[:e.at], append([]byte(e.text), out[e.end:]...)...)
	}
	return out, nil
}

// helperFiles are the files the overlay adds: one attestation helper per
// package an instrumented test belongs to (a directory can hold both p
// and p_test), by path.
func (a *attestation) helperFiles() map[string][]byte {
	files := map[string][]byte{}
	for k := range a.helpers {
		dir, pkg := k[0], k[1]
		name := fmt.Sprintf("zz_oge_attest_%s_%s_test.go", a.suffix, pkg)
		files[path.Join(dir, name)] = []byte(fmt.Sprintf(attestHelper, pkg, a.suffix, AttestEnv))
	}
	return files
}

// attestHelper is the Go source of a package's attestation helper: the
// package name, the identifier suffix and AttestEnv. Each line is one
// write well under PIPE_BUF, so lines from test binaries running in
// parallel never interleave.
const attestHelper = `// Code generated by Öge for this Check only (ADR-0020). DO NOT EDIT.

package %[1]s

import (
	"os"
	"strconv"
	"sync"
	"testing"
)

var (
	ogeAttestMu%[2]s  sync.Mutex
	ogeAttestOut%[2]s *os.File
)

func ogeAttest%[2]s(t *testing.T, token string) {
	ogeAttestSend%[2]s(token + " start\n")
	t.Cleanup(func() {
		status := "pass"
		if t.Failed() {
			status = "fail"
		} else if t.Skipped() {
			status = "skip"
		}
		ogeAttestSend%[2]s(token + " " + status + "\n")
	})
}

func ogeAttestSend%[2]s(line string) {
	ogeAttestMu%[2]s.Lock()
	defer ogeAttestMu%[2]s.Unlock()
	if ogeAttestOut%[2]s == nil {
		fd, err := strconv.Atoi(os.Getenv(%[3]q))
		if err != nil {
			return
		}
		ogeAttestOut%[2]s = os.NewFile(uintptr(fd), "oge-attest")
	}
	_, _ = ogeAttestOut%[2]s.WriteString(line)
}
`

// channel is a Check's attestation pipe and what arrived on it.
type channel struct {
	r, w     *os.File
	done     chan struct{}
	finished sync.Once

	mu     sync.Mutex
	events map[string]map[string]bool // token → events seen
	stray  int                        // lines that attest no test of this Check
}

func openChannel() (*channel, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c := &channel{r: r, w: w, done: make(chan struct{}), events: map[string]map[string]bool{}}
	go c.read()
	return c, nil
}

func (c *channel) read() {
	defer close(c.done)
	br := bufio.NewReaderSize(c.r, 4096)
	for {
		line, long, err := br.ReadLine()
		if long { // no attestation is this long: drain the rest, count it
			for long && err == nil {
				_, long, err = br.ReadLine()
			}
			c.mu.Lock()
			c.stray++
			c.mu.Unlock()
		} else if len(line) > 0 {
			c.record(string(line))
		}
		if err != nil {
			return
		}
	}
}

func (c *channel) record(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tok, ev, ok := strings.Cut(line, " ")
	switch ev {
	case "start", AttestPass, AttestFail, AttestSkip:
	default:
		ok = false
	}
	if !ok {
		c.stray++
		return
	}
	if c.events[tok] == nil {
		if len(c.events) > 1<<16 {
			c.stray++
			return
		}
		c.events[tok] = map[string]bool{}
	}
	c.events[tok][ev] = true
}

// finish closes Öge's write end and reads what is left. A process the
// Check left behind can hold the pipe open; past a short grace, whatever
// it sends no longer counts.
func (c *channel) finish() { c.finished.Do(c.drain) }

func (c *channel) drain() {
	_ = c.w.Close()
	if c.r.SetReadDeadline(time.Now().Add(2*time.Second)) != nil {
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
		}
	}
	_ = c.r.Close()
	select { // a read Close can't unblock (no poller) is abandoned
	case <-c.done:
	case <-time.After(time.Second):
	}
}

// results is each expected test's disposition: what its tokens attested,
// with what the reports claimed beside it. Lines with a token unknown to
// this Check are counted as stray.
func (c *channel) results(a *attestation, expected []TestID, reports []*Report) ([]TestResult, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stray := c.stray
	byID := map[TestID][]string{}
	for tok, id := range a.tokens {
		byID[id] = append(byID[id], tok)
	}
	for tok := range c.events {
		if _, ok := a.tokens[tok]; !ok {
			stray++
		}
	}
	var out []TestResult
	for _, id := range expected {
		r := TestResult{TestID: id, Attested: AttestAbsent}
		toks := byID[id]
		for i, tok := range toks {
			d := disposition(c.events[tok])
			if i == 0 || worse(d, r.Attested) {
				r.Attested = d
			}
		}
		for _, rep := range reports {
			if o := rep.outcome(id); o != "" && (r.Report == "" || o == AttestFail || (o == AttestPass && r.Report == AttestSkip)) {
				r.Report = o
			}
		}
		out = append(out, r)
	}
	return out, stray
}

// disposition is one token's outcome across every command that ran it:
// any failure fails it, then any pass passes it.
func disposition(ev map[string]bool) string {
	switch {
	case ev[AttestFail]:
		return AttestFail
	case ev[AttestPass]:
		return AttestPass
	case ev[AttestSkip]:
		return AttestSkip
	case ev["start"]:
		return AttestExited
	}
	return AttestAbsent
}

// worse orders dispositions for a test declared in more than one file
// (only possible with no go.mod, where tests match by name alone).
func worse(a, b string) bool {
	rank := map[string]int{AttestFail: 4, AttestExited: 3, AttestAbsent: 2, AttestSkip: 1, AttestPass: 0}
	return rank[a] > rank[b]
}

// judge applies the attestation pass rule to a Check whose commands all
// passed: every expected test attested passing, or skipped where its
// Snapshot control skipped too (ADR-0020's baseline-relative skips). The
// control is consulted only when some test didn't attest passing, so the
// happy path never waits for it.
func judge(res *Result, control *Control) {
	need := false
	for _, t := range res.Tests {
		need = need || t.Attested != AttestPass
	}
	if !need {
		return
	}
	var snap map[TestID]string
	ctlWhy := "no Snapshot control"
	if control != nil {
		ctl, err := control.Wait()
		switch {
		case err != nil:
			ctlWhy = "the Snapshot control failed: " + short(err)
		case ctl.Setup != nil && !ctl.Setup.Pass:
			ctlWhy = "setup failed on the Snapshot control (" + ctl.Setup.Why + ")"
		default:
			snap = map[TestID]string{}
			for _, t := range ctl.Tests {
				d := t.Attested
				if d == AttestAbsent && t.Report != "" {
					d = AttestUnattested
				}
				snap[t.TestID] = d
			}
		}
	}
	var failed, infra []string
	for i := range res.Tests {
		t := &res.Tests[i]
		t.Snapshot = snap[t.TestID]
		name := t.TestID.String()
		switch t.Attested {
		case AttestPass:
		case AttestFail:
			failed = append(failed, name+" failed")
		case AttestSkip:
			switch {
			case snap == nil:
				infra = append(infra, name+" skipped, and "+ctlWhy)
			case t.Snapshot == AttestSkip:
				res.Skipped = append(res.Skipped, name)
			case t.Snapshot == AttestUnattested:
				infra = append(infra, name+" skipped, and attestation failed on the Snapshot control")
			case t.Snapshot == AttestAbsent:
				// TODO(#73-decision): a test that never ran on the Snapshot
				// (it didn't build there) grants no skip: the Candidate must
				// run it.
				failed = append(failed, name+" skipped, and never ran on the Snapshot")
			default:
				failed = append(failed, name+" skipped, but it ran on the Snapshot")
			}
		default: // exited or absent
			why := name + " never ran"
			if t.Attested == AttestExited {
				why = name + " started but never finished"
			}
			if t.Report != "" {
				why += "; the report claims " + t.Report
			}
			if t.Snapshot == AttestUnattested {
				infra = append(infra, why+", and attestation failed on the Snapshot control")
			} else {
				failed = append(failed, why)
			}
		}
	}
	switch {
	case len(failed) > 0:
		res.Pass = false
		res.Missing = failed
		res.Why = fmt.Sprintf("Oracle tests not attested passing (%d): %s", len(failed), list(failed))
	case len(infra) > 0:
		res.Pass = false
		res.Infra = "the attestation of Oracle tests failed on the Snapshot control: " + list(infra)
	}
}

func list(items []string) string {
	if len(items) > 5 {
		items = append(items[:5:5], "…")
	}
	return strings.Join(items, ", ")
}

// Control is the Snapshot control of one Oracle version (ADR-0020): the
// same protected tests, run on the immutable Snapshot with the same
// setup, environment policy and attestation as a Check. It runs in the
// background, overlapping the implementer's Attempt, and a Check consults
// it only for tests that didn't attest passing. Its own pass or fail is
// meaningless: the Snapshot is expected to fail the Task's tests.
type Control struct {
	Version int
	done    chan struct{}
	cancel  func()
	started time.Time
	ended   time.Time
	res     *Result
	err     error

	mu        sync.Mutex
	consulted bool
	waited    time.Duration
}

// StartControl starts the Snapshot control for m under root. r should
// carry the Run's seed: the control waits for its warm step like a Check.
func (r *Runner) StartControl(ctx context.Context, repo Repo, m *Manifest, snapshot, setup, root string) *Control {
	ctx, cancel := context.WithCancel(ctx)
	c := &Control{Version: m.Version, done: make(chan struct{}), cancel: cancel, started: time.Now()}
	// Niced: this deviates from "same environment" in scheduling
	// priority only, so the control never slows the Check or the agent
	// it overlaps. Setup, environment, overlay (test_config included) and
	// attestation are the Check's own.
	ctl := *r
	ctl.nice = true
	r = &ctl
	go func() {
		defer close(c.done)
		c.res, c.err = r.CheckAgainst(ctx, repo, m, snapshot, setup, root, nil)
		c.ended = time.Now()
	}()
	return c
}

// Wait waits for the control to finish and returns its result.
func (c *Control) Wait() (*Result, error) {
	start := time.Now()
	<-c.done
	c.mu.Lock()
	c.consulted = true
	c.waited += time.Since(start)
	c.mu.Unlock()
	if c.err == nil && c.res == nil {
		return nil, errors.New("no result")
	}
	return c.res, c.err
}

// ControlRecord is the Snapshot control's Evidence.
type ControlRecord struct {
	OracleVersion int `json:"oracle_version"`
	// Consulted is set when a Check needed it; WaitedMs is how long the
	// Verdict waited for it.
	Consulted bool  `json:"consulted"`
	WaitedMs  int64 `json:"waited_ms"`
	// Stopped is set when nothing needed it and it was still running.
	Stopped    bool    `json:"stopped,omitempty"`
	DurationMs int64   `json:"duration_ms,omitempty"`
	Result     *Result `json:"result,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// Stop ends the control, if it is still running, and returns its record.
func (c *Control) Stop() ControlRecord {
	if c == nil {
		return ControlRecord{}
	}
	stopped := false
	select {
	case <-c.done:
	default:
		stopped = true
		c.cancel()
		<-c.done
	}
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := ControlRecord{OracleVersion: c.Version, Consulted: c.consulted, WaitedMs: c.waited.Milliseconds(), Stopped: stopped}
	if !stopped {
		rec.DurationMs = c.ended.Sub(c.started).Milliseconds()
		rec.Result = c.res
		if c.err != nil {
			rec.Error = c.err.Error()
		}
	}
	return rec
}
