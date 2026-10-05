// Package fake is the scripted test adapter (ADR-0017). It satisfies the
// same Session contract as real adapters but is never a supported product
// agent: only test builds register it.
//
// A Session runs a POSIX shell script in the Workspace as the "agent". The
// script starts at Open and waits for the first turn, which it gets in
// $OGE_FAKE_TURN. Each line it prints is a Claim; a line "exit: <name>"
// declares an Exit. A script that exits 0 without declaring one settles
// with Exit done; a nonzero exit is an Attempt failure.
package fake

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/proc"
)

// Name is the agent name the fake binds as.
const Name = "fake"

// Adapter runs Script for every Session.
type Adapter struct {
	Script string // path to a shell script
}

// New returns an adapter that runs script.
func New(script string) *Adapter { return &Adapter{Script: script} }

// wrapper waits for the turn on stdin, then runs the script with it.
const wrapper = `OGE_FAKE_TURN=$(cat); export OGE_FAKE_TURN; exec /bin/sh "$1"`

// Open starts the script's process; it waits for Send.
func (a *Adapter) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	if a.Script == "" {
		return nil, fmt.Errorf("fake adapter: no script")
	}
	cmd := exec.Command("/bin/sh", "-c", wrapper, "oge-fake", a.Script)
	cmd.Dir = spec.Workspace
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := proc.Start(cmd); err != nil {
		return nil, fmt.Errorf("fake adapter: %w", err)
	}
	s := &session{cmd: cmd, stdin: stdin, events: make(chan agent.Event, 64)}
	s.process = agent.Process{PID: cmd.Process.Pid, PGID: proc.Group(cmd)}
	s.events <- agent.Event{Kind: agent.SessionOpened}
	go s.read(stdout)
	return s, nil
}

type session struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	process agent.Process
	events  chan agent.Event

	mu   sync.Mutex
	sent bool
}

func (s *session) Process() agent.Process      { return s.process }
func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) Send(t agent.Turn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sent {
		return agent.ErrTurnInFlight // the fake plays exactly one turn
	}
	s.sent = true
	_, err := io.WriteString(s.stdin, t.Text)
	if cerr := s.stdin.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *session) read(stdout io.Reader) {
	defer close(s.events)
	accepted := false
	exit := ""
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if !accepted {
			s.events <- agent.Event{Kind: agent.TurnAccepted}
			accepted = true
		}
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "exit: "); ok {
			exit = strings.TrimSpace(name)
			continue
		}
		s.events <- agent.Event{Kind: agent.Claim, Text: line}
	}
	err := s.cmd.Wait()
	if !accepted {
		s.events <- agent.Event{Kind: agent.TurnAccepted}
	}
	switch {
	case err != nil:
		s.events <- agent.Event{Kind: agent.TurnSettled, Failure: "agent_crash: " + err.Error()}
	case exit == "":
		s.events <- agent.Event{Kind: agent.TurnSettled, Exit: "done"}
	default:
		s.events <- agent.Event{Kind: agent.TurnSettled, Exit: exit}
	}
}

func (s *session) Interrupt() error {
	proc.Kill(s.cmd)
	return nil
}

func (s *session) Close() error {
	proc.Kill(s.cmd) // before closing stdin, so an unsent script never runs
	s.mu.Lock()
	if !s.sent {
		s.sent = true
		s.stdin.Close()
	}
	s.mu.Unlock()
	return nil
}
