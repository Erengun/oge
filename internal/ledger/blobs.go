package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Blobs is a Run's content-addressed store: SHA-256 over uncompressed
// content (ADR-0014).
type Blobs struct{ dir string }

// BlobsDir is a Run's blob store, relative to its directory.
const BlobsDir = "blobs"

// OpenBlobs opens (creating) the blob store in runDir.
func OpenBlobs(runDir string) (*Blobs, error) {
	dir := filepath.Join(runDir, BlobsDir, "sha256")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Blobs{dir: dir}, nil
}

// Put stores b durably (write, fsync, rename, fsync the directory) and
// returns its id, "sha256:<hex>".
func (s *Blobs) Put(b []byte) (string, error) {
	h := hashHex(b)
	final := filepath.Join(s.dir, h)
	if _, err := os.Stat(final); err == nil {
		return "sha256:" + h, nil
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(b)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := firstErr(werr, serr, cerr); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := syncDir(s.dir); err != nil {
		return "", err
	}
	return "sha256:" + h, nil
}

var blobID = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)

// Get reads a blob by id and checks its content still matches.
func (s *Blobs) Get(id string) ([]byte, error) {
	m := blobID.FindStringSubmatch(id)
	if m == nil {
		return nil, fmt.Errorf("not a blob id: %q", id)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, m[1]))
	if err != nil {
		return nil, err
	}
	if hashHex(b) != m[1] {
		return nil, fmt.Errorf("blob %s doesn't match its content", id)
	}
	return b, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
