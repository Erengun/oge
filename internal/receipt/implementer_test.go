package receipt

import (
	"errors"
	"testing"
)

// showSource serves files per commit; a path in fail errors on read.
type showSource struct {
	files map[string]map[string]string
	fail  map[string]bool
}

func (s showSource) Blob(string) ([]byte, error)              { return nil, errors.New("no blobs") }
func (s showSource) Changed(string, string) ([]string, error) { return nil, nil }
func (s showSource) Show(commit, path string) ([]byte, bool, error) {
	if s.fail[path] {
		return nil, false, errors.New("unreadable")
	}
	d, ok := s.files[commit][path]
	return []byte(d), ok, nil
}

// The count is a lower bound: every new file matching the test globs
// counts at least one, whatever it holds or whether it can be read.
func TestNewTestsCountsEveryDeliveredTestFile(t *testing.T) {
	t.Parallel()
	src := showSource{
		files: map[string]map[string]string{
			"snap": {"old_test.go": "package x\n"},
			"cand": {
				"old_test.go":    "package x\n\nfunc TestNew(t *testing.T) {}\n",
				"two_test.go":    "package x\n\nfunc TestA(t *testing.T) {}\nfunc ExampleB() {}\nfunc helper() {}\n",
				"helper_test.go": "package x\n\nfunc init() {}\n",
				"broken_test.go": "package x\n\nfunc {",
				"testdata/g.txt": "golden",
			},
		},
		fail: map[string]bool{"gone_test.go": false, "locked_test.go": true},
	}
	changed := []string{"old_test.go", "two_test.go", "helper_test.go", "broken_test.go", "testdata/g.txt", "gone_test.go", "locked_test.go", "main.go"}
	globs := []string{"**/*_test.go", "**/testdata/**"}
	// two_test.go 2 + helper 1 + broken 1 + golden 1 + locked 1; old_test.go
	// existed (its additions come from Kept), gone_test.go was deleted.
	if got := newTests(src, "snap", "cand", changed, globs); got != 6 {
		t.Errorf("newTests = %d, want 6", got)
	}
	if got := newTests(src, "snap", "cand", changed, nil); got != 0 {
		t.Errorf("no test globs: newTests = %d, want 0", got)
	}
}
