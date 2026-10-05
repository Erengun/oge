package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
)

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
			case <-ch:
				i.interrupt()
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
