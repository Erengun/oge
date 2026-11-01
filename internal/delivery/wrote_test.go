package delivery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// What an apply wrote is named by each path's git blob id as written: a
// merged write's bytes, not the Candidate's, and a deleted path has none.
func TestPlanWroteNamesTheBytesWritten(t *testing.T) {
	user, r := planFixture(t, map[string]string{"m.txt": "one\ntwo\nthree\n", "gone.txt": "x\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, "m.txt"), []byte("one\ntwo\nTHREE\n"), 0o644)
		os.Remove(filepath.Join(ws, "gone.txt"))
		os.WriteFile(filepath.Join(ws, "new.sh"), []byte("#!/bin/sh\n"), 0o755)
	})
	// The user's own edit since the Snapshot: the write is merged.
	os.WriteFile(filepath.Join(user, "m.txt"), []byte("ONE\ntwo\nthree\n"), 0o644)
	p, err := PlanApply(r, user)
	if err != nil || len(p.Conflicts) > 0 || p.Merged != 1 {
		t.Fatalf("plan %+v %v", p, err)
	}
	if err := p.write(user); err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, w := range p.Wrote() {
		got[w.Path] = [2]string{w.Mode, w.OID}
	}
	hash := func(rel string) string {
		out, err := exec.Command("git", "hash-object", "--no-filters", filepath.Join(user, rel)).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	want := map[string][2]string{
		"m.txt":    {"100644", hash("m.txt")},
		"new.sh":   {"100755", hash("new.sh")},
		"gone.txt": {"", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("wrote %v, want %v", got, want)
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s: %v, want %v", p, got[p], w)
		}
	}
}
