//go:build unix

package run_test

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/run"
)

// shortDir is a private tree with a path short enough for a unix socket.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "ogd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type added struct{ repo, task string }

// serve opens a Desk under a fresh private tree and serves add on it.
func serve(t *testing.T, s run.DeskServe) (private string, d *run.Desk, got *[]added) {
	t.Helper()
	private = shortDir(t)
	desk, err := run.OpenDesk(private, env(map[string]string{"TMUX": "/tmp/tmux-1/default,4242,3", "TMUX_PANE": "%7"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(desk.Release)
	got = &[]added{}
	if s.Add == nil {
		s.Add = func(repo, task string) (int, string, error) {
			*got = append(*got, added{repo, task})
			return len(*got) + 1, filepath.Base(repo), nil
		}
	}
	if err := desk.Serve(s); err != nil {
		t.Fatal(err)
	}
	return private, desk, got
}

// The socket is the user's alone: a 0700 directory and a 0600 socket.
func TestDeskSocketPermissions(t *testing.T) {
	private, _, _ := serve(t, run.DeskServe{})
	for p, want := range map[string]os.FileMode{
		filepath.Join(private, run.DeskDir):               0o700,
		filepath.Join(private, run.DeskDir, run.DeskSock): 0o600,
	} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s is %o, want %o", p, got, want)
		}
	}
}

// add reaches the Desk, which answers with the Chat's number.
func TestDeskSocketAdd(t *testing.T) {
	private, _, got := serve(t, run.DeskServe{})
	n, name, err := run.AddToDesk(private, "/work/api", "fix the parser")
	if err != nil || n != 2 || name != "api" {
		t.Fatalf("add: %d %q %v", n, name, err)
	}
	if len(*got) != 1 || (*got)[0] != (added{"/work/api", "fix the parser"}) {
		t.Errorf("the Desk was handed %+v", *got)
	}
	o, err := run.LocateDesk(private)
	if err != nil || o.Host != run.HostTmux || o.Session != "$3" || o.Pane != "%7" {
		t.Errorf("locate: %+v %v", o, err)
	}
}

// A supervised agent can run oge add: past three in ten minutes the
// Desk refuses, with a line the caller prints; later, it takes them again.
func TestDeskSocketRateLimit(t *testing.T) {
	now := time.Now()
	private, _, got := serve(t, run.DeskServe{Now: func() time.Time { return now }})
	for i := range run.AddLimit {
		if _, _, err := run.AddToDesk(private, "/r", "task"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	_, _, err := run.AddToDesk(private, "/r", "one too many")
	var refused *run.DeskRefused
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "3 adds in 10 minutes") || strings.Contains(refused.Reason, "\n") {
		t.Fatalf("fourth add: %v", err)
	}
	if len(*got) != run.AddLimit {
		t.Errorf("the Desk got %d tasks", len(*got))
	}
	now = now.Add(run.AddWindow + time.Second)
	if _, _, err := run.AddToDesk(private, "/r", "later"); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

// Another user's connection is refused before it is read.
func TestDeskSocketRefusesOtherUIDs(t *testing.T) {
	var asked bool
	private, _, got := serve(t, run.DeskServe{
		Peer: func(net.Conn) (uint32, error) { return uint32(os.Getuid()) + 1, nil },
		Add:  func(string, string) (int, string, error) { asked = true; return 1, "r", nil },
	})
	_, _, err := run.AddToDesk(private, "/r", "task")
	var refused *run.DeskRefused
	if !errors.As(err, &refused) || asked || len(*got) != 0 {
		t.Fatalf("other uid: %v, asked %v", err, asked)
	}
	// And a peer whose uid can't be read.
	private, _, _ = serve(t, run.DeskServe{Peer: func(net.Conn) (uint32, error) { return 0, errors.New("no creds") }})
	if _, _, err := run.AddToDesk(private, "/r", "task"); !errors.As(err, &refused) {
		t.Errorf("unreadable peer: %v", err)
	}
}

// Only locate and add are answered: nothing else the Desk does is
// reachable, whatever is asked for.
func TestDeskSocketAnswersOnlyTwoMethods(t *testing.T) {
	private, _, _ := serve(t, run.DeskServe{})
	for _, m := range []string{"answer", "apply", "gate", "keys", "send", "", "ADD"} {
		c, err := net.Dial("unix", filepath.Join(private, run.DeskDir, run.DeskSock))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(run.DeskRequest{Method: m, Repo: "/r", Task: "x"})
		c.Write(append(b, '\n'))
		var rep run.DeskReply
		if err := json.NewDecoder(c).Decode(&rep); err != nil || rep.OK || !strings.Contains(rep.Error, "unknown method") {
			t.Errorf("method %q: %+v %v", m, rep, err)
		}
		c.Close()
	}
}

// A socket left by a dead Desk is removed, and the caller falls back.
func TestDeskStaleSocketIsRemoved(t *testing.T) {
	private := shortDir(t)
	dir := filepath.Join(private, run.DeskDir)
	os.MkdirAll(dir, 0o700)
	sock := filepath.Join(dir, run.DeskSock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // the file stays, as after a crash
	if _, err := os.Stat(sock); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run.AddToDesk(private, "/r", "task"); !errors.Is(err, run.ErrNoDesk) {
		t.Fatalf("stale socket: %v", err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the stale socket stayed: %v", err)
	}
}

// A clean exit removes the socket; no Desk at all is ErrNoDesk too.
func TestDeskReleaseRemovesTheSocket(t *testing.T) {
	private, d, _ := serve(t, run.DeskServe{})
	sock := filepath.Join(private, run.DeskDir, run.DeskSock)
	d.Close(true)
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket outlived the Desk: %v", err)
	}
	if _, _, err := run.AddToDesk(private, "/r", "task"); !errors.Is(err, run.ErrNoDesk) {
		t.Errorf("after Close: %v", err)
	}
	if _, _, err := run.AddToDesk(shortDir(t), "/r", "task"); !errors.Is(err, run.ErrNoDesk) {
		t.Errorf("never a Desk: %v", err)
	}
}

// ChatQueued is a ChatAdded with no Chat id yet, from oge add.
func TestChatQueuedRecord(t *testing.T) {
	private := shortDir(t)
	d, err := run.OpenDesk(private, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	d.ChatQueued(2, "fix it", "/work/api")
	d.Close(true)
	recs, _ := run.ReadDesk(private)
	var a run.ChatAdded
	if len(recs) != 3 || recs[1].Type != run.RecChatAdded || json.Unmarshal(recs[1].Data, &a) != nil ||
		a != (run.ChatAdded{N: 2, Title: "fix it", Repo: "/work/api", Source: "oge add"}) {
		t.Errorf("records %+v, ChatAdded %+v", recs, a)
	}
}
