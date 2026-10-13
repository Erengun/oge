package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/run"
)

// The implementer's reply is transcript text (ADR-0022 amendment): printed
// whole, cleaned, above its stage line. QA's words never are.

func TestTUIReplyPrintsAboveTheImplementLine(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.m.scroll = true
	h.working()
	h.agent(agent.Event{Kind: agent.Claim, Text: "hello there\x1b[2J\n\nsecond paragraph"})
	h.agent(agent.Event{Kind: agent.Claim, Tool: "Edit", Target: "add.go"})
	h.implemented("")
	got := strings.Join(h.m.take(), "\n")
	for _, want := range []string{"hello there", "second paragraph"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not in the transcript:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[2J") {
		t.Errorf("a control sequence reached the transcript:\n%q", got)
	}
	if strings.Contains(got, "Edit add.go") {
		t.Errorf("tool activity became transcript text:\n%s", got)
	}
	if r, l := strings.Index(got, "hello there"), strings.Index(got, "✓ implement"); r < 0 || l < r {
		t.Errorf("the reply is not above the implement line:\n%s", got)
	}
}

func TestTUIReplyJoinsMessagesAndKeepsLines(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.m.scroll = true
	h.working()
	h.agent(agent.Event{Kind: agent.Claim, Text: "first line\nsecond line"})
	h.agent(agent.Event{Kind: agent.Claim, Text: "next message"})
	h.implemented("")
	got := strings.Join(h.m.take(), "\n")
	if !strings.Contains(got, "first line\nsecond line\n\nnext message\n") {
		t.Errorf("lines or messages ran together:\n%q", got)
	}
}

func TestTUIReplyKeepsALongReplyWhole(t *testing.T) {
	h := newTUIHarness(t, false, 80)
	h.m.scroll = true
	h.working()
	long := strings.Repeat("word ", 200) + "the end"
	h.agent(agent.Event{Kind: agent.Claim, Text: long})
	h.implemented("")
	if got := strings.Join(h.m.take(), "\n"); !strings.Contains(got, "the end") {
		t.Errorf("a long reply was cut:\n%s", got)
	}
}

func TestTUIQATextNeverReachesTheTranscript(t *testing.T) {
	h := newStandardHarness(t)
	h.m.scroll = true
	h.working()
	h.implemented("")
	v := h.reviewing(1)
	h.at(9 * time.Second)
	h.qaDone(v, 2)
	h.send(run.Event{Kind: run.EvAgent, Attempt: v, Agent: agent.Event{Kind: agent.Claim, Text: "QA also says more"}})
	all := strings.Join(h.m.take(), "\n") + h.m.render()
	for _, bad := range []string{"Add(-2, 1)", "QA also says more"} {
		if strings.Contains(all, bad) {
			t.Errorf("QA's words %q reached the transcript:\n%s", bad, all)
		}
	}
}

func TestPlainPrintsTheImplementersReply(t *testing.T) {
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "\nfixing Add in add.go\n") {
		t.Errorf("the reply is not a plain line:\n%s", out)
	}
}
