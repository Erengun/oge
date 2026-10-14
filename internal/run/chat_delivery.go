package run

import "fmt"

// RecDelivered is a Chat log record: a cut's Candidate was delivered to
// the user's repository, and the next Task starts after its turns (D7).
const RecDelivered = "Delivered"

// Delivered marks the cut run as delivered (applied or taken as a
// branch): the next cut's Task, and the Tamper events that gate it, are
// the turns after its last one (D7). The Chat goes on. A cut delivered
// after a later one was already marked moves nothing back.
func (c *Chat) Delivered(run, how string) error {
	c.supersedeMu.Lock()
	to, ok := c.cutTo[run]
	moved := ok && to > c.delivered
	if moved {
		c.delivered = to
	}
	c.supersedeMu.Unlock()
	if !ok {
		return fmt.Errorf("run %s is no verified cut of this Chat", run)
	}
	if !moved {
		return nil
	}
	return c.log(RecDelivered, map[string]any{"run": run, "how": how, "turn": to})
}
