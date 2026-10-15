// Command fakeagy is the fake agy executable for adapter contract tests
// and binary end-to-end tests (ADR-0017, ADR-0023). It replays recorded
// stream-json events over stdin/stdout and never touches real models or
// credentials.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type frame struct {
	Dir   string          `json:"dir"` // "in", "out", "act", "exit"
	Msg   json.RawMessage `json:"msg"`
	Code  *int            `json:"code"`
	Write *struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"write"`
}

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" {
			if v := os.Getenv("OGE_FAKE_AGY_VERSION"); v != "" {
				fmt.Println(v)
				return
			}
			fmt.Println("1.3.5")
			return
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "models" {
		fmt.Println("gemini-3.8-flash-high     Gemini 3.8 Flash (High)")
		fmt.Println("gemini-3.8-flash-medium   Gemini 3.8 Flash (Medium)")
		fmt.Println("gemini-3.8-flash-low      Gemini 3.8 Flash (Low)")
		return
	}

	if rec := os.Getenv("OGE_FAKE_AGY_RECORD"); rec != "" {
		b, _ := json.Marshal(map[string]any{"argv": os.Args[1:], "env": os.Environ()})
		_ = os.WriteFile(rec, b, 0o644)
	}

	if errText := os.Getenv("OGE_FAKE_AGY_STDERR"); errText != "" {
		fmt.Fprintln(os.Stderr, errText)
	}

	if exitStr := os.Getenv("OGE_FAKE_AGY_EXIT"); exitStr != "" {
		code, _ := strconv.Atoi(exitStr)
		os.Exit(code)
	}

	fixturePath := os.Getenv("OGE_FAKE_AGY_FIXTURE")
	if fixturePath == "" {
		fixturePath = os.Getenv("OGE_FAKE_AGY_SCRIPT")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if fixturePath != "" {
		runFixture(fixturePath, sigCh)
		return
	}

	// Default minimal flow
	cwd, _ := os.Getwd()
	fmt.Printf(`{"event":"init","cwd":%q,"conversation_id":"fake-conv-1","tools":["run_command","write_to_file","replace_file_content","view_file"],"model":"gemini-3.8-flash-high"}`+"\n", cwd)

	in := bufio.NewScanner(os.Stdin)
	if !in.Scan() {
		return
	}

	if os.Getenv("OGE_FAKE_AGY_DENIED_ACTIONS") != "" {
		fmt.Println(`{"event":"result","status":"SUCCESS","response":"","num_turns":1,"denied_actions":[{"action":"command","display_name":"RunCommand"}]}`)
		for in.Scan() {
		}
		return
	}

	if os.Getenv("OGE_FAKE_AGY_HANG") != "" {
		<-sigCh
		fmt.Println(`{"event":"result","status":"INTERRUPTED","response":"Interrupted"}`)
		for in.Scan() {
		}
		return
	}

	fmt.Println(`{"event":"step_update","step_type":"agent_response","text_delta":"Fixed."}`)
	fmt.Println(`{"event":"result","status":"SUCCESS","response":"Fixed.\nExit: done","num_turns":1,"usage":{"total_tokens":42}}`)
	for in.Scan() {
	}
}

func runFixture(path string, sigCh <-chan os.Signal) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeagy: read fixture: %v\n", err)
		os.Exit(1)
	}

	in := bufio.NewScanner(os.Stdin)
	lines := strings.Split(string(data), "\n")

	for _, rawLine := range lines {
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" || strings.HasPrefix(rawLine, "#") {
			continue
		}

		var f frame
		if err := json.Unmarshal([]byte(rawLine), &f); err == nil && f.Dir != "" {
			switch f.Dir {
			case "out":
				fmt.Println(string(f.Msg))
			case "in":
				if !in.Scan() {
					return
				}
			case "act":
				if f.Write != nil {
					_ = os.MkdirAll(filepath.Dir(f.Write.Path), 0o755)
					_ = os.WriteFile(f.Write.Path, []byte(f.Write.Content), 0o644)
				}
			case "exit":
				code := 0
				if f.Code != nil {
					code = *f.Code
				}
				os.Exit(code)
			case "hang":
				<-sigCh
				fmt.Println(`{"event":"result","status":"INTERRUPTED","response":"Interrupted"}`)
				for in.Scan() {
				}
				return
			}
			continue
		}

		// Plain JSON NDJSON frame: print directly
		fmt.Println(rawLine)
	}
	for in.Scan() {
	}
}
