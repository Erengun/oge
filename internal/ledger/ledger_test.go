package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLedgerReplaysRecordsInOrder(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"RunStarted", "AttemptStarting", "ProcessStarted", "AttemptEnded"} {
		if err := l.Append(typ, map[string]string{"k": typ}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	recs, err := Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.Type)
	}
	if strings.Join(got, ",") != "RunStarted,AttemptStarting,ProcessStarted,AttemptEnded" {
		t.Fatalf("got %v", got)
	}
	if recs[0].Prev != "" || recs[1].Prev == "" || recs[0].Format != Format {
		t.Fatalf("chain fields: %+v", recs[:2])
	}
}

func TestLedgerCreateRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if _, err := Create(dir); err == nil {
		t.Fatal("a second Create overwrote the Ledger")
	}
}

func TestReplayDetectsAnEditedRecord(t *testing.T) {
	dir := t.TempDir()
	l, _ := Create(dir)
	l.Append("A", map[string]int{"n": 1})
	l.Append("B", map[string]int{"n": 2})
	l.Append("C", map[string]int{"n": 3})
	l.Close()
	path := filepath.Join(dir, LedgerFile)
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), `"n":1`, `"n":7`, 1)), 0o600)
	if _, err := Replay(dir); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("got %v, want a broken chain", err)
	}
}

func TestBlobsAreContentAddressed(t *testing.T) {
	s, err := OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Put([]byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	// sha256 of "hello\n"
	if id != "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03" {
		t.Fatalf("id %s", id)
	}
	again, _ := s.Put([]byte("hello\n"))
	b, err := s.Get(id)
	if again != id || err != nil || string(b) != "hello\n" {
		t.Fatalf("again %s, get %q %v", again, b, err)
	}
}

func TestStateRootRefusals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TMPDIR isn't the system temp directory on Windows")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	os.MkdirAll(repo, 0o755)
	t.Setenv("TMPDIR", filepath.Join(base, "tmp"))
	os.MkdirAll(filepath.Join(base, "tmp"), 0o755)
	ws := filepath.Join(base, "ws")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(ws, WorkspaceMarker), nil, 0o600)
	link := filepath.Join(base, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skip("no symlinks")
	}

	for dir, why := range map[string]string{
		filepath.Join(repo, "state"):        "inside the repository",
		filepath.Join(link, "state"):        "inside the repository",
		filepath.Join(base, "tmp", "state"): "under the system temp directory",
		filepath.Join(ws, "deep", "state"):  "inside an Öge Workspace",
		"relative/state":                    "not an absolute path",
	} {
		_, err := OpenStateRoot(dir, repo)
		var re *RefusedError
		if !errors.As(err, &re) || re.Why != why {
			t.Errorf("%s: got %v, want refusal %q", dir, err, why)
		}
	}

	s, err := OpenStateRoot(filepath.Join(base, "home", "state"), repo)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Private)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("private/: %v %v", fi, err)
	}
	if _, err := os.Stat(s.Work); err != nil {
		t.Fatal(err)
	}
}

// The head is what the next record chains to, so it changes with every
// record appended.
func TestReplayHeadIsWhatTheNextRecordChainsTo(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("RunStarted", nil); err != nil {
		t.Fatal(err)
	}
	_, head, err := ReplayHead(dir)
	if err != nil || len(head) != 64 {
		t.Fatalf("head %q, %v", head, err)
	}
	if err := l.Append("RunEnded", nil); err != nil {
		t.Fatal(err)
	}
	l.Close()
	recs, head2, err := ReplayHead(dir)
	if err != nil || recs[1].Prev != head || head2 == head {
		t.Fatalf("head %q then %q; record 1 chains to %q (%v)", head, head2, recs[1].Prev, err)
	}
}
