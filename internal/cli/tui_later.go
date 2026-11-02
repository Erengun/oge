package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

// Later (docs/ux/desk.md, "Intake and resume", I1-I3): an idea kept
// without starting a Chat. Plain text and the repo it was captured in,
// nothing else. It is seen, not pushed: the rail says how many, and
// nothing here reaches a card, a toast, a bell or a notification. Only
// Enter on an item in /later turns it into a Chat.

// laterKey is the composer key that keeps the draft for later: the first
// one bound, "" when the keybindings file unbinds it.
func (h *home) laterKey() string {
	if ks := h.kb.actionKeys(ctxComposer, actLater); len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// laterState is /later while it is open.
type laterState struct{ sel int }

// laterCmd is /later: with text it keeps that text; alone it lists what
// was kept, oldest first.
func (h *home) laterCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		if err := h.keepLater(strings.Join(args, " "), h.repoCtx.root); err != nil {
			return nil, "/later: " + clean(err.Error())
		}
		h.sessNote = "◇ kept for later"
		return nil, ""
	}
	h.laterOn = &laterState{}
	return nil, ""
}

// laterHint reports whether the composer's hint names the keep key: only
// with a draft that is a Task, never an empty box or a command.
func (h *home) laterHint() bool {
	t := strings.TrimSpace(h.comp.text())
	return t != "" && !strings.HasPrefix(t, "/") && !strings.HasPrefix(t, "!") && h.laterKey() != ""
}

// keepLater records text for later, kept in repo. The repo is only noted.
func (h *home) keepLater(text, repo string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	it := run.LaterItem{ID: run.NewLaterID(h.clock()), Text: text, Repo: repo, At: h.clock()}
	var err error
	switch {
	case h.desk != nil:
		err = h.desk.LaterAdded(it)
	case h.absence.private != "":
		err = run.AppendLater(h.absence.private, it)
	}
	if err != nil {
		return fmt.Errorf("couldn't keep it: %w", err)
	}
	h.later = append(h.later, it)
	return nil
}

// keepDraft is the composer's key: the draft is kept for later instead
// of sent, and the box is cleared.
func (h *home) keepDraft() {
	text := strings.TrimSpace(task.StripComments(h.comp.expanded()))
	if task.IsEmpty(text) {
		return
	}
	if err := h.keepLater(text, h.repoCtx.root); err != nil {
		h.msg = []string{clean(err.Error())}
		return
	}
	h.comp.set("")
	h.dropAdviceEdit()
	h.navigating = false
	h.sessNote = "◇ kept for later"
}

// removeLater takes the item off the store and the list.
func (h *home) removeLater(id, why string) {
	if h.desk != nil {
		_ = h.desk.LaterRemoved(id, why)
	}
	h.later = slices.DeleteFunc(h.later, func(it run.LaterItem) bool { return it.ID == id })
}

// laterPress is a key while /later is open: Enter promotes the item, d or
// delete removes it, esc closes. Nothing else acts.
func (h *home) laterPress(k tea.KeyPressMsg) tea.Cmd {
	l := h.laterOn
	l.sel = min(max(l.sel, 0), max(len(h.later)-1, 0))
	switch k.String() {
	case "esc", "ctrl+c":
		h.laterOn = nil
	case "up", "k", "ctrl+p":
		l.sel = max(l.sel-1, 0)
	case "down", "j", "ctrl+n":
		l.sel = min(l.sel+1, max(len(h.later)-1, 0))
	case "d", "delete":
		if l.sel < len(h.later) {
			h.removeLater(h.later[l.sel].ID, run.LaterDeleted)
			l.sel = min(l.sel, max(len(h.later)-1, 0))
		}
	case "enter":
		if l.sel < len(h.later) {
			return h.promoteLater(h.later[l.sel])
		}
	}
	return nil
}

// promoteLater opens a Chat in the item's repo with its text as the first
// message, as /new and the Inbox's task do (the soft-cap note included),
// and takes the item off. When the Chat can't open, the item stays.
func (h *home) promoteLater(it run.LaterItem) tea.Cmd {
	rc := h.repoCtx
	if it.Repo != "" && repoKey(it.Repo) != repoKey(h.root) {
		if h.repoFor == nil {
			h.msg = []string{"/later: this session can't open " + clean(filepath.Base(it.Repo))}
			return nil
		}
		var lines []string
		if rc, lines = h.repoFor(it.Repo); rc == nil {
			h.msg = []string{strings.Join(lines, "; ")}
			return nil
		}
		h.msg = append(h.msg, lines...)
	}
	h.laterOn = nil
	h.removeLater(it.ID, run.LaterPromoted)
	fresh := h.rest == nil && h.turn == nil && h.verify == nil && h.comp.empty() && (h.chat == nil || h.chat.get() == nil)
	switch {
	case !fresh:
		h.openView(rc, false)
	case rc != h.repoCtx:
		h.moveTo(rc)
	}
	text := it.Text
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!") {
		h.comp.set(text) // a command or shell line is the human's to send
		return nil
	}
	if n := hiddenCount(text); n > 0 {
		h.comp.set(text)
		h.hidden = &hiddenPrompt{text: text, n: n}
		return nil
	}
	_, cmd := h.submit(text)
	return cmd
}

// laterView is /later's screen.
func (h *home) laterView() string {
	st := h.st
	lines := []string{"", " " + st.bold("Later") + st.dim(" · kept, never started on their own")}
	if len(h.later) == 0 {
		lines = append(lines, " "+st.dim("nothing kept for later · /later <text>"+h.laterKeyNote()+" keeps one"), "", " "+st.dim("esc closes"))
		return strings.Join(lines, "\n")
	}
	w := max(h.width, 20)
	for i, it := range h.later {
		repo := filepath.Base(it.Repo)
		first, _, _ := strings.Cut(it.Text, "\n")
		room := max(w-ansi.StringWidth(repo)-8, 10)
		line := ansi.Truncate(clean(first), room, "…") + "  " + st.dim(clean(repo))
		if i == h.laterOn.sel {
			lines = append(lines, " "+st.accent("> ")+line)
		} else {
			lines = append(lines, "   "+line)
		}
	}
	lines = append(lines, "", " "+st.dim("⏎ opens a Chat with it · d deletes · esc closes"))
	return strings.Join(lines, "\n")
}

// laterKeyNote is " or <key> on a draft" for the empty list's line.
func (h *home) laterKeyNote() string {
	if k := h.laterKey(); k != "" {
		return " or " + k + " on a draft"
	}
	return ""
}

// laterFold is the rail's line for what was kept: "" when nothing was.
func (h *home) laterFold() string {
	if len(h.later) == 0 {
		return ""
	}
	return h.st.dim("◇ " + strconv.Itoa(len(h.later)) + " later")
}
