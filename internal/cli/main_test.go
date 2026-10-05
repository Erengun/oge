package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/oracle/seedtest"
)

// testSeed is a warm build cache every test Run's cache seed starts from,
// so each Run doesn't compile the standard library again. Each Run still
// gets its own seed, and each Check a private copy of it.
var testSeed string

// fixtureRoot holds every runFixture's directory. It sits outside the
// tests' TMPDIR, so a fixture's state root isn't refused as being under
// the system temp directory.
var fixtureRoot string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oge-cli-")
	if err != nil {
		panic(err)
	}
	isolateProcess(dir)
	if werr := seedtest.Warm(filepath.Join(dir, "seed")); werr != nil {
		fmt.Fprintln(os.Stderr, werr) // the Runs warm their seeds from cold
	} else {
		testSeed = seedtest.GoCache(filepath.Join(dir, "seed"))
	}
	code := m.Run()
	_ = oracle.RemoveAll(dir)
	os.Exit(code)
}

// isolateProcess sets the environment every test shares, once, before
// any test runs: a synthetic HOME and TMPDIR under dir, no state-root
// override, and no user or system git config. No test changes the process
// environment after this; a test's own HOME, state root and TERM reach
// the CLI through Env.Getenv (see runFixture).
//
// It covers what reads the process environment rather than Env: git
// subprocesses, the test helpers' git and the fake agent's defaults.
func isolateProcess(dir string) {
	home, tmp := filepath.Join(dir, "home"), filepath.Join(dir, "tmp")
	fixtureRoot = filepath.Join(dir, "fixtures")
	for _, d := range []string{home, tmp, fixtureRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			panic(err)
		}
	}
	for k, v := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "TMPDIR": tmp,
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}
	for _, k := range []string{"XDG_STATE_HOME", ledger.StateDirEnv} {
		if err := os.Unsetenv(k); err != nil {
			panic(err)
		}
	}
}
