//go:build unix

package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// One Desk per user (docs/ux/away.md): a second bare oge opens nothing,
// says where the live one is, and exits 0. oge "task" goes on as ever.
func TestSecondOgePointsAtTheLiveDesk(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	state, err := ledger.OpenStateRoot(f.state, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]string{"TMUX": "/tmp/tmux-501/default,4242,3", "TMUX_PANE": "%7"}
	d, err := run.OpenDesk(state.Private, func(k string) string { return live[k] })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Release()
	f.setenv("TMUX", "")
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if got, want := s.out.String(), "oge: your Desk is already open in tmux session $3: tmux attach -t '$3'\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended"); code != ExitOK {
		t.Fatalf("oge \"task\" beside a live Desk: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// Where a second oge says the Desk is, under each host.
func TestDeskWhere(t *testing.T) {
	none := func(string) string { return "" }
	inTmux := func(k string) string {
		if k == "TMUX" {
			return "/tmp/tmux-501/default,1,0"
		}
		return ""
	}
	for _, c := range []struct {
		o      run.DeskOpened
		getenv func(string) string
		want   string
	}{
		{run.DeskOpened{Host: run.HostTmux, Session: "$3", Socket: "/tmp/tmux-501/default"}, inTmux, "in tmux session $3: tmux switch-client -t '$3'"},
		{run.DeskOpened{Host: run.HostTmux, Session: "$0", Socket: "/tmp/tmux-501/work"}, none, "in tmux session $0: tmux -S /tmp/tmux-501/work attach -t '$0'"},
		{run.DeskOpened{Host: run.HostScreen, Session: "99.pts-1.box", Window: "2"}, none, "in screen session 99.pts-1.box, window 2: screen -x 99.pts-1.box"},
		{run.DeskOpened{Host: run.HostHerdr, Pane: "p-3"}, none, "in herdr pane p-3: switch to that pane"},
		{run.DeskOpened{Host: run.HostNone, PID: 77}, none, "in another terminal (oge process 77): use that one, or quit it there first"},
	} {
		if got := deskWhere(c.o, c.getenv); got != "oge: your Desk is already open "+c.want {
			t.Errorf("%+v: %q", c.o, got)
		}
	}
}

// /quit with a Chat open asks (A3): k keeps it for next time, Workspace
// and all, with no ChatClosed; c closes it as /close does. Either way
// desk.log ends with a clean DeskClosed.
func TestQuitKeepsOrClosesTheChat(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{"k", "c"} {
		t.Run(answer, func(t *testing.T) {
			t.Parallel()
			s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "hello from the chat"}}}, nil)
			s.composer()
			s.type_("yo\r")
			s.expect("  hello from the chat")
			s.composer()
			s.type_("/quit\r")
			s.expectScreen("Keep 1 Chat for next time? k keep · c close · esc cancel")
			s.type_(answer)
			if code := s.wait(); code != ExitOK {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
			}
			left, _ := filepath.Glob(filepath.Join(f.state, "work", "chats", "*"))
			if keep := answer == "k"; keep != (len(left) == 1) {
				t.Errorf("%s: Workspaces left %v", answer, left)
			}
			state, err := ledger.OpenStateRoot(f.state, f.repo)
			if err != nil {
				t.Fatal(err)
			}
			recs, err := run.ReadDesk(state.Private)
			if err != nil {
				t.Fatal(err)
			}
			var types []string
			for _, r := range recs {
				types = append(types, r.Type)
			}
			want := []string{run.RecDeskOpened, run.RecChatAdded, run.RecChatClosed, run.RecDeskClosed}
			if answer == "k" {
				want = []string{run.RecDeskOpened, run.RecChatAdded, run.RecDeskClosed}
			}
			if strings.Join(types, " ") != strings.Join(want, " ") {
				t.Fatalf("desk.log %v, want %v", types, want)
			}
			var a run.ChatAdded
			var c run.DeskClosed
			_ = json.Unmarshal(recs[1].Data, &a)
			_ = json.Unmarshal(recs[len(recs)-1].Data, &c)
			if a.N != 1 || a.Title != "yo" || a.Chat == "" || a.Repo == "" || !c.Clean {
				t.Errorf("ChatAdded %+v, DeskClosed %+v", a, c)
			}
		})
	}
}
