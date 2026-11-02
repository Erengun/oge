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

func writeSession(t *testing.T, lines ...string) []Profile {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "projects", "-p")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "s1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return []Profile{{Dir: dir}}
}

func TestDiscoverSessionsMixedSidechainStaysListed(t *testing.T) {
	got := DiscoverSessions(writeSession(t,
		`{"type":"user","sessionId":"s1","cwd":"/r","isSidechain":false,"timestamp":"2026-10-30T10:00:00Z"}`,
		`{"type":"assistant","sessionId":"s1","cwd":"/r","isSidechain":true,"timestamp":"2026-10-30T10:01:00Z"}`,
	))
	if len(got) != 1 || got[0].ID != "s1" || got[0].Cwd != "/r" {
		t.Fatalf("got %+v", got)
	}
	if !got[0].LastActive.Equal(time.Date(2026, 10, 30, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("last %v", got[0].LastActive)
	}
}

func TestDiscoverSessionsLargeContentLines(t *testing.T) {
	big := `{"type":"user","sessionId":"s1","cwd":"/r","isSidechain":false,"timestamp":"2026-10-30T10:00:00Z","message":"` +
		strings.Repeat("x", 8<<20) + `"}`
	got := DiscoverSessions(writeSession(t,
		`{"type":"user","sessionId":"s1","cwd":"/r","isSidechain":false,"timestamp":"2026-10-30T09:00:00Z"}`,
		`{"type":"ai-title","aiTitle":"big one","sessionId":"s1"}`,
		big,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-10-30T10:05:00Z"}`,
		big,
	))
	if len(got) != 1 || got[0].Title != "big one" || got[0].Cwd != "/r" {
		t.Fatalf("got %+v", got)
	}
	if got[0].LastActive.IsZero() {
		t.Fatal("no last activity")
	}
}

func preambleLines() []string {
	var l []string
	l = append(l, `{"type":"ai-title","aiTitle":"t","sessionId":"s1"}`)
	for i := 0; i < 20; i++ {
		l = append(l, `{"type":"file-history-snapshot","sessionId":"s1"}`)
	}
	return l
}

func TestDiscoverSessionsLongMetadataPreamble(t *testing.T) {
	lines := append(preambleLines(),
		`{"type":"attachment","sessionId":"s1","cwd":"/r/app","isSidechain":false,"timestamp":"2026-10-30T10:00:00Z"}`)
	got := DiscoverSessions(writeSession(t, lines...))
	if len(got) != 1 || got[0].ID != "s1" || got[0].Cwd != "/r/app" {
		t.Fatalf("got %+v", got)
	}
}

func TestDiscoverSessionsSidechainAfterMetadataPreamble(t *testing.T) {
	lines := append(preambleLines(),
		`{"type":"user","sessionId":"s1","cwd":"/r/app","isSidechain":true,"timestamp":"2026-10-30T10:00:00Z"}`)
	if got := DiscoverSessions(writeSession(t, lines...)); len(got) != 0 {
		t.Fatalf("sidechain listed: %+v", got)
	}
}

func TestDiscoverSessionsTitleFromHeadAndNewestInTail(t *testing.T) {
	filler := `{"type":"user","sessionId":"s1","message":"` + strings.Repeat("x", 300<<10) + `"}`
	lines := []string{
		`{"type":"user","sessionId":"s1","cwd":"/r","isSidechain":false,"timestamp":"2026-10-30T09:00:00Z"}`,
		`{"type":"ai-title","aiTitle":"from the head","sessionId":"s1"}`,
		filler, filler, // the middle: more than the head and tail windows
		`{"type":"custom-title","customTitle":"old name","sessionId":"s1"}`,
		`{"type":"custom-title","customTitle":"newest name","sessionId":"s1"}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-10-30T10:05:00Z"}`,
	}
	got := DiscoverSessions(writeSession(t, lines...))
	if len(got) != 1 || got[0].Title != "newest name" {
		t.Fatalf("got %+v", got)
	}
}

// A title record in the middle of a file longer than the head and tail
// windows together is not seen: the documented cost of a bounded scan.
func TestDiscoverSessionsTitleInTheMiddleOfAHugeFileIsMissed(t *testing.T) {
	filler := `{"type":"user","sessionId":"s1","message":"` + strings.Repeat("x", 200<<10) + `"}`
	lines := []string{`{"type":"user","sessionId":"s1","cwd":"/r","isSidechain":false,"timestamp":"2026-10-30T09:00:00Z"}`}
	for i := 0; i < 4; i++ {
		lines = append(lines, filler)
	}
	lines = append(lines, `{"type":"ai-title","aiTitle":"mid","sessionId":"s1"}`)
	for i := 0; i < 4; i++ {
		lines = append(lines, filler)
	}
	got := DiscoverSessions(writeSession(t, lines...))
	if len(got) != 1 || got[0].Title != "" || got[0].Cwd != "/r" {
		t.Fatalf("got %+v", got)
	}
}

func TestDiscoverSessionsSecondDiscoveryReusesTheCache(t *testing.T) {
	ps := writeSession(t,
		`{"type":"user","sessionId":"s1","cwd":"/r","isSidechain":false,"timestamp":"2026-10-30T09:00:00Z"}`,
		`{"type":"ai-title","aiTitle":"cached","sessionId":"s1"}`)
	before := sessionReads.Load()
	first := DiscoverSessions(ps)
	if n := sessionReads.Load() - before; n != 1 {
		t.Fatalf("first discovery read %d files, want 1", n)
	}
	second := DiscoverSessions(ps)
	if n := sessionReads.Load() - before; n != 1 {
		t.Fatalf("second discovery read the file again (%d reads)", n)
	}
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	// A file that grew is read again.
	f := first[0].Path
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(`{"type":"custom-title","customTitle":"renamed","sessionId":"s1"}` + "\n")
	fh.Close()
	third := DiscoverSessions(ps)
	if n := sessionReads.Load() - before; n != 2 || len(third) != 1 || third[0].Title != "renamed" {
		t.Fatalf("reads %d, third %+v", n, third)
	}
}
