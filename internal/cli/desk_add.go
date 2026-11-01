package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// oge add "task" (docs/ux/away.md, A7 and "The #338 socket"): from another
// terminal, a task joins the open Desk as a new Chat. It lands as a card
// and starts nothing: the first turn begins when the human focuses the
// Chat or opens its Inbox card. With no Desk open it is oge "task".

// deskAddMsg is a task from the Desk's socket, for the home's loop; the
// answer goes back on reply.
type deskAddMsg struct {
	repo, task string
	reply      chan deskAddReply
}

type deskAddReply struct {
	n    int
	name string
	err  error
}

// serveAdd opens the Desk's socket for this home. It is best effort: a
// Desk with no socket still works, and oge add then runs its task itself.
func (u *tui) serveAdd(ctx context.Context) {
	if u.desk == nil {
		return
	}
	_ = u.desk.Serve(run.DeskServe{Add: func(repo, text string) (int, string, error) {
		send := u.send.Load()
		if send == nil {
			return 0, "", errors.New("the Desk is still starting; try again")
		}
		reply := make(chan deskAddReply, 1)
		(*send)(deskAddMsg{repo: repo, task: text, reply: reply})
		select {
		case r := <-reply:
			return r.n, r.name, r.err
		case <-ctx.Done():
			return 0, "", errors.New("the Desk is closing")
		case <-time.After(5 * time.Second):
			return 0, "", errors.New("the Desk didn't answer in time")
		}
	}})
}

// deskAdd puts the task on a new Chat's card in the repo, by the same
// path as /new <path>, and leaves the focus where it is.
func (h *home) deskAdd(m deskAddMsg) {
	reply := func(r deskAddReply) { m.reply <- r }
	root, problem := projectRoot(m.repo, h.root, h.getenv)
	if problem != "" {
		reply(deskAddReply{err: errors.New(strings.TrimPrefix(problem, "/new: "))})
		return
	}
	rc := h.repoCtx
	if repoKey(root) != repoKey(h.root) {
		if h.repoFor == nil {
			reply(deskAddReply{err: errors.New("this Desk can't open another repo")})
			return
		}
		var lines []string
		if rc, lines = h.repoFor(root); rc == nil {
			reply(deskAddReply{err: errors.New(strings.Join(lines, "; "))})
			return
		}
		h.msg = append(h.msg, lines...)
	}
	v := h.newViewIn(rc)
	v.added, v.addedAt = m.task, h.clock()
	v.name = task.Parse(m.task).Title
	h.place(h.repoCtx.root)
	h.place(v.root)
	h.views = append(h.views, v)
	h.desk.ChatQueued(v.num, v.name, v.root)
	reply(deskAddReply{n: v.num, name: filepath.Base(v.root)})
}

// startAdded begins the first turn of the focused Chat when it is a card
// oge add made: the human has focused it. It waits while something else
// has the screen, and tries again at the next message.
func (h *home) startAdded() tea.Cmd {
	v := h.chatView
	if v == nil || v.added == "" || h.overlaid() || h.run != nil || h.job != nil || h.quitAsk > 0 {
		return nil
	}
	text := v.added
	v.added = ""
	// Typed by whoever ran oge add, which may be a supervised agent: what
	// the composer would hold back for the human's eyes is held back here.
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!"):
		h.comp.set(text)
		return nil
	case hiddenCount(text) > 0:
		h.comp.set(text)
		h.hidden = &hiddenPrompt{text: text, n: hiddenCount(text)}
		return nil
	}
	h.comp.set(text) // kept in the box when the Task can't start
	_, cmd := h.submit(text)
	return cmd
}

// addCommand is oge add "task": hand the task to the open Desk and exit 0
// once it has it, or run it here as oge "task" when no Desk is open. args
// are what follows add; anything but one task goes on as oge's own
// arguments do.
func addCommand(env Env, args []string) int {
	f, positional, err := parseRunFlags(args)
	if err != nil || len(positional) != 1 || f.taskFile != "" || strings.TrimSpace(positional[0]) == "" {
		return runCommand(env, args)
	}
	root, err := workspace.RepoRoot(env.Dir)
	if err != nil {
		return runCommand(env, args) // says why it can't
	}
	state, code := openState(env, root)
	if code != ExitOK {
		return code
	}
	n, name, err := run.AddToDesk(state.Private, root, positional[0])
	var refused *run.DeskRefused
	switch {
	case err == nil:
		fmt.Fprintf(env.Stdout, "added to the Desk as Chat %d (%s)\n", n, name)
		return ExitOK
	case errors.As(err, &refused):
		fmt.Fprintf(env.Stderr, "oge add: %s\n", strings.TrimPrefix(refused.Reason, "refused: "))
		return ExitRefused
	case errors.Is(err, run.ErrDeskDeaf):
		fmt.Fprintln(env.Stderr, "oge add: the Desk is open but didn't take it: running it here")
	default:
		fmt.Fprintln(env.Stderr, "oge add: no Desk open: running it here")
	}
	return runCommand(env, args)
}
