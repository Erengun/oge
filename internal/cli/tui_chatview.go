package cli

import (
	"slices"
	"strings"
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
	// num is the Chat's number for alt+N, the rail and /chats: one more
	// than the highest when it opened, kept for its whole life, so closing
	// another Chat never renumbers it. name is its first message's title;
	// "" until then.
	num  int
	name string
	// comp is the Chat's composer: its draft, undo and pastes stay with it
	// while another Chat has the focus, and a message for this Chat that
	// puts text back in the box puts it in this one.
	comp *composer

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
}

// focus makes v the Chat the home shows and the keys go to.
func (h *home) focus(v *chatView) { h.chatView = v }

// newView is a fresh Chat on the session's config: empty, numbered after
// the others, with a Chat of its own to open on its first message.
func (h *home) newView() *chatView {
	n := 0
	for _, v := range h.views {
		n = max(n, v.num)
	}
	v := &chatView{num: n + 1, comp: h.composer(), tr: &transcript{}, chat: h.chat.clone()}
	v.side.frozen = h.side.frozen
	return v
}

// switchTo is the human moving to another Chat (/new, /close, /chats,
// alt+N, a rail click): what is half done in the home's overlays and
// history walk ends, and v's own draft and scroll come back with it. A
// walk through the history gives the box back its draft first, and what
// is said under the box, or held for ↑ after a Ctrl+C, was this Chat's.
func (h *home) switchTo(v *chatView) {
	if v == h.chatView {
		return
	}
	if h.navigating {
		h.comp.set(h.draft)
	}
	h.navigating, h.visual = false, 0
	h.msg, h.note, h.cleared = nil, "", ""
	h.focus(v)
	if h.full {
		h.layout()
	}
}

// viewNum is the open Chat numbered n, nil when there is none.
func (h *home) viewNum(n int) *chatView {
	for _, v := range h.views {
		if v.num == n {
			return v
		}
	}
	return nil
}

// label is how the Chat is named in the rail and /chats: its first
// message's title, "new" before one.
func (v *chatView) caption() string {
	if v.name == "" {
		return "new"
	}
	return v.name
}

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
	case chatDiffMsg:
		return m.view
	case diffEntryMsg:
		return m.view
	case sideChangesMsg:
		return m.view
	case adviceMsg:
		return m.view
	case adviceTickMsg:
		return m.view
	case askAdviceMsg:
		return m.view
	case askAdviceTickMsg:
		return m.view
	}
	return nil
}

// route makes a message's Chat the focused one while it is handled, and
// returns what puts the focus back. ok is false for a Chat the home no
// longer has: its message is dropped. A message that names no Chat is
// the focused one's.
//
// The handlers run as they do for the focused Chat, with h.background
// set: what is the whole terminal's (msg, the toast, quitting, printing
// above the program) they must leave alone then. The composer is the
// Chat's own (chatView.comp), so text a handler puts back in it waits
// there.
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
	h.background = true
	return func() {
		h.background = false
		h.focus(prev)
	}, true
}

// problem says what went wrong with the Chat: under the box for the
// focused one, in its transcript for one in the background, whose lines
// must never land under another Chat's box.
func (h *home) problem(lines ...string) tea.Cmd {
	if !h.background {
		h.msg = append(h.msg, lines...)
		return nil
	}
	for i, l := range lines {
		lines[i] = h.ogeLine(strings.TrimPrefix(l, "oge: "))
	}
	return h.println(strings.Join(lines, "\n"))
}
