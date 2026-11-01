package herdr

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeExec struct {
	mu    sync.Mutex
	calls [][]string
	block chan struct{} // when set, a call waits for it or its ctx
}

func (f *fakeExec) run(ctx context.Context, bin string, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{bin}, args...))
	b := f.block
	f.mu.Unlock()
	if b != nil {
		select {
		case <-b:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *fakeExec) got() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

func (f *fakeExec) waitFor(t *testing.T, n int) [][]string {
	t.Helper()
	for i := 0; i < 400; i++ {
		if c := f.got(); len(c) >= n {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("want %d calls, got %v", n, f.got())
	return nil
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNopOutsideHerdr(t *testing.T) {
	for _, m := range []map[string]string{
		nil,
		{"HERDR_BIN_PATH": "/b", "HERDR_PANE_ID": "p"},
		{"HERDR_ENV": "1", "HERDR_PANE_ID": "p"},
		{"HERDR_ENV": "1", "HERDR_BIN_PATH": "/b"},
	} {
		if _, ok := New(env(m)).(Nop); !ok {
			t.Errorf("%v: want Nop", m)
		}
	}
	if _, ok := New(env(map[string]string{"HERDR_ENV": "1", "HERDR_BIN_PATH": "/b", "HERDR_PANE_ID": "p"})).(Nop); ok {
		t.Error("inside herdr: want a real reporter")
	}
	Nop{}.Report(Blocked, "x")
	Nop{}.Release()
}

func TestArgvShape(t *testing.T) {
	f := &fakeExec{}
	r := newReporter("/bin/herdr", "pane-1", f.run, func() time.Time { return time.Unix(0, 1000) })
	r.Report(Blocked, "oge: waiting for you")
	c := f.waitFor(t, 1)[0]
	want := "/bin/herdr pane report-agent pane-1 --source oge --agent oge --state blocked --seq 1000 --message oge: waiting for you"
	if strings.Join(c, " ") != want {
		t.Errorf("argv = %q", c)
	}
	r.done.Wait()
	r.Report(Working, "")
	c = f.waitFor(t, 2)[1]
	want = "/bin/herdr pane report-agent pane-1 --source oge --agent oge --state working --seq 1001"
	if strings.Join(c, " ") != want {
		t.Errorf("argv = %q (seq must rise, no --message)", c)
	}
}

func TestSkipsRepeat(t *testing.T) {
	f := &fakeExec{}
	r := newReporter("b", "p", f.run, time.Now)
	r.Report(Working, "")
	r.done.Wait()
	r.Report(Working, "")
	r.Report(Working, "")
	r.done.Wait()
	if n := len(f.got()); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

func TestCoalescesToLatest(t *testing.T) {
	f := &fakeExec{block: make(chan struct{})}
	r := newReporter("b", "p", f.run, time.Now)
	r.Report(Working, "")
	f.waitFor(t, 1)
	r.Report(Blocked, "m1")
	r.Report(Idle, "")
	r.Report(Blocked, "oge: waiting for you")
	close(f.block)
	r.done.Wait()
	c := f.got()
	if len(c) != 2 {
		t.Fatalf("calls = %v, want first and only the latest", c)
	}
	if !strings.Contains(strings.Join(c[1], " "), "--state blocked") || !strings.Contains(strings.Join(c[1], " "), "waiting for you") {
		t.Errorf("second = %v", c[1])
	}
}

func TestHangingBinaryNeverBlocksReport(t *testing.T) {
	f := &fakeExec{block: make(chan struct{})} // never released: only ctx ends it
	r := newReporter("b", "p", f.run, time.Now)
	start := time.Now()
	for i := 0; i < 50; i++ {
		r.Report(State([]State{Idle, Working, Blocked}[i%3]), "")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("Report blocked for %v", d)
	}
	r.done.Wait() // every call ends at the timeout, so this ends too
}

func TestReleaseOnExit(t *testing.T) {
	f := &fakeExec{}
	r := newReporter("/h", "p", f.run, time.Now)
	r.Report(Working, "")
	r.done.Wait()
	r.Release()
	c := f.got()
	if len(c) != 2 {
		t.Fatalf("calls = %v", c)
	}
	last := strings.Join(c[1], " ")
	if !strings.HasPrefix(last, "/h pane release-agent p --source oge --agent oge --seq ") || strings.Contains(last, "--state") {
		t.Errorf("release argv = %q", last)
	}
	r.Report(Idle, "")
	r.Release()
	r.done.Wait()
	if len(f.got()) != 2 {
		t.Error("reports after Release must be dropped, and Release is once")
	}
}

func TestReleaseBoundedByTimeout(t *testing.T) {
	f := &fakeExec{block: make(chan struct{})}
	r := newReporter("b", "p", f.run, time.Now)
	start := time.Now()
	r.Release()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Release took %v", d)
	}
}

// The only messages Öge sends are the fixed ones it passes in; the reporter
// adds none of its own and a message-less state carries no --message.
func TestNoMessageUnlessGiven(t *testing.T) {
	f := &fakeExec{}
	r := newReporter("b", "p", f.run, time.Now)
	r.Report(Idle, "")
	r.done.Wait()
	for _, a := range f.got()[0] {
		if a == "--message" {
			t.Error("idle carried a message")
		}
	}
}
