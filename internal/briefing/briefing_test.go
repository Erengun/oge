package briefing

import (
	"strings"
	"testing"

	"github.com/erengun/oge/internal/task"
)

func TestImplementerBriefing(t *testing.T) {
	b := Implementer(task.Task{Text: "fix Add\n"}, []string{"go test -json ./..."})
	for _, want := range []string{"fix Add\n\n---\n", Assumptions, "`go test -json ./...`", "`Exit: done`", "`Exit: infeasible`", "no cd, &&, pipes"} {
		if !strings.Contains(b, want) {
			t.Errorf("Briefing lacks %q:\n%s", want, b)
		}
	}
}

func TestVerifierBriefing(t *testing.T) {
	tk := task.Parse("# Fix Add\n\n## Acceptance criteria\n- Add returns the sum\n")
	b := Verifier(tk, []string{"go test -json ./..."}, []string{"**/*_test.go"})
	for _, want := range []string{"# Fix Add", "You are QA", "`**/*_test.go`", "`go test -json ./...`",
		"// AC-1:", Assumptions, "`Exit: extended`", "`Exit: no_additions`", "`Exit: conflicts_with_oracle`", "don't change"} {
		if !strings.Contains(b, want) {
			t.Errorf("Briefing lacks %q:\n%s", want, b)
		}
	}
	// Nothing about how the change was made, or who made it.
	for _, never := range []string{"implementer", "Claim", "Verdict"} {
		if strings.Contains(b, never) {
			t.Errorf("the verifier's Briefing mentions %q:\n%s", never, b)
		}
	}
}

func TestHeldOutFeedback(t *testing.T) {
	for _, c := range []struct {
		criteria [][]string
		want     string
	}{
		{[][]string{{"AC-2"}, {"AC-4"}, {"AC-2"}}, "3 held-out tests failed: AC-2 ×2, AC-4 ×1."},
		{[][]string{{"AC-1", "AC-3"}, nil}, "2 held-out tests failed: AC-1 ×1, AC-3 ×1, unmapped ×1."},
		{[][]string{nil}, "1 held-out test failed (it names no acceptance criterion)."},
	} {
		got := HeldOutFeedback(c.criteria)
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("got %q, want prefix %q", got, c.want)
		}
	}
}

// A follow-up Run is briefed with the Task, then each follow-up message in
// order, labelled as the human's (#185); QA gets the same thread.
func TestBriefingWithFollowUps(t *testing.T) {
	tk := task.Task{Text: "fix Add\n", FollowUps: []string{"add Sub too", "and Mul"}}
	for _, b := range []string{Implementer(tk, nil), Verifier(tk, nil, []string{"**/*_test.go"})} {
		i, j, k := strings.Index(b, "fix Add"), strings.Index(b, "Human follow-up 1 of 2"), strings.Index(b, "Human follow-up 2 of 2")
		if i != 0 || j < i || k < j || !strings.Contains(b[j:k], "add Sub too") || !strings.Contains(b[k:], "and Mul") {
			t.Errorf("Briefing doesn't hold the thread in order:\n%s", b)
		}
	}
	if b := Implementer(task.Task{Text: "fix Add"}, nil); strings.Contains(b, "follow-up") {
		t.Errorf("a first Run's Briefing mentions follow-ups:\n%s", b)
	}
}
