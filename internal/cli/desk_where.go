package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// openDesk takes the user's one Desk (docs/ux/away.md, Model) for this
// home. When another oge has it, the second one opens nothing: it says
// where the live Desk is and exits 0 (done is true, with code).
func openDesk(env Env, state *ledger.StateRoot) (d *run.Desk, code int, done bool) {
	d, err := run.OpenDesk(state.Private, env.Getenv)
	var held *run.DeskHeldError
	switch {
	case errors.As(err, &held):
		fmt.Fprintln(env.Stdout, deskWhere(held.At, env.Getenv))
		return nil, ExitOK, true
	case err != nil:
		fmt.Fprintf(env.Stderr, "oge: opening the Desk: %v\n", err)
		return nil, ExitInternal, true
	}
	return d, ExitOK, false
}

// deskWhere says where the live Desk is and how to get there, from its
// DeskOpened; getenv is this terminal's, which may be inside the same
// host already.
func deskWhere(o run.DeskOpened, getenv func(string) string) string {
	const lead = "oge: your Desk is already open "
	switch o.Host {
	case run.HostTmux:
		if o.Session == "" {
			break
		}
		target := "'" + o.Session + "'" // $N, a tmux session id, quoted for the shell
		cmd := "tmux attach -t " + target
		if getenv("TMUX") != "" {
			cmd = "tmux switch-client -t " + target
		}
		if o.Socket != "" && filepath.Base(o.Socket) != "default" {
			cmd = "tmux -S " + o.Socket + cmd[len("tmux"):]
		}
		return lead + "in tmux session " + o.Session + ": " + cmd
	case run.HostScreen:
		w := ""
		if o.Window != "" {
			w = ", window " + o.Window
		}
		return lead + "in screen session " + o.Session + w + ": screen -x " + o.Session
	case run.HostHerdr:
		return lead + "in herdr pane " + o.Pane + ": switch to that pane"
	}
	if o.PID > 0 {
		return lead + "in another terminal (oge process " + strconv.Itoa(o.PID) + "): use that one, or quit it there first"
	}
	return lead + "in another terminal: use that one, or quit it there first"
}
