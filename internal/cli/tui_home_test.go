package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		Checks: []pipeline.CheckCommand{{Run: "go test -json ./..."}},
	}
}

func testHome(width int) *home {
	f := homeFrozen()
	h := &home{comp: newComposer(), title: "oge · demo · main", setup: setupLines(f), st: newStyles(false)}
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
	h.start = func(tk task.Task, _, _ int) (*model, tea.Cmd) {
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
	if !h.comp.empty() || h.quit {
		t.Fatal("Esc with text should clear it")
	}
	if _, cmd := h.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); !h.quit || cmd == nil {
		t.Fatal("Esc on an empty composer should quit")
	}
	h = testHome(70)
	h.start = func(tk task.Task, _, _ int) (*model, tea.Cmd) {
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

// normalized hides what differs between runs: ids, hashes and durations.
var homeNorm = regexp.MustCompile(`\d{8}T\d{6}-[0-9a-f]{6}|[0-9a-f]{7,64}\b|\d+(\.\d+)?m?s\b`)

// The whole session in one terminal: compose, Run, Accepted, the action
// bar, and the composer back, empty, until Ctrl+C leaves with 0.
func TestBareOgeComposesRunsAndLoops(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.interactive = true
	f.setenv("TERM", "xterm-256color")
	f.setenv("NO_COLOR", "1")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()
	defer pr.Close()
	script := filepath.Join(filepath.Dir(f.repo), "agent.sh")
	if err := os.WriteFile(script, []byte(fixScript), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut syncBuf
	env := f.envWithFake(script, &out, &errOut)
	env.Stdin = pr
	exit := make(chan int, 1)
	go func() { exit <- Main(env, []string{"--fast", "--agent", "fake"}) }()
	typeAfter := func(n int, shown, keys string) {
		t.Helper()
		out.waitForN(t, shown, n)
		time.Sleep(100 * time.Millisecond) // the program's read loop is up
		if _, err := pw.WriteString(keys); err != nil {
			t.Fatal(err)
		}
	}
	typeAfter(1, "Describe the task", "fix Add\r")
	typeAfter(1, "[a] apply", "q")
	out.waitForN(t, "Describe the task", 2)
	time.Sleep(200 * time.Millisecond)
	// What the terminal shows now: the Run's summary above, and the
	// composer back, empty.
	var screen vt
	screen.write([]byte(out.String()))
	got := homeNorm.ReplaceAllString(screen.String(), "X")
	typeAfter(2, "Describe the task", "\x03")
	select {
	case code := <-exit:
		if code != ExitOK {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
		}
	case <-time.After(120 * time.Second):
		t.Fatalf("oge never exited\n%s", out.String())
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
	end.write([]byte(out.String()))
	if strings.Contains(end.String(), "Describe the task") {
		t.Errorf("the composer's frame stayed after quitting:\n%s", end.String())
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

// waitForN waits until text has been written at least n times.
func (s *syncBuf) waitForN(t *testing.T, text string, n int) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if strings.Count(s.String(), text) >= n {
			return
		}
	}
	t.Fatalf("%q never appeared %d times in:\n%s", text, n, s.String())
}
