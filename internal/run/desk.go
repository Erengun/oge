package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// A native session put on the Desk with a, and taken off it again
	// (docs/ux/sessions.md). Only what the rail shows is kept.
	RecSessionAdded    = "SessionAdded"
	RecSessionArchived = "SessionArchived"
	RecSessionSeen     = "SessionSeen" // a row's title and age refreshed in place
	RecDeskClosed      = "DeskClosed"
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

// SessionAdded is a native session the human put on the Desk: the rail
// row's title and last known activity, and where it lives. It holds no
// transcript text.
type SessionAdded struct {
	Provider string    `json:"provider"`
	ID       string    `json:"id"`
	Profile  string    `json:"profile,omitempty"`
	Repo     string    `json:"repo"`
	Title    string    `json:"title,omitempty"`
	Last     time.Time `json:"last,omitzero"`
}

// SessionArchived is a session taken off the Desk; the native session is
// untouched.
type SessionArchived struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
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

// ChatClosed records the Chat closed, ahead of its closing.
func (d *Desk) ChatClosed(chat string) {
	_ = d.append(RecChatClosed, ChatClosed{Chat: chat})
}

// Seen records the Chat's ✓ seen, up to the Run run.
func (d *Desk) Seen(chat, run string) {
	_ = d.append(RecSeen, Seen{Chat: chat, Upto: run})
}

// SessionAdded records a native session put on the Desk.
func (d *Desk) SessionAdded(s SessionAdded) { _ = d.append(RecSessionAdded, s) }

// SessionSeen records a row's refreshed title and last activity. Replay
// updates the row where it is.
func (d *Desk) SessionSeen(s SessionAdded) { _ = d.append(RecSessionSeen, s) }

// SessionArchived records a session taken off the Desk.
func (d *Desk) SessionArchived(provider, id string) {
	_ = d.append(RecSessionArchived, SessionArchived{Provider: provider, ID: id})
}

// SessionsOnDesk replays recs: the sessions added and not archived since,
// in the order they were added. A session added again after its archive
// goes to the end.
func SessionsOnDesk(recs []DeskRecord) []SessionAdded {
	var out []SessionAdded
	drop := func(provider, id string) {
		out = slices.DeleteFunc(out, func(s SessionAdded) bool { return s.Provider == provider && s.ID == id })
	}
	for _, r := range recs {
		switch r.Type {
		case RecSessionAdded:
			var s SessionAdded
			if json.Unmarshal(r.Data, &s) == nil && s.Provider != "" && s.ID != "" {
				drop(s.Provider, s.ID)
				out = append(out, s)
			}
		case RecSessionSeen:
			var s SessionAdded
			if json.Unmarshal(r.Data, &s) == nil {
				for i := range out {
					if out[i].Provider == s.Provider && out[i].ID == s.ID {
						out[i].Last = s.Last
						if s.Title != "" {
							out[i].Title = s.Title
						}
					}
				}
			}
		case RecSessionArchived:
			var s SessionArchived
			if json.Unmarshal(r.Data, &s) == nil {
				drop(s.Provider, s.ID)
			}
		}
	}
	return out
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
