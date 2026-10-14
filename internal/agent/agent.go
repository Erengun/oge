// Package agent is the session-level adapter seam every agent sits behind
// (ADR-0005). The orchestrator branches only on the normalised events
// defined here; process topology stays inside each adapter.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Adapter opens Sessions for one agent.
type Adapter interface {
	Open(ctx context.Context, spec LaunchSpec) (Session, error)
}

// LaunchSpec is what a Session is opened with.
type LaunchSpec struct {
	Role  string // Role kind, e.g. "implementer"
	Model string
	// Effort is the thinking/effort level; "" is the agent's default. The
	// Claude adapter passes it as --effort and hashes it into the Launch
	// profile (#179).
	Effort string
	// Profile names the agent's config profile ("work" for claude@work);
	// empty is the default. Never a path (ADR-0006).
	Profile   string
	Workspace string // the directory the agent works in
	Network   string // "off" or "on"
	RunID     string
	// RepoInstructions is the Snapshot's agent instruction file (CLAUDE.md),
	// read by Öge from the Snapshot, never from the Workspace (ADR-0009).
	RepoInstructions string
	// CheckCommands are the Run's Check commands, which a Launch profile
	// may pre-authorise the implementer to run (ADR-0019).
	CheckCommands []string
	// WriteGlobs, when set, limit the Session to creating new files that
	// match them: the verifier's Write scope (spec #35). Unset, the role
	// may write anywhere in its Workspace.
	WriteGlobs []string
	// DenyRead are absolute paths no agent may read: Öge's private state.
	DenyRead []string
	// Cache is a Run-private directory, outside the Workspace, for the
	// agent's tool caches (such as GOCACHE).
	Cache string
	// Permissions is who decides a tool call: "" is Öge's Launch-profile
	// policy; AutoPermissions is the agent's own auto mode under Öge's
	// hard denies, with its asks routed to the host (Chat, D2 in
	// docs/ux/chat-first.md).
	Permissions string
	// StateRoots are Öge's state directories (the state dir, its private
	// and work roots), wherever they are. Under AutoPermissions the
	// sandbox denies reading them, reopening only the Workspace and cache.
	StateRoots []string
}

// AutoPermissions is LaunchSpec.Permissions for the agent's auto mode.
const AutoPermissions = "auto"

// AutoMode is an Adapter whose agent has its own auto permission mode,
// which a Chat runs under (D2). The bare oge home opens a Chat only on
// one; an agent without it runs each message as a one-shot Run under its
// Launch profile.
type AutoMode interface {
	Adapter
	AutoMode() bool
}

// HasAutoMode reports whether a has an auto permission mode.
func HasAutoMode(a Adapter) bool {
	m, ok := a.(AutoMode)
	return ok && m.AutoMode()
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
	// Friction is set by an adapter that measures policy friction
	// (ADR-0019): on a settled turn, the turn's; on a Host request, the
	// turn's so far, for a turn that never settles. nil when it doesn't.
	Friction *Friction `json:",omitempty"`
	// CostUSD and Tokens are what the agent reported for its session so
	// far, on a Usage event; nil when it reported none. Never estimated.
	CostUSD *float64 `json:",omitempty"`
	Tokens  *int     `json:",omitempty"`
	// Context is how full the agent's context was after its last message,
	// on a Usage event, when the agent reported both numbers: display
	// only, never written to the stream or Ledger.
	Context *ContextUse `json:"-"`
	// Todos is the agent's plan after a todo update (a Claim with Tool
	// "TodoWrite"): display only, never written to the stream or Ledger.
	Todos []Todo `json:"-"`
	// Ask is set on a Host request the agent's auto mode raised for the
	// human (Host.Decision "ask"): the turn waits for its Reply. On a Host
	// request with Decision "cancel" it is the Ask the agent withdrew.
	// Never written to the stream or Ledger.
	Ask *Ask `json:"-"`
	// Scope is a Chat turn's scope result, on its TurnSettled event: set by
	// the Chat, never by an adapter, and never written to the stream.
	Scope *TurnScope `json:"-"`
}

// TurnScope is what a Chat turn changed in the Workspace, as a judged role
// would see it (ADR-0009): the paths it changed, Promoted, Ambiguous or
// Excluded against the Snapshot, and the protected paths it wrote, which
// were reverted and recorded as Tamper events.
type TurnScope struct {
	Turn                          int
	Promoted, Ambiguous, Excluded []string
	Tamper                        []string
}

// Ask is a permission prompt waiting for the host's answer.
type Ask struct {
	Tool, Target string
	// Detail is the whole of what is asked about, exactly as "yes" would
	// approve it: the raw input, never redacted or clipped, made visible
	// by Visible. It is shown live and never printed or logged. An
	// adapter denies, without asking, what is empty or longer than
	// AskMaxLines lines.
	Detail string
	// Questions are an AskUserQuestion's questions, answered with text,
	// one answer per question.
	Questions []AskQuestion
	reply     func(Answer) error
}

// AskMaxLines is the most lines an Ask's Detail may take: a longer one is
// denied without asking, since consent to text the human can't see isn't
// consent.
const AskMaxLines = 8

// AskQuestion is one question the agent asks the human, with its options.
type AskQuestion struct {
	Text    string
	Options []string
}

// Answer is the host's answer to an Ask. Answers answer its Questions,
// in order; Reason is why a denial was made, for the agent.
type Answer struct {
	Allow   bool
	Answers []string
	Reason  string
}

// Visible makes every character of s show: newlines stay, a tab is \t, a
// carriage return \r, another control character \x.., and a bidi
// override or isolate or a zero-width character \u{...}. Nothing is
// dropped, so what is shown is what was sent.
func Visible(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		raw := s[:n]
		s = s[n:]
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, raw[0]) // a byte that isn't UTF-8
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029:
			// Format characters (bidi controls, zero-width, soft hyphen, …)
			// are invisible or reorder text: show them.
			fmt.Fprintf(&b, `\u{%x}`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NewAsk returns an Ask that reply answers; adapters build it.
func NewAsk(tool, target, detail string, reply func(Answer) error) *Ask {
	return &Ask{Tool: tool, Target: target, Detail: detail, reply: reply}
}

// Reply answers the ask yes or no; only the first answer counts.
func (a *Ask) Reply(allow bool) error { return a.reply(Answer{Allow: allow}) }

// Answer answers the ask with ans; only the first answer counts.
func (a *Ask) Answer(ans Answer) error { return a.reply(ans) }

// ContextUse is the context an agent's last message used (input, cache read
// and cache creation tokens) against the window the agent reported for its
// model. Both are reported, never guessed.
type ContextUse struct {
	Used, Window int
}

// Percent is Used as a share of Window, 0 to 100.
func (c ContextUse) Percent() int {
	if c.Window <= 0 {
		return 0
	}
	return min(max(c.Used*100/c.Window, 0), 100)
}

// Todo is one item of an agent's plan; Text is redacted but not yet cleaned
// for display.
type Todo struct {
	Text   string
	Status string // "pending", "in_progress" or "completed"
	// Active is the present-tense form of an in-progress item.
	Active string
}

// Friction is a turn's policy friction (ADR-0019, #90).
type Friction struct {
	// Denied counts the tool requests the policy denied.
	Denied int
	// LostTurns counts the model turns lost to policy: those with a
	// denied tool request and no allowed one.
	LostTurns int
	// EnvelopeRefusals counts requests refused because the startup
	// envelope hadn't passed: timing, not policy, so in neither count.
	EnvelopeRefusals int
}

// SumFriction is a and b added up; nil when both are.
func SumFriction(a, b *Friction) *Friction {
	if a == nil && b == nil {
		return nil
	}
	var s Friction
	for _, f := range []*Friction{a, b} {
		if f != nil {
			s.Denied += f.Denied
			s.LostTurns += f.LostTurns
			s.EnvelopeRefusals += f.EnvelopeRefusals
		}
	}
	return &s
}

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
	// Hint is a denial's one-line recovery hint, also in Reason (#90).
	Hint string `json:",omitempty"`
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
