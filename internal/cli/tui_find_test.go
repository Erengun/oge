package cli

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func charKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func typeFind(m *model, s string) {
	for _, r := range s {
		m.Update(charKey(r))
	}
}

func TestMatchesAreCaseInsensitiveSubstrings(t *testing.T) {
	steps := []string{"reading Add.go", "running go test", "ADD fails", "done"}
	if got := matches(steps, "add"); !slices.Equal(got, []int{0, 2}) {
		t.Errorf("matches = %v", got)
	}
	if got := matches(steps, ""); got != nil {
		t.Errorf("empty query matched %v", got)
	}
	if got := matches(steps, "zzz"); len(got) != 0 {
		t.Errorf("matches = %v", got)
	}
	// Matched on the escaped text: the raw escape never reaches a step.
	if got := matches([]string{clean("x\x1b[2Jfix")}, "\x1b"); len(got) != 0 {
		t.Errorf("matched a raw escape: %v", got)
	}
}

// findHarness is a Run with 11 steps in two Attempts (the boundary is set by hand: the live path
// is a retried Attempt in the same stage), the
// view expanded in a short window.
func findHarness(t *testing.T) *tuiHarness {
	h := newTUIHarness(t, false, 80)
	h.m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	h.working()
	h.m.steps, h.m.marks = nil, []int{5}
	for i := 0; i < 5; i++ {
		h.m.steps = append(h.m.steps, "step alpha "+string(rune('a'+i)))
	}
	for i := 0; i < 6; i++ {
		h.m.steps = append(h.m.steps, "step Beta "+string(rune('a'+i)))
	}
	h.m.Update(ctrlKey('o'))
	return h
}

func TestSearchNextPrevWrapAndCounter(t *testing.T) {
	h := findHarness(t)
	m := h.m
	m.Update(charKey('/'))
	typeFind(m, "BETA")
	if !strings.Contains(m.render(), "/BETA") {
		t.Fatalf("no search line:\n%s", m.render())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.render(), "1/6") {
		t.Errorf("counter after enter:\n%s", m.render())
	}
	first := m.stepOf()
	m.Update(charKey('N')) // wraps back to the last
	if got := m.stepOf(); got != first+5 || !strings.Contains(m.render(), "6/6") {
		t.Errorf("N: step %d, want %d\n%s", got, first+5, m.render())
	}
	m.Update(charKey('n')) // wraps forward to the first
	if got := m.stepOf(); got != first {
		t.Errorf("n: step %d, want %d", got, first)
	}
	m.Update(charKey('n'))
	if got := m.stepOf(); got != first+1 {
		t.Errorf("n: step %d, want %d", got, first+1)
	}
}

func TestSearchEscClosesSearchThenView(t *testing.T) {
	h := findHarness(t)
	m := h.m
	m.Update(charKey('/'))
	typeFind(m, "alpha")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(escKey())
	if m.find != nil || !m.expanded {
		t.Fatalf("first esc: find %v expanded %v", m.find != nil, m.expanded)
	}
	m.Update(escKey())
	if m.expanded {
		t.Error("second esc did not collapse")
	}
	// Esc while typing closes the line too.
	m.Update(ctrlKey('o'))
	m.Update(charKey('/'))
	m.Update(escKey())
	if m.find != nil || !m.expanded {
		t.Errorf("esc in the line: find %v expanded %v", m.find != nil, m.expanded)
	}
}

func TestAttemptJumps(t *testing.T) {
	h := findHarness(t)
	m := h.m
	if !slices.Equal(m.marks, []int{5}) {
		t.Fatalf("marks = %v (steps %d)", m.marks, len(m.steps))
	}
	m.Update(charKey('{')) // from the bottom: the start of the second Attempt
	if !strings.Contains(m.render(), "step Beta a") {
		t.Errorf("{ did not reach the second Attempt:\n%s", m.render())
	}
	m.Update(charKey('{'))
	if !strings.Contains(m.render(), "step alpha a") {
		t.Errorf("{ did not reach the first Attempt:\n%s", m.render())
	}
	m.Update(charKey('}'))
	if !strings.Contains(m.render(), "step Beta a") {
		t.Errorf("} did not reach the second Attempt:\n%s", m.render())
	}
}

func TestSearchKeysAreOnlyForTheExpandedView(t *testing.T) {
	h := findHarness(t)
	h.m.Update(ctrlKey('o'))
	h.m.Update(charKey('/'))
	if h.m.find != nil {
		t.Error("/ opened a search in the collapsed view")
	}
}

func TestSearchViewGolden(t *testing.T) {
	h := findHarness(t)
	h.m.Update(charKey('/'))
	typeFind(h.m, "beta")
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.m.Update(charKey('n'))
	golden(t, "search-view", h.m.render())
}
