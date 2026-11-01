package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
			if s, ok := readSession(f, p.Label()); ok {
				out = append(out, s)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	return out
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
			if len(line) <= maxParsedLine && isTitleLine(line) {
				var r sessionRecord
				if json.Unmarshal(line, &r) == nil {
					rank, title := 0, ""
					switch r.Type {
					case "custom-title":
						rank, title = 3, r.CustomTitle
					case "ai-title":
						rank, title = 2, r.AITitle
					case "summary":
						rank, title = 1, r.Summary
					}
					if title = strings.TrimSpace(title); title != "" && rank >= titleRank {
						s.Title, titleRank = oneLine(title), rank
					}
				}
			}
		}
		if err != nil {
			break // io.EOF, or a read error: what was read so far stands
		}
	}
	last := tailActivity(f, st.Size())
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

const (
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

// tailActivity is the newest timestamp among the complete lines in the
// file's last tailBytes, or zero when none parses. A file is appended to,
// so the tail holds the last activity without reading the rest; within it
// timestamps may step back slightly, hence the newest, not the last.
func tailActivity(f *os.File, size int64) time.Time {
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
			return time.Time{}
		}
	}
	var last time.Time
	lines := bytes.Split(buf, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if len(lines[i]) == 0 {
			continue
		}
		var r sessionRecord
		if json.Unmarshal(lines[i], &r) != nil {
			continue
		}
		// Timestamps can step back by a fraction of a second near the end
		// of a file: take the newest in the tail, not the last line's.
		if t, e := time.Parse(time.RFC3339Nano, r.Timestamp); e == nil && t.After(last) {
			last = t
		}
	}
	return last
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
