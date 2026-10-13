package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// historyCap is how many entries a repo's prompt history keeps.
const historyCap = 500

// history is the composer's prompt history for one repo (#144): what the
// human typed as a Task or follow-up message, oldest first. It lives in
// Öge's private state dir, one file per repo, never in the repo. A nil
// history is empty and keeps nothing. Writing it is best effort: a history
// that can't be saved must never stop a Run.
type history struct {
	path    string
	entries []string
}

// historyPath is the file for the repo at root under dir. The key is the
// canonical root (absolute, clean, symlinks resolved), like the user
// config's, so one repo has one history however it is reached.
func historyPath(dir, root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	root = filepath.Clean(root)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".jsonl")
}

// loadHistory reads the history at path. A missing file is an empty
// history, and lines that don't parse are skipped, so a damaged file loses
// only what is damaged.
func loadHistory(path string) *history {
	h := &history{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	for _, line := range strings.Split(string(b), "\n") {
		var s string
		if json.Unmarshal([]byte(line), &s) == nil && strings.TrimSpace(s) != "" {
			h.entries = append(h.entries, s)
		}
	}
	if len(h.entries) > historyCap {
		h.entries = h.entries[len(h.entries)-historyCap:]
	}
	return h
}

// add records text as the newest entry (a repeat of the newest is kept
// once) and rewrites the file, mode 0600, with the last historyCap entries.
func (h *history) add(text string) error {
	if h == nil {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" || (len(h.entries) > 0 && h.entries[len(h.entries)-1] == text) {
		return nil
	}
	h.entries = append(h.entries, text)
	if len(h.entries) > historyCap {
		h.entries = h.entries[len(h.entries)-historyCap:]
	}
	var b strings.Builder
	for _, e := range h.entries {
		line, _ := json.Marshal(e)
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, h.path)
}

func (h *history) len() int {
	if h == nil {
		return 0
	}
	return len(h.entries)
}

// find is the newest entry at or before index from that contains query
// (case-insensitively), or -1.
func (h *history) find(query string, from int) int {
	if h == nil {
		return -1
	}
	q := strings.ToLower(query)
	for i := min(from, len(h.entries)-1); i >= 0; i-- {
		if strings.Contains(strings.ToLower(h.entries[i]), q) {
			return i
		}
	}
	return -1
}

// histSearch is Ctrl+R's reverse-incremental search.
type histSearch struct {
	query string
	hit   int // index of the match in the history; -1 when none
}
