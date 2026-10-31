// Package multiturn is the scripted multi-turn test double behind Chat
// (docs/ux/chat-first.md, "Session mechanics"). One Scenario scripts what a
// long-lived agent does on each user turn. Two processes play it: the fake
// agent (./fakeagent, a line protocol) and fakeclaude's scenario mode
// (Claude's stream-json). The Driver types script a session from a test
// and read its events back. Nothing here is shipped, and nothing is a
// shell script, so it runs the same on Windows.
package multiturn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Scenario maps user turns, in order, to what the agent does. A turn past
// the end of Turns gets a one-line reply and no change.
type Scenario struct {
	Turns []Turn `json:"turns"`
}

// Turn is one scripted agent turn. Its steps run in this order: Reply,
// Tools, Ask, Writes, Hold, then Usage and the end of the turn.
type Turn struct {
	// Reply is the agent's text for the turn; empty says nothing.
	Reply string `json:"reply,omitempty"`
	// Tools are tool Claims, e.g. a Bash ls. They change nothing.
	Tools []Tool `json:"tools,omitempty"`
	// Ask stands in for an auto-mode permission prompt: the turn waits
	// for an answer, and Writes happen only if it is allow.
	Ask *Ask `json:"ask,omitempty"`
	// Writes create or overwrite files in the Workspace (the process's
	// working directory); paths are relative and slash-separated.
	Writes []File `json:"writes,omitempty"`
	// Hold keeps the turn open after its output until an interrupt
	// arrives. The interrupt ends the turn only: the process keeps serving.
	Hold bool `json:"hold,omitempty"`
	// Usage is reported when the turn completes; an interrupted turn
	// reports none.
	Usage *Usage `json:"usage,omitempty"`
}

// Tool is a tool Claim: a tool name and a short target.
type Tool struct {
	Name   string `json:"name"`
	Target string `json:"target,omitempty"`
}

// Ask is a permission prompt for a tool use.
type Ask struct {
	Tool   string `json:"tool"`
	Target string `json:"target,omitempty"`
}

// File is a file the agent writes.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Usage is what the agent reports for the turn.
type Usage struct {
	CostUSD float64 `json:"cost_usd"`
	Tokens  int     `json:"tokens,omitempty"`
	// OutTokens is the output share of Tokens.
	OutTokens int `json:"out_tokens,omitempty"`
}

// Turn returns scripted turn i, or the default for a turn past the end.
func (s Scenario) Turn(i int) Turn {
	if i < len(s.Turns) {
		return s.Turns[i]
	}
	return Turn{Reply: "no scripted turn"}
}

// Load reads a Scenario from a JSON file.
func Load(path string) (Scenario, error) {
	var s Scenario
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("scenario %s: %w", path, err)
	}
	return s, nil
}

// Save writes the Scenario as JSON.
func (s Scenario) Save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// WriteFiles creates the turn's files under dir. A path that would leave
// dir is an error: the fakes never write outside their Workspace.
func WriteFiles(dir string, files []File) error {
	for _, f := range files {
		rel := filepath.FromSlash(f.Path)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("scenario writes outside the workspace: %s", f.Path)
		}
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
