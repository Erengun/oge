//go:build unix

package run_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/erengun/oge/internal/run"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// The Desk is one per user: while one process holds it, another open
// fails and says where the first one is, from its DeskOpened.
func TestDeskLockHeldSaysWhere(t *testing.T) {
	private := t.TempDir()
	d, err := run.OpenDesk(private, env(map[string]string{"TMUX": "/tmp/tmux-501/work,4242,3", "TMUX_PANE": "%7"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = run.OpenDesk(private, env(nil))
	var held *run.DeskHeldError
	if !errors.As(err, &held) {
		t.Fatalf("second open: %v", err)
	}
	want := run.DeskOpened{PID: os.Getpid(), Host: run.HostTmux, Pane: "%7", Session: "$3", Socket: "/tmp/tmux-501/work"}
	got := held.At
	got.Start = ""
	if got != want {
		t.Errorf("held at %+v, want %+v", got, want)
	}
	d.Close(true)
	d2, err := run.OpenDesk(private, env(nil))
	if err != nil {
		t.Fatalf("open after Close: %v", err)
	}
	d2.Release()
}

// The OS drops the lock when its holder dies, however it dies: a killed
// oge never leaves the Desk locked.
func TestDeskLockReleasedWhenTheHolderDies(t *testing.T) {
	if p := os.Getenv("OGE_TEST_DESK_HOLDER"); p != "" {
		if _, err := run.OpenDesk(p, os.Getenv); err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("held\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	private := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDeskLockReleasedWhenTheHolderDies$")
	cmd.Env = append(os.Environ(), "OGE_TEST_DESK_HOLDER="+private, "STY=99.pts-1.box", "WINDOW=2", "TMUX=", "HERDR_ENV=")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(out).ReadString('\n'); line != "held\n" {
		t.Fatalf("holder: %q, %v", line, err)
	}
	_, err = run.OpenDesk(private, env(nil))
	var held *run.DeskHeldError
	if !errors.As(err, &held) || held.At.PID != cmd.Process.Pid || held.At.Host != run.HostScreen || held.At.Session != "99.pts-1.box" || held.At.Window != "2" {
		t.Fatalf("while held: %v (%+v)", err, held)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	d, err := run.OpenDesk(private, env(nil))
	if err != nil {
		t.Fatalf("after the holder died: %v", err)
	}
	d.Release()
}

// desk.log holds each record in order, synced as written; a last line
// a crash cut off is left out.
func TestDeskLogRecords(t *testing.T) {
	private := t.TempDir()
	d, err := run.OpenDesk(private, env(map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "p-3"}))
	if err != nil {
		t.Fatal(err)
	}
	d.ChatAdded("c1", 1, "fix Add", "/repo")
	d.Seen("c1", "r1")
	d.ChatClosed("c1")
	d.Close(true)
	d.ChatAdded("late", 2, "after close", "/repo") // records nothing
	f, err := os.OpenFile(private+"/desk/desk.log", os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"at":"2026-11-01T00:00:00Z","type":"ChatAd`)
	f.Close()
	recs, err := run.ReadDesk(private)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, r := range recs {
		types = append(types, r.Type)
	}
	want := []string{run.RecDeskOpened, run.RecChatAdded, run.RecSeen, run.RecChatClosed, run.RecDeskClosed}
	if len(types) != len(want) {
		t.Fatalf("records %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("records %v, want %v", types, want)
		}
	}
	var o run.DeskOpened
	var a run.ChatAdded
	var s run.Seen
	var c run.DeskClosed
	for _, p := range []struct {
		i int
		v any
	}{{0, &o}, {1, &a}, {2, &s}, {4, &c}} {
		if err := json.Unmarshal(recs[p.i].Data, p.v); err != nil {
			t.Fatal(err)
		}
	}
	if o.PID != os.Getpid() || o.Start == "" || o.Host != run.HostHerdr || o.Pane != "p-3" {
		t.Errorf("DeskOpened %+v", o)
	}
	if a != (run.ChatAdded{Chat: "c1", N: 1, Title: "fix Add", Repo: "/repo"}) || s != (run.Seen{Chat: "c1", Upto: "r1"}) || !c.Clean {
		t.Errorf("ChatAdded %+v, Seen %+v, DeskClosed %+v", a, s, c)
	}
	if fi, err := os.Stat(private + "/desk"); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("desk dir: %v %v", fi, err)
	}
}
