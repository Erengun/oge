//go:build unix

package delivery

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A new file gets the user's umask, as any other tool's would.
func TestApplyRespectsTheUmask(t *testing.T) {
	user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
		os.WriteFile(filepath.Join(ws, "new.txt"), []byte("n\n"), 0o644)
	})
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	p, err := PlanApply(r, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.write(user); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(user, "new.txt"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("new.txt: %v %v", fi.Mode(), err)
	}
}
