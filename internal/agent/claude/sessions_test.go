package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sessionsFixtures = "testdata/sessions"

func fixtureProfiles() []Profile {
	return []Profile{
		{Dir: filepath.Join(sessionsFixtures, "default")},
		{Name: "work", Dir: filepath.Join(sessionsFixtures, "work")},
		{Name: "bare", Dir: filepath.Join(sessionsFixtures, "empty")}, // no projects/
		{Name: "gone", Dir: filepath.Join(sessionsFixtures, "missing")},
	}
}

func TestDiscoverSessionsGolden(t *testing.T) {
	got := DiscoverSessions(fixtureProfiles())
	var b strings.Builder
	for _, s := range got {
		fmt.Fprintf(&b, "%s %s %s cwd=%q title=%q last=%s file=%s\n", s.Provider, s.Profile, s.ID, s.Cwd, s.Title,
			s.LastActive.UTC().Format(time.RFC3339), filepath.Base(s.Path))
	}
	want := `claude claude 22222222-bbbb cwd="/repo/a" title="" last=2026-10-31T09:01:00Z file=22222222-bbbb.jsonl
claude claude 11111111-aaaa cwd="/repo/a" title="fix the parser" last=2026-10-30T10:05:00Z file=11111111-aaaa.jsonl
claude claude@work 55555555-eeee cwd="/repo/b" title="Old summary" last=2026-10-29T08:00:00Z file=55555555-eeee.jsonl
`
	// 44444444-dddd has an unknown shape: listed by file name, last active
	// is its write time, so it sorts first and is checked apart.
	var unknown bool
	for _, s := range got {
		if s.ID == "44444444-dddd" {
			unknown = true
			if s.Cwd != "" || s.Title != "" || !s.LastActive.Equal(s.Written) || s.Written.IsZero() {
				t.Errorf("unknown shape: %+v", s)
			}
			continue
		}
		if s.ID == "33333333-cccc" {
			t.Errorf("sidechain listed: %+v", s)
		}
	}
	if !unknown {
		t.Error("unknown-shape session not listed by id")
	}
	var rest strings.Builder
	for _, l := range strings.SplitAfter(b.String(), "\n") {
		if !strings.Contains(l, "44444444-dddd") {
			rest.WriteString(l)
		}
	}
	if rest.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", rest.String(), want)
	}
}

// No content field is read: the sentinel sits in message text and tool
// results of the fixtures, and must not reach any field of any session.
func TestDiscoverSessionsNeverReadsContent(t *testing.T) {
	var files int
	for _, p := range fixtureProfiles() {
		fs, _ := filepath.Glob(filepath.Join(p.Dir, "projects", "*", "*.jsonl"))
		for _, f := range fs {
			if b, _ := os.ReadFile(f); strings.Contains(string(b), "SENTINEL-") {
				files++
			}
		}
	}
	if files < 2 {
		t.Fatalf("fixtures lost their sentinels (%d files)", files)
	}
	for _, s := range DiscoverSessions(fixtureProfiles()) {
		if strings.Contains(fmt.Sprintf("%+v", s), "SENTINEL") {
			t.Errorf("content reached a field: %+v", s)
		}
	}
}

func TestDiscoverSessionsNoProfiles(t *testing.T) {
	if got := DiscoverSessions(nil); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
