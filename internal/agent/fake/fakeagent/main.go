// Command fakeagent is the multi-turn fake agent (docs/ux/chat-first.md,
// slice 3). It serves the scenario in OGE_FAKE_SCENARIO from stdin to
// stdout until stdin closes; see package multiturn for the line protocol.
// The working directory is its Workspace. It is never shipped.
package main

import (
	"fmt"
	"os"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

func main() {
	path := os.Getenv("OGE_FAKE_SCENARIO")
	if path == "" {
		fmt.Fprintln(os.Stderr, "fakeagent: no OGE_FAKE_SCENARIO")
		os.Exit(3)
	}
	sc, err := multiturn.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		os.Exit(3)
	}
	dir, _ := os.Getwd()
	if err := multiturn.Serve(os.Stdin, os.Stdout, dir, sc); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		os.Exit(1)
	}
}
