package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	say(h.st.dim("restored from the Desk record · reopening this Chat comes next · /new starts another"))
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
