package ledger

import (
	"bytes"
	"errors"
	"fmt"
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

// A crash mid-Append leaves a partial last line: the records before it
// still verify, and the failure is marked ErrTornTail so readers can
// tell it from real corruption.
func TestReplayMarksATornTail(t *testing.T) {
	dir := t.TempDir()
	l, _ := Create(dir)
	for _, typ := range []string{"A", "B", "C"} {
		l.Append(typ, map[string]int{"n": 1})
	}
	l.Close()
	_, goodHead, err := ReplayHead(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, LedgerFile)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"format":1,"seq":3,"pre`)
	f.Close()

	recs, head, err := ReplayHead(dir)
	if !errors.Is(err, ErrTornTail) {
		t.Fatalf("got %v, want ErrTornTail", err)
	}
	if len(recs) != 3 || head != goodHead {
		t.Fatalf("torn tail should keep the verified prefix: %d recs, head %q", len(recs), head)
	}
}

// A tear can land on a '}' boundary: the partial line parses as JSON but
// fails the seq/prev check. That is still a torn tail, not an edited
// record — the difference is that the file is unterminated.
func TestReplayMarksATornTailOnAClosingBrace(t *testing.T) {
	dir := t.TempDir()
	l, _ := Create(dir)
	l.Append("A", map[string]int{"n": 1})
	l.Close()
	path := filepath.Join(dir, LedgerFile)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"format":1,"seq":9}`)
	f.Close()

	if _, _, err := ReplayHead(dir); !errors.Is(err, ErrTornTail) {
		t.Fatalf("got %v, want ErrTornTail", err)
	}
}

// A bad line that isn't the file's last — or any failure in a
// newline-terminated file — is real corruption, not a torn write.
func TestReplayKeepsMidFileCorruptionAHardError(t *testing.T) {
	for name, mutate := range map[string]func(b []byte) []byte{
		"mid-file":         func(b []byte) []byte { return bytes.Replace(b, []byte(`"n":1`), []byte(`"n":9`), 1) },
		"terminated-bad":   func(b []byte) []byte { return append(b, []byte("garbage line\n")...) },
		"terminated-chain": func(b []byte) []byte { return append(b, []byte(`{"format":1,"seq":9}`+"\n")...) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			l, _ := Create(dir)
			for _, typ := range []string{"A", "B", "C"} {
				l.Append(typ, map[string]int{"n": 1})
			}
			l.Close()
			path := filepath.Join(dir, LedgerFile)
			b, _ := os.ReadFile(path)
			os.WriteFile(path, mutate(b), 0o600)
			if _, _, err := ReplayHead(dir); err == nil || errors.Is(err, ErrTornTail) {
				t.Fatalf("got %v, want a non-torn error", err)
			}
		})
	}
}

// Open refuses a torn Ledger: appending after the partial line would
// splice it into a mid-file garbage record.
func TestOpenRefusesATornLedger(t *testing.T) {
	dir := t.TempDir()
	l, _ := Create(dir)
	l.Append("A", map[string]int{"n": 1})
	l.Close()
	f, _ := os.OpenFile(filepath.Join(dir, LedgerFile), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"format":1,"seq":1,"pre`)
	f.Close()
	if _, err := Open(dir); !errors.Is(err, ErrTornTail) {
		t.Fatalf("got %v, want ErrTornTail", err)
	}
}

// A complete, valid last record that merely lacks the trailing newline
// still verifies; it isn't a torn record (Open refuses to append to it).
func TestReplayKeepsAnUnterminatedValidRecord(t *testing.T) {
	dir := t.TempDir()
	l, _ := Create(dir)
	l.Append("A", map[string]int{"n": 1})
	l.Close()
	path := filepath.Join(dir, LedgerFile)
	b, _ := os.ReadFile(path)
	os.WriteFile(path, bytes.TrimSuffix(b, []byte("\n")), 0o600)

	recs, _, err := ReplayHead(dir)
	if err != nil || len(recs) != 1 {
		t.Fatalf("got %d recs, %v", len(recs), err)
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

func TestPutAllRepeatedContentWritesOnce(t *testing.T) {
	s, err := OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var bs [][]byte
	for i := range 40 {
		bs = append(bs, []byte(fmt.Sprintf("blob %d\n", i%3)))
	}
	ids, err := s.PutAll(bs)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if want, _ := s.Put(bs[i]); id != want {
			t.Fatalf("id %d = %s, want %s", i, id, want)
		}
		if b, err := s.Get(id); err != nil || !bytes.Equal(b, bs[i]) {
			t.Fatalf("get %d: %q %v", i, b, err)
		}
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
