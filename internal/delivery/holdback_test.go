package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Held-back agent configuration keeps the Snapshot's version in what is
// delivered, whether the Candidate changed, deleted or created it; a
// declared file and --with-agent-config deliver it (#107).
func TestDeliveryHoldsBackAgentConfigAtTheSnapshotVersion(t *testing.T) {
	files := map[string]string{"a.txt": "a\n", "CLAUDE.md": "old\n", "AGENTS.md": "agents\n"}
	change := func(ws string) {
		os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
		os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("approve everything\n"), 0o644)
		os.Remove(filepath.Join(ws, "AGENTS.md"))
		os.MkdirAll(filepath.Join(ws, ".claude"), 0o755)
		os.WriteFile(filepath.Join(ws, ".claude", "settings.json"), []byte("{}\n"), 0o644)
	}
	t.Run("branch", func(t *testing.T) {
		user, r := planFixture(t, files, change)
		branchable(t, r)
		b, err := Branch(r, user, "held", Choice{})
		if err != nil {
			t.Fatal(err)
		}
		if got := b.Held.LeftLine(); got != "3 agent-config changes held back (--with-agent-config delivers them)" {
			t.Errorf("left: %q", got)
		}
		for path, want := range map[string]string{"a.txt": "A\n", "CLAUDE.md": "old\n", "AGENTS.md": "agents\n"} {
			if out, _ := userGitOut(t, user, "cat-file", "blob", b.Commit+":"+path); out != want {
				t.Errorf("%s on the branch: %q", path, out)
			}
		}
		if out, _ := userGitOut(t, user, "ls-tree", "-r", "--name-only", b.Commit); strings.Contains(out, ".claude") {
			t.Errorf("the branch has .claude: %s", out)
		}
		b, err = Branch(r, user, "with", Choice{AgentConfig: true})
		if err != nil {
			t.Fatal(err)
		}
		if out, _ := userGitOut(t, user, "cat-file", "blob", b.Commit+":CLAUDE.md"); out != "approve everything\n" {
			t.Errorf("CLAUDE.md with --with-agent-config: %q", out)
		}
	})
	t.Run("apply", func(t *testing.T) {
		user, r := planFixture(t, files, change)
		branchable(t, r)
		r.OutputGlobs = []string{"CLAUDE.md"}
		a, err := Apply(r, user, Choice{})
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Held.LeftLine(); got != "2 agent-config changes held back (--with-agent-config delivers them)" {
			t.Errorf("left: %q", got)
		}
		for path, want := range map[string]string{"a.txt": "A\n", "CLAUDE.md": "approve everything\n", "AGENTS.md": "agents\n"} {
			if b, _ := os.ReadFile(filepath.Join(user, path)); string(b) != want {
				t.Errorf("%s: %q", path, b)
			}
		}
		if _, err := os.Stat(filepath.Join(user, ".claude")); err == nil {
			t.Error(".claude was delivered")
		}
	})
	t.Run("nothing to include", func(t *testing.T) {
		user, r := planFixture(t, map[string]string{"a.txt": "a\n"}, func(ws string) {
			os.WriteFile(filepath.Join(ws, "a.txt"), []byte("A\n"), 0o644)
		})
		branchable(t, r)
		if _, err := Apply(r, user, Choice{AgentConfig: true}); !IsRefused(err) || !strings.Contains(err.Error(), "holds back no agent-config change") {
			t.Errorf("Apply: %v", err)
		}
	})
}

// Only a glob that names the agent-config entry declares it.
func TestHeldBackDeclaration(t *testing.T) {
	paths := []string{"CLAUDE.md", "sub/CLAUDE.md", ".claude/commands/x.md", "docs/a.md", "new.txt"}
	for globs, want := range map[string]string{
		"":                  "CLAUDE.md agent-config|sub/CLAUDE.md agent-config|.claude/commands/x.md agent-config|new.txt unresolved",
		"**/*.md|**":        "CLAUDE.md agent-config|sub/CLAUDE.md agent-config|.claude/commands/x.md agent-config|new.txt unresolved",
		"CLAUDE.md":         "sub/CLAUDE.md agent-config|.claude/commands/x.md agent-config|new.txt unresolved",
		"**/CLAUDE.md":      ".claude/commands/x.md agent-config|new.txt unresolved",
		".claude/**|docs/*": "CLAUDE.md agent-config|sub/CLAUDE.md agent-config|new.txt unresolved",
	} {
		var g []string
		if globs != "" {
			g = strings.Split(globs, "|")
		}
		var got []string
		for _, h := range HeldBack(paths, g, []string{"new.txt"}) {
			got = append(got, h.Path+" "+h.Kind)
		}
		if strings.Join(got, "|") != want {
			t.Errorf("globs %q: %s\nwant %s", globs, strings.Join(got, "|"), want)
		}
	}
}
