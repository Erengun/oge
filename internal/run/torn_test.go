package run

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/erengun/oge/internal/ledger"
)

// tearLedger appends a partial last line, as a crash mid-Append would.
func tearLedger(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ledger.LedgerFile), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"format":1,"seq":9,"pre`); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// A torn Chat ledger still yields the records before the tear: a cut's
// TamperEvent scan doesn't lose the Chat to one dead write.
func TestChatRecordsReadATornChatLedger(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(RecTamperEvent, map[string]any{"id": "t1", "turn": 2}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	tearLedger(t, dir)

	c := &Chat{Dir: dir}
	recs, err := c.chatRecords(RecTamperEvent, 1, 9)
	if err != nil || len(recs) != 1 {
		t.Fatalf("got %d records, %v", len(recs), err)
	}
}

// Deliverable reads a cut Run's ledger for carried Tamper events; a torn
// tail there doesn't make the cut undeliverable.
func TestDeliverableReadsATornRunLedger(t *testing.T) {
	chatDir := t.TempDir()
	l, err := ledger.Create(chatDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(RecTamperEvent, map[string]any{"id": "t1", "turn": 2}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	runDir := t.TempDir()
	rl, err := ledger.Create(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := rl.Append(RecTamperEvent, map[string]any{"id": "t1"}); err != nil {
		t.Fatal(err)
	}
	rl.Close()
	tearLedger(t, runDir)

	c := &Chat{Dir: chatDir, cuts: map[string]*cutMark{"r1": {turn: 2}}}
	if err := c.Deliverable("r1", runDir); err != nil {
		t.Fatal(err)
	}
}
