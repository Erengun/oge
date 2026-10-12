// Package oracle owns Oracle versions as immutable, content-addressed
// manifests, the Check directory overlay, the Check runner, Öge's own
// report parsers and Verdicts (ADR-0011, ADR-0014).
package oracle

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
)

// ManifestFormat versions the manifest encoding.
const ManifestFormat = 1

// Manifest is one immutable Oracle version. v0 is the Snapshot's tests and
// the frozen Check commands; later versions name their parent.
type Manifest struct {
	Format    int       `json:"format"`
	Version   int       `json:"version"`
	Parent    string    `json:"parent,omitempty"` // blob id of the parent manifest
	TestGlobs []string  `json:"test_globs"`
	Commands  []Command `json:"commands"`
	Tests     []File    `json:"tests"`
	// TestConfigGlobs and Config are the test configuration (spec #35:
	// v0 holds the listed test-config files), laid over a Check directory
	// like Tests.
	TestConfigGlobs []string `json:"test_config_globs,omitempty"`
	Config          []File   `json:"config,omitempty"`
	// Expected are the top-level Go tests in Tests; each must pass in a
	// go-test-json report for the Check to pass.
	Expected []TestID `json:"expected,omitempty"`
	// HeldOut are the Held-out tests among Tests, with their criterion
	// mappings; Added are the files this version added to its parent.
	HeldOut []HeldOut `json:"held_out,omitempty"`
	Added   []string  `json:"added,omitempty"`
}

// Command is one Check command as the Oracle pins it.
type Command struct {
	Run           string `json:"run"`
	Report        string `json:"report,omitempty"`
	ExpectedTests int    `json:"expected_tests"`
	TimeoutSec    int    `json:"timeout_sec"`
	OutputCap     int64  `json:"output_cap"`
}

// File is one Oracle test file.
type File struct {
	Path string `json:"path"`
	Blob string `json:"blob"`
	// HeldOut marks a verifier addition: private, never in any
	// implementer Briefing or Workspace.
	HeldOut bool   `json:"held_out,omitempty"`
	Attempt string `json:"attempt,omitempty"` // the verifier Attempt that added it
	// Expected are the expected tests the file declares; each attests its
	// execution in a Check (ADR-0020).
	Expected []TestID `json:"expected,omitempty"`
}

// Source reads a commit's files.
type Source interface {
	Files(commit string) ([]string, error)
	Show(commit, path string) ([]byte, bool, error)
}

// batchSource is a Source that reads many files at once (the Run
// repository): one git process for Oracle v0 instead of two per file.
type batchSource interface {
	ShowAll(commit string, paths []string) (map[string][]byte, error)
}

// showAll reads paths at commit, in one batch when src can. A path absent
// from the commit is absent from the map.
func showAll(src Source, commit string, paths []string) (map[string][]byte, error) {
	if b, ok := src.(batchSource); ok {
		return b.ShowAll(commit, paths)
	}
	files := make(map[string][]byte, len(paths))
	for _, p := range paths {
		b, ok, err := src.Show(commit, p)
		if err != nil {
			return nil, err
		}
		if ok {
			files[p] = b
		}
	}
	return files, nil
}

// NewV0 builds Oracle v0 from the Snapshot: every Snapshot file matching
// the test globs, stored as blobs, plus the frozen Check commands. It
// returns the manifest and its blob id.
func NewV0(src Source, snapshot string, f *pipeline.Frozen, blobs *ledger.Blobs) (*Manifest, string, error) {
	m := &Manifest{Format: ManifestFormat, Version: 0, TestGlobs: append([]string(nil), f.Project.TestGlobs...),
		TestConfigGlobs: append([]string(nil), f.Project.TestConfig...)}
	for _, c := range f.Checks {
		m.Commands = append(m.Commands, Command{Run: c.Run, Report: c.Report, ExpectedTests: c.ExpectedTests,
			TimeoutSec: int(c.Timeout.Seconds()), OutputCap: c.OutputCap})
	}
	files, err := src.Files(snapshot)
	if err != nil {
		return nil, "", err
	}
	// Every protected file, and every go.mod for the expected tests' import
	// paths, read in one pass.
	var want []string
	for _, p := range files {
		if MatchAny(m.TestGlobs, p) || MatchAny(m.TestConfigGlobs, p) || path.Base(p) == "go.mod" {
			want = append(want, p)
		}
	}
	content, err := showAll(src, snapshot, want)
	if err != nil {
		return nil, "", fmt.Errorf("reading the Snapshot: %v", err)
	}
	var protected []string
	var data [][]byte
	for _, p := range files {
		if !MatchAny(m.TestGlobs, p) && !MatchAny(m.TestConfigGlobs, p) {
			continue
		}
		b, ok := content[p]
		if !ok {
			return nil, "", fmt.Errorf("reading %s from the Snapshot: not found", p)
		}
		protected, data = append(protected, p), append(data, b)
	}
	ids, err := blobs.PutAll(data)
	if err != nil {
		return nil, "", err
	}
	for i, p := range protected {
		if MatchAny(m.TestGlobs, p) {
			m.Tests = append(m.Tests, File{Path: p, Blob: ids[i]})
		} else {
			m.Config = append(m.Config, File{Path: p, Blob: ids[i]})
		}
	}
	sort.Slice(m.Tests, func(i, j int) bool { return m.Tests[i].Path < m.Tests[j].Path })
	var testPaths []string
	for _, t := range m.Tests {
		testPaths = append(testPaths, t.Path)
	}
	var byFile map[string][]TestID
	m.Expected, byFile = expectedTests(testPaths, files, func(p string) ([]byte, error) {
		if b, ok := content[p]; ok {
			return b, nil
		}
		return nil, fmt.Errorf("%s isn't in the Snapshot", p)
	})
	for i := range m.Tests {
		m.Tests[i].Expected = byFile[m.Tests[i].Path]
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	id, err := blobs.Put(raw)
	return m, id, err
}
