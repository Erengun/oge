//go:build unix

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/agent/fake"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// advisedAdapter is the claude adapter for the implementer and a fake
// script for the advisor: only the Chat's own agent advises, so the
// advisor shares its name, and its Role tells the two apart.
type advisedAdapter struct {
	*claude.Adapter
	advisor agent.Adapter
}

func (a advisedAdapter) Open(ctx context.Context, spec agent.LaunchSpec) (agent.Session, error) {
	if spec.Role == "advisor" {
		return a.advisor.Open(ctx, spec)
	}
	return a.Adapter.Open(ctx, spec)
}

// startAdvisedSession is startChatSession with a fake advisor running
// script.
func startAdvisedSession(t *testing.T, sc multiturn.Scenario, script string, setup ...func(*runFixture)) (*homeSession, *runFixture) {
	t.Helper()
	f := newRunFixture(t)
	for _, fn := range setup {
		fn(f)
	}
	scen := filepath.Join(t.TempDir(), "scenario.json")
	if err := sc.Save(scen); err != nil {
		t.Fatal(err)
	}
	sp := filepath.Join(t.TempDir(), "advisor.sh")
	if err := os.WriteFile(sp, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ad := advisedAdapter{Adapter: &claude.Adapter{Path: multiturn.BuildClaude(t), CloseGrace: time.Second,
		Environ: func() []string { return append(os.Environ(), "OGE_FAKE_CLAUDE_SCENARIO="+scen) }},
		advisor: &fake.Adapter{Script: sp}}
	s := startHomeSessionWith(t, f, func(e *Env) { e.Agents = map[string]agent.Adapter{"claude": ad} }, "--fast", "--agent", "claude")
	return s, f
}

// chatRecords are the Chat log's records of typ.
func chatRecords(t *testing.T, f *runFixture, typ string) []map[string]any {
	t.Helper()
	logs, _ := filepath.Glob(filepath.Join(f.state, "private", "chats", "*"))
	var out []map[string]any
	for _, dir := range logs {
		recs, err := ledger.Replay(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			if r.Type != typ {
				continue
			}
			m := map[string]any{}
			if err := json.Unmarshal(r.Data, &m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m)
		}
	}
	return out
}

const adviceScript = "echo 'Inject a Clock.'\n"

// /advise runs in the background and prints the advice as a block; ↵
// sends it as Öge's dim turn, which is in the log as an advice turn and
// an AdviceSent.
func TestHomeChatAdviseSendsDim(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first reply"},
		{Reply: "took the advice"},
	}}, adviceScript)
	s.composer()
	s.type_("yo\r")
	s.expect("  first reply")
	s.composer()
	s.type_("/advise is there a cleaner seam?\r")
	s.expect("a Claim, not checked")
	s.expect("Inject a Clock.")
	s.expect("send to agent")
	s.type_("\r")
	s.expect("Öge → agent: An advisor suggests (a second opinion, not checked): ")
	s.expect("  took the advice")
	s.composer()
	s.quitChat(f)
	sent := chatRecords(t, f, run.RecAdviceSent)
	if len(sent) != 1 || sent[0]["how"] != "verbatim" || sent[0]["by"] != "oge" || sent[0]["consultation"] == "" {
		t.Errorf("AdviceSent %v", sent)
	}
	if ended := chatRecords(t, f, run.RecConsultationEnded); len(ended) != 1 || ended[0]["outcome"] != run.ConsultAdvised {
		t.Errorf("ConsultationEnded %v", ended)
	}
}

// e puts the advice in the composer, and what is sent from there is the
// user's turn.
func TestHomeChatAdviseEditIsTheUsersTurn(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first reply"},
		{Reply: "did it my way"},
	}}, adviceScript)
	s.composer()
	s.type_("yo\r")
	s.expect("  first reply")
	s.composer()
	s.type_("/advise how?\r")
	s.expect("send to agent")
	s.type_("e")
	s.expect("Inject a Clock.")
	s.type_(", in sched\r")
	s.expect("  did it my way")
	s.composer()
	if strings.Contains(s.out.String(), "Öge → agent: An advisor") {
		t.Errorf("an edited advice was shown as Öge's turn:\n%s", s.out.String())
	}
	s.quitChat(f)
	sent := chatRecords(t, f, run.RecAdviceSent)
	if len(sent) != 1 || sent[0]["how"] != "edited" || sent[0]["by"] != "user" {
		t.Errorf("AdviceSent %v", sent)
	}
	for _, r := range chatRecords(t, f, run.RecTurnStarted) {
		if r["consultation"] != nil && r["by"] != nil {
			t.Errorf("an edited advice turn is Öge's: %v", r)
		}
	}
}

// Esc dismisses the advice: the Consultation stays in the log, and
// nothing reaches the agent.
func TestHomeChatAdviseDismiss(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "first reply"}}}, adviceScript)
	s.composer()
	s.type_("yo\r")
	s.expect("  first reply")
	s.composer()
	s.type_("/advise how?\r")
	s.expect("send to agent")
	s.type_("\x1b")
	s.expect("advice dismissed")
	s.composer()
	s.quitChat(f)
	if n := len(chatRecords(t, f, run.RecAdviceSent)); n != 0 {
		t.Errorf("%d AdviceSent after a dismissal", n)
	}
	if n := len(chatRecords(t, f, run.RecTurnStarted)); n != 1 {
		t.Errorf("%d turns, want only the user's", n)
	}
	if n := len(chatRecords(t, f, run.RecConsultationEnded)); n != 1 {
		t.Errorf("%d ConsultationEnded, want the dismissed one kept", n)
	}
}

// /advise without a question says how to use it, and Esc cancels a
// Consultation still advising.
func TestHomeChatAdviseUsageAndCancel(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{{Reply: "first reply"}}}, "sleep 30\n")
	s.composer()
	s.type_("yo\r")
	s.expect("  first reply")
	s.composer()
	s.type_("/advise \r")
	s.expect("usage: /advise <question>")
	s.type_("\x15")
	s.type_("/advise slow one\r")
	s.expect("advising · claude")
	s.expect("fresh, read-only")
	s.expect("esc cancel")
	s.type_("\x1b")
	s.expect("advising cancelled")
	s.composer()
	s.quitChat(f)
	if ended := chatRecords(t, f, run.RecConsultationEnded); len(ended) != 1 || ended[0]["outcome"] != run.ConsultCancelled {
		t.Errorf("ConsultationEnded %v", ended)
	}
}

// During a turn /advise runs at once, never queued; Esc then interrupts
// the turn and leaves the Consultation going; its block waits until the
// turn has settled.
func TestHomeChatAdviseAlongsideATurn(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "working on it", Hold: true},
	}}, "sleep 1\n"+adviceScript) // slow enough that the status line is drawn: a frame is not drawn per update
	s.composer()
	s.type_("long job\r")
	s.expect("working on it")
	s.type_("/advise meanwhile?\r")
	s.expect("advising · claude")
	if out := s.out.String(); strings.Contains(out, "queued › /advise") {
		t.Errorf("/advise was queued:\n%s", out)
	}
	time.Sleep(2500 * time.Millisecond) // the advisor has finished
	if out := s.out.String(); strings.Contains(out, "a Claim, not checked") {
		t.Errorf("the advice block printed into a streaming turn:\n%s", out)
	}
	s.type_("\x1b")
	s.expect("interrupted")
	s.expect("a Claim, not checked")
	s.expect("Inject a Clock.")
	s.type_("\x1b")
	s.expect("advice dismissed")
	s.composer()
	s.quitChat(f)
}

// A cut's Task never holds the advice, sent verbatim: its text is not
// among the Run's task turns.
func TestHomeChatAdviseIsNeverTheTask(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "changed Add", Writes: []multiturn.File{addGo("a + b")}},
		{Reply: "took the advice", Writes: []multiturn.File{addGo("b + a")}},
	}}, adviceScript)
	s.composer()
	s.type_("change Add\r")
	s.expect("oge › ✓ Accepted · 1 file · ")
	s.composer()
	s.type_("/advise how?\r")
	s.expect("send to agent")
	s.type_("\r")
	s.expect("Öge → agent: An advisor suggests")
	s.expect("  took the advice")
	s.expectAfter("  took the advice", "oge › ✓ Accepted · 1 file · ")
	s.composer()
	s.quitChat(f)
	hash := func(x string) string { h := sha256.Sum256([]byte(x)); return hex.EncodeToString(h[:]) }
	bad := map[string]bool{hash("Inject a Clock."): true, hash(run.AdvicePrefix + "Inject a Clock."): true}
	runs, _ := filepath.Glob(filepath.Join(f.state, "private", "runs", "*"))
	cuts := 0
	for _, dir := range runs {
		recs, err := ledger.Replay(dir)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if r.Type != run.RecRunStarted {
				continue
			}
			cuts++
			var started struct {
				TaskTurns []struct{ Sha256 string } `json:"task_turns"`
			}
			if err := json.Unmarshal(r.Data, &started); err != nil {
				t.Fatal(err)
			}
			for _, tt := range started.TaskTurns {
				if bad[tt.Sha256] {
					t.Errorf("the advice is a Task turn of %s", dir)
				}
			}
		}
	}
	if cuts < 2 {
		t.Errorf("%d cuts found, want 2 (the advice turn's change is cut)", cuts)
	}
}

// Emptying the box after e drops the edit: the next, unrelated message
// is a plain turn with no AdviceSent. Sent as it was, the advice is still
// Öge's verbatim turn.
func TestHomeChatAdviseEditFlagDoesNotOutliveItsText(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first reply"},
		{Reply: "unrelated answer"},
	}}, adviceScript)
	s.composer()
	s.type_("yo\r")
	s.expect("  first reply")
	s.composer()
	s.type_("/advise how?\r")
	s.expect("send to agent")
	s.type_("e")
	s.expect("Inject a Clock.")
	s.type_("\x15") // empty the box
	s.type_("something else\r")
	s.expect("  unrelated answer")
	s.composer()
	s.quitChat(f)
	if n := len(chatRecords(t, f, run.RecAdviceSent)); n != 0 {
		t.Errorf("%d AdviceSent for an unrelated message", n)
	}
}

func TestHomeChatAdviseEditedButUnchangedIsVerbatim(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first reply"},
		{Reply: "took it"},
	}}, adviceScript)
	s.composer()
	s.type_("yo\r")
	s.expect("  first reply")
	s.composer()
	s.type_("/advise how?\r")
	s.expect("send to agent")
	s.type_("e")
	s.expect("Inject a Clock.")
	s.type_("\r")
	s.expect("Öge → agent: An advisor suggests")
	s.expect("  took it")
	s.composer()
	s.quitChat(f)
	sent := chatRecords(t, f, run.RecAdviceSent)
	if len(sent) != 1 || sent[0]["how"] != "verbatim" || sent[0]["by"] != "oge" {
		t.Errorf("AdviceSent %v", sent)
	}
}

// The user's own turn drops the advice shown: no ↵ or e binding is left,
// and nothing was sent to the agent.
func TestHomeChatAdviseOwnTurnDismisses(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "first reply"},
		{Reply: "second reply"},
	}}, adviceScript)
	s.composer()
	s.type_("yo\r")
	s.expect("  first reply")
	s.composer()
	s.type_("/advise how?\r")
	s.expect("send to agent")
	s.type_("my own message\r")
	s.expect("advice dismissed")
	s.expect("  second reply")
	s.composer()
	if out := s.out.String()[s.mark:]; strings.Contains(out, "send to agent") {
		t.Errorf("the advice keys outlived the user's turn:\n%s", out)
	}
	s.quitChat(f)
	if n := len(chatRecords(t, f, run.RecAdviceSent)); n != 0 {
		t.Errorf("%d AdviceSent", n)
	}
}

// advisorAuto writes a user config with [advisor] auto = mode, in a config
// home of this fixture's own.
func advisorAuto(t *testing.T, f *runFixture, mode string) {
	t.Helper()
	f.setenv("XDG_CONFIG_HOME", t.TempDir())
	path := pipeline.UserConfigPath(f.getenv())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("schema = 1\n[advisor]\nauto = \""+mode+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// autoScenario fails the user's turn and each of the budget's feedback
// turns, then answers an advice turn.
func autoScenario() multiturn.Scenario {
	var turns []multiturn.Turn
	for i := range run.FeedbackBudget + 1 {
		turns = append(turns, multiturn.Turn{Reply: fmt.Sprintf("try %d", i), Writes: []multiturn.File{addGo(fmt.Sprintf("a - b - %d", i))}})
	}
	return multiturn.Scenario{Turns: append(turns, multiturn.Turn{Reply: "took the advice"})}
}

// With the feedback budget spent, Öge consults once and sends the advice
// as its own dim turn (D-A3), recorded as the automatic trigger with the
// cap used and as an AdviceSent by oge.
func TestHomeChatAutoConsultSendsAdvice(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, autoScenario(), adviceScript)
	s.composer()
	s.type_("change Add\r")
	s.expect("still failing after 3 tries; asking the advisor")
	s.expect("a Claim, not checked")
	s.expect("Inject a Clock.")
	s.expect("Öge → agent: An advisor suggests (a second opinion, not checked): ")
	s.expect("  took the advice")
	s.composerAfter("  took the advice")
	s.quitChat(f)
	started := chatRecords(t, f, run.RecConsultationStarted)
	if len(started) != 1 || started[0]["trigger"] != "feedback_budget" || started[0]["requested_by"] != "oge" || started[0]["auto_cap"] != float64(1) {
		t.Errorf("ConsultationStarted %v", started)
	}
	if sent := chatRecords(t, f, run.RecAdviceSent); len(sent) != 1 || sent[0]["by"] != "oge" || sent[0]["how"] != "verbatim" {
		t.Errorf("AdviceSent %v", sent)
	}
}

// With auto = "ask" the advice waits for the same key as /advise's: nothing
// reaches the agent until the user confirms.
func TestHomeChatAutoConsultAskWaitsForTheUser(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, autoScenario(), adviceScript, func(f *runFixture) { advisorAuto(t, f, "ask") })
	s.composer()
	s.type_("change Add\r")
	s.expect("Inject a Clock.")
	s.expect("send to agent")
	if got := chatRecords(t, f, run.RecAdviceSent); len(got) != 0 {
		t.Fatalf("sent before the user confirmed: %v", got)
	}
	s.type_("\r")
	s.expect("Öge → agent: An advisor suggests (a second opinion, not checked): ")
	s.expect("  took the advice")
	s.composerAfter("  took the advice")
	s.quitChat(f)
	if sent := chatRecords(t, f, run.RecAdviceSent); len(sent) != 1 || sent[0]["by"] != "oge" {
		t.Errorf("AdviceSent %v", sent)
	}
}

// An advisor that gives nothing sends nothing: the chain stops as it did.
func TestHomeChatAutoConsultEmptyStopsTheChain(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, autoScenario(), "true\n")
	s.composer()
	s.type_("change Add\r")
	s.expect("still failing · your call")
	s.composer()
	s.quitChat(f)
	if got := chatRecords(t, f, run.RecAdviceSent); len(got) != 0 {
		t.Errorf("AdviceSent %v", got)
	}
}

// What the user types while the advisor works survives the auto-send.
func TestHomeChatAutoConsultKeepsTheDraft(t *testing.T) {
	t.Parallel()
	s, f := startAdvisedSession(t, autoScenario(), "sleep 2\n"+adviceScript)
	s.composer()
	s.type_("change Add\r")
	s.expect("still failing after 3 tries; asking the advisor")
	s.type_("mydraftxyz")
	s.expect("Öge → agent: An advisor suggests (a second opinion, not checked): ")
	s.expect("  took the advice")
	s.composerAfter("  took the advice")
	if !strings.Contains(vtScreen(s.out.String()), "mydraftxyz") {
		t.Errorf("the draft was wiped by the auto-send:\n%s", vtScreen(s.out.String()))
	}
	s.type_("\x15") // clear the box, so quitting is Ctrl+C alone
	s.quitChat(f)
}

// When the cut after the advice fails too, the chain's one Consultation is
// spent: it stops with a plain "your call", never asking a second time.
func TestHomeChatAutoConsultSpentSaysYourCall(t *testing.T) {
	t.Parallel()
	sc := autoScenario()
	sc.Turns[len(sc.Turns)-1] = multiturn.Turn{Reply: "took the advice", Writes: []multiturn.File{addGo("a - b - 9")}}
	s, f := startAdvisedSession(t, sc, adviceScript)
	s.composer()
	s.type_("change Add\r")
	s.expect("  took the advice")
	s.expectAfter("  took the advice", "still failing after 3 tries; your call")
	s.composerAfter("still failing after 3 tries; your call")
	out := s.out.String()
	if n := strings.Count(out, "asking the advisor"); n != 1 {
		t.Errorf("asked the advisor %d times, want 1", n)
	}
	if strings.Contains(out, "still failing · your call") || strings.Contains(out, "automatic consultation is used") {
		t.Errorf("the spent chain printed the failed-consultation line:\n%s", out)
	}
	s.quitChat(f)
}
