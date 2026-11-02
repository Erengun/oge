package run

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// Later (docs/ux/desk.md, "Intake and resume"): ideas kept without
// starting a Chat. Plain text and the repo it was captured in, nothing
// else: no priority, label, deadline, state or order beyond when it was
// kept (I1). Items live in desk.log beside the Desk's other records.

// The desk.log record types for Later.
const (
	RecLaterAdded   = "LaterAdded"
	RecLaterRemoved = "LaterRemoved"
)

// Why a Later item left.
const (
	LaterPromoted = "promoted"
	LaterDeleted  = "deleted"
)

// LaterItem is one kept idea.
type LaterItem struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	Repo string    `json:"repo"`
	At   time.Time `json:"-"` // when it was kept: the record's time
}

// laterRemoved is a Later item taken off.
type laterRemoved struct {
	ID  string `json:"id"`
	Why string `json:"why"`
}

// NewLaterID is an id for an item kept now.
func NewLaterID(now time.Time) string {
	return strconv.FormatInt(now.UnixNano(), 36)
}

// LaterAdded records an item kept for later.
func (d *Desk) LaterAdded(it LaterItem) error { return d.append(RecLaterAdded, it) }

// LaterRemoved records an item that left: promoted to a Chat or deleted.
func (d *Desk) LaterRemoved(id, why string) error {
	return d.append(RecLaterRemoved, laterRemoved{ID: id, Why: why})
}

// LaterItems replays recs: the items kept and not removed since, oldest
// first.
func LaterItems(recs []DeskRecord) []LaterItem {
	var out []LaterItem
	for _, r := range recs {
		switch r.Type {
		case RecLaterAdded:
			var it LaterItem
			if json.Unmarshal(r.Data, &it) == nil && it.ID != "" && it.Text != "" {
				it.At = r.At
				out = append(out, it)
			}
		case RecLaterRemoved:
			var x laterRemoved
			if json.Unmarshal(r.Data, &x) == nil {
				out = slices.DeleteFunc(out, func(it LaterItem) bool { return it.ID == x.ID })
			}
		}
	}
	return out
}

// AppendLater keeps an item when no Desk is open: it takes the Desk's
// lock for the write, so it never races a Desk starting. ErrDeskDeaf says
// a Desk holds the lock, and it should be asked over its socket instead.
func AppendLater(private string, it LaterItem) error {
	dir := filepath.Join(private, DeskDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := lockDesk(filepath.Join(dir, deskLock))
	if errors.Is(err, errDeskHeld) {
		return ErrDeskDeaf
	}
	if err != nil {
		return err
	}
	defer unlockDesk(lock)
	log, err := os.OpenFile(filepath.Join(dir, DeskLog), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	if err := endLine(log); err != nil {
		return err
	}
	raw, err := json.Marshal(it)
	if err != nil {
		return err
	}
	line, err := json.Marshal(DeskRecord{At: time.Now().UTC(), Type: RecLaterAdded, Data: raw})
	if err != nil {
		return err
	}
	if _, err := log.Write(append(line, '\n')); err != nil {
		return err
	}
	return log.Sync()
}

// KeepForLater hands text to the live Desk to keep for later, kept in
// repo; or, with no Desk open, keeps it in the store itself and says so
// (direct is true). ErrDeskDeaf says a Desk is open but didn't take it;
// a *DeskRefused says it said no.
func KeepForLater(private, repo, text string, now time.Time) (direct bool, err error) {
	_, _, err = callAdd(private, DeskRequest{Method: MethodAdd, Repo: repo, Task: text, Later: true})
	if errors.Is(err, ErrNoDesk) {
		return true, AppendLater(private, LaterItem{ID: NewLaterID(now), Text: text, Repo: repo})
	}
	return false, err
}
