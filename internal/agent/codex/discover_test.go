package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
)

// listWith runs ListSessions against the fake replaying script, with the
// recorded codex home moved into a real directory.
func listWith(t *testing.T, name string, cwds ...string) ([]agent.NativeSession, string, []map[string]any) {
	t.Helper()
	h := &harness{t: t}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	var b strings.Builder
	for _, r := range load(t, name) {
		line, _ := json.Marshal(r)
		b.WriteString(strings.ReplaceAll(string(line), "/home/user/.codex", filepath.ToSlash(home)) + "\n")
	}
	script := filepath.Join(dir, "script.jsonl")
	if err := os.WriteFile(script, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	h.log = filepath.Join(dir, "client.jsonl")
	// One rollout file exists, written just now.
	roll := filepath.Join(home, "sessions/2026/11/01")
	if err := os.MkdirAll(roll, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roll, "rollout-a.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Path: fakePath, Timeout: 10 * time.Second, CloseGrace: time.Second,
		Environ: func() []string {
			return append(os.Environ(), "OGE_FAKECODEX_SCRIPT="+script, "OGE_FAKECODEX_LOG="+h.log)
		}}
	got, reason := a.ListSessions(context.Background(), cwds)
	return got, reason, h.sent()["thread/list"]
}

func TestListSessionsPagesAndFilters(t *testing.T) {
	got, reason, calls := listWith(t, "thread_list.jsonl", "/home/user/proj")
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID[len(s.ID)-4:])
	}
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	// Newest first; the sub-agent thread is left out; two pages were read.
	if strings.Join(ids, " ") != "0001 0002 0003" {
		t.Fatalf("ids = %v", ids)
	}
	if len(calls) != 2 {
		t.Fatalf("thread/list calls = %d, want 2", len(calls))
	}
	p := calls[0]["params"].(map[string]any)
	if p["cwd"] != "/home/user/proj" || p["archived"] != false || p["sortDirection"] != "desc" || p["sortKey"] != "updated_at" {
		t.Errorf("params = %v", p)
	}
	if got, _ := json.Marshal(p["sourceKinds"]); string(got) != `["cli","vscode","appServer"]` {
		t.Errorf("sourceKinds = %s", got)
	}
	if calls[1]["params"].(map[string]any)["cursor"] != "cursor-1" {
		t.Errorf("second page params = %v", calls[1]["params"])
	}
	for _, s := range got {
		if s.Provider != "codex" || s.Cwd != "/home/user/proj" || s.LastActive.IsZero() || strings.Contains(s.Title, "PREVIEW") {
			t.Errorf("session = %+v", s)
		}
	}
	first, second := got[0], got[1]
	if first.Title != "fix the parser" || first.LastActive.Unix() != 1793000300 {
		t.Errorf("first = %+v", first)
	}
	if second.Title != "" {
		t.Errorf("an unnamed thread's title = %q, want none (never the preview)", second.Title)
	}
	if first.Written.IsZero() || !got[2].Written.IsZero() {
		t.Errorf("Written: existing rollout %v, missing rollout %v", first.Written, got[2].Written)
	}
}

func TestListSessionsWithoutThreadListIsEmptyNotAnError(t *testing.T) {
	got, reason, _ := listWith(t, "thread_list_unsupported.jsonl", "/home/user/proj")
	if len(got) != 0 || !strings.Contains(reason, "no thread/list") {
		t.Errorf("sessions %v, reason %q", got, reason)
	}
}

func TestListSessionsWithoutCodexSaysSo(t *testing.T) {
	a := &Adapter{Path: filepath.Join(t.TempDir(), "missing")}
	got, reason := a.ListSessions(context.Background(), []string{"/x"})
	if len(got) != 0 || reason == "" {
		t.Errorf("sessions %v, reason %q", got, reason)
	}
}
