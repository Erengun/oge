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
	return stoppedChatWith(t, nil, nil)
}

// stoppedChatWith is stoppedChat with test globs and extra files in the
// repository, so a protected path exists.
func stoppedChatWith(t *testing.T, globs []string, files map[string]string) (ResumeParams, string) {
	t.Helper()
	ad := fakeClaude(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "one", Writes: []multiturn.File{{Path: "b.go", Content: "package fx\n"}}},
		{Reply: "two"},
	}})
	f := &followFixture{user: t.TempDir()}
	f.git(t, "init", "-q", "-b", "main")
	f.write(t, "add.go", "package fx\n")
	for name, body := range files {
		f.write(t, name, body)
	}
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "init")
	d := t.TempDir()
	state := &ledger.StateRoot{Dir: d, Private: filepath.Join(d, "private"), Work: filepath.Join(d, "work")}
	agents := map[string]agent.Adapter{"claude": ad}
	fr := chatFrozen("implementer")
	fr.Project.TestGlobs = globs
	c, err := OpenChat(context.Background(), ChatParams{Repo: f.user, Frozen: fr,
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

// startUnsettledTurn records a turn 2 that started and never settled.
func startUnsettledTurn(t *testing.T, p ResumeParams) {
	t.Helper()
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
}

// A turn that never settled and left edits: they are scope-checked and kept
// as that turn's commit; the records say so in order.
func TestResumeChatFoundOnRestoreKeepsScopedEdits(t *testing.T) {
	const base = "package fx\n\nfunc TestA() {}\n"
	p, ws := stoppedChatWith(t, []string{"*_test.go"}, map[string]string{"add_test.go": base})
	startUnsettledTurn(t, p)
	if err := os.WriteFile(filepath.Join(ws, "c.go"), []byte("package fx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An out-of-scope write: the protected test is changed, not added to.
	if err := os.WriteFile(filepath.Join(ws, "add_test.go"), []byte("package fx\n"), 0o644); err != nil {
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
	if got, _ := os.ReadFile(filepath.Join(ws, "add_test.go")); string(got) != base {
		t.Errorf("the protected test wasn't reverted: %q", got)
	}
	if _, err := os.Stat(filepath.Join(ws, "c.go")); err != nil {
		t.Errorf("the in-scope edit was lost: %v", err)
	}
}

// An unsettled turn whose Workspace is clean left nothing: it is still
// recorded as interrupted, but Öge doesn't claim edits.
func TestResumeChatUnsettledTurnWithCleanWorkspaceFindsNothing(t *testing.T) {
	p, _ := stoppedChat(t)
	startUnsettledTurn(t, p)
	res, err := ResumeChat(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Chat.Close() })
	if res.Found {
		t.Error("a clean Workspace was reported as left edits")
	}
	if r := recordsOf(t, res.Chat, RecSessionRestored); len(r) != 1 || r[0]["found_on_restore"] != false {
		t.Errorf("SessionRestored %v", r)
	}
}

// A scope check that can't run refuses the Chat and records neither the
// interruption nor the restore, so the next restore checks again.
func TestResumeChatScopeFailureIsRefusedAndRecordsNothing(t *testing.T) {
	p, ws := stoppedChat(t)
	startUnsettledTurn(t, p)
	// The Workspace is no longer a directory: the scope check can't run.
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeChat(context.Background(), p); !errors.Is(err, ErrRestoreRefused) {
		t.Fatalf("err %v", err)
	}
	recs, err := ledger.Replay(p.Chat.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Type == RecTurnInterrupted || r.Type == RecSessionRestored {
			t.Errorf("the refused restore recorded %s", r.Type)
		}
	}
}

// A reopen typed over a change that never got a Session (the launch
// failed) isn't the Chat's own: with the settings back as they were, the
// Chat launches the settings it last ran under, not the unlaunched ones.
func TestResumeChatAcceptedReopenWithoutSessionIsNotTheChatsOwn(t *testing.T) {
	p, _ := stoppedChat(t)
	was := chatFrozen("implementer")
	other := chatFrozen("implementer")
	other.Stages[0].Model = "other-model"
	p.Now = other
	ch, err := RestoreChanges(p)
	if err != nil || len(ch) == 0 {
		t.Fatalf("changes %v %v", ch, err)
	}
	p.Reopen, p.Shown = true, ch
	res, err := ResumeChat(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	// The Session never launched: no SessionReopened. oge goes away.
	if err := res.Chat.Stop("quit"); err != nil {
		t.Fatal(err)
	}
	p.Reopen, p.Shown, p.Now = false, nil, was
	ch, err = RestoreChanges(p)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := ResumeChat(context.Background(), p)
	if err != nil {
		if len(ch) == 0 {
			t.Fatalf("no change shown, but refused: %v", err)
		}
		return // the card shows the change again: also fine
	}
	t.Cleanup(func() { res2.Chat.Close() })
	if res2.Chat.stage.Model == "other-model" {
		t.Errorf("launched the model that never ran, with no change shown (%v)", ch)
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
