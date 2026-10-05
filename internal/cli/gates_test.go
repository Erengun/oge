package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erengun/oge/internal/gate"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// attended makes the fixture a human at a terminal who types stdin.
func (f *runFixture) attended(stdin string) {
	f.interactive, f.stdin = true, stdin
}

// gateRecords are the Run's Gate records, as "type choice reason".
func gateRecords(t *testing.T, runDir string) []string {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		switch r.Type {
		case run.RecGateOpened, run.RecGateDecided, run.RecRunEnded, run.RecRunParked:
			var d struct {
				Pins struct {
					Gate      string
					Candidate string
					Verdicts  []int
				}
				Actor, Choice, Reason, Outcome string
			}
			if err := json.Unmarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
			if r.Type == run.RecGateDecided && (d.Actor != "human" || d.Pins.Candidate == "" || len(d.Pins.Verdicts) == 0) {
				t.Errorf("a decision isn't pinned: %s", r.Data)
			}
			var parts []string
			for _, p := range []string{r.Type, d.Pins.Gate, d.Choice, d.Reason, d.Outcome} {
				if p != "" {
					parts = append(parts, p)
				}
			}
			out = append(out, strings.Join(parts, " "))
		}
	}
	return out
}

func TestAttendedBoundExhaustionGate(t *testing.T) {
	for _, c := range []struct {
		name, stdin string
		code        int
		want        []string
		records     string
	}{
		{
			// Enter alone does nothing; "r" isn't reject; the reason is
			// asked for when none is inline.
			name: "reject", stdin: "\nr\nreject\n\nthe test can't pass as written\n", code: ExitRejected,
			want: []string{
				"bound-exhaustion Gate", "The Check failed and the send-back limit (0) is used up.",
				"Candidate ", " · Attempt implement#1 · Oracle v0 · Verdicts #1",
				"  s  send back  one more send-back past the limit",
				"  reject        end the Run Rejected", "  q  quit       end the Run Cancelled",
				gateHint, `type "reject" in full`, "a reason is required",
				"decision   reject · recorded at the bound-exhaustion Gate · reason: the test can't pass as written",
				"REJECTED   Candidate ",
			},
			records: "GateOpened gate.bound_exhaustion|GateDecided gate.bound_exhaustion reject the test can't pass as written|RunEnded Rejected",
		},
		{
			name: "quit", stdin: "q\n", code: ExitCancelled,
			want:    []string{"decision   quit · recorded at the bound-exhaustion Gate", "CANCELLED  Candidate "},
			records: "GateOpened gate.bound_exhaustion|GateDecided gate.bound_exhaustion quit|RunEnded Cancelled",
		},
		{
			name: "reject with an inline reason", stdin: "reject no fix exists\n", code: ExitRejected,
			want:    []string{"reason: no fix exists"},
			records: "GateOpened gate.bound_exhaustion|GateDecided gate.bound_exhaustion reject no fix exists|RunEnded Rejected",
		},
		{
			name: "the terminal closes", stdin: "", code: ExitInfra,
			want:    []string{"the bound-exhaustion Gate got no decision: the terminal closed"},
			records: "GateOpened gate.bound_exhaustion|RunEnded Infrastructure stop",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRunFixture(t)
			f.sendBackLimit(t, 0)
			f.attended(c.stdin)
			code, out, errOut := f.run(t, cheatScript, "fix Add", "--fast", "--agent", "fake", "--plain")
			if code != c.code {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("stdout lacks %q:\n%s", w, out)
				}
			}
			if got := strings.Join(gateRecords(t, f.onlyRun(t)), "|"); got != c.records {
				t.Errorf("records:\n got %s\nwant %s", got, c.records)
			}
			f.assertUntouched(t)
		})
	}
}

// A send back past the limit needs a reason, and the next turn carries it
// with the failure output.
func TestAttendedSendBackPastTheLimit(t *testing.T) {
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	f.attended("s\n\ntry once more\n")
	code, out, errOut := f.run(t, fixOnSendBack, "fix Add", "--fast", "--agent", "fake", "--plain")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, w := range []string{
		"a reason is required",
		"decision   send back · recorded at the bound-exhaustion Gate · reason: try once more",
		"send back  1 (limit 0, extended at the Gate) · ", "ACCEPTED   Candidate ",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
}

// The reason can be written in $EDITOR; its comment lines are dropped.
func TestGateReasonFromTheEditor(t *testing.T) {
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	f.attended("reject\ne\n")
	f.edit = func(path string) error {
		return os.WriteFile(path, []byte("# a comment\nwritten in the editor\n"), 0o600)
	}
	code, out, errOut := f.run(t, cheatScript, "fix Add", "--fast", "--agent", "fake", "--plain")
	if code != ExitRejected || !strings.Contains(out, "reason: written in the editor") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func TestResultGate(t *testing.T) {
	for _, c := range []struct {
		name, stdin, script string
		code                int
		want                []string
		records             string
	}{
		{
			name: "take", stdin: "\nt\n", script: fixScript, code: ExitOK,
			want: []string{
				"Result gate", "Öge's Check passed on the Candidate.", "1 ran · 0 failed · pass",
				"  t  take       end the Run Accepted", "  s  send back  return it to the implementer",
				"decision   take · recorded at the Result gate", "ACCEPTED   Candidate ",
			},
			records: "GateOpened gate.result|GateDecided gate.result take|RunEnded Accepted",
		},
		{
			name: "reject", stdin: "reject not what I asked for\n", script: fixScript, code: ExitRejected,
			want:    []string{"REJECTED   Candidate "},
			records: "GateOpened gate.result|GateDecided gate.result reject not what I asked for|RunEnded Rejected",
		},
		{
			name: "quit", stdin: "quit\n", script: fixScript, code: ExitCancelled,
			want:    []string{"CANCELLED  Candidate "},
			records: "GateOpened gate.result|GateDecided gate.result quit|RunEnded Cancelled",
		},
		{
			name: "send back with a note", stdin: "send back keep it shorter\nt\n", script: "echo \"$OGE_FAKE_TURN\" | grep -q 'keep it shorter' && echo 'got the note'\n" + fixScript, code: ExitOK,
			want: []string{"note: keep it shorter", "send back  1 of 3 · ", `"got the note"`},
			records: "GateOpened gate.result|GateDecided gate.result send back|" +
				"GateOpened gate.result|GateDecided gate.result take|RunEnded Accepted",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRunFixture(t)
			f.attended(c.stdin)
			code, out, errOut := f.run(t, c.script, "fix Add", "--fast", "--agent", "fake", "--plain", "--confirm", "-v")
			if code != c.code {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("stdout lacks %q:\n%s", w, out)
				}
			}
			if got := strings.Join(gateRecords(t, f.onlyRun(t)), "|"); got != c.records {
				t.Errorf("records:\n got %s\nwant %s", got, c.records)
			}
		})
	}
}

// Without --confirm, or unattended, a pass ends Accepted with no Gate
// (ADR-0019).
func TestNoResultGateByDefaultOrUnattended(t *testing.T) {
	for _, args := range [][]string{{"--plain"}, {"--confirm", "--unattended"}} {
		f := newRunFixture(t)
		f.interactive = true
		code, out, errOut := f.run(t, fixScript, append([]string{"fix Add", "--fast", "--agent", "fake"}, args...)...)
		if code != ExitOK || strings.Contains(out, "Result gate") {
			t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
		if got := strings.Join(gateRecords(t, f.onlyRun(t)), "|"); got != "RunEnded Accepted" {
			t.Errorf("%v: records %s", args, got)
		}
	}
}

// syncBuf is a writer a test can read while another goroutine writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) waitFor(t *testing.T, text string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		ok := strings.Contains(s.b.String(), text)
		s.mu.Unlock()
		if ok {
			return
		}
	}
	t.Fatalf("never saw %q", text)
}

func boundExhaustionRequest() gate.Request {
	return gate.Request{
		Name: "bound-exhaustion", What: "The Check failed and the send-back limit (3) is used up.", Need: "Decide.",
		Pins: gate.Pins{Gate: "gate.bound_exhaustion", Attempt: "implement#4", Candidate: "6d1231d9f00d", Verdicts: []int{1, 2, 3, 4}},
		Choices: []gate.Choice{
			{Word: "send back", Key: "s", Reason: true}, gate.Choices["reject"], gate.Choices["quit"],
		},
	}
}

// Ctrl-C at the reason prompt returns to the Gate, and doesn't cancel the
// Run.
func TestPlainCtrlCAtTheReasonPromptReturnsToTheGate(t *testing.T) {
	pr, pw := io.Pipe()
	out := &syncBuf{}
	cancelled := false
	in := newInterrupts(func() { cancelled = true })
	r := &renderer{w: out, input: &lines{in: pr}, intr: in}
	go func() {
		io.WriteString(pw, "reject\n")
		out.waitFor(t, "reason (")
		in.interrupt()
		out.waitFor(t, "back at the Gate")
		io.WriteString(pw, "q\n")
	}()
	d, err := r.gate(context.Background(), boundExhaustionRequest())
	if err != nil || d.Choice != "quit" || cancelled {
		t.Fatalf("decision %+v, err %v, cancelled %v\n%s", d, err, cancelled, out.b.String())
	}
	// The interrupt was the prompt's; the next one cancels the Run again.
	in.interrupt()
	if !cancelled {
		t.Error("Ctrl-C after the Gate didn't cancel the Run")
	}
}
