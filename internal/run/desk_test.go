package run

import (
	"testing"
	"time"
)

func TestSessionsOnDeskReplay(t *testing.T) {
	private := t.TempDir()
	d, err := OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	d.SessionAdded(SessionAdded{Provider: "claude", ID: "a", Repo: "/r", Title: "one"})
	d.SessionAdded(SessionAdded{Provider: "codex", ID: "b", Repo: "/r"})
	d.SessionArchived("claude", "a")
	d.SessionAdded(SessionAdded{Provider: "claude", ID: "a", Repo: "/r", Title: "one again"})
	d.Close(true)
	recs, err := ReadDesk(private)
	if err != nil {
		t.Fatal(err)
	}
	got := SessionsOnDesk(recs)
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" || got[1].Title != "one again" {
		t.Errorf("got %+v", got)
	}
}

func TestSessionSeenUpdatesInPlace(t *testing.T) {
	private := t.TempDir()
	d, err := OpenDesk(private, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := old.Add(72 * time.Hour)
	d.SessionAdded(SessionAdded{Provider: "claude", ID: "a", Repo: "/r", Title: "one", Last: old})
	d.SessionAdded(SessionAdded{Provider: "claude", ID: "b", Repo: "/r", Last: old})
	d.SessionSeen(SessionAdded{Provider: "claude", ID: "a", Title: "renamed", Last: now})
	d.Close(true)
	recs, _ := ReadDesk(private)
	got := SessionsOnDesk(recs)
	if len(got) != 2 || got[0].ID != "a" || got[0].Title != "renamed" || !got[0].Last.Equal(now) || !got[1].Last.Equal(old) {
		t.Errorf("got %+v", got)
	}
}
