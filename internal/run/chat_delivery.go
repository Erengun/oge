package run

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/erengun/oge/internal/ledger"
)

// RecDelivered is a Chat log record: a cut's Candidate was delivered to
// the user's repository, and the next Task starts after its turns (D7).
const RecDelivered = "Delivered"

// ErrTamperAfterCut refuses delivering a cut whose turns have a Tamper
// event the cut's Run never carried: it was recorded late, after the cut
// was verified, and the next cut's Gate shows it.
var ErrTamperAfterCut = errors.New("a Tamper event was recorded after this cut was verified; it's in the next cut")

// Delivered marks the cut run as delivered (applied or taken as a
// branch): the next cut's Task, and the Tamper events that gate it, are
// the turns after its last one (D7). rev is the commit delivered: once
// applied, the next apply merges on it. The Chat goes on. A cut
// delivered after a later one was marked moves nothing back.
func (c *Chat) Delivered(run, how, rev string) error {
	c.supersedeMu.Lock()
	m, ok := c.cuts[run]
	moved := ok && m.turn > c.delivered
	if moved {
		c.delivered = m.turn
	}
	if ok && how == "apply" {
		c.applied, c.appliedRun = rev, m.dir
	}
	c.supersedeMu.Unlock()
	if !ok {
		return fmt.Errorf("run %s is no verified cut of this Chat", run)
	}
	if !moved {
		return nil
	}
	return c.log(RecDelivered, map[string]any{"run": run, "how": how, "turn": m.turn, "rev": rev, "resolved": m.resolved})
}

// Applied is the commit last applied to the user's tree from this Chat,
// "" before the first apply, and the directory of the Run whose
// repository holds it.
func (c *Chat) Applied() (rev, runDir string) {
	c.supersedeMu.Lock()
	defer c.supersedeMu.Unlock()
	return c.applied, c.appliedRun
}

// Deliverable refuses delivering the cut run when its turns since the
// last delivery have a Tamper event its Run's Ledger (in dir) lacks:
// delivery never moves the window past one no Gate showed. The cut is
// then cut again: the next cut covers its turns, and gates the event.
func (c *Chat) Deliverable(run, dir string) error {
	c.supersedeMu.Lock()
	m, from := c.cuts[run], c.delivered+1
	c.supersedeMu.Unlock()
	if m == nil {
		return fmt.Errorf("run %s is no verified cut of this Chat", run)
	}
	recs, err := c.chatRecords(RecTamperEvent, from, m.turn)
	if err != nil || len(recs) == 0 {
		return err
	}
	carried := map[string]bool{}
	rl, err := ledger.Replay(dir)
	if err != nil && !errors.Is(err, ledger.ErrTornTail) {
		return err
	}
	for _, r := range rl {
		if r.Type == RecTamperEvent {
			var t struct{ ID string }
			if json.Unmarshal(r.Data, &t) == nil {
				carried[t.ID] = true
			}
		}
	}
	for _, rec := range recs {
		var t struct{ ID string }
		if err := json.Unmarshal(rec, &t); err != nil {
			return err
		}
		if !carried[t.ID] {
			// Cut again: its turns are the next cut's. A cut in flight
			// covers them already.
			if c.cutMu.TryLock() {
				c.supersedeMu.Lock()
				if c.lastCut == m {
					c.lastCut = m.prev
				}
				c.supersedeMu.Unlock()
				c.cutMu.Unlock()
			}
			return ErrTamperAfterCut
		}
	}
	return nil
}
