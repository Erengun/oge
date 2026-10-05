package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/oracle/seedtest"
)

// testSeed is a warm build cache every test Run's cache seed starts from,
// so each Run doesn't compile the standard library again. Each Run still
// gets its own seed, and each Check a private copy of it.
var testSeed string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oge-cli-seed-")
	if err == nil {
		if werr := seedtest.Warm(dir); werr != nil {
			fmt.Fprintln(os.Stderr, werr) // the Runs warm their seeds from cold
		} else {
			testSeed = seedtest.GoCache(dir)
		}
	}
	code := m.Run()
	if dir != "" {
		_ = oracle.RemoveAll(dir)
	}
	os.Exit(code)
}
