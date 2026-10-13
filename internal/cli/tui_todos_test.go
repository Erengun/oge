package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
)

func todoEvent(items ...agent.Todo) agent.Event {
	return agent.Event{Kind: agent.Claim, Tool: "TodoWrite", Todos: items}
}

func sixTodos() []agent.Todo {
	return []agent.Todo{
		{Text: "Read add.go", Status: "completed"},
		{Text: "Write a failing test", Status: "completed"},
		{Text: "Fix Add", Status: "in_progress", Active: "Fixing Add"},
		{Text: "Run go test", Status: "pending", Active: "Running go test"},
		{Text: "Run go vet", Status: "pending"},
		{Text: "Update the docs", Status: "pending"},
		{Text: "Tidy up", Status: "pending"},
	}
}

func TestTUITodoList(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.agent(todoEvent(sixTodos()[:5]...))
	got := h.m.render()
	golden(t, "todos", got)
	if strings.Contains(got, "TodoWrite") {
		t.Errorf("the tool call shows as a step:\n%s", got)
	}
}

func TestTUITodoOverflowAndExpanded(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.agent(todoEvent(sixTodos()...))
	golden(t, "todos-overflow", h.m.render())
	h.m.Update(ctrlKey('o'))
	golden(t, "todos-expanded", h.m.render())
}

func TestTUITodoHostileTextIsEscaped(t *testing.T) {
	h := newTUIHarness(t, false, 200)
	h.working()
	h.agent(todoEvent(
		agent.Todo{Text: hostile, Status: "pending"},
		agent.Todo{Text: "x", Status: "in_progress", Active: hostile},
	))
	got := h.m.render()
	assertInert(t, "todos", got)
	if !strings.Contains(got, "now: x") {
		t.Errorf("the plan line is missing:\n%s", got)
	}
}

func TestTUITodosAreNotQAs(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.working()
	h.att.Role = "verifier"
	h.agent(todoEvent(sixTodos()...))
	if len(h.m.todos) != 0 {
		t.Errorf("a verifier's plan reached the view: %v", h.m.todos)
	}
}

func TestPlainTodoLine(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	var out bytes.Buffer
	r := &renderer{w: &out, verbose: true, frozen: h.f}
	for _, e := range []agent.Event{todoEvent(sixTodos()[:5]...), todoEvent(sixTodos()...), {Kind: agent.Claim, Tool: "TodoWrite"}} {
		r.observe(run.Event{Kind: run.EvAgent, Agent: e, Attempt: h.att})
	}
	golden(t, "plain-todos", out.String())
}

func TestPlainNoTodoLineWithoutV(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	var out bytes.Buffer
	r := &renderer{w: &out, frozen: h.f}
	r.observe(run.Event{Kind: run.EvAgent, Agent: todoEvent(sixTodos()...), Attempt: h.att})
	if out.Len() != 0 {
		t.Errorf("default plain output shows the plan:\n%s", out.String())
	}
}
