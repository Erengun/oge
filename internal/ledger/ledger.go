package ledger

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Format is the record format version this binary writes and reads.
const Format = 1

// Record is one Ledger line. Prev is the hex SHA-256 of the previous line's
// bytes ("" for the first), which chains the Ledger.
type Record struct {
	Format int             `json:"format"`
	Seq    int             `json:"seq"`
	Prev   string          `json:"prev"`
	At     time.Time       `json:"at"`
	Type   string          `json:"type"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Ledger appends records durably to one Run's ledger.jsonl.
type Ledger struct {
	f    *os.File
	seq  int
	prev string
}

// LedgerFile is a Run's Ledger, relative to its directory.
const LedgerFile = "ledger.jsonl"

// Create starts a new Ledger in runDir, which must not have one.
func Create(runDir string) (*Ledger, error) {
	f, err := os.OpenFile(filepath.Join(runDir, LedgerFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syncDir(runDir); err != nil {
		f.Close()
		return nil, err
	}
	return &Ledger{f: f}, nil
}

// Append writes a record of type typ with data, fsyncs it, and only then
// returns: the record's effect may count as durable after Append.
func (l *Ledger) Append(typ string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	line, err := json.Marshal(Record{Format: Format, Seq: l.seq, Prev: l.prev, At: time.Now().UTC(), Type: typ, Data: raw})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := l.f.Write(line); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	l.seq++
	l.prev = hashHex(line)
	return nil
}

// Close closes the Ledger file.
func (l *Ledger) Close() error { return l.f.Close() }

// ErrBrokenChain means a record doesn't follow the one before it.
var ErrBrokenChain = errors.New("ledger hash chain is broken")

// Replay reads every record of runDir's Ledger, verifying the chain and the
// format version.
func Replay(runDir string) ([]Record, error) {
	recs, _, err := ReplayHead(runDir)
	return recs, err
}

// ReplayHead is Replay that also returns the head: the hex SHA-256 of the
// last record's line, which the next record's Prev would name. It
// identifies the Ledger's state; it is no proof the Ledger wasn't rewritten.
func ReplayHead(runDir string) ([]Record, string, error) {
	b, err := os.ReadFile(filepath.Join(runDir, LedgerFile))
	if err != nil {
		return nil, "", err
	}
	var recs []Record
	prev := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		line := append(sc.Bytes(), '\n')
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return recs, prev, fmt.Errorf("record %d: %w", len(recs), err)
		}
		if r.Format > Format {
			return recs, prev, fmt.Errorf("record %d has format %d, newer than this oge reads (%d)", len(recs), r.Format, Format)
		}
		if r.Seq != len(recs) || r.Prev != prev {
			return recs, prev, fmt.Errorf("record %d: %w", len(recs), ErrBrokenChain)
		}
		recs = append(recs, r)
		prev = hashHex(line)
	}
	return recs, prev, sc.Err()
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// syncDir fsyncs a directory so a new or renamed entry in it is durable.
// Windows can't open a directory for syncing; NTFS makes the entry durable
// with the file's own flush.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
