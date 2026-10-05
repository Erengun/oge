// Package agent is the session-level adapter seam every agent sits behind
// (ADR-0005). The orchestrator branches only on the normalised events
// defined here; process topology stays inside each adapter.
package agent

import (
	"context"
	"errors"
)

// Adapter opens Sessions for one agent.
type Adapter interface {
	Open(ctx context.Context, spec LaunchSpec) (Session, error)
}

// LaunchSpec is what a Session is opened with.
type LaunchSpec struct {
	Role      string // Role kind, e.g. "implementer"
	Model     string
	Workspace string // the directory the agent works in
	Network   string // "off" or "on"
}

// Session is one agent conversation. At most one turn is in flight;
// Interrupt is non-terminal.
type Session interface {
	// Process is the agent process the Session runs, known once Open
	// returns. It is the zero value when nothing was spawned.
	Process() Process
	Send(t Turn) error
	// Events delivers normalised events and is closed after Close.
	Events() <-chan Event
	Interrupt() error
	Close() error
}

// Process identifies a spawned agent process (ADR-0012).
type Process struct {
	PID  int
	PGID int
}

// Turn is one message to the agent.
type Turn struct {
	Text string
}

// EventKind is the closed set of normalised events (ADR-0005).
type EventKind string

const (
	SessionOpened EventKind = "session_opened"
	TurnAccepted  EventKind = "turn_accepted"
	TurnSettled   EventKind = "turn_settled"
	HostRequest   EventKind = "host_request"
	Claim         EventKind = "claim"
	Usage         EventKind = "usage"
	Warning       EventKind = "warning"
	Unknown       EventKind = "unknown"
)

// Event is one normalised event.
type Event struct {
	Kind EventKind
	// Text is a Claim's or Warning's text.
	Text string
	// Exit is the Exit a settled turn declared, from the Role kind's closed
	// set (e.g. "done"); empty when none was declared.
	Exit string
	// Failure is set on a settled turn that did not complete: an Attempt
	// failure reason code.
	Failure string
}

// ErrTurnInFlight is returned by Send while a turn is active.
var ErrTurnInFlight = errors.New("a turn is already in flight")
