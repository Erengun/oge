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
	RunID     string
	// RepoInstructions is the Snapshot's agent instruction file (CLAUDE.md),
	// read by Öge from the Snapshot, never from the Workspace (ADR-0009).
	RepoInstructions string
	// CheckCommands are the Run's Check commands, which a Launch profile
	// may pre-authorise the implementer to run (ADR-0019).
	CheckCommands []string
	// DenyRead are absolute paths no agent may read: Öge's private state.
	DenyRead []string
	// Cache is a Run-private directory, outside the Workspace, for the
	// agent's tool caches (such as GOCACHE).
	Cache string
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
	// Text is a Claim's or Warning's text, or an Unknown message's type.
	Text string
	// Tool and Target are a tool-use Claim's tool name and a short,
	// redacted target, such as a Workspace-relative path or a command's
	// first line. Tool is empty for any other Claim.
	Tool   string `json:",omitempty"`
	Target string `json:",omitempty"`
	// Host is a Host request and how it was answered.
	Host *HostDecision `json:",omitempty"`
	// Session is set on SessionOpened when the agent reports it.
	Session *SessionInfo `json:",omitempty"`
	// Exit is the Exit a settled turn declared, from the Role kind's closed
	// set (e.g. "done"); empty when none was declared.
	Exit string
	// Failure is set on a settled turn that did not complete: an Attempt
	// failure reason code.
	Failure string
	// Stop is set instead of Failure when the turn ended for an
	// environmental reason the agent signalled in structured form, such as
	// authentication or quota: an Infrastructure stop, not an Attempt
	// failure (ADR-0012).
	Stop string `json:",omitempty"`
	// Friction is set on a settled turn by an adapter that measures
	// policy friction (ADR-0019); nil when it doesn't.
	Friction *Friction `json:",omitempty"`
}

// Friction is a turn's policy friction (ADR-0019, #90): the Host requests
// Öge denied, and the model turns spent recovering from them.
type Friction struct {
	Denied        int
	RecoveryTurns int
}

// Turns is the friction count: denied requests plus recovery turns.
func (f Friction) Turns() int { return f.Denied + f.RecoveryTurns }

// Host-request families (ADR-0005).
const (
	Approval = "approval"
	Question = "question"
)

// HostDecision is a Host request and its answer. Until interactive
// answers exist (#45) every one is answered by policy: pre-authorised by
// the Launch profile, or denied with a reason. A question is never
// answered on the human's behalf (ADR-0019).
type HostDecision struct {
	Family string // Approval or Question
	Tool   string
	Target string // short and redacted, as on a tool-use Claim
	// Decision is "allow", "deny" or "cancel".
	Decision string
	Reason   string `json:",omitempty"`
	// By is who answered, e.g. "launch_profile:<hash>" or "policy".
	By string
	// Rule names the policy rule that decided, e.g. "pre_authorised",
	// "outside_role" or "no_interactive_approval".
	Rule string
}

// SessionInfo is what the agent reported about its Session at startup.
type SessionInfo struct {
	AgentVersion string
	// Capabilities are the effective Capabilities (ADR-0005).
	Capabilities []string
	// Profile is the Launch profile's hash.
	Profile string
	// Envelope is "ok" or "warn"; a failed envelope settles the turn.
	Envelope string
	// AuthSource is the kind of authentication the agent reports, never
	// a credential (ADR-0006).
	AuthSource string `json:",omitempty"`
	// Residue is what an isolated launch still loads and the user can't
	// remove from it, such as organisation-managed plugins and skills.
	Residue *Residue `json:",omitempty"`
}

// Residue is an agent's known startup residue (#44): recorded in the
// envelope Evidence, shown once when it first appears or changes.
type Residue struct {
	Plugins, Skills, Agents []string
	// Fingerprint identifies the set, to notice a change.
	Fingerprint string
}

// ErrTurnInFlight is returned by Send while a turn is active.
var ErrTurnInFlight = errors.New("a turn is already in flight")
