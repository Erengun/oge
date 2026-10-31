// Package herdr tells herdr (github.com/herdrdev/herdr) what an Öge pane is
// doing. herdr reads agent state off the screen, and Öge drives its agents
// headlessly, so without a report an Öge pane is a plain terminal to it.
// Reporting is optional, fire-and-forget and never blocks the UI: a missing,
// slow or failing herdr binary changes nothing for the human.
package herdr

import (
	"context"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// State is what herdr shows for the pane.
type State string

const (
	Idle    State = "idle"
	Working State = "working"
	Blocked State = "blocked"
)

// timeout bounds each herdr call. A hanging binary is killed after this.
const timeout = 500 * time.Millisecond

const name = "oge"

// Reporter reports the pane's state. Messages must be Öge's own fixed
// texts, never agent, Candidate or Task text.
type Reporter interface {
	Report(state State, message string)
	// Release clears the pane's agent in herdr. Call it once, at exit.
	Release()
}

// Active reports whether the process runs in a herdr pane.
func Active(getenv func(string) string) bool {
	return getenv("HERDR_ENV") == "1" && getenv("HERDR_BIN_PATH") != "" && getenv("HERDR_PANE_ID") != ""
}

// New returns a reporter for the herdr pane in getenv, or a no-op outside herdr.
func New(getenv func(string) string) Reporter {
	if !Active(getenv) {
		return Nop{}
	}
	return newReporter(getenv("HERDR_BIN_PATH"), getenv("HERDR_PANE_ID"), runExec, time.Now)
}

// Nop does nothing.
type Nop struct{}

func (Nop) Report(State, string) {}
func (Nop) Release()             {}

// execFunc runs bin with args, bounded by ctx. Tests replace it.
type execFunc func(ctx context.Context, bin string, args ...string) error

func runExec(ctx context.Context, bin string, args ...string) error {
	return exec.CommandContext(ctx, bin, args...).Run() // no shell
}

type report struct {
	state State
	msg   string
}

type reporter struct {
	bin, pane string
	run       execFunc
	now       func() time.Time

	mu       sync.Mutex
	inflight bool
	pending  *report
	last     *report // the last report accepted for sending
	seq      int64
	released bool
	done     sync.WaitGroup
}

func newReporter(bin, pane string, run execFunc, now func() time.Time) *reporter {
	return &reporter{bin: bin, pane: pane, run: run, now: now}
}

// nextSeq is a strictly increasing unix-nanos number. Callers hold mu.
func (r *reporter) nextSeq() int64 {
	n := r.now().UnixNano()
	if n <= r.seq {
		n = r.seq + 1
	}
	r.seq = n
	return n
}

func (r *reporter) Report(state State, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return
	}
	next := report{state, message}
	if r.last != nil && *r.last == next {
		return
	}
	r.last = &next
	if r.inflight {
		r.pending = &next // only the latest waits
		return
	}
	r.inflight = true
	seq := r.nextSeq()
	r.done.Add(1)
	go r.drain(next, seq)
}

// drain sends rep, then whatever is pending, one at a time.
func (r *reporter) drain(rep report, seq int64) {
	defer r.done.Done()
	for {
		r.send(rep, seq)
		r.mu.Lock()
		if r.pending == nil {
			r.inflight = false
			r.mu.Unlock()
			return
		}
		rep, r.pending = *r.pending, nil
		seq = r.nextSeq()
		r.mu.Unlock()
	}
}

func (r *reporter) send(rep report, seq int64) {
	args := []string{"pane", "report-agent", r.pane, "--source", name, "--agent", name,
		"--state", string(rep.state), "--seq", strconv.FormatInt(seq, 10)}
	if rep.msg != "" {
		args = append(args, "--message", rep.msg)
	}
	r.call(args)
}

func (r *reporter) call(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = r.run(ctx, r.bin, args...) // failures are ignored: herdr is optional
}

// Release runs synchronously, bounded by the timeout, so it never holds the
// exit for long. Later reports are dropped.
func (r *reporter) Release() {
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return
	}
	r.released = true
	r.pending = nil
	seq := r.nextSeq()
	r.mu.Unlock()
	r.call([]string{"pane", "release-agent", r.pane, "--source", name, "--agent", name,
		"--seq", strconv.FormatInt(seq, 10)})
}
