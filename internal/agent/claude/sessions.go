package claude

import (
	"bufio"
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
	IsSidechain bool   `json:"isSidechain"`
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
	var last time.Time
	titleRank := 0
	rd := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		var r sessionRecord
		if len(line) > 0 && json.Unmarshal(line, &r) == nil {
			if r.IsSidechain {
				return agent.NativeSession{}, false
			}
			if s.ID == "" {
				s.ID = r.SessionID
			}
			if s.Cwd == "" {
				s.Cwd = r.Cwd
			}
			if t, e := time.Parse(time.RFC3339Nano, r.Timestamp); e == nil && t.After(last) {
				last = t
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
			if title = strings.TrimSpace(title); title != "" && rank >= titleRank {
				s.Title, titleRank = oneLine(title), rank
			}
		}
		if err != nil {
			break // io.EOF, or a read error: what was read so far stands
		}
	}
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

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
