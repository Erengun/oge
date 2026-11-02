package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
)

// stoppedChat is a Chat oge left behind: one turn sent and settled, then
// the process gone (Stop keeps the Workspace), as the records show it.
func stoppedChat(t *testing.T) (ResumeParams, string) {
	t.Helper()
	ad := fakeClaude(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "one", Writes: []multiturn.File{{Path: "b.go", Content: "package fx\n"}}},
		{Reply: "two"},
	}})
	f := &followFixture{user: t.TempDir()}
	f.git(t, "init", "-q", "-b", "main")
	f.write(t, "add.go", "package fx\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "init")
	d := t.TempDir()
	state := &ledger.StateRoot{Dir: d, Private: filepath.Join(d, "private"), Work: filepath.Join(d, "work")}
	agents := map[string]agent.Adapter{"claude": ad}
	c, err := OpenChat(context.Background(), ChatParams{Repo: f.user, Frozen: chatFrozen("implementer"),
		Agents: agents, State: state, Getenv: chatOn})
	if err != nil {
		t.Fatal(err)
	}
	settledOf(t, drain(t, send(t, c, "first"), nil))
	if err := c.Stop("quit"); err != nil {
		t.Fatal(err)
	}
	rc := &RestoredChat{ID: c.ID, N: 1, Repo: f.user, Dir: c.Dir, Work: c.work}
	return ResumeParams{Chat: rc, Agents: agents, State: state, Getenv: chatOn}, c.Workspace
}

// Resuming starts no agent; the first message reopens one Session, with the
// re-brief, and the turn settles. A missing Workspace is rebuilt first.
func TestResumeChatReopensLazilyAndRebuildsTheWorkspace(t *testing.T) {
	p, ws := stoppedChat(t)
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	res, err := ResumeChat(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Chat
	t.Cleanup(func() { c.Close() })
	if !res.Rebuilt {
		t.Error("the missing Workspace wasn't rebuilt")
	}
	if _, err := os.Stat(filepath.Join(ws, "b.go")); err != nil {
		t.Errorf("the rebuilt Workspace lacks the turn's work: %v", err)
	}
	if c.sess != nil {
		t.Fatal("resuming opened a Session")
	}
	if n := len(recordsOf(t, c, RecSessionReopened)); n != 0 {
		t.Fatalf("resuming reopened a Session: %d", n)
	}
	evs := drain(t, send(t, c, "second"), nil)
	if s := settledOf(t, evs); s.Failure != "" || said(evs) != "one" {
		t.Fatalf("the turn after the restore: %+v", evs)
	}
	if r := recordsOf(t, c, RecSessionReopened); len(r) != 1 || r[0]["turn"] != float64(2) {
		t.Errorf("SessionReopened %v", r)
	}
	if r := recordsOf(t, c, RecSessionRestored); len(r) != 1 || r[0]["workspace_rebuilt"] != true {
		t.Errorf("SessionRestored %v", r)
	}
	if types := strings.Join(typesOf(t, c), " "); !strings.Contains(types, RecSessionRestored+" "+RecSessionReopened+" "+RecTurnStarted) {
		t.Errorf("the records out of order: %s", types)
	}
}

// A changed Launch profile is found before anything opens, and a restore
// goes on only when the human typed reopen.
func TestResumeChatSettingsChangedNeedsTypedReopen(t *testing.T) {
	p, _ := stoppedChat(t)
	if ch, err := RestoreChanges(p); err != nil || len(ch) != 0 {
		t.Fatalf("unchanged settings: %v %v", ch, err)
	}
	now := chatFrozen("implementer")
	now.Stages[0].Model = "other-model"
	p.Now = now
	ch, err := RestoreChanges(p)
	if err != nil || len(ch) != 1 || !strings.Contains(ch[0], "model") {
		t.Fatalf("changes %v %v", ch, err)
	}
	if _, err := ResumeChat(context.Background(), p); !errors.Is(err, ErrRestoreRefused) {
		t.Fatalf("a changed Chat resumed without reopen: %v", err)
	}
	p.Reopen = true
	if _, err := ResumeChat(context.Background(), p); !errors.Is(err, ErrRestoreRefused) {
		t.Fatalf("reopen over changes the card didn't show: %v", err)
	}
	p.Shown = ch
	res, err := ResumeChat(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Chat.Close() })
	if r := recordsOf(t, res.Chat, RecSessionRestored); len(r) != 1 || r[0]["accepted"] != true {
		t.Errorf("SessionRestored %v", r)
	}
}

// An accepted reopen is the Chat's own settings from then on: a later
// restore with the same settings launches under them, not the first blob.
func TestResumeChatAcceptedReopenSurvivesTheNextRestore(t *testing.T) {
	p, _ := stoppedChat(t)
	now := chatFrozen("implementer")
	now.Stages[0].Model = "other-model"
	p.Now = now
	ch, _ := RestoreChanges(p)
	p.Reopen, p.Shown = true, ch
	res, err := ResumeChat(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Chat
	settledOf(t, drain(t, send(t, c, "second"), nil))
	if err := c.Stop("quit"); err != nil {
		t.Fatal(err)
	}
	p.Reopen, p.Shown = false, nil
	res2, err := ResumeChat(context.Background(), p)
	if err != nil {
		t.Fatalf("the accepted settings asked again: %v", err)
	}
	t.Cleanup(func() { res2.Chat.Close() })
	if res2.Chat.stage.Model != "other-model" {
		t.Errorf("restored under %q, not the accepted model", res2.Chat.stage.Model)
	}
	if len(res2.Changes) != 0 {
		t.Errorf("changes %v", res2.Changes)
	}
}

// A turn that never settled and left edits: they are scope-checked and kept
// as that turn's commit; the records say so in order.
func TestResumeChatFoundOnRestoreKeepsScopedEdits(t *testing.T) {
	p, ws := stoppedChat(t)
	// Pretend turn 2 started and never settled.
	c0 := p.Chat
	l, err := ledger.Open(c0.Dir)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := ledger.OpenBlobs(c0.Dir)
	if err != nil {
		t.Fatal(err)
	}
	txt, err := blobs.Put([]byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(RecTurnStarted, map[string]any{"chat": c0.ID, "turn": 2, "by": "human", "text": txt}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if err := os.WriteFile(filepath.Join(ws, "c.go"), []byte("package fx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ResumeChat(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Chat.Close() })
	if !res.Found {
		t.Fatal("the unsettled turn wasn't found")
	}
	r := recordsOf(t, res.Chat, RecTurnInterrupted)
	if len(r) != 1 || r[0]["why"] != "found_on_restore" || r[0]["commit"] == "" {
		t.Fatalf("TurnInterrupted %v", r)
	}
	types := strings.Join(typesOf(t, res.Chat), " ")
	if !strings.Contains(types, RecScopeObserved+" "+RecScopeReverted) || !strings.Contains(types, RecTurnInterrupted+" "+RecSessionRestored) {
		t.Errorf("records out of order: %s", types)
	}
	if got, ok := res.Chat.repo.RefCommit("refs/oge/chat/turns/2"); !ok || got != r[0]["commit"] {
		t.Errorf("turn 2 ref %q %v", got, ok)
	}
}

// A repo that is gone leaves the Chat read-only.
func TestResumeChatRepoGoneIsRefused(t *testing.T) {
	p, _ := stoppedChat(t)
	p.Chat.Repo = filepath.Join(t.TempDir(), "nope")
	if _, err := ResumeChat(context.Background(), p); !errors.Is(err, ErrRestoreRefused) {
		t.Fatalf("err %v", err)
	}
}
