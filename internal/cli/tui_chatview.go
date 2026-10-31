package cli

import (
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/pipeline"
)

// chatView is one Chat's state in the home (docs/ux/desk.md, slice 2):
// its turn, its cuts, what it can deliver and its transcript. The home
// embeds the focused one, so h.turn is the focused Chat's turn; what is
// the whole terminal's (the composer, overlays, size, alerts) stays on
// the home.
type chatView struct {
	// chat is the Chat (#259): each message is its turn. nil runs each
	// message as a one-shot Run (an agent without auto mode, or
	// OGE_CHAT=0). turn is the turn in flight; verify is the newest cut
	// verifying in the background, while turns go on.
	chat *homeChat
	// adv is the Chat's Consultation (/advise); adviceEdit is the
	// Consultation whose advice the composer holds, edited: the next
	// message is the user's turn, and its AdviceSent says where it came
	// from.
	adv        *adviseState
	advSeq     int
	adviceEdit *adviceEditState
	turn       *chatTurn
	verify     *chatVerify
	cuts       int // the cuts started, each one's seq
	// unchecked: a turn changed something no cut covers yet (it was
	// interrupted or continued by queued messages); the next cut does.
	unchecked        bool
	uncheckedChanged int // largest known deferred or cancelled change count
	// stale is the delivery waiting on its stale-edit choice.
	stale *staleAsk
	// pendingFB is a failing cut's feedback turn, held under [chat] feedback
	// = "ask" until the human sends it back (s or /feedback). A newer cut or
	// a new turn of theirs drops it.
	pendingFB *pendingFeedback
	// cutGate is the newest cut's Gate, open at /apply or /branch;
	// gateFor is that delivery, made once the cut is Accepted.
	cutGate *gateState
	gateFor *staleAsk
	// cutFrozen is the newest cut's Pipeline; recut: a delivery refused
	// for a late Tamper event asks for the cut again.
	cutFrozen *pipeline.Frozen
	recut     bool
	// resetting: /new is closing the old Chat.
	resetting bool

	// thread is the Run the next message continues (#185): set when a Run
	// ends with a Candidate that wasn't applied, until /new, /clear or
	// /apply. nil: the next message is a fresh Task.
	thread *thread
	// last is the latest ended Run the human can take: /apply, /diff,
	// /receipt, /branch, or ↓ and a key. nil when there is none.
	last *lastRun
	// actions: ↓ moved the focus to the action row, whose keys act once
	// actionsArm has passed since actionsAt. Keys before were typed ahead,
	// never meant for it (ADR-0015: applying is an explicit act).
	actions   bool
	actionsAt time.Time

	// tr is the transcript (full screen); steps are its tool steps, to
	// hang a result on; side is the sidebar's view of the Chat.
	tr    *transcript
	steps []*toolStep
	side  sideState
	// scroll is what was printed above the program, for /export (#168).
	scroll []string
	// unsent is the composer's text while another Chat has the focus
	// (slice 3); unused while there is one Chat.
	unsent string
}

// focus makes v the Chat the home shows and the keys go to.
func (h *home) focus(v *chatView) { h.chatView = v }

// chatViewOf is the Chat a message is for: nil when it names none.
func chatViewOf(msg tea.Msg) *chatView {
	switch m := msg.(type) {
	case chatSentMsg:
		return m.view
	case chatEvMsg:
		return m.view
	case chatCutMsg:
		return m.view
	case chatGateMsg:
		return m.view
	case chatStepMsg:
		return m.view
	case verifyTickMsg:
		return m.view
	case chatClosedMsg:
		return m.view
	}
	return nil
}

// route makes a message's Chat the focused one while it is handled, and
// returns what puts the focus back. ok is false for a Chat the home no
// longer has: its message is dropped. A message that names no Chat is
// the focused one's.
//
// The handlers run as they do for the focused Chat, so the ones that
// touch the whole terminal (the composer, toast, notify, msg) do so for
// a Chat in the background too; slice 4 gates them.
func (h *home) route(msg tea.Msg) (restore func(), ok bool) {
	v := chatViewOf(msg)
	if v == nil || v == h.chatView {
		return func() {}, true
	}
	if !slices.Contains(h.views, v) {
		h.dropped++ // the home has no debug log to write it to
		return nil, false
	}
	prev := h.chatView
	h.focus(v)
	return func() { h.focus(prev) }, true
}
