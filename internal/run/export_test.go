package run

import (
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// SiblingX is the file the sibling Chat's apply writes.
const SiblingX = siblingX

// SiblingRetakeCut opens a Chat that applied its first cut, retook its
// Snapshot on a sibling Chat's apply of x.go, and cut a turn adding
// mul.go on it. source is the user's repository.
func SiblingRetakeCut(t *testing.T) (c *Chat, res *Result, source string) {
	t.Helper()
	c = appliedSub(t, multiturn.Turn{Reply: "mul", Writes: []multiturn.File{{Path: "mul.go", Content: refreshMul}}})
	if got, err := sibling(t, c, map[string]string{"x.go": siblingX}); err != nil || got != RefreshDone {
		t.Fatalf("sibling apply: %v %v", got, err)
	}
	drain(t, send(t, c, "now Mul"), nil)
	res = cut(t, c)
	if res.Candidate == "" {
		t.Fatalf("cut %s %v", res.Outcome, res.Why)
	}
	return c, res, c.source
}
