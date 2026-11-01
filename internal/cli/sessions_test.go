package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

func TestSessionsInProjects(t *testing.T) {
	base := t.TempDir()
	mk := func(name string) string {
		d := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(d, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", d, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		return d
	}
	a, b := mk("a"), mk("b")
	all := []agent.NativeSession{
		{ID: "top", Cwd: a},
		{ID: "nested", Cwd: filepath.Join(a, "sub")},
		{ID: "other", Cwd: b},
		{ID: "gone", Cwd: filepath.Join(base, "deleted")},
		{ID: "nocwd"},
	}
	got := sessionsInProjects(all, []string{a})
	if len(got) != 2 || got[0].ID != "top" || got[1].ID != "nested" || got[0].Project != a || got[1].Project != a {
		t.Errorf("got %+v", got)
	}
}
