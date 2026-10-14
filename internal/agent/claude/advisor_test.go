package claude

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

// The advisor's Launch profile only reads (docs/ux/advisor.md): its
// --tools are the reading ones, its policy denies every write, and its
// questions are cancelled, never put to the human.
func TestAdvisorProfileOnlyReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the policy reasons about POSIX paths; Runs are refused on Windows (ADR-0017)")
	}
	prof, err := profileFor("advisor")
	if err != nil {
		t.Fatal(err)
	}
	args, err := prof.args(agent.LaunchSpec{Role: "advisor", Workspace: t.TempDir(), Model: "opus"}, "id", nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--tools Read,Glob,Grep ") || !strings.Contains(joined, "--permission-mode default") {
		t.Errorf("advisor args: %s", joined)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// ws is the advisor's clone; live is the Chat Workspace it is denied.
	ws, live := filepath.Join(dir, "clone"), filepath.Join(dir, "live")
	spec := agent.LaunchSpec{Role: "advisor", Workspace: ws, DenyRead: []string{live}}
	p := newPolicy(spec, "abc")
	in := func(k, v string) json.RawMessage { b, _ := json.Marshal(map[string]string{k: v}); return b }
	for _, c := range []struct {
		tool  string
		input json.RawMessage
		want  string
	}{
		{"Read", in("file_path", ws+"/a.go"), "allow"},
		{"Grep", in("pattern", "Add"), "allow"},
		{"Read", in("file_path", "a.go"), "allow"},
		{"Read", in("file_path", "/etc/passwd"), "deny"},
		{"Read", in("file_path", live+"/a.go"), "deny"},
		{"Read", in("file_path", "../live/a.go"), "deny"},
		{"Glob", json.RawMessage(`{"pattern":"*.go","path":"/etc"}`), "deny"},
		{"Glob", json.RawMessage(`{"pattern":"*.go","path":"` + live + `"}`), "deny"},
		{"mcp__x__y", json.RawMessage(`{}`), "deny"},
		{"WebFetch", in("url", "https://example.com"), "deny"},
		{"Task", in("prompt", "write a file"), "deny"},
		{"Write", in("file_path", ws+"/a.go"), "deny"},
		{"Edit", in("file_path", ws+"/a.go"), "deny"},
		{"NotebookEdit", in("notebook_path", ws+"/a.ipynb"), "deny"},
		{"Bash", in("command", "go test ./..."), "deny"},
		{"AskUserQuestion", in("question", "which?"), "cancel"},
	} {
		if d := p.decide(c.tool, c.input); d.Decision != c.want {
			t.Errorf("%s: %s (%s), want %s", c.tool, d.Decision, d.Reason, c.want)
		}
	}
	// The sandbox's rules deny the live Workspace too.
	settings := ""
	if args, err = prof.args(spec, "id", nil); err != nil {
		t.Fatal(err)
	}
	for i, a := range args {
		if a == "--settings" && i+1 < len(args) {
			settings = args[i+1]
		}
	}
	var set struct {
		Permissions struct{ Deny []string } `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(settings), &set); err != nil {
		t.Fatalf("settings %q: %v", settings, err)
	}
	if want := "Read(/" + live + "/**)"; !oneOf(want, set.Permissions.Deny) {
		t.Errorf("permissions.deny %v lacks %s", set.Permissions.Deny, want)
	}
	if !New().AdvisorReadOnly() || New().StrongestModel() != "opus" {
		t.Error("the claude adapter's advisor is not read-only on opus")
	}
}
