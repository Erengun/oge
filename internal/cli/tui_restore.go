package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/run"
)

// Restore on the home (docs/ux/away.md, build slice 4): bare oge with
// open Chats in desk.log draws them again, each with its transcript and
// the state its records show. No agent starts and no Session opens: a
// restored Chat is read-only until reopening on focus lands, and a
// message to it says so rather than opening a fresh Chat in its place.

// restoredView is a Chat restored from the Desk record: what its records
// said at startup, and the Desk to record its closing in.
type restoredView struct {
	*run.RestoredChat
	desk *run.Desk
}

// mark is the Chat's glyph and coarse word. A ✓ the human has seen
// (seen) is idle: the Desk record's Seen, or a look since.
func (r *restoredView) mark(seen bool) (mark, coarse string) {
	word := func(def string) string {
		if r.Word != "" {
			return r.Word
		}
		return def
	}
	switch r.State {
	case run.RestoredParked:
		return "▲", "parked"
	case run.RestoredFailed:
		return "✗", "check failed"
	case run.RestoredDone:
		if seen {
			return "○", "idle"
		}
		return "✓", "done"
	case run.RestoredStopped:
		return "○", "stopped"
	case run.RestoredUnknown:
		return "?", "unknown"
	}
	return "○", word("idle")
}

// stopped reports whether the Chat is one the rail keeps in its own row:
// a stopped Chat is never folded in with the idle ones.
func stoppedMark(mark, coarse string) bool { return mark == "○" && coarse == "stopped" }

// restoreViews builds the home's Chats from d, one per restored Chat,
// each in its own repo. A repo that is gone, or that can't be opened,
// still shows its Chat: its records are Öge's own.
func (h *home) restoreViews(d *run.RestoredDesk, desk *run.Desk, repoFor func(string) (*repoCtx, []string)) []*chatView {
	var views []*chatView
	for _, c := range d.Chats {
		rc := repoGone(c.Repo)
		gone := true
		if _, err := os.Stat(c.Repo); err == nil && repoFor != nil {
			if r, _ := repoFor(c.Repo); r != nil {
				rc, gone = r, false
			}
		}
		v := &chatView{num: c.N, name: c.Title, comp: newComposer(), tr: &transcript{}, repoCtx: rc, chat: rc.hc.clone()}
		if rc.sel != nil {
			v.side.frozen = rc.sel.lastFrozen
		}
		v.rest = &restoredView{RestoredChat: c, desk: desk}
		v.doneAt = c.Since
		if c.State == run.RestoredDone || c.Seen {
			v.side.outcome, v.doneRun = run.Accepted, c.Run
			v.seen = c.Seen
			if c.Seen {
				v.seenRun = c.Run
			}
		}
		h.place(rc.root)
		// What the records show is no news: no toast, no push, no ring.
		v.attnMark, _ = glyph(v, h.clock(), h.quietAfter())
		h.restoreTranscript(v, gone)
		views = append(views, v)
	}
	return views
}

// repoGone is the stand-in for a repo that can't be opened: it names the
// directory and holds nothing else, so a Chat of it can only be read.
func repoGone(root string) *repoCtx {
	return &repoCtx{root: root, title: filepath.Base(root) + " · not found",
		resolve: func(*setupState) (*resolved, []string) {
			return nil, []string{"the repo " + clean(root) + " isn't there"}
		}}
}

// restoreTranscript fills v's transcript from its turns, then says how the
// Chat stands. It prints through the home's own blocks, with v as the
// focused Chat while it does.
func (h *home) restoreTranscript(v *chatView, gone bool) {
	prev := h.chatView
	h.chatView = v
	defer func() { h.chatView = prev }()
	r := v.rest
	say := func(text string) { h.println(h.ogeLine(text)) }
	for _, t := range r.Turns {
		switch {
		case t.Oge:
			say(h.st.dim(fmt.Sprintf("turn %d was Öge's: sent for a failing Check or an advisor", t.N)))
		case t.Text != "":
			h.userEntry(clean(t.Text))
		default:
			say(h.st.dim(fmt.Sprintf("turn %d: its message isn't in the log", t.N)))
		}
		if t.Reply != "" {
			h.replyEntry(t.Reply)
		}
		switch {
		case !t.Settled:
			say(h.st.dim(fmt.Sprintf("turn %d had no clean end", t.N)))
		case t.Failure != "":
			say(h.st.dim(fmt.Sprintf("turn %d ended: %s", t.N, clean(t.Failure))))
		}
	}
	if gone {
		say("■ the repo " + clean(r.Repo) + " isn't there: this Chat can only be read")
	}
	if r.Swept > 0 {
		say(fmt.Sprintf("Öge stopped %d process group(s) the last oge left running", r.Swept))
	}
	switch r.State {
	case run.RestoredStopped:
		if r.Card != nil && r.Card.Kind == run.CardCheckInterrupted {
			say("■ the Check stopped when oge exited")
		} else if r.Card != nil {
			say(fmt.Sprintf("■ turn %d stopped when oge exited", r.Card.Turn))
		}
	case run.RestoredParked:
		say("■ parked at " + gateName(r.Card) + " when oge exited")
	case run.RestoredFailed:
		say("✗ the last Check failed")
	case run.RestoredDone:
		say("✓ the last cut was Accepted: not applied, not seen yet")
	case run.RestoredUnknown:
		say("? state unknown: " + clean(r.Problem))
	}
	say(h.st.dim("restored from the Desk record · send a message to reopen it · /new starts another"))
}

// gateName is the parked Gate as the card names it.
func gateName(c *run.RestoredCard) string {
	g := "a Gate"
	if c != nil && c.Gate != "" {
		g = "the " + strings.ReplaceAll(strings.TrimPrefix(c.Gate, "gate."), "_", "-") + " Gate"
	}
	return g
}

// cardText is what a restored Chat's Inbox card says, in Öge's words.
// It offers only what works on a Chat with no Session.
func (r *restoredView) cardText() string {
	c := r.Card
	if c == nil {
		return ""
	}
	switch c.Kind {
	case run.CardParked:
		lead := "parked at "
		if c.Verified {
			lead = "verified, parked at "
		}
		return lead + gateName(c) + " when oge exited"
	case run.CardCheckInterrupted:
		return "Check interrupted when oge exited"
	}
	return "turn " + strconv.Itoa(c.Turn) + " stopped when oge exited"
}

// closeRestored closes the restored Chats the home still holds, as the
// human asked: each one's ChatClosed, then its Workspace.
func (h *home) closeRestored() {
	for _, v := range h.views {
		if v.rest != nil {
			run.CloseRestored(v.rest.desk, v.rest.RestoredChat)
		}
	}
}

// chatID is the Chat's id for the Desk record: the restored one's, or the
// open Chat's; "" when it has none yet.
func (v *chatView) chatID() string {
	if v.rest != nil {
		return v.rest.ID
	}
	if v.chat == nil {
		return ""
	}
	if c := v.chat.get(); c != nil {
		return c.ID
	}
	return ""
}

// deskOf is the Desk record the Chat's closing and Seen are written to.
func (v *chatView) deskOf() *run.Desk {
	if v.rest != nil {
		return v.rest.desk
	}
	if v.chat == nil {
		return nil
	}
	return v.chat.desk
}

// reopenAsk is the settings-changed card (A6): the message that asked, and
// what changed since the Chat's last Session started.
type reopenAsk struct {
	text    string
	changes []string
}

// restoredSubmit is submit's first step for a restored Chat: the card's
// answer when one waits, and the refusal when the Chat can't reopen. It
// returns the message to send; ok false means nothing is sent now.
// Enter alone never answers the card: only the typed words do.
func (h *home) restoredSubmit(text string) (string, tea.Cmd, bool) {
	r := h.rest
	if _, err := os.Stat(r.Repo); err != nil || h.chat == nil || h.chat.resume == nil {
		h.msg = append(h.msg, "this Chat can only be read: its repo isn't there or its Session can't reopen · /new starts another Chat")
		return text, nil, false
	}
	a := h.reopenAsk
	if a == nil {
		return text, nil, true
	}
	switch strings.TrimSpace(text) {
	case "reopen":
		h.reopenAsk, h.reopenOK = nil, true
		return a.text, nil, true
	case "n":
		h.reopenAsk = nil
		h.comp.set(a.text)
		return text, h.println(h.ogeLine("not reopened: this Chat stays read-only, and your message is back in the box · /new starts another Chat")), false
	}
	h.msg = append(h.msg, "settings changed: type reopen to continue under them, or n to leave this Chat read-only")
	return text, nil, false
}

// restoredTrust is the semantic trust check on the first message to a
// restored Chat: what would launch now against what its last Session
// started under. A change shows the card and sends nothing; it is checked
// against the Chat's records, never against a hash of oge.toml.
func (h *home) restoredTrust(text string, res *resolved) (tea.Cmd, bool) {
	if h.reopenOK {
		return nil, true // the human typed reopen
	}
	changes, err := h.chat.changes(run.ResumeParams{Chat: h.rest.RestoredChat, Now: res.frozen})
	if err != nil {
		h.msg = append(h.msg, "this Chat can't be reopened: "+clean(err.Error()))
		return nil, false
	}
	if len(changes) == 0 {
		return nil, true
	}
	h.reopenAsk = &reopenAsk{text: text, changes: changes}
	h.comp.set("")
	lines := []string{h.ogeLine("settings changed since this Chat's Session last ran · nothing was reopened")}
	for _, c := range changes {
		lines = append(lines, "  "+h.st.dim(clean(c)))
	}
	lines = append(lines, h.ogeLine("type reopen to continue under the new settings (Enter alone doesn't decide) · n leaves it read-only"))
	return h.println(strings.Join(lines, "\n")), false
}

// finishRestore makes the view a live Chat once its message reopened it,
// and says what the reopening did besides.
func (h *home) finishRestore(r *run.Resumed) tea.Cmd {
	h.rest = nil
	var lines []string
	say := func(s string) { lines = append(lines, h.ogeLine(h.st.dim(s))) }
	if r.Rebuilt {
		say("rebuilt this Chat's Workspace from its last turn")
	}
	if r.Found {
		say("the stopped turn left edits: scope-checked and kept as that turn's checkpoint")
	}
	if r.Outside {
		say(run.OutsideNote + ": this Chat still works from the repo it started from")
	}
	if len(r.Changes) > 0 {
		say("continuing under the new settings, as you typed")
	}
	say("reopened with a re-brief of your messages so far: the work survived, the agent's memory of it didn't")
	return h.println(strings.Join(lines, "\n"))
}
