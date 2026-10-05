package claude

import (
	"encoding/json"

	"github.com/erengun/oge/internal/agent"
)

// frictionMeter measures one turn's policy friction (ADR-0019, #90).
//
//   - Denied counts the Host requests Öge denies, refusals before the
//     envelope check passes included.
//   - A recovery turn is each model turn after a denial, up to and
//     including the next turn whose tool use is allowed or differs from
//     the denied one. A model turn is one assistant message, by its id.
//     A turn that only talks counts, and so does a final answer that
//     follows a denial with no tool use in between.
//
// The friction count is Denied + RecoveryTurns, so one denial recovered
// from on the very next turn counts 2. The session's reader is its only
// caller, with s.mu held.
type frictionMeter struct {
	f          agent.Friction
	recovering bool
	denied     string // the denied tool use, as useKey
	msg        string // the current assistant message's id
}

// message is an assistant frame of the message with this id. Frames of
// one message share its id; a frame without one is a turn of its own.
func (m *frictionMeter) message(id string) {
	if id != "" && id == m.msg {
		return
	}
	m.msg = id
	if m.recovering {
		m.f.RecoveryTurns++
	}
}

// toolUse is a tool use the model asked for: a different one than the
// denied one ends the recovery.
func (m *frictionMeter) toolUse(key string) {
	if m.recovering && key != m.denied {
		m.recovering = false
	}
}

// decided is how Öge answered a tool use.
func (m *frictionMeter) decided(key string, d agent.HostDecision) {
	switch d.Decision {
	case "deny":
		m.f.Denied++
		m.recovering, m.denied = true, key
	case "allow":
		m.recovering = false
	}
}

// take returns the turn's friction and starts the next turn's at zero.
func (m *frictionMeter) take() *agent.Friction {
	f := m.f
	*m = frictionMeter{}
	return &f
}

// useKey identifies a tool use by its tool and input, whichever frame
// carries it.
func useKey(tool string, input json.RawMessage) string {
	var v any
	if json.Unmarshal(input, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			input = b
		}
	}
	return tool + "\x00" + string(input)
}
