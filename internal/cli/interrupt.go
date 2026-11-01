package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// hangupSignal is the terminal closing.
var hangupSignal os.Signal = syscall.SIGHUP

// ExitInterrupted is the exit code when Öge stops without waiting for the
// Run to end.
// TODO(#79-decision): 130, the shell's code for death by SIGINT; ADR-0015
// reserves it for interrupted Runs. A cancel the Run does wait for still
// ends as an Infrastructure stop (11) until ADR-0012's interrupted status.
const ExitInterrupted = 130

// errForced means a second interrupt stopped Öge before the Run ended.
var errForced = errors.New("stopped without waiting for the Run")

// interrupts counts Ctrl-C presses and termination signals. The first
// cancels the Run, which then ends and shows its summary. The second stops
// Öge without waiting, for a Run that won't stop.
type interrupts struct {
	cancel context.CancelFunc
	forced chan struct{}
	mu     sync.Mutex
	n      int
	// diverted, when set, takes the next interrupt instead: Ctrl-C at a
	// Gate's reason prompt returns to the Gate.
	diverted func()
	// forward, when set, takes every interrupt instead: the home's session
	// passes them to the Run it started.
	forward *interrupts
	// hangup, when set, runs once on the first SIGHUP, before that
	// interrupt: the terminal is gone.
	hangup  func()
	hungUp  bool
	stopped bool // ended by a signal rather than by the human
	ended   bool // a hangup or SIGTERM came: Öge is going, not just interrupted
	// exit, when set, runs on a hangup or SIGTERM before that interrupt
	// is passed on: Öge is exiting, so a Gate waiting in a forwarded Run
	// parks rather than being abandoned by the Run's cancel.
	exit func()
}

// forwardTo sends interrupts to r until it is called with nil.
func (i *interrupts) forwardTo(r *interrupts) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.forward = r
}

// divert sends interrupts to f until it is called with nil.
func (i *interrupts) divert(f func()) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.diverted = f
}

func newInterrupts(cancel context.CancelFunc) *interrupts {
	return &interrupts{cancel: cancel, forced: make(chan struct{})}
}

func (i *interrupts) interrupt() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if r := i.forward; r != nil {
		r.interrupt()
		return
	}
	if f := i.diverted; f != nil && i.n == 0 {
		i.diverted = nil
		f()
		return
	}
	i.n++
	switch i.n {
	case 1:
		i.cancel()
	case 2:
		close(i.forced)
	}
}

// watch turns sigs into interrupts until the returned stop is called.
func (i *interrupts) watch(sigs ...os.Signal) (stop func()) {
	ch := make(chan os.Signal, 2)
	done := make(chan struct{})
	signal.Notify(ch, sigs...)
	go func() {
		for {
			select {
			case sig := <-ch:
				i.signalled(sig)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// signalled is interrupt for a signal: a hangup first runs i.hangup.
func (i *interrupts) signalled(sig os.Signal) {
	exiting := sig == hangupSignal || sig == syscall.SIGTERM
	i.mu.Lock()
	i.stopped = true
	i.ended = i.ended || exiting
	f := i.hangup
	if sig != hangupSignal || i.hungUp {
		f = nil
	}
	if sig == hangupSignal {
		i.hungUp = true
	}
	exit := i.exit
	i.mu.Unlock()
	if f != nil {
		f()
	}
	if exiting && exit != nil {
		exit()
	}
	i.interrupt()
	if exiting {
		// Öge is exiting: a hangup leaves no terminal to come back to, and
		// SIGTERM asks Öge to end. Either ends the home even while
		// interrupts go to a Run or an editor (forwarded or diverted).
		i.cancel()
	}
}

// why is how the session was ended by a signal: "hangup", "interrupt",
// or "" when no signal came.
func (i *interrupts) why() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	switch {
	case i.hungUp:
		return "hangup"
	case i.stopped:
		return "interrupt"
	}
	return ""
}

// terminated reports whether a hangup or SIGTERM ended the session.
func (i *interrupts) terminated() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.ended
}

// hangUp ends the session as a SIGHUP would, for a terminal read that
// failed with EIO: it runs i.hangup unless a SIGHUP already did, then
// cancels without counting an interrupt, so a late SIGHUP doesn't force.
func (i *interrupts) hangUp() {
	i.mu.Lock()
	f := i.hangup
	if i.hungUp {
		f = nil
	}
	i.hungUp, i.stopped, i.ended = true, true, true
	exit := i.exit
	i.mu.Unlock()
	if f != nil {
		f()
	}
	if exit != nil {
		exit()
	}
	i.cancel()
}
