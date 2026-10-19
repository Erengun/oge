package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/erengun/oge/internal/oracle"
)

// livePID names the file a Run holds its process id in while it runs.
// liveLock names the file a Run flocks for the same span: the pid file is
// advisory for sweepers, the lock is what keeps two processes from
// running one Run.
const livePID, liveLock = "live.pid", "live.lock"

// errLocked means another process holds the Run's lock.
var errLocked = errors.New("the Run's lock is held")

// markLive locks the Run and records that this process runs it. The lock
// file is never removed: a stale one's lock is free for the next owner.
func markLive(dir string) (release func(), err error) {
	lk, err := os.OpenFile(filepath.Join(dir, liveLock), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockRun(lk); err != nil {
		lk.Close()
		return nil, fmt.Errorf("%s: %w", filepath.Base(dir), err)
	}
	p := filepath.Join(dir, livePID)
	if err := os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		unlockRun(lk)
		lk.Close()
		return nil, err
	}
	return func() {
		_ = os.Remove(p)
		unlockRun(lk)
		lk.Close()
	}, nil
}

// sweepDeadRuns removes the cache seeds and Check directories that Runs
// stopped without cleanup (a forced stop, a crash) left behind. A Run is
// live while its live.pid names a running process; a Run without one is
// dead, since every Run writes it before making either directory. Ledgers
// and Run repositories are never touched. A warm step orphaned by SIGKILL
// may still be running into a swept seed; it fails harmlessly.
func sweepDeadRuns(runs, self string) {
	entries, err := os.ReadDir(runs)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(runs, e.Name())
		if !e.IsDir() || dir == self || alive(dir) || runLocked(dir) {
			continue
		}
		_ = oracle.RemoveAll(filepath.Join(dir, "cache-seed"))
		_ = oracle.RemoveAll(filepath.Join(dir, "checks"))
		_ = os.Remove(filepath.Join(dir, livePID))
	}
}

func alive(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, livePID))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return processAlive(pid)
}
