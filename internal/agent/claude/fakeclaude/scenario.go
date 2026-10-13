package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// Scenario mode (OGE_FAKE_CLAUDE_SCENARIO) is the multi-turn fake for Chat
// (docs/ux/chat-first.md, slice 3). It serves a multiturn.Scenario in the
// shapes of `claude -p --input-format stream-json --output-format
// stream-json`: one user message per stdin line, a result frame ending
// each turn, and the process alive until stdin closes. The frames follow
// the recordings in testdata, which docs/research/claude-stream-json-spike.md
// describes: turns.ndjson (a turn's lifecycle, tool use and results, a
// can_use_tool request and its answer) and interrupt.ndjson (an interrupt
// control request and the error_during_execution result that follows).
// An Ask is a can_use_tool request, as an auto-mode ask would be.

const (
	scenSession = "00000000-0000-4000-8000-00000000fa4e"
	scenModel   = "claude-haiku-4-5-20251001"
)

type scen struct {
	in    *bufio.Reader
	out   io.Writer
	dir   string
	queue []map[string]any // user messages that arrived mid-turn
	seq   int
}

func runScenario(path string) {
	sc, err := multiturn.Load(path)
	if err != nil {
		fail("%v", err)
	}
	dir, _ := os.Getwd()
	s := &scen{in: bufio.NewReaderSize(os.Stdin, 1<<20), out: os.Stdout, dir: dir}
	s.serve(sc)
}

func (s *scen) emit(v map[string]any) {
	v["session_id"] = scenSession
	if _, ok := v["uuid"]; !ok {
		v["uuid"] = s.id("u")
	}
	b, _ := json.Marshal(v)
	fmt.Fprintln(s.out, string(b))
}

func (s *scen) id(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%04d", prefix, s.seq)
}

// read is the next JSON frame from stdin; false when stdin closed.
func (s *scen) read() (map[string]any, bool) {
	for {
		line, err := s.in.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var m map[string]any
			if json.Unmarshal(line, &m) != nil {
				fail("the host sent a line that isn't JSON")
			}
			return m, true
		}
		if err != nil {
			return nil, false
		}
	}
}

func (s *scen) next() (map[string]any, bool) {
	if len(s.queue) > 0 {
		m := s.queue[0]
		s.queue = s.queue[1:]
		return m, true
	}
	return s.read()
}

func subtype(m map[string]any) string {
	r, _ := m["request"].(map[string]any)
	st, _ := r["subtype"].(string)
	return st
}

func (s *scen) respond(req map[string]any, body map[string]any) {
	id, _ := req["request_id"].(string)
	s.emit(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": id, "response": body}, "uuid": s.id("r")})
}

func (s *scen) serve(sc multiturn.Scenario) {
	for n := 0; ; {
		m, ok := s.next()
		if !ok {
			return
		}
		switch m["type"] {
		case "control_request":
			switch subtype(m) {
			case "initialize":
				s.respond(m, map[string]any{"commands": []any{}, "agents": []any{}, "models": []any{}})
			case "interrupt": // idle: nothing to interrupt
				s.respond(m, map[string]any{"still_queued": []any{}})
			}
		case "user":
			s.turn(m, sc.Turn(n))
			n++
		}
	}
}

func (s *scen) lifecycle(uuid, state string) {
	s.emit(map[string]any{"type": "command_lifecycle", "command_uuid": uuid, "state": state})
}

func (s *scen) assistant(content ...map[string]any) {
	s.emit(map[string]any{"type": "assistant", "message": map[string]any{
		"model": scenModel, "id": s.id("msg"), "type": "message", "role": "assistant",
		"content": content, "stop_reason": nil}, "parent_tool_use_id": nil})
}

func (s *scen) toolResult(useID, content string, isErr bool, extra map[string]any) {
	r := map[string]any{"tool_use_id": useID, "type": "tool_result", "content": content}
	if isErr {
		r["is_error"] = true
	}
	m := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{r}},
		"parent_tool_use_id": nil}
	if extra != nil {
		m["tool_use_result"] = extra
	}
	s.emit(m)
}

func (s *scen) turn(msg map[string]any, t multiturn.Turn) {
	uuid, _ := msg["uuid"].(string)
	if uuid == "" {
		uuid = s.id("cmd")
	}
	s.lifecycle(uuid, "queued")
	s.lifecycle(uuid, "started")
	s.emit(map[string]any{"type": "system", "subtype": "init", "cwd": s.dir, "model": scenModel,
		"permissionMode": "auto", "tools": []string{"Bash", "Edit", "Read", "Write"}})
	if t.Reply != "" {
		s.assistant(map[string]any{"type": "text", "text": t.Reply})
	}
	for _, tl := range t.Tools {
		useID := s.id("toolu")
		s.assistant(map[string]any{"type": "tool_use", "id": useID, "name": tl.Name,
			"input": toolInput(tl.Name, tl.Target)})
		s.toolResult(useID, "ok", false, nil)
	}
	allowed := true
	if t.Ask != nil {
		useID := s.id("toolu")
		s.assistant(map[string]any{"type": "tool_use", "id": useID, "name": t.Ask.Tool,
			"input": toolInput(t.Ask.Tool, t.Ask.Target)})
		reqID := s.id("ask")
		s.emit(map[string]any{"type": "control_request", "request_id": reqID, "uuid": s.id("c"),
			"request": map[string]any{"subtype": "can_use_tool", "tool_name": t.Ask.Tool,
				"display_name": t.Ask.Tool, "input": toolInput(t.Ask.Tool, t.Ask.Target),
				"description": t.Ask.Target, "tool_use_id": useID}})
		allowed = s.awaitAnswer(reqID)
		if allowed {
			s.toolResult(useID, "ok", false, nil)
		} else {
			s.toolResult(useID, "The user doesn't want to proceed with this tool use.", true, nil)
		}
	}
	if allowed {
		for _, f := range t.Writes {
			useID := s.id("toolu")
			abs := filepath.Join(s.dir, filepath.FromSlash(f.Path))
			s.assistant(map[string]any{"type": "tool_use", "id": useID, "name": "Write",
				"input": map[string]any{"file_path": abs, "content": f.Content}})
			if err := multiturn.WriteFiles(s.dir, []multiturn.File{f}); err != nil {
				fail("%v", err)
			}
			// A Write of a new file has structuredPatch [] (spike finding 3).
			s.toolResult(useID, "File created successfully at: "+abs, false,
				map[string]any{"type": "create", "filePath": abs, "content": f.Content, "structuredPatch": []any{}})
		}
	}
	if t.Hold && s.hold() {
		s.result(uuid, t, true)
		s.lifecycle(uuid, "cancelled")
		return
	} else if t.Hold { // stdin closed mid-turn
		os.Exit(0)
	}
	s.result(uuid, t, false)
	s.lifecycle(uuid, "completed")
}

// awaitAnswer waits for the control_response to request id; a deny, or
// stdin closing, is false. Other user messages queue.
func (s *scen) awaitAnswer(id string) bool {
	for {
		m, ok := s.read()
		if !ok {
			return false
		}
		switch m["type"] {
		case "control_response":
			r, _ := m["response"].(map[string]any)
			if r["request_id"] != id {
				continue
			}
			body, _ := r["response"].(map[string]any)
			return body["behavior"] != "deny"
		case "user":
			s.queue = append(s.queue, m)
		}
	}
}

// hold waits for an interrupt control request and answers it with the
// receipt interrupt.ndjson shows; false means stdin closed first.
func (s *scen) hold() bool {
	for {
		m, ok := s.read()
		if !ok {
			return false
		}
		switch {
		case m["type"] == "user":
			s.queue = append(s.queue, m)
		case m["type"] == "control_request" && subtype(m) == "interrupt":
			s.respond(m, map[string]any{"still_queued": []any{}})
			s.assistant(map[string]any{"type": "text", "text": "partial"})
			s.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user",
				"content": []any{map[string]any{"type": "text", "text": "[Request interrupted by user]"}}},
				"parent_tool_use_id": nil})
			return true
		}
	}
}

func (s *scen) result(uuid string, t multiturn.Turn, interrupted bool) {
	var cost float64
	var tokens int
	if t.Usage != nil && !interrupted {
		cost, tokens = t.Usage.CostUSD, t.Usage.Tokens
	}
	r := map[string]any{"type": "result", "subtype": "success", "is_error": false,
		"result": t.Reply, "stop_reason": "end_turn", "terminal_reason": "completed",
		"num_turns": 1, "total_cost_usd": cost, "user_message_uuid": uuid,
		"permission_denials": []any{},
		"usage":              map[string]any{"input_tokens": tokens, "output_tokens": 0, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
		"modelUsage":         map[string]any{scenModel: map[string]any{"contextWindow": 200000}}}
	if interrupted {
		r["subtype"], r["is_error"], r["stop_reason"], r["terminal_reason"] = "error_during_execution", true, nil, "aborted_streaming"
		r["result"] = ""
	}
	s.emit(r)
}

func toolInput(name, target string) map[string]any {
	switch name {
	case "Bash":
		return map[string]any{"command": target}
	case "Read", "Edit", "Write":
		return map[string]any{"file_path": target}
	}
	return map[string]any{"target": target}
}
