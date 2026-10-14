package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
)

func chatFrozen(role string) *pipeline.Frozen {
	return &pipeline.Frozen{Mode: pipeline.Fast, Checks: []pipeline.CheckCommand{{Run: "go test ./..."}},
		Stages: []pipeline.Stage{{Name: "implement", Role: role, Agent: "claude", Network: "off"}}}
}

func chatOn(k string) string {
	if k == ChatFlag {
		return "1"
	}
	return ""
}

// openChat opens a Chat on a one-file repository, with fakeclaude serving
// sc through the claude adapter.
func openChat(t *testing.T, sc multiturn.Scenario, idle time.Duration) *Chat {
	t.Helper()
	return openChatIn(context.Background(), t, fakeClaude(t, sc), idle, chatFrozen("implementer"), map[string]string{"add.go": "package fx\n"})
}

// fakeClaude is the claude adapter with fakeclaude serving sc.
func fakeClaude(t *testing.T, sc multiturn.Scenario) agent.Adapter {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	scen := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(scen); err != nil {
		t.Fatal(err)
	}
	return &claude.Adapter{Path: multiturn.BuildClaude(t), CloseGrace: time.Second,
		Environ: func() []string { return append(os.Environ(), "OGE_FAKE_CLAUDE_SCENARIO="+scen) }}
}

// openChatWith opens a Chat on a one-file repository with ad.
func openChatWith(ctx context.Context, t *testing.T, ad agent.Adapter, idle time.Duration) *Chat {
	t.Helper()
	return openChatIn(ctx, t, ad, idle, chatFrozen("implementer"), map[string]string{"add.go": "package fx\n"})
}

// openChatIn opens a Chat with ad on a repository holding files, under f.
func openChatIn(ctx context.Context, t *testing.T, ad agent.Adapter, idle time.Duration, fr *pipeline.Frozen, files map[string]string) *Chat {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Runs are refused on Windows (ADR-0017)")
	}
	f := &followFixture{user: t.TempDir()}
	f.git(t, "init", "-q", "-b", "main")
	for p, s := range files {
		f.write(t, p, s)
	}
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "init")
	d := t.TempDir()
	state := &ledger.StateRoot{Dir: d, Private: filepath.Join(d, "private"), Work: filepath.Join(d, "work")}
	c, err := OpenChat(ctx, ChatParams{Repo: f.user, Frozen: fr,
		Agents: map[string]agent.Adapter{"claude": ad}, State: state, Getenv: chatOn, IdleTimeout: idle})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// drain reads a turn's events until its channel closes, answering each
// Ask with answer.
func drain(t *testing.T, ch <-chan agent.Event, answer func(*agent.Ask) bool) []agent.Event {
	t.Helper()
	var evs []agent.Event
	limit := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
			if ev.Ask != nil {
				if answer == nil {
					t.Fatalf("an unexpected ask: %+v", ev.Host)
				}
				if err := ev.Ask.Reply(answer(ev.Ask)); err != nil {
					t.Fatal(err)
				}
			}
		case <-limit:
			t.Fatalf("the turn never ended; so far %+v", evs)
		}
	}
}

func send(t *testing.T, c *Chat, text string) <-chan agent.Event {
	t.Helper()
	ch, err := c.Send(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func settledOf(t *testing.T, evs []agent.Event) agent.Event {
	t.Helper()
	if len(evs) == 0 || evs[len(evs)-1].Kind != agent.TurnSettled {
		t.Fatalf("the turn didn't end settled: %+v", evs)
	}
	return evs[len(evs)-1]
}

func said(evs []agent.Event) string {
	var b []string
	for _, ev := range evs {
		// fakeclaude also says what Öge's hook answered: not a reply.
		if ev.Kind == agent.Claim && ev.Tool == "" && !strings.HasPrefix(ev.Text, "hook ") {
			b = append(b, ev.Text)
		}
	}
	return strings.Join(b, "|")
}

// Three turns reach one Session: the fake serves its turns in order, so a
// second process would have answered "one" again. Each turn has its own
// manifest entry; only the first carries the Briefing.
func TestChatThreeTurnsOneSession(t *testing.T) {
	c := openChat(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "one"}, {Reply: "two", Writes: []multiturn.File{{Path: "b.go", Content: "package fx\n"}}}, {Reply: "three"},
	}}, 0)
	for i, want := range []string{"one", "two", "three"} {
		evs := drain(t, send(t, c, "msg "+want), nil)
		if s := settledOf(t, evs); s.Failure != "" || s.Stop != "" {
			t.Fatalf("turn %d: %+v", i+1, s)
		}
		if got := said(evs); got != want {
			t.Errorf("turn %d said %q, want %q", i+1, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "b.go")); err != nil {
		t.Errorf("turn 2's write isn't in the Workspace: %v", err)
	}
	recs, err := ledger.Replay(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var mans []map[string]any
	for _, r := range recs {
		if r.Type == RecBriefingManifest {
			var m map[string]any
			_ = json.Unmarshal(r.Data, &m)
			mans = append(mans, m)
		}
	}
	if len(mans) != 3 {
		t.Fatalf("%d manifest entries, want 3", len(mans))
	}
	if mans[0]["session_reused"] != false || mans[0]["session"] != "fresh" || !strings.Contains(string(mustJSON(mans[0]["items"])), `"briefing"`) {
		t.Errorf("turn 1's manifest %v", mans[0])
	}
	for i, m := range mans[1:] {
		items := string(mustJSON(m["items"]))
		if m["session_reused"] != true || m["turn"] != float64(i+2) || !strings.Contains(items, sha("msg "+[]string{"two", "three"}[i])) || strings.Contains(items, "briefing") {
			t.Errorf("turn %d's manifest %v", i+2, m)
		}
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// A message sent while a turn runs queues until it settles; an interrupt
// ends the running turn, not the Session, and the queued turn then runs.
func TestChatQueueAndInterrupt(t *testing.T) {
	c := openChat(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "working", Hold: true}, {Reply: "queued reply"}, {Reply: "after"},
	}}, 0)
	first := send(t, c, "long task")
	for ev := range first {
		if ev.Kind == agent.Claim && ev.Text == "working" {
			break
		}
	}
	second := send(t, c, "and another thing")
	select {
	case ev := <-second:
		t.Fatalf("the queued turn started during the running one: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
	if err := c.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if s := settledOf(t, drain(t, first, nil)); s.Failure != "interrupted" {
		t.Errorf("the interrupted turn settled %+v", s)
	}
	evs := drain(t, second, nil)
	if s := settledOf(t, evs); s.Failure != "" || said(evs) != "queued reply" {
		t.Errorf("the queued turn: %+v", evs)
	}
	evs = drain(t, send(t, c, "next"), nil)
	if s := settledOf(t, evs); s.Failure != "" || said(evs) != "after" {
		t.Errorf("the turn after an interrupt: %+v", evs)
	}
}

// An auto-mode ask reaches the caller, whose answer decides it.
func TestChatAskAllowedAndDenied(t *testing.T) {
	ask := &multiturn.Ask{Tool: "Bash", Target: "make deploy"}
	c := openChat(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Ask: ask, Writes: []multiturn.File{{Path: "a.txt", Content: "a"}}},
		{Ask: ask, Writes: []multiturn.File{{Path: "b.txt", Content: "b"}}},
	}}, 0)
	for _, allow := range []bool{true, false} {
		asked := 0
		evs := drain(t, send(t, c, "deploy"), func(a *agent.Ask) bool {
			asked++
			if a.Tool != "Bash" || a.Target == "" {
				t.Errorf("ask %+v", a)
			}
			return allow
		})
		settledOf(t, evs)
		if asked != 1 {
			t.Fatalf("asked %d times", asked)
		}
		want := map[bool]string{true: "allow", false: "deny"}[allow]
		var answered bool
		for _, ev := range evs {
			if h := ev.Host; h != nil && h.By == "human" && h.Decision == want {
				answered = true
			}
		}
		if !answered {
			t.Errorf("no recorded %s answer in %+v", want, evs)
		}
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "a.txt")); err != nil {
		t.Errorf("the allowed write didn't happen: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "b.txt")); err == nil {
		t.Error("the denied write happened")
	}
}

// A request for a path outside the Workspace is denied by Öge before it
// can reach the human.
func TestChatHardDenyNeverAsks(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	c := openChat(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Ask: &multiturn.Ask{Tool: "Read", Target: outside}, Writes: []multiturn.File{{Path: "x.txt", Content: "x"}}},
	}}, 0)
	evs := drain(t, send(t, c, "read it"), nil) // nil: an ask fails the test
	settledOf(t, evs)
	var denied bool
	for _, ev := range evs {
		if h := ev.Host; h != nil && h.Decision == "deny" && h.Rule == "outside_role" {
			denied = true
		}
	}
	if !denied {
		t.Errorf("no hard deny in %+v", evs)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "x.txt")); err == nil {
		t.Error("the turn went on as if allowed")
	}
}

// With no turn in flight past the idle timeout, the Session is closed.
func TestChatIdleTimeout(t *testing.T) {
	c := openChat(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "hi"}}}, 300*time.Millisecond)
	settledOf(t, drain(t, send(t, c, "yo"), nil))
	select {
	case <-c.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the idle Session was never closed")
	}
	if !errors.Is(c.Err(), ErrChatIdle) {
		t.Errorf("Err() = %v", c.Err())
	}
	// The next message reopens a fresh Session, which serves its first
	// scripted turn again.
	evs := drain(t, send(t, c, "again"), nil)
	if s := settledOf(t, evs); s.Failure != "" || said(evs) != "hi" {
		t.Errorf("the turn after the idle timeout: %+v", evs)
	}
	if r := recordsOf(t, c, RecSessionReopened); len(r) != 1 || r[0]["reason"] != "idle" || r[0]["turn"] != float64(2) {
		t.Errorf("SessionReopened %v", r)
	}
}

type noOpen struct{ t *testing.T }

func (a noOpen) Open(context.Context, agent.LaunchSpec) (agent.Session, error) {
	a.t.Error("a Session was opened")
	return nil, errors.New("no")
}

// D1: only an implementer Session may be reused; a verifier's (QA's) is a
// hard error before anything starts. The Chat is behind its hidden flag.
func TestChatRefusesVerifierReuse(t *testing.T) {
	agents := map[string]agent.Adapter{"claude": noOpen{t}}
	_, err := OpenChat(context.Background(), ChatParams{Frozen: chatFrozen("verifier"), Agents: agents, Getenv: chatOn, Role: "verifier"})
	if !errors.Is(err, ErrSessionReuse) {
		t.Errorf("reusing the verifier: %v", err)
	}
	if err := reuse("verifier"); !errors.Is(err, ErrSessionReuse) {
		t.Errorf("reuse(verifier) = %v", err)
	}
	if err := reuse("implementer"); err != nil {
		t.Errorf("reuse(implementer) = %v", err)
	}
	_, err = OpenChat(context.Background(), ChatParams{Frozen: chatFrozen("implementer"), Agents: agents, Getenv: func(string) string { return "" }})
	if !errors.Is(err, ErrChatDisabled) {
		t.Errorf("without the flag: %v", err)
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// stub is a Session whose process can die on cue.
type stub struct {
	events  chan agent.Event
	sendErr error
	once    sync.Once
}

type stubAdapter struct{ s *stub }

func (a stubAdapter) Open(context.Context, agent.LaunchSpec) (agent.Session, error) { return a.s, nil }

func newStub(sendErr error) *stub { return &stub{events: make(chan agent.Event, 8), sendErr: sendErr} }

func (s *stub) Process() agent.Process     { return agent.Process{} }
func (s *stub) Events() <-chan agent.Event { return s.events }
func (s *stub) Interrupt() error           { return nil }
func (s *stub) Close() error               { s.die(); return nil }
func (s *stub) die()                       { s.once.Do(func() { close(s.events) }) }
func (s *stub) Send(agent.Turn) error {
	if s.sendErr != nil {
		s.die() // the process dies as the message is written
		return s.sendErr
	}
	s.events <- agent.Event{Kind: agent.TurnAccepted}
	return nil
}

// A Send that fails as the process dies settles its turn as failed, and
// the Chat takes no more turns.
func TestChatSendFailsAsProcessDies(t *testing.T) {
	c := openChatWith(context.Background(), t, stubAdapter{newStub(errors.New("broken pipe"))}, 0)
	ch, err := c.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("Send succeeded on a dead process")
	}
	if s := settledOf(t, drain(t, ch, nil)); !strings.HasPrefix(s.Failure, "send_failed") {
		t.Errorf("settled %+v", s)
	}
	<-c.Done()
	if !errors.Is(c.Err(), ErrChatLost) {
		t.Errorf("Err() after the process died: %v", c.Err())
	}
}

// When the process dies mid-turn, the turn in flight and every queued one
// settle as failed.
func TestChatProcessDiesWithQueuedTurns(t *testing.T) {
	st := newStub(nil)
	c := openChatWith(context.Background(), t, stubAdapter{st}, 0)
	first, second := send(t, c, "one"), send(t, c, "two")
	st.die()
	for _, ch := range []<-chan agent.Event{first, second} {
		if s := settledOf(t, drain(t, ch, nil)); !strings.Contains(s.Failure, "chat_ended") {
			t.Errorf("settled %+v", s)
		}
	}
}

// Cancelling the Chat's parent context closes it.
func TestChatClosesWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := openChatWith(ctx, t, stubAdapter{newStub(nil)}, 0)
	cancel()
	select {
	case <-c.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the Chat outlived its context")
	}
	if !errors.Is(c.Err(), ErrChatClosed) {
		t.Errorf("Err() = %v", c.Err())
	}
}
