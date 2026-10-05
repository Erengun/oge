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
