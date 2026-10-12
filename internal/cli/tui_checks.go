package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/workspace"
)

// The setup panel's Checks section (#132): the Check commands and test globs
// are the Oracle (ADR-0011), so editing them is a human act that is recorded:
// the frozen Pipeline shows them with the source "setup", and one the repo's
// file has and the panel dropped is trust-weakening. They apply to the next
// Run as overrides and are never written to the user config. Only the
// explicit save writes .oge/oge.toml, after a reviewed diff, and never
// commits it.

// oracleState is the Checks section's working copy.
type oracleState struct {
	checks     []pipeline.CheckCommandConfig
	globs      []string
	baseChecks []pipeline.CheckCommandConfig // what .oge/oge.toml has (none on a first run)
	baseGlobs  []string
	touched    bool // edited: sent to the next Run as overrides
	noFile     bool // first run: no .oge/oge.toml; the Checks are Öge's proposal
	seed       string
	outputs    []string // the proposal's output globs, for a first run
	testConfig []string // and its test_config
	// flagChecks and flagTests came from --check and --tests: they are not
	// the panel's to edit, but they count for the coverage check.
	flagChecks, flagTests []string

	paths   []string
	pathsOK bool
	memo    struct {
		key     string
		refusal string
		warns   []string
	}
}

// seedProposal makes the Checks section the first-run proposal: the detected
// Checks and globs, pre-filled. It reports whether this project's toolchain
// was recognised.
func (s *setupState) seedProposal() bool {
	p, ok := pipeline.Propose(s.root)
	if !ok {
		return false
	}
	cfg, probs := pipeline.Load([]byte(p.TOML))
	if len(probs) > 0 {
		return false
	}
	s.noFile, s.seed, s.touched = true, p.TOML, true
	s.checks = append([]pipeline.CheckCommandConfig(nil), cfg.Check.Commands...)
	s.globs = append([]string(nil), cfg.Project.TestGlobs...)
	s.outputs, s.testConfig = cfg.Project.OutputGlobs, cfg.Project.TestConfig
	return true
}

// seedEmpty is the first run of a project Öge doesn't recognise: the Checks
// section starts empty, and a Run can't start until it has a Check and a glob.
func (s *setupState) seedEmpty() {
	s.noFile, s.seed, s.touched = true, "schema = 1\n", true
}

// syncOracle takes the repo's Checks and globs as the shown ones, unless the
// human edited them.
func (s *setupState) syncOracle(res *resolved) {
	s.flagChecks, s.flagTests = nil, nil
	for _, c := range res.flags.o.Checks {
		s.flagChecks = append(s.flagChecks, strings.TrimSpace(c))
	}
	for _, g := range res.flags.o.Tests {
		s.flagTests = append(s.flagTests, strings.TrimSpace(g))
	}
	if s.touched || s.noFile {
		return
	}
	if res.cfg != nil {
		s.checks = append([]pipeline.CheckCommandConfig(nil), res.cfg.Check.Commands...)
		s.globs = append([]string(nil), res.cfg.Project.TestGlobs...)
	} else {
		// The frozen Pipeline has the flags' entries too; they are shown
		// apart, so the panel doesn't offer them for editing (Resolve would
		// add them again).
		s.checks = nil
		for _, c := range res.frozen.Checks {
			if c.Source != pipeline.FromCLI {
				s.checks = append(s.checks, pipeline.CheckCommandConfig{Run: c.Run, Report: c.Report})
			}
		}
		s.globs = nil
		skip := append([]string(nil), s.flagTests...)
		for _, g := range res.frozen.Project.TestGlobs {
			if i := slices.Index(skip, g); i >= 0 {
				skip = slices.Delete(skip, i, i+1)
				continue
			}
			s.globs = append(s.globs, g)
		}
	}
	s.baseChecks = append([]pipeline.CheckCommandConfig(nil), s.checks...)
	s.baseGlobs = append([]string(nil), s.globs...)
}

// oracleFlags adds the edited Checks and globs to the flags for the next Run.
func (s *setupState) oracleFlags(f runFlags) runFlags {
	if !s.touched {
		return f
	}
	f.o.TUIOracle = true
	f.o.TUIChecks = append([]pipeline.CheckCommandConfig(nil), s.checks...)
	f.o.TUITests = append([]string(nil), s.globs...)
	f.o.TUITestConfig = append([]string(nil), s.testConfig...)
	f.o.TUIOutputs = append([]string(nil), s.outputs...)
	return f
}

func (s *setupState) touchOracle() { s.touched = true; s.rev++ }

// oracleDirty says the Checks or globs differ from what .oge/oge.toml has.
func (s *setupState) oracleDirty() bool {
	return s.noFile || !reflect.DeepEqual(nilIfEmpty(s.checks), nilIfEmpty(s.baseChecks)) ||
		!reflect.DeepEqual(nilIfEmpty(s.globs), nilIfEmpty(s.baseGlobs))
}

func nilIfEmpty[T any](v []T) []T {
	if len(v) == 0 {
		return nil
	}
	return v
}

// coverage is the #120 check over the given Checks and globs, on the
// Snapshot as it was when the panel opened. The last answer is kept, so
// drawing the panel again costs nothing.
func (s *setupState) coverage(checks []pipeline.CheckCommandConfig, globs []string) (refusal string, warns []string) {
	runs := append([]string(nil), s.flagChecks...)
	for _, c := range checks {
		if r := strings.TrimSpace(c.Run); r != "" {
			runs = append(runs, r)
		}
	}
	gs := append([]string(nil), s.flagTests...)
	for _, g := range globs {
		if g = strings.TrimSpace(g); g != "" {
			gs = append(gs, g)
		}
	}
	key := strings.Join(runs, "\x00") + "\x01" + strings.Join(gs, "\x00")
	if s.memo.key == key && s.pathsOK {
		return s.memo.refusal, s.memo.warns
	}
	if !s.pathsOK {
		s.paths, _ = workspace.SnapshotPaths(s.root) // a Snapshot that can't be listed makes no claim
		s.pathsOK = true
	}
	refusal, warns = "", nil
	switch {
	case len(runs) == 0:
		refusal = "no Check command: a Run can't accept anything without one"
	case len(gs) == 0:
		refusal = "no test globs: the Oracle's tests must be named"
	default:
		refusal, warns = checkCoverage(s.root, s.paths, gs, runs)
	}
	s.memo.key, s.memo.refusal, s.memo.warns = key, refusal, warns
	return refusal, warns
}

// reopen forgets the Snapshot's file list: the tree may have changed since.
func (s *setupState) reopen() { s.pathsOK = false }

func (s *setupState) projectPath() string {
	return filepath.Join(s.root, filepath.FromSlash(pipeline.ConfigPath))
}

var errChanged = errors.New(pipeline.ConfigPath + " changed since you reviewed it")

// projectReview is the reviewed save of the Checks into .oge/oge.toml.
type projectReview struct {
	text string
	diff []string
	old  string // the file as reviewed; "" when there was none
	had  bool
}

// reviewProject builds the text that would be written and its diff.
func (s *setupState) reviewProject() (*projectReview, error) {
	if refusal, _ := s.coverage(s.checks, s.globs); refusal != "" {
		return nil, errors.New("not saved: these Checks would be refused, so fix them first (" + refusal + ")")
	}
	if !s.oracleDirty() {
		return nil, errors.New("nothing to save: the Checks and test globs are as " + pipeline.ConfigPath + " has them")
	}
	old, had := s.seed, false
	if !s.noFile {
		had = true
		b, err := os.ReadFile(s.projectPath())
		if err != nil {
			return nil, fmt.Errorf("reading %s: %v", pipeline.ConfigPath, err)
		}
		old = string(b)
	}
	text, err := pipeline.RenderOracle(old, s.checks, s.globs)
	if err != nil {
		var want []string
		for _, c := range s.checks {
			want = append(want, c.Run)
		}
		return nil, fmt.Errorf("%v; set Checks %q and test_globs %q by hand", err, want, s.globs)
	}
	before := ""
	if had {
		before = old
	}
	return &projectReview{text: text, diff: lineDiff(before, text), old: before, had: had}, nil
}

// writeProject writes the reviewed text. It never commits.
//
// The file is read again first: if it changed since it was reviewed, nothing
// is written (errChanged) and the human reviews again.
func (s *setupState) writeProject(r *projectReview) error {
	path := s.projectPath()
	cur, err := os.ReadFile(path)
	switch {
	case err == nil && (!r.had || string(cur) != r.old):
		return errChanged
	case errors.Is(err, os.ErrNotExist) && r.had:
		return errChanged
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".oge.toml-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(r.text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// The file is the repo's now: later edits are measured against it.
	s.baseChecks = append([]pipeline.CheckCommandConfig(nil), s.checks...)
	s.baseGlobs = append([]string(nil), s.globs...)
	s.noFile, s.touched, s.seed = false, false, ""
	s.rev++
	return nil
}

// lineDiff is a line diff with two lines of context, as "+ ", "- " and "  "
// lines.
func lineDiff(before, after string) []string {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimRight(s, "\n"), "\n")
	}
	a, b := split(before), split(after)
	// lcs[i][j] is the longest common run of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var all []string
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			all = append(all, "  "+a[i])
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			all = append(all, "+ "+b[j])
			j++
		default:
			all = append(all, "- "+a[i])
			i++
		}
	}
	const context = 2
	near := make([]bool, len(all))
	for k, l := range all {
		if l[0] != ' ' {
			for d := -context; d <= context; d++ {
				if k+d >= 0 && k+d < len(all) {
					near[k+d] = true
				}
			}
		}
	}
	var out []string
	skipped := false
	for k, l := range all {
		if near[k] {
			out = append(out, l)
			skipped = false
		} else if !skipped {
			out = append(out, "  …")
			skipped = true
		}
	}
	return out
}

// ---- the panel's part ----

// checksHeader is the Checks section's title line.
func (p *setupPanel) checksHeader() string {
	st, s := p.st, p.sel
	note := "next Run · [enter] edit · [d/x] remove · [w] save to " + pipeline.ConfigPath
	switch {
	case s.noFile:
		note = "no " + pipeline.ConfigPath + " yet: Öge's proposal from your project · [enter] edit · [d/x] remove · [w] save them to " + pipeline.ConfigPath
	case s.oracleDirty():
		note += " · edited, not saved"
	}
	return st.bold("Checks") + st.dim("   "+note)
}

// commitOracle applies an edited or added Check command or glob. A blank
// value changes nothing: removing is [d].
func (p *setupPanel) commitOracle(r setupRow, v string) {
	if v == "" {
		return
	}
	s := p.sel
	switch r.kind {
	case rowCheck:
		old := s.checks[r.idx]
		if old.Run == v {
			return
		}
		old.Run = v
		if rep := pipeline.InferReport(v); rep != "" {
			old.Report = rep
		}
		s.checks[r.idx] = old
	case rowAddCheck:
		s.checks = append(s.checks, pipeline.CheckCommandConfig{Run: v, Report: pipeline.InferReport(v)})
	case rowGlob:
		if s.globs[r.idx] == v {
			return
		}
		s.globs[r.idx] = v
	case rowAddGlob:
		s.globs = append(s.globs, v)
	}
	s.touchOracle()
}

// remove drops the Check or glob under the cursor.
func (p *setupPanel) remove(r setupRow) {
	s := p.sel
	switch r.kind {
	case rowCheck:
		s.checks = append(s.checks[:r.idx:r.idx], s.checks[r.idx+1:]...)
	case rowGlob:
		s.globs = append(s.globs[:r.idx:r.idx], s.globs[r.idx+1:]...)
	default:
		return
	}
	s.touchOracle()
}

// validation is the #120 coverage check, inline under the section: it is
// recomputed as you type, with the box's text in place of the row's.
func (p *setupPanel) validation(rows []setupRow) []string {
	s := p.sel
	checks, globs := s.checks, s.globs
	if p.input != nil {
		v := strings.TrimSpace(p.input.Value())
		r := rows[p.cursor]
		switch r.kind {
		case rowCheck:
			checks = append([]pipeline.CheckCommandConfig(nil), checks...)
			if v != "" {
				checks[r.idx].Run = v
			}
		case rowAddCheck:
			checks = append(append([]pipeline.CheckCommandConfig(nil), checks...), pipeline.CheckCommandConfig{Run: v})
		case rowGlob:
			globs = append([]string(nil), globs...)
			if v != "" {
				globs[r.idx] = v
			}
		case rowAddGlob:
			globs = append(append([]string(nil), globs...), v)
		}
	}
	refusal, warns := s.coverage(checks, globs)
	st := p.st
	var out []string
	for _, c := range s.flagChecks {
		out = append(out, "   "+st.dim("(from flags) check: "+clean(c)))
	}
	for _, g := range s.flagTests {
		out = append(out, "   "+st.dim("(from flags) test glob: "+clean(g)))
	}
	if refusal != "" {
		for i, l := range wrapText("✗ refused: "+clean(refusal), max(p.width-4, 40)) {
			if i > 0 {
				l = "  " + l
			}
			out = append(out, "   "+st.bad(l))
		}
		out = append(out, "   "+st.dim("a Run can't start until this is fixed"))
	} else {
		out = append(out, "   "+st.ok("✓ these Checks can run the protected tests"))
	}
	for _, w := range warns {
		out = append(out, "   "+st.dim("! "+clean(w)))
	}
	return out
}

// wrapText breaks s at spaces into lines of at most width columns.
func wrapText(s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line != "" && len([]rune(line))+1+len([]rune(w)) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	return append(lines, line)
}
