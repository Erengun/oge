// Package oracle owns Oracle versions as immutable, content-addressed
// manifests, the Check directory overlay, the Check runner, Öge's own
// report parsers and Verdicts (ADR-0011, ADR-0014).
package oracle

import (
	"encoding/json"
	"fmt"
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
}

// Source reads a commit's files.
type Source interface {
	Files(commit string) ([]string, error)
	Show(commit, path string) ([]byte, bool, error)
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
	for _, p := range files {
		isTest, isConfig := MatchAny(m.TestGlobs, p), MatchAny(m.TestConfigGlobs, p)
		if !isTest && !isConfig {
			continue
		}
		b, ok, err := src.Show(snapshot, p)
		if err != nil || !ok {
			return nil, "", fmt.Errorf("reading %s from the Snapshot: %v", p, err)
		}
		id, err := blobs.Put(b)
		if err != nil {
			return nil, "", err
		}
		if isTest {
			m.Tests = append(m.Tests, File{Path: p, Blob: id})
		} else {
			m.Config = append(m.Config, File{Path: p, Blob: id})
		}
	}
	sort.Slice(m.Tests, func(i, j int) bool { return m.Tests[i].Path < m.Tests[j].Path })
	var testPaths []string
	for _, t := range m.Tests {
		testPaths = append(testPaths, t.Path)
	}
	m.Expected = expectedTests(testPaths, files, func(p string) ([]byte, error) {
		b, _, err := src.Show(snapshot, p)
		return b, err
	})
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	id, err := blobs.Put(raw)
	return m, id, err
}
