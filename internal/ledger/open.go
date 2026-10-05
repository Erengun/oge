package ledger

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// Open continues the Ledger of a finished Run, for the records written
// after it ends (a Delivery, ADR-0014). The whole chain is checked first;
// a broken or torn Ledger is refused, never repaired.
// TODO(#54-decision): ADR-0014's torn-tail recovery isn't done here; a
// Ledger whose last record is torn can't take a Delivery.
func Open(runDir string) (*Ledger, error) {
	path := filepath.Join(runDir, LedgerFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	recs, err := Replay(runDir)
	if err != nil {
		return nil, err
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return nil, fmt.Errorf("%s ends in a torn record", path)
	}
	prev := ""
	if n := bytes.LastIndexByte(b[:max(len(b)-1, 0)], '\n'); len(recs) > 0 {
		prev = hashHex(b[n+1:])
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Ledger{f: f, seq: len(recs), prev: prev}, nil
}
