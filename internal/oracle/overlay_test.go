package oracle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
)

// The overlay never writes an Oracle test through a symlink the Candidate
// put on its path.
func TestOverlayNeverWritesThroughASymlink(t *testing.T) {
	base := t.TempDir()
	blobs, err := ledger.OpenBlobs(filepath.Join(base, "run"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := blobs.Put([]byte("package sub\n"))
	if err != nil {
		t.Fatal(err)
	}
	dir, outside := filepath.Join(base, "check"), filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Blobs: blobs}
	m := &Manifest{TestGlobs: []string{"**/*_test.go"}, Tests: []File{{Path: "sub/x_test.go", Blob: id}}}
	why, err := r.overlay(m, dir)
	if err != nil || !strings.Contains(why, "Oracle path sub/x_test.go is blocked by a symlink in the Candidate") {
		t.Fatalf("overlay = %q, %v", why, err)
	}
	if left, _ := os.ReadDir(outside); len(left) != 0 {
		t.Errorf("the overlay wrote outside the Check directory: %v", left)
	}
}
