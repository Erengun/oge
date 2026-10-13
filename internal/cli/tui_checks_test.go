package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/pipeline"
)

func typ(h *home, s string) {
	for _, r := range s {
		h.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// checksHome is a setup home over a real repo with one test file at its
// root, and a config that is run 1 of #114: every test is protected and the
// Check runs one package.
func checksHome(t *testing.T, check string) (*home, string) {
	t.Helper()
	h, dir := setupHome(t)
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	writeFile(t, filepath.Join(repo, "add_test.go"), []byte(fxTest))
	writeFile(t, filepath.Join(repo, "sub", "sub_test.go"), []byte(fxTest))
	cfg := "schema = 1\n\n# the Oracle\n[project]\ntest_globs = [\"**/*_test.go\"]\n\n[[check.commands]]\nrun = \"" + check + "\"\nreport = \"go-test-json\"\n"
	writeFile(t, filepath.Join(repo, ".oge", "oge.toml"), []byte(cfg))
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "fixture")
	cf, probs := pipeline.Load([]byte(cfg))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	h.sel = newSetupState(h.sel.env, repo)
	h.sel.init(&resolved{cfg: cf, frozen: homeFrozen()})
	h.save = h.sel.save
	return h, repo
}

func TestChecksSectionGolden(t *testing.T) {
	h, _ := checksHome(t, "go test -json ./...")
	press(h, tab())
	golden(t, "setup-checks", frame(h))
}

func TestChecksInlineRefusalForRun1Of114(t *testing.T) {
	h, _ := checksHome(t, "go test -json ./sub/...")
	press(h, tab())
	got := frame(h)
	if !strings.Contains(got, "✗ refused: the Check can't run 1 protected test file in 1 package dir (.)") {
		t.Fatalf("no inline refusal:\n%s", got)
	}
	golden(t, "setup-checks-refused", got)
}

// Editing a test glob turns the refusal into a valid config, live, and the
// edit goes to the next Run as an override, not into the user config.
func TestChecksEditingAGlobTurnsCoverageValid(t *testing.T) {
	h, dir := checksHome(t, "go test -json ./sub/...")
	press(h, tab())
	cursorTo(h, rowGlob)
	press(h, enter(), tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	typ(h, "sub/**/*_test.go")
	if got := frame(h); !strings.Contains(got, "✓ these Checks can run") {
		t.Fatalf("not valid while typing:\n%s", got)
	}
	press(h, enter())
	f := h.sel.flags(runFlags{})
	if !f.o.TUIOracle || len(f.o.TUITests) != 1 || f.o.TUITests[0] != "sub/**/*_test.go" {
		t.Errorf("overrides %+v", f.o)
	}
	if _, err := os.Stat(filepath.Join(dir, ".config")); err == nil {
		t.Error("the user config was written")
	}
	press(h, tab())
	if h.saving {
		t.Error("an Oracle edit asked to save into the user config")
	}
}

func TestChecksReviewedSaveWritesTheFileAndNeverCommits(t *testing.T) {
	h, repo := checksHome(t, "go test -json ./...")
	before := gitOut(t, repo, "rev-list", "--count", "HEAD")
	orig, _ := os.ReadFile(filepath.Join(repo, ".oge", "oge.toml"))
	press(h, tab())
	cursorTo(h, rowAddGlob)
	press(h, enter())
	typ(h, "testdata/**")
	press(h, enter(), tea.KeyPressMsg{Code: 'w', Text: "w"})
	if h.review == nil {
		t.Fatalf("no review:\n%s", frame(h))
	}
	got := frame(h)
	for _, want := range []string{`+ test_globs = ["**/*_test.go", "testdata/**"]`, `- test_globs = ["**/*_test.go"]`, "# the Oracle"} {
		if !strings.Contains(got, want) {
			t.Errorf("review lacks %q:\n%s", want, got)
		}
	}
	golden(t, "setup-checks-review", got)
	press(h, enter()) // not an answer
	if now, _ := os.ReadFile(filepath.Join(repo, ".oge", "oge.toml")); string(now) != string(orig) {
		t.Fatal("written before a yes")
	}
	press(h, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if h.review != nil {
		t.Fatal("n left the review open")
	}
	press(h, tea.KeyPressMsg{Code: 'w', Text: "w"}, tea.KeyPressMsg{Code: 'y', Text: "y"})
	now, _ := os.ReadFile(filepath.Join(repo, ".oge", "oge.toml"))
	if !strings.Contains(string(now), `"testdata/**"`) || string(now) == string(orig) {
		t.Errorf("file:\n%s", now)
	}
	if gitOut(t, repo, "rev-list", "--count", "HEAD") != before || !strings.Contains(gitOut(t, repo, "status", "--porcelain"), " M .oge/oge.toml") {
		t.Error("the save committed, or didn't leave the change in the working tree")
	}
	press(h, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if h.review != nil {
		t.Error("offered to save what is already saved")
	}
}

func TestChecksSetupBarSaysFromSetup(t *testing.T) {
	f := homeFrozen()
	f.Checks[0].Source = pipeline.FromTUI
	if got := strings.Join(setupLines(f), "\n"); !strings.Contains(got, "Checks: go test -json ./... (from setup)") {
		t.Errorf("%s", got)
	}
}

func TestBareOgeSetupEditsAGlobSavesItAndRuns(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	before := gitOut(t, f.repo, "rev-list", "--count", "HEAD")
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.type_("\t")
	s.expectScreen("Checks   next Run")
	// Down to "test glob 1" (Fast mode, fake agent: eight rows down; past the last row ↓ scrolls).
	s.type_(strings.Repeat("\x1b[B", 8) + "\r")
	s.expectScreen("> **/*_test.go")
	s.type_("\x15*_test.go\r")
	s.expectScreen("test glob 1        *_test.go")
	// Narrowing the glob is a weaker Oracle, and the Run says so.
	s.type_("w")
	s.expect("Save these Checks to .oge/oge.toml?")
	s.type_("y")
	s.expect("Saved .oge/oge.toml")
	s.type_("\t")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	b, _ := os.ReadFile(filepath.Join(f.repo, ".oge", "oge.toml"))
	if !strings.Contains(string(b), `test_globs = ["*_test.go"]`) {
		t.Errorf("file:\n%s", b)
	}
	if gitOut(t, f.repo, "rev-list", "--count", "HEAD") != before {
		t.Error("Öge committed")
	}
}

func TestBareOgeFirstRunChecksSectionIsTheProposal(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	gitIn(t, f.repo, "rm", "-q", ".oge/oge.toml")
	gitIn(t, f.repo, "commit", "-q", "-m", "no config")
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.expectScreen("no .oge/oge.toml yet")
	s.type_("\t")
	s.expectScreen("go test -json ./...")
	s.type_("w")
	s.expect("Save these Checks to .oge/oge.toml?")
	s.expect("+ run     = \"go test -json ./...\"")
	if _, err := os.Stat(filepath.Join(f.repo, ".oge", "oge.toml")); err == nil {
		t.Fatal("written before a yes")
	}
	s.type_("y")
	s.expect("Saved .oge/oge.toml")
	s.type_("\t")
	s.composer()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	b, _ := os.ReadFile(filepath.Join(f.repo, ".oge", "oge.toml"))
	if !strings.Contains(string(b), "[[check.commands]]") || !strings.Contains(gitOut(t, f.repo, "status", "--porcelain"), "?? .oge/") {
		t.Errorf("file:\n%s\nstatus:\n%s", b, gitOut(t, f.repo, "status", "--porcelain"))
	}
}

// anyRunFile reports whether a file under the Run's state holds all of the
// substrings.
func anyRunFile(t *testing.T, f *runFixture, subs ...string) bool {
	t.Helper()
	found := false
	for _, d := range runDirs(t, f) {
		_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			b, _ := os.ReadFile(p)
			ok := true
			for _, sub := range subs {
				ok = ok && strings.Contains(string(b), sub)
			}
			found = found || ok
			return nil
		})
	}
	return found
}

// A narrowed glob, left unsaved, is a weaker Oracle: the bar and the
// Receipt say so, and the Run still starts.
func TestBareOgeUnsavedNarrowedGlobIsTrustWeakening(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.type_("\t")
	s.expectScreen("Checks   next Run")
	s.type_(strings.Repeat("\x1b[B", 8) + "\r")
	s.expectScreen("> **/*_test.go")
	s.type_("\x15*_test.go\r")
	s.expectScreen("test glob 1        *_test.go")
	s.type_("\t")
	s.expect("! trust-weakening: test glob removed in setup: **/*_test.go (from setup)")
	s.expect("Test globs: *_test.go (from setup)")
	s.type_("fix Add\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if !strings.Contains(s.out.String(), "trust-weakening option in effect: test glob removed in setup") {
		t.Errorf("the Receipt doesn't say so:\n%s", s.out.String())
	}
	b, _ := os.ReadFile(filepath.Join(f.repo, ".oge", "oge.toml"))
	if !strings.Contains(string(b), `"**/*_test.go"`) {
		t.Errorf("an unsaved edit changed the file:\n%s", b)
	}
}

// First run, never saved: the on-screen proposal is what runs, labelled
// "(from setup)", with the proposal's test_config.
func TestBareOgeRunsFromTheUnsavedProposal(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	gitIn(t, f.repo, "rm", "-q", ".oge/oge.toml")
	gitIn(t, f.repo, "commit", "-q", "-m", "no config")
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.expect("Checks: go test -json ./... (from setup)")
	s.type_("fix Add\r")
	s.ended()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	if strings.Contains(s.out.String(), "test_config is empty") {
		t.Error("the proposal's test_config was dropped")
	}
	if !anyRunFile(t, f, `"go.mod"`, `"go.sum"`, `"tui"`) {
		t.Error("the frozen record lacks the proposal's test_config and its source")
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".oge", "oge.toml")); err == nil {
		t.Error("the proposal was written without a yes")
	}
}

// A project Öge doesn't recognise opens the composer with empty Checks; a
// Run can't start until there is a Check and a test glob.
func TestBareOgeUnknownToolchainOpensEmptyChecks(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	gitIn(t, f.repo, "rm", "-q", ".oge/oge.toml", "go.mod")
	gitIn(t, f.repo, "commit", "-q", "-m", "no config")
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.expectScreen("no Check command")
	s.type_("\t")
	s.expectScreen("Checks   no .oge/oge.toml yet")
	s.expectScreen("✗ refused: no Check command")
	s.type_("\x03\x03") // closes the panel, then leaves the empty box
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
}

func TestChecksWriteRefusesAFileChangedSinceTheReview(t *testing.T) {
	h, repo := checksHome(t, "go test -json ./...")
	h.sel.globs = append(h.sel.globs, "more/**")
	h.sel.touchOracle()
	r, err := h.sel.reviewProject()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".oge", "oge.toml")
	writeFile(t, path, []byte("schema = 1\n# edited meanwhile\n"))
	if err := h.sel.writeProject(r); err != errChanged {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "edited meanwhile") {
		t.Errorf("overwritten:\n%s", b)
	}
}

// --check and --tests aren't offered for editing (Resolve would add them
// again); they are shown, and count for the coverage check.
func TestChecksFlagEntriesAreReadOnly(t *testing.T) {
	h, _ := checksHome(t, "go test -json ./...")
	var f runFlags
	f.o.Checks, f.o.Tests = []string{"go test ./x"}, []string{"x/**"}
	fr := homeFrozen()
	fr.Checks = append(fr.Checks, pipeline.CheckCommand{Run: "go test ./x", Source: pipeline.FromCLI})
	fr.Project.TestGlobs = append(fr.Project.TestGlobs, "x/**")
	h.sel.ready, h.sel.touched = false, false
	h.sel.init(&resolved{frozen: fr, flags: f})
	if len(h.sel.checks) != 1 || len(h.sel.globs) != 1 {
		t.Fatalf("editable: %v %v", h.sel.checks, h.sel.globs)
	}
	press(h, tab())
	if got := frame(h); !strings.Contains(got, "(from flags) check: go test ./x") || !strings.Contains(got, "(from flags) test glob: x/**") {
		t.Errorf("flags not shown:\n%s", got)
	}
}
