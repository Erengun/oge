package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// fakeRun writes a Run directory whose Ledger says it started on source.
func fakeRun(t *testing.T, private, id, source string) string {
	t.Helper()
	dir := filepath.Join(private, "runs", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Append(run.RecRunStarted, map[string]any{"run": id, "source": source}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// With no Run id, a newest Run of this repository that can't be read is
// refused, never skipped for an older one.
func TestFindRefusesAnUnreadableNewestRun(t *testing.T) {
	private, repo := t.TempDir(), t.TempDir()
	fakeRun(t, private, "20261005T090000-aaaaaa", repo)
	newest := fakeRun(t, private, "20261005T100000-bbbbbb", repo)
	f, _ := os.OpenFile(filepath.Join(newest, ledger.LedgerFile), os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"torn`)
	f.Close()

	r, err := Find(private, repo, "")
	if err == nil || !IsRefused(err) || !strings.Contains(err.Error(), "20261005T100000-bbbbbb") || !strings.Contains(err.Error(), "can't be read") {
		t.Fatalf("Find = %v, %v", r, err)
	}
	// Another repository's Runs don't block this one.
	other := t.TempDir()
	fakeRun(t, private, "20261005T110000-cccccc", other)
	if r, err := Find(private, other, ""); err != nil || r.ID != "20261005T110000-cccccc" {
		t.Fatalf("Find(other) = %v, %v", r, err)
	}
}
