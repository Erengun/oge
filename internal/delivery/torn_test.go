package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// tear appends a partial last line, as a crash mid-Append would leave.
func tear(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ledger.LedgerFile), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"format":1,"seq":2,"pre`); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// A Ledger torn at its last record still Loads: the verified records
// before the tear stand, and the Run is flagged Torn.
func TestLoadReadsATornLedger(t *testing.T) {
	private, repo := t.TempDir(), t.TempDir()
	dir := fakeRun(t, private, "20261005T090000-aaaaaa", repo)
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(run.RecRunEnded, map[string]any{"outcome": run.Accepted, "candidate": "c0ffee"}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	tear(t, dir)

	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Torn || r.Outcome != run.Accepted || r.Candidate != "c0ffee" {
		t.Fatalf("got %+v", r)
	}
}

// A torn Run's Candidate can't be delivered: the Ledger's end can't be
// trusted, so its refusal names the torn Ledger and oge diff.
func TestAuthorizeRefusesATornRun(t *testing.T) {
	r := &Run{ID: "r1", Dir: t.TempDir(), Candidate: "c0ffee", Outcome: run.Accepted, Torn: true}
	err := Authorize(r, "")
	if err == nil || !IsRefused(err) || !strings.Contains(err.Error(), "Ledger is torn") || !strings.Contains(err.Error(), "oge diff r1") {
		t.Fatalf("got %v", err)
	}
}

// A Ledger torn at its very first record has nothing verified to read:
// Load still fails, and Find refuses the newest Run rather than silently
// delivering an older one.
func TestLoadFailsATornLedgerWithNoRecords(t *testing.T) {
	private, repo := t.TempDir(), t.TempDir()
	fakeRun(t, private, "20261005T090000-aaaaaa", repo)
	newest := filepath.Join(private, "runs", "20261005T100000-bbbbbb")
	if err := os.MkdirAll(newest, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Create(newest)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	tear(t, newest)

	if _, err := Load(newest); err == nil {
		t.Fatal("a zero-record torn Ledger loaded")
	}
	if _, err := Find(private, repo, ""); err == nil || !IsRefused(err) || !strings.Contains(err.Error(), "can't be read") {
		t.Fatalf("Find = %v", err)
	}
}

// Find and Recent return a torn Run rather than refusing or skipping it:
// oge diff, oge receipt and /runs stay usable on it.
func TestFindAndRecentReturnATornRun(t *testing.T) {
	private, repo := t.TempDir(), t.TempDir()
	fakeRun(t, private, "20261005T090000-aaaaaa", repo)
	newest := fakeRun(t, private, "20261005T100000-bbbbbb", repo)
	tear(t, newest)

	r, err := Find(private, repo, "")
	if err != nil || r.ID != "20261005T100000-bbbbbb" || !r.Torn {
		t.Fatalf("Find = %v, %v", r, err)
	}
	if r, err := Find(private, repo, "20261005T100000"); err != nil || !r.Torn {
		t.Fatalf("Find(id) = %v, %v", r, err)
	}
	rs, err := Recent(private, repo, 5)
	if err != nil || len(rs) != 2 || !rs[0].Torn {
		t.Fatalf("Recent = %v, %v", rs, err)
	}
}
