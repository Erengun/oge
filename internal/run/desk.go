package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The Desk record (docs/ux/away.md, Model): one per user, under the
// state root's Private tree, so it survives a reboot. desk.log says which
// Chats the Desk holds and how it last ended; lock is held while one oge
// process owns the Desk, and the OS drops it when that process dies.

// The Desk record's file and directory names, under Private.
const (
	DeskDir  = "desk"
	DeskLog  = "desk.log"
	deskLock = "lock"
)

// The desk.log record types.
const (
	RecDeskOpened = "DeskOpened"
	RecChatAdded  = "ChatAdded"
	RecChatClosed = "ChatClosed"
	RecSeen       = "Seen"
	// RecResidueSwept: a restore killed a process group the last oge left.
	RecResidueSwept = "ResidueSwept"
	RecDeskClosed   = "DeskClosed"
)

// The hosts a Desk can live under (docs/ux/away.md, A1).
const (
	HostHerdr  = "herdr"
	HostTmux   = "tmux"
	HostScreen = "screen"
	HostNone   = "none"
)

// DeskRecord is one desk.log line.
type DeskRecord struct {
	At   time.Time       `json:"at"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// DeskOpened is who owns the Desk and where it can be reached: the
// process (its pid and start time, which tell it from a later process
// with the same pid) and the host it runs under, with the host's own
// pane, session or window.
type DeskOpened struct {
	PID   int    `json:"pid"`
	Start string `json:"start,omitempty"`
	Host  string `json:"host"`
	// Pane is herdr's HERDR_PANE_ID or tmux's TMUX_PANE; Session is
	// tmux's session id ($N) or screen's STY; Socket is the tmux server's
	// socket; Window is screen's WINDOW.
	Pane    string `json:"pane,omitempty"`
	Session string `json:"session,omitempty"`
	Socket  string `json:"socket,omitempty"`
	Window  string `json:"window,omitempty"`
}

// ChatAdded is a Chat the Desk holds, from its first message.
type ChatAdded struct {
	Chat  string `json:"chat"`
	N     int    `json:"n"`
	Title string `json:"title"`
	Repo  string `json:"repo"`
	// Source is "oge add" for a Chat that landed as a card before any
	// turn: its Chat id comes with the ChatAdded its first turn records.
	Source string `json:"source,omitempty"`
}

// ChatClosed is a Chat the human closed: its Workspace goes, and a
// restore leaves it out.
type ChatClosed struct {
	Chat string `json:"chat"`
}

// Seen is the Chat's ✓ seen, up to the Run of its Accepted cut.
type Seen struct {
	Chat string `json:"chat"`
	Upto string `json:"upto"`
}

// DeskClosed is the Desk's process ending: clean on /quit, not on a
// hangup or SIGTERM. A crash writes nothing.
type DeskClosed struct {
	Clean bool `json:"clean"`
}

// Desk is the open Desk record, held by this process. Its methods may be
// called from any goroutine; a nil Desk records nothing.
type Desk struct {
	mu   sync.Mutex
	lock *os.File
	log  *os.File
	done bool
	// ln and sock are the socket Serve opened, closed with the Desk.
	ln   net.Listener
	sock string
}

// DeskHeldError is the Desk already open in another live process.
type DeskHeldError struct {
	// At is where it is, from its DeskOpened; zero when desk.log doesn't
	// say.
	At DeskOpened
}

func (e *DeskHeldError) Error() string {
	return fmt.Sprintf("the Desk is open in process %d", e.At.PID)
}

// OpenDesk takes the Desk under private for this process and records
// DeskOpened with the host getenv names. When another live process holds
// it, the error is a *DeskHeldError saying where.
func OpenDesk(private string, getenv func(string) string) (*Desk, error) {
	dir := filepath.Join(private, DeskDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := lockDesk(filepath.Join(dir, deskLock))
	if errors.Is(err, errDeskHeld) {
		// A probe from oge add holds the lock for an instant; a second try
		// tells it from a Desk that is really open.
		time.Sleep(25 * time.Millisecond)
		lock, err = lockDesk(filepath.Join(dir, deskLock))
	}
	if errors.Is(err, errDeskHeld) {
		held := &DeskHeldError{}
		if o, ok := lastOpened(dir); ok {
			held.At = o
		}
		return nil, held
	}
	if err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(dir, DeskLog), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		unlockDesk(lock)
		return nil, err
	}
	// A crash can leave a cut-off last line; drop it before appending.
	if err := endLine(log); err != nil {
		log.Close()
		unlockDesk(lock)
		return nil, err
	}
	d := &Desk{lock: lock, log: log}
	o := deskHost(getenv)
	o.PID = os.Getpid()
	o.Start = processStart(o.PID)
	if err := d.append(RecDeskOpened, o); err != nil {
		d.Release()
		return nil, err
	}
	return d, nil
}

// endLine cuts a crash's half-written last line off f, so the next record
// starts on its own line and the log still reads.
func endLine(f *os.File) error {
	b, err := os.ReadFile(f.Name())
	if err != nil || len(b) == 0 || b[len(b)-1] == '\n' {
		return err
	}
	return f.Truncate(int64(bytes.LastIndexByte(b, '\n') + 1))
}

// deskHost is the host getenv says oge runs under: herdr, then tmux, then
// screen, else none.
func deskHost(getenv func(string) string) DeskOpened {
	switch {
	case getenv("HERDR_ENV") == "1" && getenv("HERDR_PANE_ID") != "":
		return DeskOpened{Host: HostHerdr, Pane: getenv("HERDR_PANE_ID")}
	case getenv("TMUX") != "":
		// TMUX is the server's socket, its pid and the session's id.
		o := DeskOpened{Host: HostTmux, Pane: getenv("TMUX_PANE")}
		parts := strings.Split(getenv("TMUX"), ",")
		o.Socket = parts[0]
		if len(parts) == 3 && parts[2] != "" {
			o.Session = "$" + parts[2]
		}
		return o
	case getenv("STY") != "":
		return DeskOpened{Host: HostScreen, Session: getenv("STY"), Window: getenv("WINDOW")}
	}
	return DeskOpened{Host: HostNone}
}

// ChatAdded records Chat n, opened in repo by a message titled title.
func (d *Desk) ChatAdded(chat string, n int, title, repo string) {
	_ = d.append(RecChatAdded, ChatAdded{Chat: chat, N: n, Title: title, Repo: repo})
}

// ChatQueued records Chat n, which oge add put on the Desk as a card in
// repo: no Chat is open yet, and no turn has started.
func (d *Desk) ChatQueued(n int, title, repo string) {
	_ = d.append(RecChatAdded, ChatAdded{N: n, Title: title, Repo: repo, Source: "oge add"})
}

// ChatClosed records the Chat closed, ahead of its closing.
func (d *Desk) ChatClosed(chat string) {
	_ = d.append(RecChatClosed, ChatClosed{Chat: chat})
}

// Seen records the Chat's ✓ seen, up to the Run run.
func (d *Desk) Seen(chat, run string) {
	_ = d.append(RecSeen, Seen{Chat: chat, Upto: run})
}

// ResidueSwept records a process group a restore killed for chat, so the
// kill leaves a trace.
func (d *Desk) ResidueSwept(chat string, pid, pgid int) {
	_ = d.append(RecResidueSwept, map[string]any{"chat": chat, "pid": pid, "pgid": pgid})
}

// Close records DeskClosed, clean or not, and lets the Desk go.
func (d *Desk) Close(clean bool) {
	_ = d.append(RecDeskClosed, DeskClosed{Clean: clean})
	d.Release()
}

// Release lets the Desk go without a record; after Close it does nothing.
func (d *Desk) Release() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return
	}
	d.done = true
	if d.ln != nil {
		_ = d.ln.Close()
		_ = os.Remove(d.sock) // before the lock goes: a later Desk's own is not ours to remove
	}
	_ = d.log.Close()
	unlockDesk(d.lock)
}

// append writes one record and syncs it before returning: what it says
// is on disk ahead of what it records happening.
func (d *Desk) append(typ string, data any) error {
	if d == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	line, err := json.Marshal(DeskRecord{At: time.Now().UTC(), Type: typ, Data: raw})
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return os.ErrClosed
	}
	if _, err := d.log.Write(append(line, '\n')); err != nil {
		return err
	}
	return d.log.Sync()
}

// ReadDesk is desk.log under private, oldest first. A last line cut off
// by a crash is left out; any other line that doesn't parse is an error.
func ReadDesk(private string) ([]DeskRecord, error) {
	b, err := os.ReadFile(filepath.Join(private, DeskDir, DeskLog))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(b, []byte{'\n'})
	// The last piece is "" after a whole line, or a line a crash cut off.
	lines = lines[:len(lines)-1]
	recs := make([]DeskRecord, 0, len(lines))
	for i, l := range lines {
		var r DeskRecord
		if err := json.Unmarshal(l, &r); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", DeskLog, i+1, err)
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// lastOpened is the DeskOpened of the newest Desk in dir.
func lastOpened(dir string) (DeskOpened, bool) {
	recs, _ := ReadDesk(filepath.Dir(dir))
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Type != RecDeskOpened {
			continue
		}
		var o DeskOpened
		if json.Unmarshal(recs[i].Data, &o) != nil {
			return DeskOpened{}, false
		}
		return o, true
	}
	return DeskOpened{}, false
}

// errDeskHeld is the lock held by another open file: another process's
// Desk.
var errDeskHeld = errors.New("the Desk lock is held")
