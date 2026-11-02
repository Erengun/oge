package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// adoptStub plays a resumed agent: it records how it was opened, reports
// its permission mode when a turn starts, and can raise one permission
// prompt whose answer it keeps.
type adoptStub struct {
	mu       sync.Mutex
	opened   int
	spec     agent.LaunchSpec
	mode     string
	askTool  string
	answered chan agent.Answer
}

func (a *adoptStub) Open(_ context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opened++
	a.spec = spec
	return &adoptSess{a: a, ev: make(chan agent.Event, 16)}, nil
}

type adoptSess struct {
	a    *adoptStub
	ev   chan agent.Event
	once sync.Once
}

func (s *adoptSess) Process() agent.Process     { return agent.Process{} }
func (s *adoptSess) Events() <-chan agent.Event { return s.ev }
func (s *adoptSess) Interrupt() error           { return nil }
func (s *adoptSess) Close() error               { s.once.Do(func() { close(s.ev) }); return nil }
func (s *adoptSess) Send(agent.Turn) error {
	go func() {
		s.ev <- agent.Event{Kind: agent.TurnAccepted}
		s.ev <- agent.Event{Kind: agent.SessionOpened, Session: &agent.SessionInfo{PermissionMode: s.a.mode}}
		if s.a.askTool != "" {
			ask := agent.NewAsk(s.a.askTool, "make deploy", "make deploy", func(ans agent.Answer) error {
				s.a.answered <- ans
				return nil
			})
			s.ev <- agent.Event{Kind: agent.HostRequest, Host: &agent.HostDecision{Family: agent.Approval, Decision: "ask"}, Ask: ask}
			<-s.a.answered
		}
		s.ev <- agent.Event{Kind: agent.Claim, Text: "done"}
		s.ev <- agent.Event{Kind: agent.TurnSettled}
	}()
	return nil
}

// pumps are the message queues of the homes under test: a command still
// waiting from one call delivers to the next.
var pumps sync.Map

// pump runs cmd and feeds what comes back to the home, as the program
// would, until done: commands run side by side, so a turn waiting for an
// answer doesn't hold the rest.
func pump(t *testing.T, h *home, cmd tea.Cmd, done func() bool) {
	t.Helper()
	q, _ := pumps.LoadOrStore(h, make(chan tea.Msg, 64))
	msgs := q.(chan tea.Msg)
	launch := func(c tea.Cmd) {
		if c != nil {
			go func() { msgs <- c() }()
		}
	}
	launch(cmd)
	limit := time.After(10 * time.Second)
	for !done() {
		select {
		case m := <-msgs:
			if v := reflect.ValueOf(m); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(cmd) {
				for i := range v.Len() {
					launch(v.Index(i).Interface().(tea.Cmd))
				}
				continue
			}
			_, c := h.Update(m)
			launch(c)
		case <-time.After(20 * time.Millisecond):
		case <-limit:
			t.Fatalf("not done:\n%s", screenText(h))
		}
	}
}

// adoptHome is sessionsHome whose Chat can adopt on stub; the catalog's
// first row is the Claude session "fix the parser", last written written
// ago.
func adoptHome(t *testing.T, stub *adoptStub, written time.Duration) (*home, *run.Desk, string) {
	t.Helper()
	h, roots := sessionsHome(t, "")
	now := h.clock()
	cwd := t.TempDir()
	base := h.discover
	h.discover = func(ctx context.Context, rs []string) sessionFound {
		f := base(ctx, rs)
		for i, s := range f.sessions {
			if s.ID == "c2" {
				f.sessions[i].Profile = "claude@work"
			}
			if s.ID == "c1" {
				f.sessions[i].Cwd, f.sessions[i].Written = cwd, now.Add(-written)
			}
		}
		return f
	}
	private := t.TempDir()
	desk, err := run.OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	st := &ledger.StateRoot{Dir: t.TempDir(), Private: filepath.Join(t.TempDir(), "private"), Work: filepath.Join(t.TempDir(), "work")}
	h.ctx = context.Background()
	h.chat.desk = desk
	h.chat.adopt = func(s agent.NativeSession) (*run.Chat, error) {
		return run.OpenInPlaceChat(context.Background(), run.InPlaceParams{Adapter: stub, Provider: s.Provider, ID: s.ID,
			Cwd: s.Cwd, Profile: nativeProfile(s), State: st})
	}
	t.Cleanup(func() { desk.Close(true) })
	// Close every adopted Chat before the temp dirs go: Windows can't
	// remove a Chat log that is still open.
	t.Cleanup(func() {
		for _, v := range h.views {
			v.chat.close()
		}
	})
	_ = roots
	return h, desk, private
}

// catalogAt opens /sessions with the Claude "fix the parser" row selected.
func catalogAt(h *home) {
	slash(h, "/sessions")
	press(h, tea.KeyPressMsg{Code: tea.KeyDown}) // the Codex one is newest
}

func TestAdoptRefusesARecentWrite(t *testing.T) {
	stub := &adoptStub{}
	h, _, _ := adoptHome(t, stub, 40*time.Second)
	catalogAt(h)
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := screenText(h)
	if !strings.Contains(got, "written to 40s ago: close it there first") {
		t.Errorf("no refusal:\n%s", got)
	}
	typeText(h, "y")
	if stub.opened != 0 || h.catalog == nil || strings.Contains(got, "adopt fix the parser") {
		t.Errorf("a recent write was adopted (opened %d):\n%s", stub.opened, screenText(h))
	}
}

func TestAdoptEnterAloneNeverDecides(t *testing.T) {
	stub := &adoptStub{}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	catalogAt(h)
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := screenText(h); !strings.Contains(got, "adopt fix the parser in place? not isolated · y adopt · esc") {
		t.Fatalf("no prompt:\n%s", got)
	}
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	if stub.opened != 0 || h.catalog == nil || h.catalog.adopt == nil {
		t.Errorf("Enter decided (opened %d)", stub.opened)
	}
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.catalog == nil || h.catalog.adopt != nil || stub.opened != 0 {
		t.Error("esc didn't drop the prompt and keep the catalog")
	}
}

// typeCmd is one typed key, with the command it returns.
func typeCmd(h *home, k string) tea.Cmd {
	_, cmd := h.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
	return cmd
}

// adopted adopts "fix the parser" from the catalog, with y, and waits for
// its Chat to be a view.
func adopted(t *testing.T, h *home, stub *adoptStub) {
	t.Helper()
	catalogAt(h)
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	pump(t, h, typeCmd(h, "y"), func() bool { return len(h.views) == 2 })
}

func TestAdoptResumesInPlaceAndLabelsIt(t *testing.T) {
	stub := &adoptStub{mode: "default"}
	h, desk, private := adoptHome(t, stub, 10*time.Minute)
	// On the Desk as a ◌ row first: adopting takes it off.
	slash(h, "/sessions")
	press(h, tea.KeyPressMsg{Code: tea.KeyDown})
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	if len(h.onDesk) != 1 {
		t.Fatal("setup: not on the Desk")
	}
	adopted(t, h, stub)
	if stub.spec.Resume == nil || stub.spec.Resume.ID != "c1" || stub.spec.Workspace == "" || stub.spec.Profile != "" {
		t.Errorf("opened with %+v", stub.spec)
	}
	v := h.views[1]
	if !v.inPlace || h.chatView != v || v.chat.get() == nil || !v.chat.get().InPlace() {
		t.Fatal("the adopted Chat isn't focused and in place")
	}
	if len(h.onDesk) != 0 {
		t.Errorf("still a ◌ row: %+v", h.onDesk)
	}
	if rail := railText(h); !strings.Contains(rail, "in place") || !strings.Contains(rail, "not isolated") {
		t.Errorf("the rail row has no label:\n%s", rail)
	}
	if hdr := screenLines(h)[0]; !strings.Contains(hdr, "in place · not isolated") {
		t.Errorf("the header has no label: %q", hdr)
	}
	if got := screenText(h); !strings.Contains(got, "Checks: not run on in-place Chats yet") {
		t.Errorf("Checks aren't said to be missing:\n%s", got)
	}
	// Idle, it keeps its label: the in-place row never folds away.
	v.lastEvent = h.clock().Add(-time.Hour)
	if rail := railText(h); !strings.Contains(rail, "not isolated") {
		t.Errorf("the label went when it went idle:\n%s", rail)
	}
	desk.Close(false)
	recs, _ := run.ReadDesk(private)
	var added run.ChatAdded
	for _, r := range recs {
		if r.Type == run.RecChatAdded {
			if err := jsonUnmarshal(r.Data, &added); err != nil {
				t.Fatal(err)
			}
		}
	}
	if added.Provider != "claude" || added.NativeID != "c1" || !added.InPlace {
		t.Errorf("desk.log ChatAdded: %+v", added)
	}
}

func TestAdoptBypassIsNotGatedAndNothingClaimsSupervision(t *testing.T) {
	stub := &adoptStub{mode: "bypassPermissions"}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	adopted(t, h, stub)
	typeText(h, "go")
	_, cmd := h.Update(enter())
	pump(t, h, cmd, func() bool { return h.turn == nil && strings.Contains(screenText(h), "done") })
	got := screenText(h)
	if !strings.Contains(got, "not gated: bypassPermissions") {
		t.Errorf("no coverage line:\n%s", got)
	}
	for _, claim := range []string{"gated: what your settings", "supervised", "isolated by", "Verdict", "no changes", "no cut yet"} {
		if strings.Contains(got, claim) {
			t.Errorf("claims %q:\n%s", claim, got)
		}
	}
	c := h.views[1].chat.get()
	recs, err := ledger.Replay(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, r := range recs {
		if r.Type == run.RecCoverageReported {
			line = string(r.Data)
		}
	}
	if !strings.Contains(line, "not gated: bypassPermissions") {
		t.Errorf("the Chat log's coverage: %q", line)
	}
}

func TestAdoptDefaultModeLineSaysWhatIsGated(t *testing.T) {
	stub := &adoptStub{mode: "default"}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	adopted(t, h, stub)
	typeText(h, "go")
	_, cmd := h.Update(enter())
	pump(t, h, cmd, func() bool { return h.turn == nil && strings.Contains(screenText(h), "done") })
	// The line wraps on screen; the Chat log keeps it whole.
	if got := screenText(h); !strings.Contains(got, "gated: what your settings would ask") || strings.Contains(got, "not gated") {
		t.Errorf("no coverage line:\n%s", got)
	}
	recs, err := ledger.Replay(h.views[1].chat.get().Dir)
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, r := range recs {
		if r.Type == run.RecCoverageReported {
			line = string(r.Data)
		}
	}
	if !strings.Contains(line, "gated: what your settings would ask · allowed without Öge: your allow rules") {
		t.Errorf("the Chat log's coverage: %q", line)
	}
}

func TestAdoptedPermissionPromptIsAGateAndItsAnswerReaches(t *testing.T) {
	stub := &adoptStub{mode: "default", askTool: "Bash", answered: make(chan agent.Answer, 1)}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	adopted(t, h, stub)
	typeText(h, "deploy")
	_, cmd := h.Update(enter())
	pump(t, h, cmd, func() bool { return h.ask() != nil })
	if got := screenText(h); !strings.Contains(got, "make deploy") {
		t.Errorf("the prompt isn't shown:\n%s", got)
	}
	press(h, enter())
	select {
	case ans := <-stub.answered:
		t.Fatalf("Enter answered the prompt: %+v", ans)
	case <-time.After(100 * time.Millisecond):
	}
	if h.ask() == nil {
		t.Fatal("Enter dropped the prompt")
	}
	cmd = typeCmd(h, "y")
	pump(t, h, cmd, func() bool { return h.turn == nil && strings.Contains(screenText(h), "done") })
	select {
	case ans := <-stub.answered:
		t.Fatalf("one answer expected, a second came: %+v", ans)
	default:
	}
}

func TestAdoptClickOnARowAsksAndRefusesLikeTheCatalog(t *testing.T) {
	stub := &adoptStub{}
	h, _, _ := adoptHome(t, stub, 40*time.Second)
	slash(h, "/sessions")
	press(h, tea.KeyPressMsg{Code: tea.KeyDown})
	typeText(h, "a")
	press(h, tea.KeyPressMsg{Code: tea.KeyEscape})
	y := rowY(t, h, "◌ fix th")
	_, cmd := h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: y})
	feed(h, cmd)
	if got := screenText(h); !strings.Contains(got, "written to 40s ago: close it there first") {
		t.Errorf("a click doesn't refuse a recent write:\n%s", got)
	}
	if stub.opened != 0 {
		t.Error("opened")
	}
}

// A second adopt of a session already open here as a Chat is refused, even
// long after its file was last written: Öge must not be the second writer.
func TestAdoptRefusesASessionAlreadyOpenHere(t *testing.T) {
	stub := &adoptStub{mode: "default"}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	adopted(t, h, stub)
	if stub.opened != 1 {
		t.Fatalf("setup: opened %d", stub.opened)
	}
	later := h.clock().Add(10 * time.Minute)
	h.now = func() time.Time { return later }
	catalogAt(h)
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := screenText(h); !strings.Contains(got, "already open here as Chat") {
		t.Errorf("no refusal:\n%s", got)
	}
	typeText(h, "y")
	if stub.opened != 1 || len(h.views) != 2 {
		t.Errorf("adopted twice: opened %d, views %d", stub.opened, len(h.views))
	}
}

// The quiet check runs again at y: a write that lands while the prompt is
// up stops the adopt.
func TestAdoptRechecksTheFileAtY(t *testing.T) {
	stub := &adoptStub{}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	path := filepath.Join(t.TempDir(), "c1.jsonl")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := h.clock().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	base := h.discover
	h.discover = func(ctx context.Context, rs []string) sessionFound {
		f := base(ctx, rs)
		for i := range f.sessions {
			if f.sessions[i].ID == "c1" {
				f.sessions[i].Path = path
			}
		}
		return f
	}
	h.now = func() time.Time { return time.Now() }
	catalogAt(h)
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.catalog == nil || h.catalog.adopt == nil {
		t.Fatalf("no prompt:\n%s", screenText(h))
	}
	// Someone types into the native session while the prompt is up.
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	typeText(h, "y")
	if stub.opened != 0 || len(h.views) != 1 {
		t.Errorf("adopted a session written to under the prompt (opened %d)", stub.opened)
	}
	if got := screenText(h); !strings.Contains(got, "close it there first") {
		t.Errorf("no refusal:\n%s", got)
	}
}

// The profile a session was found under is the one it resumes with.
func TestAdoptKeepsTheSessionsProfile(t *testing.T) {
	stub := &adoptStub{}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	base := h.discover
	h.discover = func(ctx context.Context, rs []string) sessionFound {
		f := base(ctx, rs)
		for i := range f.sessions {
			if f.sessions[i].ID == "c2" {
				f.sessions[i].Cwd, f.sessions[i].Written = t.TempDir(), h.clock().Add(-time.Hour)
			}
		}
		return f
	}
	slash(h, "/sessions")
	press(h, tea.KeyPressMsg{Code: tea.KeyDown})
	press(h, tea.KeyPressMsg{Code: tea.KeyDown}) // claude@work
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter})
	pump(t, h, typeCmd(h, "y"), func() bool { return len(h.views) == 2 })
	if stub.spec.Profile != "work" || stub.spec.Resume == nil || stub.spec.Resume.ID != "c2" {
		t.Errorf("opened with %+v", stub.spec)
	}
}

// The Ledger for an in-place turn says only what happened: the user's
// message, no copy, no sandbox.
func TestAdoptManifestMakesNoIsolationClaim(t *testing.T) {
	stub := &adoptStub{mode: "default"}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	adopted(t, h, stub)
	typeText(h, "go")
	_, cmd := h.Update(enter())
	pump(t, h, cmd, func() bool { return h.turn == nil && strings.Contains(screenText(h), "done") })
	recs, err := ledger.Replay(h.views[1].chat.get().Dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Type != run.RecBriefingManifest {
			continue
		}
		n++
		got := string(r.Data)
		for _, bad := range []string{"a copy of revision", "built by Öge", "\"fresh\"", "denied", "deny_read"} {
			if strings.Contains(got, bad) {
				t.Errorf("manifest claims %q: %s", bad, got)
			}
		}
		if !strings.Contains(got, "in place") || !strings.Contains(got, "c1") {
			t.Errorf("manifest doesn't say in place / which session: %s", got)
		}
	}
	if n == 0 {
		t.Error("no manifest recorded")
	}
}

// The sidebar and the status line show the session's own settings, not
// Öge's setup for the repo.
func TestAdoptedViewShowsNoOgeSetup(t *testing.T) {
	stub := &adoptStub{mode: "bypassPermissions"}
	h, _, _ := adoptHome(t, stub, 10*time.Minute)
	adopted(t, h, stub)
	typeText(h, "go")
	_, cmd := h.Update(enter())
	pump(t, h, cmd, func() bool { return h.turn == nil && strings.Contains(screenText(h), "done") })
	got := strings.Join(h.sidebar(40, 30), "\n")
	if strings.Contains(got, "careful") || strings.Contains(got, "verify") || !strings.Contains(got, "bypassPermissions") {
		t.Errorf("sidebar:\n%s", got)
	}
	if scr := screenText(h); strings.Contains(scr, "verify ") {
		t.Errorf("status line shows a setup:\n%s", scr)
	}
}

func TestCoverageLine(t *testing.T) {
	cases := []struct{ mode, approval, sandbox, want string }{
		{"bypassPermissions", "", "", "not gated: bypassPermissions"},
		{"", "never", "", "not gated: approval policy never"},
		{"", "on-request", "danger-full-access", "not gated: sandbox danger-full-access"},
		{"default", "", "", "gated: what your settings would ask · allowed without Öge: your allow rules"},
		{"acceptEdits", "", "", "gated: what your settings would ask · allowed without Öge: your allow rules and every file edit · mode acceptEdits"},
		{"auto", "", "", "gated: what your settings would ask · allowed without Öge: what claude's auto mode allows · mode auto"},
		{"plan", "", "", "coverage unknown: mode plan is not one Öge recognises"},
		{"", "", "", "coverage unknown: the session didn't report its permission settings"},
	}
	for _, c := range cases {
		if got := coverageLine(c.mode, c.approval, c.sandbox); got != c.want {
			t.Errorf("coverageLine(%q, %q, %q) = %q", c.mode, c.approval, c.sandbox, got)
		}
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
