package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/task"
)

func homeFrozen() *pipeline.Frozen {
	return &pipeline.Frozen{
		Mode: pipeline.Standard,
		Stages: []pipeline.Stage{
			{Name: "implement", Role: "implementer", Agent: "claude", Model: "sonnet"},
			{Name: "verify", Role: "verifier", Agent: "codex"},
		},
		Project: pipeline.ProjectConfig{TestGlobs: []string{"**/*_test.go"}},
		Checks:  []pipeline.CheckCommand{{Run: "go test -json ./..."}},
	}
}

func testHome(width int) *home {
	f := homeFrozen()
	h := &home{comp: newComposer(), title: "oge · demo · main", setup: setupLines(f), st: newStyles(false)}
	h.resolve = func(*setupState) (*resolved, []string) { return &resolved{frozen: f}, nil }
	h.width, h.height = width, 24
	return h
}

func frame(h *home) string { s, _, _ := h.render(); return s }

func TestHomeEmptyComposer(t *testing.T) {
	golden(t, "home-empty", frame(testHome(70)))
}

func TestHomeComposerWithTextAndSetupBar(t *testing.T) {
	h := testHome(70)
	h.comp.insert("fix Add\n\nAdd returns 0 for every input.")
	golden(t, "home-text", frame(h))
}

func TestHomeFastModeHasNoVerifier(t *testing.T) {
	f := &pipeline.Frozen{Mode: pipeline.Fast,
		Stages: []pipeline.Stage{{Name: "implement", Role: "implementer", Agent: "claude"}},
		Checks: []pipeline.CheckCommand{{Run: "go test ./..."}, {Run: "go vet ./..."}}}
	got := strings.Join(setupLines(f), "\n")
	if !strings.HasPrefix(got, "Fast · implement claude · no verifier\nChecks: go test ./... · go vet ./...\n[tab] setup  [enter] run") {
		t.Errorf("setup bar:\n%s", got)
	}
}

func press(h *home, keys ...tea.Msg) {
	for _, k := range keys {
		h.Update(k)
	}
}

func TestComposerEditing(t *testing.T) {
	h := testHome(70)
	typ := func(s string) {
		for _, r := range s {
			h.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	typ("ab")
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	typ("cd")
	press(h, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	typ("e")
	if got := h.comp.text(); got != "ab\ncd\ne" {
		t.Errorf("newlines: %q", got)
	}
	press(h, tea.KeyPressMsg{Code: tea.KeyLeft}, tea.KeyPressMsg{Code: tea.KeyBackspace}, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := h.comp.text(); got != "ab\nce" {
		t.Errorf("backspace joins lines: %q", got)
	}
	// A paste lands whole, with its newlines, and can't carry controls.
	h.Update(tea.PasteMsg{Content: "x\r\ny\x1b[31mz\ttab"})
	if got := h.comp.text(); !strings.Contains(got, "x\ny[31mz    tab") || strings.ContainsRune(got, 0x1b) {
		t.Errorf("paste: %q", got)
	}
}

func TestComposerKeys(t *testing.T) {
	var started []string
	h := testHome(70)
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		started = append(started, tk.Title)
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	// Enter on an empty box does nothing; Ctrl+C with text clears it; on an
	// empty box it quits.
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.run != nil || len(started) != 0 {
		t.Fatal("Enter on an empty composer started a Run")
	}
	h.comp.insert("fix Add")
	h.Update(ctrlKey('c'))
	if !h.comp.empty() || h.quit {
		t.Fatalf("Ctrl+C with text: %q quit=%v", h.comp.text(), h.quit)
	}
	h.comp.insert("fix Add")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.comp.empty() || h.quit {
		t.Fatal("Esc must not wipe a Task")
	}
	h.Update(ctrlKey('c'))
	if _, cmd := h.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); !h.quit || cmd == nil {
		t.Fatal("Esc on an empty composer should quit")
	}
	h = testHome(70)
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		started = append(started, tk.Title)
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	h.comp.insert("fix Add")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.run == nil || len(started) != 1 || started[0] != "fix Add" {
		t.Fatalf("Enter did not start the Run: %v", started)
	}
	if !strings.Contains(h.View().Content, "fix Add") {
		t.Error("the Run's view did not take over")
	}
}

func TestComposerWrapsAndScrolls(t *testing.T) {
	h := testHome(24) // 20 columns inside the box
	h.comp.insert(strings.Repeat("word ", 60))
	rows, cr, cc := h.comp.layout(20)
	if len(rows) != maxComposerRows || cr != maxComposerRows-1 || cc > 20 {
		t.Errorf("rows %d cursor %d,%d", len(rows), cr, cc)
	}
	for _, l := range strings.Split(frame(h), "\n") {
		if w := len([]rune(l)); w > 24 {
			t.Errorf("a line is %d columns: %q", w, l)
		}
	}
}

// The Task never reaches the composer's frame with a control character in
// it, nor the repository's name or branch.
func TestHomeTitleAndSetupAreEscaped(t *testing.T) {
	f := homeFrozen()
	f.Checks[0].Run = "go test \x1b]0;pwned\x07"
	for _, l := range setupLines(f) {
		if strings.ContainsAny(l, "\x1b\x07") {
			t.Errorf("setup bar line: %q", l)
		}
	}
	if got := homeTitle(Env{}, "/tmp/evil\x1b[2Jrepo"); strings.ContainsRune(got, 0x1b) {
		t.Errorf("title: %q", got)
	}
}

// homeNorm hides what differs between runs: ids, hashes and durations.
var homeNorm = regexp.MustCompile(`\d{8}T\d{6}-[0-9a-f]{6}|[0-9a-f]{7,64}\b|(\d+m)?\d+(\.\d+)?m?s\b`)

// homeSession runs Main on a pipe the test types into, with the fake agent.
type homeSession struct {
	t      *testing.T
	f      *runFixture
	pw     *os.File
	out    syncBuf
	errOut syncBuf
	exit   chan int
	mark   int // how much output there was when the last keys were typed
}

func startHomeSession(t *testing.T, f *runFixture, args ...string) *homeSession {
	t.Helper()
	return startHomeSessionWith(t, f, nil, args...)
}

// startHomeSessionWith lets the test change the Env first.
func startHomeSessionWith(t *testing.T, f *runFixture, tweak func(*Env), args ...string) *homeSession {
	t.Helper()
	f.interactive = true
	f.setenv("TERM", "xterm-256color")
	f.setenv("NO_COLOR", "1")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pw.Close(); pr.Close() })
	script := filepath.Join(filepath.Dir(f.repo), "agent.sh")
	if err := os.WriteFile(script, []byte(fixScript), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &homeSession{t: t, f: f, pw: pw, exit: make(chan int, 1)}
	env := f.envWithFake(script, &s.out, &s.errOut)
	env.Stdin = pr
	if tweak != nil {
		tweak(&env)
	}
	go func() { s.exit <- Main(env, args) }()
	return s
}

// composer waits for the n-th composer frame.
// composer waits for a composer drawn after the last keys were typed. A
// composer redraws as it's used, so counting its frames isn't enough: an
// earlier composer's redraws could satisfy the count.
func (s *homeSession) composer() {
	s.t.Helper()
	s.expect("[ctrl+e] editor")
}

// expect waits until text is written after the last keys were typed.
func (s *homeSession) expect(text string) {
	s.t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if strings.Contains(s.out.String()[s.mark:], text) {
			return
		}
	}
	s.t.Fatalf("%q never appeared after the last keys in:\n%s", text, s.out.String())
}

// settledScreen is the screen on an 80x24 terminal once it has stopped
// changing. A line printed above the live area lands before the renderer
// repaints the area below it, so a snapshot taken on the first sign of
// the composer can catch the two halfway.
func (s *homeSession) settledScreen() string {
	s.t.Helper()
	render := func() string {
		v := vt{height: 24}
		v.write([]byte(s.out.String()))
		return v.String()
	}
	last, since := render(), time.Now()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cur := render(); cur != last {
			last, since = cur, time.Now()
		} else if time.Since(since) >= 300*time.Millisecond {
			return last
		}
	}
	return last
}

// expectScreen waits until text shows on the rendered screen more often
// than it did when the last keys were typed. The renderer rewrites only the
// cells that changed, so a hint that differs from the previous frame by a
// few characters never appears whole in the raw output; the screen does.
func (s *homeSession) expectScreen(text string) {
	s.t.Helper()
	screen := func(b string) string {
		v := vt{height: 24} // inline printing needs the terminal's height
		v.write([]byte(b))
		return v.String()
	}
	before := strings.Count(screen(s.out.String()[:s.mark]), text)
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Count(screen(s.out.String()), text) > before {
			return
		}
	}
	s.t.Fatalf("%q never showed on the screen after the last keys:\n%s", text, screen(s.out.String()))
}

// ended waits for the composer back after an Accepted Run: its hint line
// names the actions. An Accepted Run that wasn't applied is continued by
// the next message (#185), so the frame's last line, drawn after the hint,
// names that Run.
func (s *homeSession) ended() {
	s.t.Helper()
	s.expect("↓ actions")
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		out := s.out.String()
		if strings.Contains(out[strings.LastIndex(out, "↓ actions"):], "(/new for a fresh task)") {
			return
		}
	}
	s.t.Fatalf("the composer after the Run was never drawn whole:\n%s", s.out.String())
}

// actions moves the focus to the action row (↓ on an empty composer) and
// waits for it to take keys: until actionsArm has passed it drops them as
// typed ahead.
func (s *homeSession) actions() {
	s.t.Helper()
	s.type_("\x1b[B")
	s.expect("[a] apply")
	time.Sleep(actionsArm + 50*time.Millisecond)
}

func (s *homeSession) type_(keys string) {
	s.t.Helper()
	s.mark = len(s.out.String())
	if _, err := s.pw.WriteString(keys); err != nil {
		s.t.Fatal(err)
	}
}

func (s *homeSession) wait() int {
	s.t.Helper()
	select {
	case code := <-s.exit:
		return code
	case <-time.After(120 * time.Second):
		s.t.Fatalf("oge never exited\n%s", s.out.String())
		return -1
	}
}

// The whole session in one terminal: compose, Run, Accepted, the action
// bar, and the composer back, empty, until Ctrl+C leaves with 0.
func TestBareOgeComposesRunsAndLoops(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	// The whole scrollback now, on an 80x24 terminal (the pipe reports no
	// size): the transcript of the Run, and the composer back, empty.
	got := homeNorm.ReplaceAllString(s.settledScreen(), "X")
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if n := len(runDirs(t, f)); n != 1 {
		t.Errorf("%d Runs", n)
	}
	if !strings.Contains(got, "Result        ✓ Accepted") {
		t.Errorf("no Accepted Receipt:\n%s", got)
	}
	golden(t, "home-after-accepted", got)
	// Leaving the composer takes its frame off the screen.
	var end vt
	end.write([]byte(s.out.String()))
	if strings.Contains(end.String(), "Describe the task") {
		t.Errorf("the composer's frame stayed after quitting:\n%s", end.String())
	}
}

// A second Task in the same session, written in $EDITOR with Ctrl+E.
func TestBareOgeRunsASecondTaskFromTheEditor(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	var s *homeSession
	var edited []string
	f.edit = func(path string) error {
		b, _ := os.ReadFile(path)
		edited = append(edited, string(b))
		return os.WriteFile(path, append([]byte("second task zq7\n"), b...), 0o644)
	}
	s = startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	s.type_("\x05")                   // Ctrl+E on an empty box
	s.expectScreen("second task zq7") // the box shows what the editor left
	s.type_("\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if len(edited) != 1 || !strings.Contains(edited[0], "<!--") {
		t.Errorf("the editor got %q; an empty box should open the template", edited)
	}
	if n := len(runDirs(t, f)); n != 2 {
		t.Errorf("%d Runs", n)
	}
}

// Two Tasks in one session run in one Bubble Tea program, never a second
// one (#183). The action bar's keys work inside it: [d] prints the diff and
// [a] applies.
func TestBareOgeTwoTasksInOneProgram(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	var programs atomic.Int32
	s := startHomeSessionWith(t, f, func(env *Env) { env.programStarted = func() { programs.Add(1) } }, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.expect("Result        ✓ Accepted")
	s.ended()
	// The next message is typed straight away: its a applies nothing.
	s.type_("add tests\r")
	s.expect("> add tests")
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); strings.Contains(string(b), "return a + b") {
		t.Fatal("typing the next Task applied the Candidate")
	}
	s.expect("ÖGE RECEIPT") // the second Run's, after the keys
	s.ended()
	s.type_("/diff\r")
	s.expect("diff --git")
	s.actions()
	s.type_("a")
	s.expect("Applied Candidate")
	if b, _ := os.ReadFile(filepath.Join(f.repo, "add.go")); !strings.Contains(string(b), "return a + b") {
		t.Errorf("[a] didn't apply the Candidate: add.go is\n%s", b)
	}
	s.type_("\x03\x03") // back to the composer, then leave
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if n := len(runDirs(t, f)); n != 2 {
		t.Errorf("%d Runs", n)
	}
	if n := programs.Load(); n != 1 {
		t.Errorf("%d programs started; one owns the session", n)
	}
	// One continuous transcript: both Tasks, in order, with their Receipts.
	screen := vt{height: 24}
	screen.write([]byte(s.out.String()))
	all := screen.String()
	first, second := strings.Index(all, "> fix Add"), strings.Index(all, "> add tests")
	if first < 0 || second < first || strings.Count(all, "ÖGE RECEIPT") != 2 {
		t.Errorf("not one transcript in order:\n%s", all)
	}
}

// The action bar drops keys typed before it could be seen: an a typed ahead
// /branch <name> takes the Run as a branch, with oge branch's own code: a
// second one with the same name gets its refusal.
func TestBareOgeBranchCommand(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	// oge branch commits; CI runners have no git identity.
	gitIn(t, f.repo, "config", "user.name", "u")
	gitIn(t, f.repo, "config", "user.email", "u@example.com")
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	s.type_("/branch try1\r")
	s.expect("Created branch try1")
	if out, err := exec.Command("git", "-C", f.repo, "rev-parse", "--verify", "refs/heads/try1").CombinedOutput(); err != nil {
		t.Fatalf("no branch try1: %v %s", err, out)
	}
	s.type_("/branch try1\r")
	s.expect("branch try1 already exists")
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}

// A Gate in the session: it opens in the live area, and its outcome is
// printed into the transcript with the stages.
func TestBareOgeGateInTheTranscript(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake", "--confirm")
	s.composer()
	s.type_("fix Add\r")
	s.expect("Result gate")
	s.type_("t\r")
	s.ended()
	got := homeNorm.ReplaceAllString(s.settledScreen(), "X")
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	golden(t, "home-gate", got)
}

// Delivery never fires on a bare printable key while the composer can take
// text (#184): a typed into the composer is text; ↓ on an empty composer
// focuses the action row, whose keys act only after actionsArm; any other
// key goes back to the composer; and the slash commands act on Enter.
func TestHomeDeliveryNeedsAnExplicitAct(t *testing.T) {
	at := time.Unix(0, 0)
	applied := 0
	h := testHome(70)
	h.now = func() time.Time { return at }
	diffs := 0
	h.last = &lastRun{apply: func() string { applied++; return "applied" },
		diff: func() tea.Cmd { diffs++; return nil }}
	key := func(k string) {
		if k == "down" {
			h.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			return
		}
		h.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
	}
	key("a")
	if applied != 0 || h.comp.text() != "a" {
		t.Fatalf("a on the composer: applied %d, text %q", applied, h.comp.text())
	}
	h.comp = newComposer()
	if !strings.Contains(frame(h), "↓ actions · /apply /diff /receipt /branch · /new") {
		t.Errorf("no hint line:\n%s", frame(h))
	}
	key("down")
	key("a") // typed ahead of the row
	if applied != 0 || !h.actions {
		t.Fatalf("a before the row armed: applied %d, focused %v", applied, h.actions)
	}
	at = at.Add(actionsArm)
	key("a")
	if applied != 1 || h.actions {
		t.Fatalf("a fresh a on the row: applied %d times, still focused %v", applied, h.actions)
	}
	// Each ↓ allows one action: after d (and its pager) an a is text.
	key("down")
	at = at.Add(actionsArm)
	key("d")
	key("a")
	if diffs != 1 || applied != 1 || h.comp.text() != "a" {
		t.Fatalf("a after d: diffs %d, applied %d, text %q", diffs, applied, h.comp.text())
	}
	h.comp = newComposer()
	// A paste goes to the composer and takes the focus back.
	key("down")
	h.Update(tea.PasteMsg{Content: "add tests"})
	at = at.Add(actionsArm)
	key("a")
	if applied != 1 || h.comp.text() != "add testsa" {
		t.Fatalf("a after a paste: applied %d, text %q", applied, h.comp.text())
	}
	h.comp = newComposer()
	key("down")
	at = at.Add(actionsArm)
	key("x")
	if h.actions || h.comp.text() != "x" {
		t.Fatalf("typing on the row: focused %v, text %q", h.actions, h.comp.text())
	}
	h.comp.set("/apply")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if applied != 2 || !h.comp.empty() || h.run != nil {
		t.Fatalf("/apply: applied %d, text %q, run %v", applied, h.comp.text(), h.run != nil)
	}
	// A command Öge doesn't know is said so, and the text stays; a path is
	// a Task.
	var started []string
	h.start = func(tk task.Task, _ *resolved, _, _ int) (*model, tea.Cmd) {
		started = append(started, tk.Title)
		return newModel(tk, homeFrozen(), newStyles(false), time.Now), nil
	}
	h.comp.set("/aply")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(h.msg) == 0 || !strings.Contains(h.msg[0], "/aply: unknown command") || h.comp.text() != "/aply" || len(started) != 0 {
		t.Fatalf("/aply: msg %q, text %q, started %q", h.msg, h.comp.text(), started)
	}
	h.comp.set("/tmp/foo is broken")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(started) != 1 {
		t.Fatalf("a path didn't start a Task: %q", started)
	}
	h.run = nil
	h.comp.set("/new")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.comp.set("/apply")
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if applied != 2 || h.last != nil || len(h.msg) == 0 {
		t.Fatalf("/apply after /new: applied %d, msg %q", applied, h.msg)
	}
}

// A config edited while the composer is open is read again when the Task is
// sent: a Check that no longer covers the tests refuses, as oge "task" would,
// and no Run starts.
func TestBareOgeRefusesAConfigEditedWhileComposing(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	cfg := strings.Replace(fxConfig, "./...", "./elsewhere/...", 1)
	if cfg == fxConfig {
		t.Fatal("the fixture's Check changed; adjust this test")
	}
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	s.type_("fix Add\r")
	s.expect("this Run isn't valid")
	s.type_("\x03\x03") // clear the Task, then leave
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if n := len(runDirs(t, f)); n != 0 {
		t.Errorf("%d Runs started", n)
	}
}

func runDirs(t *testing.T, f *runFixture) []string {
	t.Helper()
	runs, _ := filepath.Glob(filepath.Join(f.state, "private", "runs", "*"))
	return runs
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSetupBarNamesAProfile(t *testing.T) {
	f := homeFrozen()
	f.Stages[0].Profile = "z"
	if got := setupLines(f)[0]; !strings.Contains(got, "implement claude@z:sonnet") {
		t.Errorf("setup bar: %s", got)
	}
}

// The status line sits in the live area during a Run, and Ctrl+O toggles
// the expanded activity there without disturbing an open Gate (#147).
func TestBareOgeStatusLineAndCtrlO(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake", "--confirm")
	s.composer()
	s.type_("fix Add\r")
	s.expect("Result gate")
	s.expect("Fast mode · implement")
	s.expect("Waiting for you")
	s.type_("\x0f")
	// The renderer rewrites only changed cells, so a short run of new text
	// is the marker; the collapse is covered by TestTUIEscCollapsesAtAGate.
	s.expectScreen("ctrl+o to collapse")
	s.type_("\x0f")
	s.type_("t\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}

// Two Tasks, then ↑ recalls them newest first and Ctrl+R finds one by a
// substring; Enter puts it in the box and a second Enter runs it. The
// history is in the state dir, mode 0600, and holds only what was typed.
func TestBareOgeHistoryRecallAndSearch(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	s.type_("add tests zq7\r")
	s.expect("ÖGE RECEIPT")
	s.ended()
	s.type_("\x1b[A") // ↑: the newest
	s.expectScreen("add tests zq7")
	s.type_("\x1b[A") // ↑: the one before
	s.expectScreen("│ fix Add")
	s.type_("\x1b[B\x1b[B") // ↓ back to the newest, then past it: empty
	s.type_("\x12")         // Ctrl+R
	s.expectScreen("(reverse-i-search)''")
	s.type_("zq7")
	s.expectScreen("(reverse-i-search)'zq7': add tests zq7")
	s.type_("\r") // accepts into the box, doesn't submit
	s.expectScreen("│ add tests zq7")
	s.type_("\x03\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if n := len(runDirs(t, f)); n != 2 {
		t.Errorf("%d Runs; accepting a search must not run a Task", n)
	}
	files, _ := filepath.Glob(filepath.Join(f.state, "private", "history", "*"))
	if len(files) != 1 {
		t.Fatalf("history files %v", files)
	}
	if got := strings.Join(loadHistory(files[0]).entries, "|"); got != "fix Add|add tests zq7" {
		t.Errorf("history %q", got)
	}
}

// Kill, yank and undo in the composer, then submit what is left (#151).
func TestBareOgeComposerKillYankUndoAndSubmit(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	// Ctrl+W kills "stale"; "x" is typed; Ctrl+Y brings the kill back; Ctrl+_
	// undoes the yank; Ctrl+Y yanks it again.
	s.type_("fix Add stale\x17x\x19\x1f\x19\r")
	s.expect("> fix Add xstale")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}
