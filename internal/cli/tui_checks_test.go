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

// rowsDown moves the cursor n rows.
func rowsDown(h *home, n int) {
	for range n {
		press(h, down())
	}
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
	rowsDown(h, 7) // the first test glob (no profile row: agent claude has one; see rows)
	for i, r := range h.panel.rows() {
		if r.kind == rowGlob {
			h.panel.cursor = i
		}
	}
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
	for i, r := range h.panel.rows() {
		if r.kind == rowAddGlob {
			h.panel.cursor = i
		}
	}
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
	s.expect("Checks   next Run")
	s.type_(strings.Repeat("\x1b[B", 7) + "\r") // the first test glob
	s.expect("> **/*_test.go")
	s.type_("\x15*_test.go\r")
	s.expect("✓ these Checks can run the protected tests")
	s.type_("w")
	s.expect("Save these Checks to .oge/oge.toml?")
	s.type_("y")
	s.expect("Saved .oge/oge.toml")
	s.type_("\t")
	s.composer()
	s.type_("fix Add\r")
	s.expect("[a] apply")
	s.type_("q")
	s.composer()
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
	s.expect("no .oge/oge.toml yet")
	s.type_("\t")
	s.expect("go test -json ./...")
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
