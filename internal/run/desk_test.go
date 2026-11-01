package run

import "testing"

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
