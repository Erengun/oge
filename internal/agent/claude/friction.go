package claude

import "github.com/erengun/oge/internal/agent"

// frictionMeter measures one turn's policy friction (ADR-0019, #90).
//
//   - Denied counts the tool requests the policy denies.
//   - LostTurns counts the model turns lost to policy. A model turn is one
//     assistant message, by its id; it is lost when it holds at least one
//     tool request Öge denied and none Öge allowed. The retry turn that
//     succeeds isn't lost, and neither is a turn that only talks.
//   - EnvelopeRefusals counts requests refused before the startup envelope
//     passed. They come from timing, not policy, so they are in neither
//     count above.
//
// A request's decision belongs to the latest assistant message: claude
// sends a tool use before asking about it. The session's reader is the
// only caller, with s.mu held.
type frictionMeter struct {
	f       agent.Friction
	msg     string // the current assistant message's id
	started bool   // a message is current
	denied  bool   // the current message had a request denied
	allowed bool   // the current message had a request allowed
}

// message is an assistant frame of the message with this id. Frames of
// one message share its id; a frame without one is a turn of its own.
func (m *frictionMeter) message(id string) {
	if m.started && id != "" && id == m.msg {
		return
	}
	m.endTurn()
	m.msg, m.started = id, true
}

func (m *frictionMeter) endTurn() {
	if m.denied && !m.allowed {
		m.f.LostTurns++
	}
	m.denied, m.allowed = false, false
}

// decided is how the policy answered a tool request.
func (m *frictionMeter) decided(d agent.HostDecision) {
	switch d.Decision {
	case "deny":
		m.f.Denied++
		m.denied = true
	case "allow":
		m.allowed = true
	}
}

// refused is a request refused before the envelope passed.
func (m *frictionMeter) refused() { m.f.EnvelopeRefusals++ }

// take returns the turn's friction and starts the next turn's at zero.
func (m *frictionMeter) take() *agent.Friction {
	m.endTurn()
	f := m.f
	*m = frictionMeter{}
	return &f
}
