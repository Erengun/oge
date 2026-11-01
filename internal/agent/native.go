package agent

import "time"

// NativeSession is a conversation an agent stores itself, found by
// discovery (ADR-0025). Öge never owns its format, id or history; this is
// the read-only catalog row, held in memory and never persisted.
type NativeSession struct {
	Provider string // "claude", "codex"
	ID       string // the agent's own session id
	Profile  string // profile label such as "claude@work"; "" when the provider has none
	Cwd      string // the cwd the agent recorded
	Project  string // git root of Cwd on the Desk; "" until the caller associates it
	Title    string // "" when the agent recorded none
	// LastActive is the newest record's time, or the file's write time
	// when the records carry none.
	LastActive time.Time
	Path       string    // the agent's file for the session ("" when there is none)
	Written    time.Time // last write time of Path, for the two-writer rule
}
