package ledger

import (
	"strings"
	"testing"
)

// Open continues a finished Ledger so later records (a Delivery) keep the
// hash chain Replay checks.
func TestOpenAppendsToAnExistingLedger(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Append("RunStarted", map[string]int{"n": 1})
	l.Append("RunEnded", map[string]int{"n": 2})
	l.Close()

	l, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("Delivery", map[string]string{"kind": "apply"}); err != nil {
		t.Fatal(err)
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
	if strings.Join(got, ",") != "RunStarted,RunEnded,Delivery" {
		t.Fatalf("got %v", got)
	}
}

func TestOpenRefusesAMissingOrBrokenLedger(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("opened a Ledger that doesn't exist")
	}
}
