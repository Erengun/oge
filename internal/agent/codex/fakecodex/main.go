// Command fakecodex stands in for `codex app-server` in the codex
// adapter's contract tests. It replays the script in OGE_FAKECODEX_SCRIPT
// (testdata's recording format): it waits for each recorded client
// message, matching requests by method, answers under the client's own
// id, and writes every client message to OGE_FAKECODEX_LOG.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

type line struct {
	Dir string         `json:"dir"`
	Msg map[string]any `json:"msg"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "app-server" {
		fmt.Fprintln(os.Stderr, "fakecodex: only app-server is faked")
		os.Exit(2)
	}
	script, err := os.ReadFile(os.Getenv("OGE_FAKECODEX_SCRIPT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakecodex:", err)
		os.Exit(2)
	}
	log, err := os.Create(os.Getenv("OGE_FAKECODEX_LOG"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakecodex:", err)
		os.Exit(2)
	}
	defer log.Close()
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 16<<20)
	out := bufio.NewWriter(os.Stdout)
	ids := map[string]any{} // recorded request id -> the client's id
	next := func() map[string]any {
		out.Flush()
		for in.Scan() {
			fmt.Fprintf(log, "%s\n", in.Bytes())
			var m map[string]any
			if json.Unmarshal(in.Bytes(), &m) == nil {
				return m
			}
		}
		os.Exit(0) // stdin closed
		return nil
	}
	sc := bufio.NewScanner(bytes.NewReader(script))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.Dir == "exit" { // the app-server dies here
			out.Flush()
			os.Exit(1)
		}
		method, _ := l.Msg["method"].(string)
		key := fmt.Sprint(l.Msg["id"])
		if l.Dir == "c2s" {
			if method == "" {
				next() // the client's answer to a server request
				continue
			}
			for {
				m := next()
				if m["method"] == method {
					if id, ok := m["id"]; ok {
						ids[key] = id
					}
					break
				}
				if id, ok := m["id"]; ok && m["method"] != nil {
					// A request the recording lacks: refuse it.
					writeMsg(out, map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": "not in the recording"}})
				}
			}
			continue
		}
		if _, isResp := l.Msg["id"]; isResp && method == "" {
			l.Msg["id"] = ids[key]
		}
		writeMsg(out, l.Msg)
	}
	for {
		next()
	}
}

func writeMsg(out *bufio.Writer, m map[string]any) {
	b, _ := json.Marshal(m)
	out.Write(append(b, '\n'))
}
