package run

import (
	"os"
	"path/filepath"
	"strings"
)

// FindResumable resolves an explicit Run id or a unique prefix among
// resumable Runs. Exact ids are returned even for finished or damaged
// Runs so Resume can explain the specific refusal. Selection is advisory:
// Resume replays again under the Run's lock before doing any work.
func FindResumable(private, id string) (string, error) {
	runs := filepath.Join(private, "runs")
	entries, err := os.ReadDir(runs)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), id) {
			continue
		}
		dir := filepath.Join(runs, e.Name())
		if e.Name() == id {
			return dir, nil
		}
		if alive(dir) || runLocked(dir) {
			continue
		}
		rt, err := replay(dir)
		if err == nil && !rt.isCut && rt.tail.kind != "" {
			candidates = append(candidates, e.Name())
		}
	}
	switch len(candidates) {
	case 0:
		return "", refuse("no resumable Run %s; give the full id for a specific Run", id)
	case 1:
		return filepath.Join(runs, candidates[0]), nil
	default:
		return "", refuse("%s names multiple resumable Runs: %s; give more of the id", id, strings.Join(candidates, ", "))
	}
}
