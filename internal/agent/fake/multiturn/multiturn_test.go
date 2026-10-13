package multiturn

import (
	"os"
	"path/filepath"
	"testing"
)

func threeTurns() Scenario {
	return Scenario{Turns: []Turn{
		{Reply: "just words"},
		{Reply: "edited", Tools: []Tool{{Name: "Bash", Target: "ls"}},
			Writes: []File{{Path: "src/a.txt", Content: "one\n"}}},
		{Reply: "costly", Usage: &Usage{CostUSD: 0.25, Tokens: 120}},
	}}
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func TestAgentThreeTurns(t *testing.T) {
	dir := t.TempDir()
	a := StartAgent(t, threeTurns(), dir)

	a.Send("hello")
	got := a.ReadTurn()
	if len(got) != 2 || got[0].Kind != KindClaim || got[0].Text != "just words" || got[1].Text != EndDone {
		t.Fatalf("turn 1: %+v", got)
	}
	if exists(dir, "src/a.txt") {
		t.Fatal("a reply-only turn changed a file")
	}

	a.Send("edit")
	got = a.ReadTurn()
	if got[1].Kind != KindTool || got[1].Tool != "Bash" || got[1].Target != "ls" {
		t.Fatalf("turn 2 tool Claim: %+v", got)
	}
	if got[2].Kind != KindFile || got[2].Target != "src/a.txt" || !exists(dir, "src/a.txt") {
		t.Fatalf("turn 2 file: %+v", got)
	}

	a.Send("cost")
	got = a.ReadTurn()
	if u := got[1]; u.Kind != KindUsage || u.CostUSD != 0.25 || u.Tokens != 120 {
		t.Fatalf("turn 3 usage: %+v", got)
	}
	a.CloseStdin()
	if err := a.Wait(); err != nil {
		t.Fatalf("stdin closing should end the process cleanly: %v", err)
	}
}

func TestAgentAsk(t *testing.T) {
	sc := Scenario{Turns: []Turn{
		{Ask: &Ask{Tool: "Bash", Target: "make"}, Writes: []File{{Path: "ok.txt", Content: "x"}}},
		{Ask: &Ask{Tool: "Bash", Target: "rm"}, Writes: []File{{Path: "no.txt", Content: "x"}}},
	}}
	dir := t.TempDir()
	a := StartAgent(t, sc, dir)

	a.Send("go")
	ask := a.ReadAsk()
	if l := ask[len(ask)-1]; l.Tool != "Bash" || l.Target != "make" {
		t.Fatalf("ask: %+v", ask)
	}
	if exists(dir, "ok.txt") {
		t.Fatal("the turn wrote before its ask was answered")
	}
	a.Answer(true)
	a.ReadTurn()
	if !exists(dir, "ok.txt") {
		t.Fatal("an allowed ask did not let the write happen")
	}

	a.Send("again")
	a.ReadAsk()
	a.Answer(false)
	if end := a.ReadTurn(); end[len(end)-1].Text != EndDone || exists(dir, "no.txt") {
		t.Fatalf("a denied ask must end the turn without the write: %+v", end)
	}
}

func TestAgentInterruptThenAnotherTurn(t *testing.T) {
	sc := Scenario{Turns: []Turn{
		{Reply: "working", Hold: true, Usage: &Usage{CostUSD: 1}},
		{Reply: "back", Usage: &Usage{CostUSD: 0.5}},
	}}
	a := StartAgent(t, sc, t.TempDir())

	a.Send("long job")
	a.Read(func(l Line) bool { return l.Kind == KindClaim })
	a.Send("queued while running") // nothing enters a running turn
	a.Interrupt()
	got := a.ReadTurn()
	if end := got[len(got)-1]; end.Text != EndInterrupted {
		t.Fatalf("interrupt: %+v", got)
	}
	for _, l := range got {
		if l.Kind == KindUsage {
			t.Fatal("an interrupted turn reported Usage")
		}
	}

	// The queued message is the next turn, and the process still serves.
	got = a.ReadTurn()
	if got[0].Text != "back" || got[len(got)-1].Text != EndDone {
		t.Fatalf("turn after interrupt: %+v", got)
	}
	a.Send("one more")
	if got = a.ReadTurn(); got[0].Text != "no scripted turn" {
		t.Fatalf("past the script: %+v", got)
	}
	a.CloseStdin()
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeThreeTurns(t *testing.T) {
	dir := t.TempDir()
	c := StartClaude(t, threeTurns(), dir)
	if f := c.Initialize(); f.Subtype() != "success" {
		t.Fatalf("initialize: %v", f)
	}

	c.Send("hello")
	fs := c.ReadTurn()
	res := fs[len(fs)-1]
	if res.Subtype() != "success" || res["result"] != "just words" || res["is_error"] != false {
		t.Fatalf("turn 1 result: %v", res)
	}
	var said bool
	for _, f := range fs {
		for _, s := range f.Texts() {
			said = said || s == "just words"
		}
	}
	if !said || exists(dir, "src/a.txt") {
		t.Fatalf("turn 1: said=%v frames=%v", said, fs)
	}

	c.Send("edit")
	fs = c.ReadTurn()
	var tools []string
	for _, f := range fs {
		tools = append(tools, f.ToolUses()...)
	}
	if len(tools) != 2 || tools[0] != "Bash" || tools[1] != "Write" || !exists(dir, "src/a.txt") {
		t.Fatalf("turn 2 tools: %v", tools)
	}

	c.Send("cost")
	fs = c.ReadTurn()
	if res = fs[len(fs)-1]; res["total_cost_usd"] != 0.25 {
		t.Fatalf("turn 3 result: %v", res)
	}
	c.CloseStdin()
	if err := c.Wait(); err != nil {
		t.Fatalf("stdin closing should end fakeclaude cleanly: %v", err)
	}
}

func TestClaudeAsk(t *testing.T) {
	sc := Scenario{Turns: []Turn{
		{Ask: &Ask{Tool: "Bash", Target: "make"}, Writes: []File{{Path: "ok.txt", Content: "x"}}},
		{Ask: &Ask{Tool: "Bash", Target: "rm"}, Writes: []File{{Path: "no.txt", Content: "x"}}},
	}}
	dir := t.TempDir()
	c := StartClaude(t, sc, dir)

	c.Send("go")
	ask := c.ReadAsk()
	req := ask[len(ask)-1]["request"].(map[string]any)
	if req["tool_name"] != "Bash" || exists(dir, "ok.txt") {
		t.Fatalf("ask: %v", req)
	}
	c.Answer(true)
	c.ReadTurn()
	if !exists(dir, "ok.txt") {
		t.Fatal("an allowed ask did not let the write happen")
	}

	c.Send("again")
	c.ReadAsk()
	c.Answer(false)
	fs := c.ReadTurn()
	if fs[len(fs)-1].Subtype() != "success" || exists(dir, "no.txt") {
		t.Fatalf("a denied ask must end the turn without the write: %v", fs)
	}
}

func TestClaudeInterruptThenAnotherTurn(t *testing.T) {
	sc := Scenario{Turns: []Turn{
		{Reply: "working", Hold: true},
		{Reply: "back", Usage: &Usage{CostUSD: 0.5}},
	}}
	c := StartClaude(t, sc, t.TempDir())

	c.Send("long job")
	c.Read(func(f Frame) bool { return f.Type() == "assistant" })
	c.Interrupt()
	fs := c.ReadTurn()
	res := fs[len(fs)-1]
	if res.Subtype() != "error_during_execution" || res["terminal_reason"] != "aborted_streaming" {
		t.Fatalf("interrupted result: %v", res)
	}

	c.Send("next")
	fs = c.ReadTurn()
	if res = fs[len(fs)-1]; res.Subtype() != "success" || res["result"] != "back" || res["total_cost_usd"] != 0.5 {
		t.Fatalf("turn after interrupt: %v", res)
	}
	c.CloseStdin()
	if err := c.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFilesStaysInWorkspace(t *testing.T) {
	if err := WriteFiles(t.TempDir(), []File{{Path: "../escape.txt", Content: "x"}}); err == nil {
		t.Fatal("a path outside the workspace was written")
	}
}
