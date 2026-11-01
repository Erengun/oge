//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/delivery"
)

// Two Chats in two repos (docs/ux/desk.md, Projects): Chat 1 in the
// fixture's repo, Chat 2 opened with /new <path> in another. Each one's
// Snapshot, Workspace and /apply are its own repo's, and nothing crosses:
// a Run of one repo never applies into the other.
func TestHomeChatsInTwoRepos(t *testing.T) {
	t.Parallel()
	fixed := "package fx\n\nfunc Add(a, b int) int { return a + b }\n"
	s, f := startChatSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "fixed Add", Writes: []multiturn.File{{Path: "add.go", Content: fixed}}},
	}}, nil)
	other := filepath.Join(filepath.Dir(f.repo), "beta")
	mkRepo(t, other)
	writeFile(t, filepath.Join(other, ".oge", "oge.toml"), []byte(fxConfig))
	writeFile(t, filepath.Join(other, "go.mod"), []byte("module fx\n\ngo 1.22\n"))
	writeFile(t, filepath.Join(other, "add.go"), []byte(fxBroken))
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "fixture")
	writeFile(t, filepath.Join(other, "add_test.go"), []byte(fxTest))

	s.composer()
	s.type_("fix Add\r")
	s.expect("oge › ✓ Accepted · 1 file · ")
	s.composer()
	s.type_("/apply\r")
	s.expect("oge › ✓ Applied ")
	s.composer()
	if got := read(t, filepath.Join(f.repo, "add.go")); got != fixed {
		t.Fatalf("Chat 1's /apply didn't land in its repo:\n%s", got)
	}
	if got := read(t, filepath.Join(other, "add.go")); got != fxBroken {
		t.Fatalf("Chat 1's /apply reached the other repo:\n%s", got)
	}

	s.type_("/new " + other + "\r")
	s.expectScreen("oge · beta")
	s.composer()
	// The same change is 1 file only on beta's own Snapshot: on the first
	// repo's, which has it applied, it would change nothing.
	s.type_("fix Add\r")
	s.expect("oge › ✓ Accepted · 1 file · ")
	s.composer()
	s.type_("/apply\r")
	s.expect("oge › ✓ Applied ")
	s.composer()
	if got := read(t, filepath.Join(other, "add.go")); got != fixed {
		t.Fatalf("Chat 2's /apply didn't land in its repo:\n%s", got)
	}
	s.quitChat(f)

	private := filepath.Join(f.state, "private")
	mine, _ := delivery.Recent(private, f.repo, 10)
	theirs, _ := delivery.Recent(private, other, 10)
	if len(mine) != 1 || len(theirs) != 1 {
		t.Fatalf("Runs: %d of the first repo, %d of beta; want one each", len(mine), len(theirs))
	}
	// The trust boundary under it: a Run of beta refuses the first repo.
	writeFile(t, filepath.Join(other, "add.go"), []byte(fxBroken))
	writeFile(t, filepath.Join(f.repo, "add.go"), []byte(fxBroken))
	_, err := delivery.ApplyOnto(theirs[0], f.repo, delivery.Choice{}, delivery.Base{})
	if err == nil || !strings.Contains(err.Error(), "run this inside that repository") {
		t.Errorf("a Run of beta applied into the first repo: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); string(b) != fxBroken {
		t.Errorf("the refused apply wrote:\n%s", b)
	}
}
