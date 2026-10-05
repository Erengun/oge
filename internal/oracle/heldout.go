package oracle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/ledger"
)

// HeldOut is one Held-out test: a verifier addition the implementer never
// sees (ADR-0009, ADR-0010). The manifest names it; its source is only in
// a blob in private state.
type HeldOut struct {
	Test    TestID `json:"test"`
	File    string `json:"file"`
	Attempt string `json:"attempt"` // the verifier Attempt that added it
	// Criteria are the Acceptance criterion ids its doc comment names;
	// empty means unmapped (spec #35 story 42): still valid, reported so.
	Criteria []string `json:"criteria,omitempty"`
}

// Addition is a new file a verifier Attempt wrote, before Öge admits it.
type Addition struct {
	Path string
	Data []byte
}

// Dropped is an Addition Öge didn't admit, and why.
type Dropped struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// criterionRef is how a held-out test names a criterion in its doc
// comment, as the verifier's Briefing asks: "// AC-2: …".
// TODO(#46-decision): the tag lives in the Go doc comment; other languages
// need their own convention once their parsers exist.
var criterionRef = regexp.MustCompile(`\bAC-[0-9]+\b`)

// NewVersion is the Oracle version after parent with a verifier Attempt's
// additions: an immutable manifest whose parent is parentBlob (ADR-0007:
// append-only). src and commit are the tree the verifier saw, for module
// paths; criteria are the Task's Acceptance criterion ids. An addition is
// left out, and returned in dropped, when no Check command would run it or
// it redeclares a test the Oracle already has: it could never pass.
// An addition at a path the Oracle already holds is kept under a new name
// (a fresh verifier never sees earlier held-out files).
func NewVersion(parent *Manifest, parentBlob, attempt string, adds []Addition, src Source, commit string,
	criteria []string, blobs *ledger.Blobs) (m *Manifest, id string, dropped []Dropped, err error) {
	m = &Manifest{Format: ManifestFormat, Version: parent.Version + 1, Parent: parentBlob,
		TestGlobs: parent.TestGlobs, Commands: parent.Commands, TestConfigGlobs: parent.TestConfigGlobs,
		Config: parent.Config, Tests: append([]File(nil), parent.Tests...),
		Expected: append([]TestID(nil), parent.Expected...), HeldOut: append([]HeldOut(nil), parent.HeldOut...)}
	files, err := src.Files(commit)
	if err != nil {
		return nil, "", nil, err
	}
	mods := modules(files, func(p string) ([]byte, error) {
		b, _, err := src.Show(commit, p)
		return b, err
	})
	taken := map[string]bool{}
	for _, f := range m.Tests {
		taken[f.Path] = true
	}
	for _, f := range m.Config {
		taken[f.Path] = true
	}
	known := map[TestID]bool{}
	for _, e := range m.Expected {
		known[e] = true
	}
	sorted := append([]Addition(nil), adds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	for _, a := range sorted {
		p := a.Path
		if !strings.HasSuffix(p, "_test.go") {
			// TODO(#46-decision): only Go test files are collected by a
			// Check command Öge can read (go-test-json).
			dropped = append(dropped, Dropped{p, "not a Go test file; no Check command Öge reads would run it"})
			continue
		}
		if why := screen(a.Data); why != "" {
			dropped = append(dropped, Dropped{p, why})
			continue
		}
		if !goPackageDir(path.Dir(p)) || nestedModule(path.Dir(p), mods) {
			dropped = append(dropped, Dropped{p, "outside the packages go test ./... builds; no Check command would run it"})
			continue
		}
		if taken[p] {
			p = renamed(p, m.Version, taken)
		}
		pkg := importPath(path.Dir(p), mods)
		tests := testDocs(a.Data)
		var ids []TestID
		clash := ""
		for _, t := range tests {
			id := TestID{Package: pkg, Name: t.name}
			if known[id] {
				clash = id.String()
				break
			}
			ids = append(ids, id)
		}
		if clash != "" {
			dropped = append(dropped, Dropped{a.Path, "redeclares " + clash + ", which the Oracle already has"})
			continue
		}
		blob, err := blobs.Put(a.Data)
		if err != nil {
			return nil, "", nil, err
		}
		taken[p] = true
		m.Tests = append(m.Tests, File{Path: p, Blob: blob, HeldOut: true, Attempt: attempt, Expected: ids})
		m.Added = append(m.Added, p)
		for i, t := range tests {
			known[ids[i]] = true
			m.Expected = append(m.Expected, ids[i])
			m.HeldOut = append(m.HeldOut, HeldOut{Test: ids[i], File: p, Attempt: attempt, Criteria: mapped(t.doc, criteria)})
		}
	}
	sort.Slice(m.Tests, func(i, j int) bool { return m.Tests[i].Path < m.Tests[j].Path })
	sort.Slice(m.Expected, func(i, j int) bool { return m.Expected[i].String() < m.Expected[j].String() })
	sort.Slice(m.HeldOut, func(i, j int) bool { return m.HeldOut[i].Test.String() < m.HeldOut[j].Test.String() })
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, "", nil, err
	}
	id, err = blobs.Put(raw)
	return m, id, dropped, err
}

// renamed is p with the version before _test.go, e.g. add_v2_test.go,
// unless that is taken too.
func renamed(p string, version int, taken map[string]bool) string {
	stem := strings.TrimSuffix(p, "_test.go")
	for n := 0; ; n++ {
		q := fmt.Sprintf("%s_v%d_test.go", stem, version)
		if n > 0 {
			q = fmt.Sprintf("%s_v%d_%d_test.go", stem, version, n)
		}
		if !taken[q] {
			return q
		}
	}
}

func mapped(doc string, criteria []string) []string {
	var out []string
	for _, ref := range criterionRef.FindAllString(doc, -1) {
		for _, c := range criteria {
			if c == ref && !oneOf(ref, out) {
				out = append(out, ref)
			}
		}
	}
	return out
}

func oneOf(s string, list []string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

type testDoc struct{ name, doc string }

// testDocs lists src's top-level tests with their doc comments.
func testDocs(src []byte) []testDoc {
	f, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil
	}
	names := map[string]bool{}
	for _, n := range testNames(src) {
		names[n] = true
	}
	var out []testDoc
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && names[fn.Name.Name] {
			out = append(out, testDoc{fn.Name.Name, fn.Doc.Text()})
		}
	}
	return out
}

// modules maps each directory holding a go.mod to its module path.
func modules(files []string, show func(string) ([]byte, error)) map[string]string {
	mods := map[string]string{}
	for _, f := range files {
		if path.Base(f) != "go.mod" {
			continue
		}
		if b, err := show(f); err == nil {
			if m := modulePath(b); m != "" {
				mods[path.Dir(f)] = m
			}
		}
	}
	return mods
}

// Unmapped counts the held-out tests that name no Acceptance criterion.
func (m *Manifest) Unmapped() int {
	n := 0
	for _, h := range m.HeldOut {
		if len(h.Criteria) == 0 {
			n++
		}
	}
	return n
}

// HeldOutFailures are the held-out tests a Check didn't see pass. With no
// report at all (setup failed, an Oracle path was blocked) it names none:
// the Check failed for a reason they don't show.
func (m *Manifest) HeldOutFailures(r *Result) []HeldOut {
	if len(r.Tests) > 0 {
		// The attestation channel decides, never a report (ADR-0020).
		attested := map[TestID]string{}
		for _, t := range r.Tests {
			attested[t.TestID] = t.Attested
		}
		var out []HeldOut
		for _, h := range m.HeldOut {
			if attested[h.Test] != AttestPass {
				out = append(out, h)
			}
		}
		return out
	}
	var reports []*Report
	for _, e := range r.Commands {
		if e.Report != nil && e.Report.Error == "" {
			reports = append(reports, e.Report)
		}
	}
	if len(reports) == 0 {
		return nil
	}
	var out []HeldOut
	for _, h := range m.HeldOut {
		passed := false
		for _, rep := range reports {
			if rep.outcome(h.Test) == "pass" {
				passed = true
			}
		}
		if !passed {
			out = append(out, h)
		}
	}
	return out
}

// OutputLine is one line of `go test -json` output text, keyed by the
// top-level test that printed it; a package-level line has no Name.
type OutputLine struct {
	Test TestID
	Text string
}

// SplitGoTestOutput is the text of `go test -json` output events, each
// keyed by its top-level test. Lines that aren't events are skipped.
func SplitGoTestOutput(raw []byte) []OutputLine {
	var out []OutputLine
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev struct{ Action, Package, Test, Output string }
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Action != "output" {
			continue
		}
		top, _, _ := strings.Cut(ev.Test, "/")
		out = append(out, OutputLine{Test: TestID{Package: ev.Package, Name: top}, Text: ev.Output})
	}
	return out
}

// screen is why a verifier's test file can't join the Oracle as it is, or
// "": it doesn't parse, declares no test, or declares TestMain or init,
// which run before (or instead of) every test in its package and could
// change how the Oracle's other tests run or report. Each is QA's own
// defect, never the implementer's.
func screen(src []byte) string {
	f, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		return "QA's test file doesn't parse; left out"
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && (fn.Name.Name == "TestMain" || fn.Name.Name == "init") {
			return "QA's test file declares " + fn.Name.Name + ", which runs around every test in its package; left out"
		}
	}
	if len(testNames(src)) == 0 {
		return "QA's file declares no TestXxx(*testing.T); left out"
	}
	return ""
}

// nestedModule reports whether dir is inside a module nested under the
// root's: go test ./... from the root never builds it.
func nestedModule(dir string, mods map[string]string) bool {
	if _, ok := mods["."]; !ok {
		return false
	}
	for d := dir; d != "." && d != "/"; d = path.Dir(d) {
		if _, nested := mods[d]; nested {
			return true
		}
	}
	return false
}

// Without is m less the added files paths, with their tests: a new
// manifest, stored as a new blob.
func (m *Manifest) Without(paths []string, blobs *ledger.Blobs) (*Manifest, string, error) {
	drop := map[string]bool{}
	for _, p := range paths {
		drop[p] = true
	}
	out := *m
	out.Tests, out.Added, out.HeldOut, out.Expected = nil, nil, nil, nil
	gone := map[TestID]bool{}
	for _, f := range m.Tests {
		if drop[f.Path] {
			for _, id := range f.Expected {
				gone[id] = true
			}
			continue
		}
		out.Tests = append(out.Tests, f)
	}
	for _, p := range m.Added {
		if !drop[p] {
			out.Added = append(out.Added, p)
		}
	}
	for _, h := range m.HeldOut {
		if !drop[h.File] {
			out.HeldOut = append(out.HeldOut, h)
		}
	}
	for _, e := range m.Expected {
		if !gone[e] {
			out.Expected = append(out.Expected, e)
		}
	}
	raw, err := json.Marshal(&out)
	if err != nil {
		return nil, "", err
	}
	id, err := blobs.Put(raw)
	return &out, id, err
}

// FuncNames are the top-level functions a Go file declares.
func FuncNames(src []byte) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
			out = append(out, fn.Name.Name)
		}
	}
	return out
}
