//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/briefing"
	"github.com/erengun/oge/internal/task"
)

// After the Session's process is killed, the next message reopens a
// fresh Session in the same Workspace, re-briefed with the user's turns
// so far (#185), and the Chat goes on.
func TestChatReopensAfterCrash(t *testing.T) {
	c := openChat(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "one", Writes: []multiturn.File{{Path: "b.go", Content: "package fx\n"}}},
	}}, 0)
	settledOf(t, drain(t, send(t, c, "first"), nil))
	c.mu.Lock()
	pgid := c.sess.Process().PGID
	c.mu.Unlock()
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the killed Session never ended")
	}
	evs := drain(t, send(t, c, "second"), nil)
	if s := settledOf(t, evs); s.Failure != "" || said(evs) != "one" {
		t.Fatalf("the turn after the crash: %+v", evs)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "b.go")); err != nil {
		t.Errorf("the work didn't survive: %v", err)
	}
	if r := recordsOf(t, c, RecSessionReopened); len(r) != 1 || r[0]["reason"] != "lost" || r[0]["turn"] != float64(2) {
		t.Errorf("SessionReopened %v", r)
	}
	mans := recordsOf(t, c, RecBriefingManifest)
	brief := briefing.ChatReopened(task.Task{Text: "first", FollowUps: []string{"second"}}, []string{"go test ./..."})
	if len(mans) != 2 || mans[1]["session_reused"] != false || !strings.Contains(string(mustJSON(mans[1]["items"])), sha(brief)) {
		t.Errorf("turn 2's manifest %v", mans[len(mans)-1])
	}
}
