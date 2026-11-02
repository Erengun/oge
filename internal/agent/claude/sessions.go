package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erengun/oge/internal/agent"
)

// sessionRecord is the fixed allowlist of top-level keys discovery reads
// from a session file (ADR-0025). Every other key, the message and tool
// results included, is skipped by the decoder and never held. Do not add a
// field without changing the ADR.
type sessionRecord struct {
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	Timestamp   string `json:"timestamp"`
	GitBranch   string `json:"gitBranch"`
	IsSidechain *bool  `json:"isSidechain"`
	Type        string `json:"type"`
	// The title of a title record, by its type.
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
	Summary     string `json:"summary"`
}

// DiscoverSessions lists the Claude sessions under every profile's
// projects/*/*.jsonl, newest first. It reads only the allowlisted keys of
// sessionRecord, nothing outside projects/, and persists nothing. A
// profile without projects/ is fine; a file it cannot make sense of lists
// by its name, or is left out when unreadable. Project is left empty: the
// caller associates it with the Desk's projects.
func DiscoverSessions(profiles []Profile) []agent.NativeSession {
	var out []agent.NativeSession
	for _, p := range profiles {
		files, _ := filepath.Glob(filepath.Join(p.Dir, "projects", "*", "*.jsonl"))
		for _, f := range files {
			if s, ok := cachedSession(f, p.Label()); ok {
				out = append(out, s)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	return out
}

// sessionKey is what a cached read is valid for: the file as it was.
type sessionKey struct {
	path, profile string
	size          int64
	mtime         time.Time
}

type sessionVal struct {
	s  agent.NativeSession
	ok bool
}

var (
	sessionMu    sync.Mutex
	sessionCache = map[sessionKey]sessionVal{}
	// sessionReads counts files actually read, for tests: a second
	// discovery of unchanged files must not move it.
	sessionReads atomic.Int64
)

// cachedSession is readSession, remembered for the life of the process
// while the file's size and modification time are the same. Only the
// allowlisted NativeSession is held, never file content (ADR-0025).
func cachedSession(path, profile string) (agent.NativeSession, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return agent.NativeSession{}, false
	}
	k := sessionKey{path, profile, st.Size(), st.ModTime()}
	sessionMu.Lock()
	v, hit := sessionCache[k]
	sessionMu.Unlock()
	if hit {
		return v.s, v.ok
	}
	s, ok := readSession(path, profile)
	sessionMu.Lock()
	for old := range sessionCache { // a changed file replaces its older entry
		if old.path == path && old.profile == profile {
			delete(sessionCache, old)
		}
	}
	sessionCache[k] = sessionVal{s, ok}
	sessionMu.Unlock()
	return s, ok
}

func readSession(path, profile string) (agent.NativeSession, bool) {
	f, err := os.Open(path)
	if err != nil {
		return agent.NativeSession{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return agent.NativeSession{}, false
	}
	sessionReads.Add(1)
	s := agent.NativeSession{Provider: Name, Profile: profile, Path: path, Written: st.ModTime()}
	titleRank := 0
	var scanned int // bytes read so far, bounds the identity search
	decided := false
	rd := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			// Content lines are megabytes and never parsed: identity comes
			// from the first records, titles from short title lines only.
			if (s.ID == "" || s.Cwd == "") && scanned < maxIdentityBytes && len(line) <= maxParsedLine && bytes.Contains(line, sessionIDMark) {
				var r sessionRecord
				if json.Unmarshal(line, &r) == nil && r.SessionID != "" {
					// Metadata lines (titles, modes, snapshots) carry no cwd
					// or isSidechain. The first record that does decides:
					// a sidechain file is one from there, a main file with a
					// later sidechain record stays listed.
					if !decided && (r.Cwd != "" || r.IsSidechain != nil) {
						decided = true
						if r.IsSidechain != nil && *r.IsSidechain {
							return agent.NativeSession{}, false
						}
					}
					if s.ID == "" {
						s.ID = r.SessionID
					}
					if s.Cwd == "" {
						s.Cwd = r.Cwd
					}
				}
			}
			scanned += len(line)
			noteTitle(&s, &titleRank, line)
		}
		// The head is read for identity, and for titles only as far as
		// headTitleBytes: past it, a title is looked for in the tail.
		if err != nil || (scanned >= headTitleBytes && s.ID != "" && s.Cwd != "") || scanned >= maxIdentityBytes {
			break // io.EOF, a read error (what was read so far stands), or the head is done
		}
	}
	tail := tailLines(f, st.Size())
	for _, line := range tail { // the newest title in the tail wins at equal rank
		noteTitle(&s, &titleRank, line)
	}
	last := tailActivity(tail)
	if s.ID == "" {
		// An unrecognised shape still lists, by the id Claude names the file.
		s.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if last.IsZero() {
		last = s.Written
	}
	s.LastActive = last
	return s, true
}

// noteTitle takes the title of line when it is a title record that ranks at
// least as high as the best so far. A later line wins a tie.
func noteTitle(s *agent.NativeSession, titleRank *int, line []byte) {
	if len(line) > maxParsedLine || !isTitleLine(line) {
		return
	}
	var r sessionRecord
	if json.Unmarshal(line, &r) != nil {
		return
	}
	rank, title := 0, ""
	switch r.Type {
	case "custom-title":
		rank, title = 3, r.CustomTitle
	case "ai-title":
		rank, title = 2, r.AITitle
	case "summary":
		rank, title = 1, r.Summary
	}
	if title = strings.TrimSpace(title); title != "" && rank >= *titleRank {
		s.Title, *titleRank = oneLine(title), rank
	}
}

const (
	// headTitleBytes is how far into a file titles are looked for from the
	// start; tailBytes is how far back from the end. A title record in the
	// middle of a file longer than both is not seen.
	headTitleBytes   = 256 << 10
	maxIdentityBytes = 1 << 20
	maxParsedLine    = 256 << 10
	tailBytes        = 256 << 10
)

var sessionIDMark = []byte(`"sessionId"`)

var titleMarks = [][]byte{[]byte(`"type":"custom-title"`), []byte(`"type":"ai-title"`), []byte(`"type":"summary"`)}

func isTitleLine(line []byte) bool {
	for _, m := range titleMarks {
		if bytes.Contains(line, m) {
			return true
		}
	}
	return false
}

// tailLines are the complete lines in the file's last tailBytes. A file is
// appended to, so the tail holds the last activity and the newest title
// without reading the rest.
func tailLines(f *os.File, size int64) [][]byte {
	off := size - tailBytes
	if off < 0 {
		off = 0
	}
	buf := make([]byte, size-off)
	n, _ := f.ReadAt(buf, off)
	buf = buf[:n]
	if off > 0 { // drop the partial first line
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			return nil
		}
	}
	return bytes.Split(buf, []byte{'\n'})
}

// tailActivity is the newest timestamp among the lines, or zero when none
// parses. Within the tail timestamps may step back slightly, hence the
// newest, not the last.
func tailActivity(lines [][]byte) time.Time {
	var last time.Time
	for i := len(lines) - 1; i >= 0; i-- {
		if len(lines[i]) == 0 {
			continue
		}
		var r sessionRecord
		if json.Unmarshal(lines[i], &r) != nil {
			continue
		}
		if t, e := time.Parse(time.RFC3339Nano, r.Timestamp); e == nil && t.After(last) {
			last = t
		}
	}
	return last
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
