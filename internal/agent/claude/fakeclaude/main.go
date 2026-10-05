// Command fakeclaude is the fake claude executable the adapter's contract
// tests and the CLI's end-to-end tests run (ADR-0017). It replays a
// recorded stream-json transcript over its real stdin and stdout, and
// never reaches a model or a credential. It is never shipped.
//
// Environment:
//
//	OGE_FAKE_CLAUDE_FIXTURE  the transcript to replay (testdata format)
//	OGE_FAKE_CLAUDE_FIXTURE_<ROLE>  the one to replay instead for $OGE_ROLE,
//	                         e.g. OGE_FAKE_CLAUDE_FIXTURE_VERIFIER
//	OGE_FAKE_CLAUDE_RECORD   where to write its argv and environment as JSON
//	OGE_FAKE_CLAUDE_STDIN    where to append every line it reads
//
// Replay: an "out" frame is written, with the recording's working
// directory /home/user/project rewritten to the real one. An "in" frame
// waits for the next stdin line and checks its type (and control
// subtype); a control response from the fake keeps the request id the
// host actually used. "act" frames are test-only: {"dir":"act","write":
// {"path":...,"content":...}} writes a file if the host allowed the last
// permission request, {"dir":"raw","line":...} writes a line as is, and
// {"dir":"die"} exits 1 at once, and {"dir":"hang"} ignores stdin EOF and
// SIGTERM. An "in" frame's "expect" ("allow" or "deny") checks the host's
// actual decision; an "act" frame's "spawn" starts a background process.
// At {"dir":"meta","exit":N} it waits for stdin to close, then exits N.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const recordedCwd = "/home/user/project"

type entry struct {
	Dir  string          `json:"dir"`
	Msg  json.RawMessage `json:"msg"`
	Exit *int            `json:"exit"`
	Argv []string        `json:"argv"`
	Line string          `json:"line"`
	// Expect, on an "in" control response, is the decision the host must
	// send: "allow" or "deny". A different one fails the replay.
	Expect string `json:"expect"`
	// Spawn, on an "act" frame, starts a background process in the
	// fake's group and writes its pid to this path.
	Spawn string `json:"spawn"`
	// NoNewline, on a "raw" frame, leaves the line unterminated.
	NoNewline bool `json:"no_newline"`
	Write     *struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"write"`
}

type msg struct {
	Type     string `json:"type"`
	Request  *ctl   `json:"request"`
	Response *ctl   `json:"response"`
	ReqID    string `json:"request_id"`
}

type ctl struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response"`
}

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Println("2.1.289 (Claude Code)")
			return
		}
	}
	if path := os.Getenv("OGE_FAKE_CLAUDE_RECORD"); path != "" {
		b, _ := json.Marshal(map[string]any{"argv": os.Args[1:], "env": os.Environ()})
		if err := os.WriteFile(path, b, 0o600); err != nil {
			fail("recording: %v", err)
		}
	}
	fixture := os.Getenv("OGE_FAKE_CLAUDE_FIXTURE")
	if f := os.Getenv("OGE_FAKE_CLAUDE_FIXTURE_" + strings.ToUpper(os.Getenv("OGE_ROLE"))); f != "" && os.Getenv("OGE_ROLE") != "" {
		fixture = f
	}
	if fixture == "" {
		fail("no OGE_FAKE_CLAUDE_FIXTURE")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		fail("%v", err)
	}
	cwd, _ := os.Getwd()
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	var stdinLog *os.File
	if p := os.Getenv("OGE_FAKE_CLAUDE_STDIN"); p != "" {
		if stdinLog, err = os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
			fail("%v", err)
		}
	}
	readLine := func() ([]byte, bool) {
		line, err := in.ReadBytes('\n')
		if stdinLog != nil && len(line) > 0 {
			stdinLog.Write(line)
		}
		return line, err == nil
	}
	ids := map[string]string{} // recorded host request id → actual
	allowed := false
	for _, l := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		var e entry
		if err := json.Unmarshal(l, &e); err != nil {
			fail("bad fixture line: %v", err)
		}
		switch e.Dir {
		case "meta":
			if e.Exit != nil {
				for {
					if _, ok := readLine(); !ok {
						break
					}
				}
				os.Exit(*e.Exit)
			}
		case "raw":
			if e.NoNewline {
				fmt.Print(e.Line)
			} else {
				fmt.Println(e.Line)
			}
		case "die":
			os.Exit(1)
		case "hang":
			// Ignore stdin EOF and SIGTERM: only SIGKILL ends it.
			signal.Ignore(syscall.SIGTERM)
			select {}
		case "act":
			if e.Spawn != "" {
				c := exec.Command("sleep", "60")
				if err := c.Start(); err != nil {
					fail("%v", err)
				}
				os.WriteFile(e.Spawn, []byte(strconv.Itoa(c.Process.Pid)), 0o600)
			}
			if e.Write != nil && allowed {
				if err := os.WriteFile(filepath.Join(cwd, e.Write.Path), []byte(e.Write.Content), 0o644); err != nil {
					fail("%v", err)
				}
			}
		case "out":
			out := strings.ReplaceAll(string(e.Msg), recordedCwd, cwd)
			var m msg
			_ = json.Unmarshal(e.Msg, &m)
			if m.Type == "control_response" && m.Response != nil {
				if actual, ok := ids[m.Response.RequestID]; ok {
					var v map[string]any
					_ = json.Unmarshal([]byte(out), &v)
					v["response"].(map[string]any)["request_id"] = actual
					b, _ := json.Marshal(v)
					out = string(b)
				}
			}
			fmt.Println(out)
		case "in":
			var want msg
			_ = json.Unmarshal(e.Msg, &want)
			line, ok := readLine()
			if !ok {
				fail("stdin closed while waiting for %s", describe(want))
			}
			var got msg
			if err := json.Unmarshal(line, &got); err != nil {
				fail("host sent a line that isn't JSON")
			}
			if describe(got) != describe(want) {
				fail("expected %s from the host, got %s", describe(want), describe(got))
			}
			switch got.Type {
			case "control_request":
				ids[want.ReqID] = got.ReqID
			case "control_response":
				if got.Response.RequestID != want.Response.RequestID {
					fail("the host answered request %s, expected %s", got.Response.RequestID, want.Response.RequestID)
				}
				allowed = permits(got.Response.Response)
				if e.Expect != "" && (e.Expect == "allow") != allowed {
					fail("host answered request %s with %s, expected %s", want.Response.RequestID, got.Response.Response, e.Expect)
				}
			}
		}
	}
	for {
		if _, ok := readLine(); !ok {
			return
		}
	}
}

func describe(m msg) string {
	switch {
	case m.Request != nil:
		return m.Type + "/" + m.Request.Subtype
	case m.Response != nil:
		return m.Type + "/" + m.Response.Subtype
	}
	return m.Type
}

// permits reports whether a permission answer lets the tool run: a
// can_use_tool allow, or a hook that made no deny decision.
func permits(resp json.RawMessage) bool {
	var r struct {
		Behavior string `json:"behavior"`
		Hook     *struct {
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Hook != nil {
		return r.Hook.Decision != "deny"
	}
	return r.Behavior != "deny"
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "fakeclaude: "+format+"\n", a...)
	os.Exit(3)
}
