package run

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The Desk's socket (docs/ux/away.md, "The #338 socket"): one per user,
// <Private>/desk/sock, served by the live Desk. It answers exactly two
// methods, "locate" (where the Desk is) and "add" (a task for a new
// Chat's card). It has no Gate, ask, apply or keystroke method, and
// never will: a supervised agent runs as the same user and can reach it.

// DeskSock is the socket's file name, in the Desk's directory.
const DeskSock = "sock"

// The socket's two methods.
const (
	MethodLocate = "locate"
	MethodAdd    = "add"
)

// The add rate limit: a supervised agent can run oge add itself, so it is
// possible agent-initiated spend (A7).
const (
	AddLimit  = 3
	AddWindow = 10 * time.Minute
)

// sockPathMax is what a unix socket path can hold with room to spare
// (macOS allows 104 bytes).
const sockPathMax = 100

// maxRequest bounds one request line.
const maxRequest = 1 << 20

// DeskRequest is one request line.
type DeskRequest struct {
	Method string `json:"method"`
	Repo   string `json:"repo,omitempty"`
	Task   string `json:"task,omitempty"`
}

// DeskReply is one reply line. Error is one line the caller prints.
type DeskReply struct {
	OK     bool        `json:"ok"`
	Error  string      `json:"error,omitempty"`
	Opened *DeskOpened `json:"opened,omitempty"` // locate
	N      int         `json:"n,omitempty"`      // add: the Chat's number
	Repo   string      `json:"repo,omitempty"`   // add: the repo's name
}

// DeskAdder lands a task as a new Chat's card in repo; the Chat's number
// and the repo's name come back. A returned error is the one-line reason.
type DeskAdder func(repo, task string) (n int, name string, err error)

// DeskServe is what the live Desk's socket serves with.
type DeskServe struct {
	Add DeskAdder
	// Peer is the uid of the connection's other end; nil reads the OS's.
	// Now is the clock the rate limit runs on; nil is time.Now.
	Peer func(net.Conn) (uint32, error)
	Now  func() time.Time
	// UID is the uid a peer must have; zero-valued means this process's.
	UID *uint32
}

// Serve starts answering on the Desk's socket. It is best effort: the
// Desk works without it, and the error says why it doesn't have one. The
// socket goes when the Desk is released or closed.
func (d *Desk) Serve(s DeskServe) error {
	if d == nil {
		return nil
	}
	path := filepath.Join(filepath.Dir(d.lock.Name()), DeskSock)
	if len(path) > sockPathMax {
		return fmt.Errorf("the socket path %s is too long for a unix socket", path)
	}
	// This process holds the Desk's lock, so any socket there is a dead
	// Desk's.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	if s.Peer == nil {
		s.Peer = peerUID
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	uid := uint32(os.Getuid())
	if s.UID != nil {
		uid = *s.UID
	}
	srv := &deskServer{d: d, s: s, uid: uid}
	d.mu.Lock()
	if d.done {
		d.mu.Unlock()
		ln.Close()
		_ = os.Remove(path)
		return os.ErrClosed
	}
	d.ln, d.sock = ln, path
	d.mu.Unlock()
	go srv.accept(ln)
	return nil
}

type deskServer struct {
	d   *Desk
	s   DeskServe
	uid uint32

	mu   sync.Mutex
	adds []time.Time
}

func (s *deskServer) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go s.serve(c)
	}
}

func (s *deskServer) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	enc := json.NewEncoder(c)
	if uid, err := s.s.Peer(c); err != nil || uid != s.uid {
		_ = enc.Encode(DeskReply{Error: "refused: this socket is for the user who runs the Desk"})
		return
	}
	line, err := bufio.NewReader(io.LimitReader(c, maxRequest)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req DeskRequest
	if json.Unmarshal(line, &req) != nil {
		_ = enc.Encode(DeskReply{Error: "refused: not a request"})
		return
	}
	_ = enc.Encode(s.answer(req))
}

// answer is the whole of what the socket does: two methods, no others.
func (s *deskServer) answer(req DeskRequest) DeskReply {
	switch req.Method {
	case MethodLocate:
		if o, ok := lastOpened(filepath.Join(s.d.privateDir(), DeskDir)); ok {
			return DeskReply{OK: true, Opened: &o}
		}
		return DeskReply{OK: true, Opened: &DeskOpened{Host: HostNone}}
	case MethodAdd:
		if strings.TrimSpace(req.Task) == "" || req.Repo == "" {
			return DeskReply{Error: "refused: add needs a repo and a task"}
		}
		if why := s.admit(); why != "" {
			return DeskReply{Error: why}
		}
		if s.s.Add == nil {
			return DeskReply{Error: "refused: this Desk takes no added tasks"}
		}
		n, name, err := s.s.Add(req.Repo, req.Task)
		if err != nil {
			return DeskReply{Error: squash(err.Error())}
		}
		return DeskReply{OK: true, N: n, Repo: name}
	}
	return DeskReply{Error: "refused: unknown method"}
}

// admit counts one add against the limit; "" when it may go on, else the
// reason.
func (s *deskServer) admit() string {
	now := s.s.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.adds[:0]
	for _, t := range s.adds {
		if now.Sub(t) < AddWindow {
			keep = append(keep, t)
		}
	}
	s.adds = keep
	if len(s.adds) >= AddLimit {
		wait := (AddWindow - now.Sub(s.adds[0])).Round(time.Minute)
		return fmt.Sprintf("refused: %d adds in %d minutes is the limit; try again in %d min", AddLimit, int(AddWindow/time.Minute), max(int(wait/time.Minute), 1))
	}
	s.adds = append(s.adds, now)
	return ""
}

func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// privateDir is the Private tree the Desk's directory is under.
func (d *Desk) privateDir() string { return filepath.Dir(filepath.Dir(d.lock.Name())) }

// ErrNoDesk is no live Desk to reach: no lock held. Its stale socket, if
// any, is removed.
var ErrNoDesk = errors.New("no Desk open")

// ErrDeskDeaf is a live Desk that doesn't answer on its socket.
var ErrDeskDeaf = errors.New("the Desk doesn't answer")

// DeskRefused is the Desk answering no, with its one-line reason.
type DeskRefused struct{ Reason string }

func (e *DeskRefused) Error() string { return e.Reason }

// deskHeld says whether a live process holds the Desk under private.
func deskHeld(private string) bool {
	lock := filepath.Join(private, DeskDir, deskLock)
	if _, err := os.Stat(lock); err != nil {
		return false
	}
	f, err := lockDesk(lock)
	if errors.Is(err, errDeskHeld) {
		return true
	}
	if err != nil {
		return false
	}
	unlockDesk(f)
	return false
}

// callDesk sends req to the live Desk's socket. With no Desk lock held,
// the socket is a dead Desk's: it is removed and the error is ErrNoDesk.
func callDesk(private string, req DeskRequest) (DeskReply, error) {
	sock := filepath.Join(private, DeskDir, DeskSock)
	if !deskHeld(private) {
		_ = os.Remove(sock)
		return DeskReply{}, ErrNoDesk
	}
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return DeskReply{}, ErrDeskDeaf
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return DeskReply{}, ErrDeskDeaf
	}
	var rep DeskReply
	if err := json.NewDecoder(c).Decode(&rep); err != nil {
		return DeskReply{}, ErrDeskDeaf
	}
	if !rep.OK {
		return rep, &DeskRefused{Reason: rep.Error}
	}
	return rep, nil
}

// LocateDesk asks the live Desk where it is.
func LocateDesk(private string) (DeskOpened, error) {
	rep, err := callDesk(private, DeskRequest{Method: MethodLocate})
	if err != nil || rep.Opened == nil {
		return DeskOpened{}, err
	}
	return *rep.Opened, nil
}

// AddToDesk hands task to the live Desk as a new Chat's card in repo; the
// Chat's number and the repo's name come back. ErrNoDesk or ErrDeskDeaf
// say it was not handed over; a *DeskRefused says the Desk said no.
func AddToDesk(private, repo, task string) (n int, name string, err error) {
	rep, err := callDesk(private, DeskRequest{Method: MethodAdd, Repo: repo, Task: task})
	if err != nil {
		return 0, "", err
	}
	return rep.N, rep.Repo, nil
}
