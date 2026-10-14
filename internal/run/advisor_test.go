package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent/fake"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
	"github.com/erengun/oge/internal/pipeline"
)

// advisorScript writes a fake advisor's script.
func advisorScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "advisor.sh")
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// withAdvisor makes the fake running script c's advisor, under the
// implementer's agent name: only the Chat's own agent advises. The
// implementer's Session keeps its own adapter.
func withAdvisor(c *Chat, script string) *recorder {
	r := &recorder{Adapter: &fake.Adapter{Script: script}}
	c.agents["claude"] = r
	return r
}

func blob(t *testing.T, c *Chat, rec map[string]any, key string) string {
	t.Helper()
	id, _ := rec[key].(string)
	b, err := c.blobs.Get(id)
	if err != nil {
		t.Fatalf("%s blob %q: %v", key, id, err)
	}
	return string(b)
}

// A Consultation reads a disposable clone of the newest settled turn's
// commit: what the advisor writes there never reaches the Chat
// Workspace, and the clone is gone after. Its Briefing is the question,
// the Task, the diff and the implementer's last reply labelled a Claim,
// and the Chat log records it ahead and its advice after.
func TestConsultReadsADisposableClone(t *testing.T) {
	c := openChat(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "I made Add negate-safe.", Writes: []multiturn.File{{Path: "add.go", Content: "package fx\n\nfunc Add() {}\n"}}},
	}}, 0)
	drain(t, send(t, c, "make Add safe"), nil)
	r := withAdvisor(c, advisorScript(t, "cat add.go\necho planted > planted.go\necho > add.go\necho \"role $OGE_FAKE_ROLE\"\necho 'Inject a Clock.'\n"))
	adv, err := c.Consult(t.Context(), "is there a cleaner seam?")
	if err != nil {
		t.Fatal(err)
	}
	// It read turn 1's add.go in its clone, then wrote over it there.
	if adv.Text != "package fx\n\nfunc Add() {}\nrole advisor\nInject a Clock." || adv.Turn != 1 || adv.Effort != "high" || adv.Agent != "claude" {
		t.Errorf("advice %+v", adv)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "planted.go")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the advisor's write reached the Chat Workspace: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(c.Workspace, "add.go")); string(b) != "package fx\n\nfunc Add() {}\n" {
		t.Errorf("the Chat Workspace's add.go is %q", b)
	}
	if _, err := os.Stat(filepath.Join(c.work, "advice", adv.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the clone outlived the Consultation: %v", err)
	}
	r.mu.Lock()
	sent := strings.Join(r.sent, "\n")
	r.mu.Unlock()
	for _, want := range []string{"Question:\nis there a cleaner seam?", "make Add safe", "+func Add() {}",
		"last reply (turn 1), verbatim. It is the agent's own claim, not checked:\n<<<\nI made Add negate-safe.\n"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the Briefing lacks %q:\n%s", want, sent)
		}
	}

	started := recordsOf(t, c, RecConsultationStarted)
	ended := recordsOf(t, c, RecConsultationEnded)
	if len(started) != 1 || len(ended) != 1 {
		t.Fatalf("records: %d started, %d ended", len(started), len(ended))
	}
	s, e := started[0], ended[0]
	if s["consultation"] != adv.ID || s["trigger"] != "user" || s["requested_by"] != "user" || s["read_only"] != ReadOnlyDiscard ||
		s["turn"] != float64(1) || s["question"] != sha("is there a cleaner seam?") {
		t.Errorf("ConsultationStarted %v", s)
	}
	if !reflect.DeepEqual(s["advisor"], map[string]any{"agent": "claude", "model": "", "role": "advisor", "network": "off", "effort": "high"}) {
		t.Errorf("advisor identity %v", s["advisor"])
	}
	if got := blob(t, c, s, "briefing"); got != sent {
		t.Errorf("the Briefing blob isn't what was sent:\n%s", got)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(blob(t, c, s, "manifest")), &m); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	reply := c.lastReply
	c.mu.Unlock()
	var kinds []string
	for _, it := range m["items"].([]any) {
		item := it.(map[string]any)
		kinds = append(kinds, item["item"].(string))
		if item["item"] == "reply" && (item["label"] != "claim" || reply == "" || item["sha256"] != sha(reply)) {
			t.Errorf("the reply's manifest item %v", item)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"question", "task", "diff", "reply", "workspace"}) {
		t.Errorf("manifest items %v", kinds)
	}
	if e["outcome"] != ConsultAdvised || blob(t, c, e, "advice") != adv.Text {
		t.Errorf("ConsultationEnded %v", e)
	}
}

// Cancelling a Consultation ends its Session and records it cancelled.
func TestConsultCancelled(t *testing.T) {
	c := openChat(t, multiturn.Scenario{}, 0)
	withAdvisor(c, advisorScript(t, "sleep 30\n"))
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.Consult(ctx, "anything?"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Consult: %v", err)
	}
	ended := recordsOf(t, c, RecConsultationEnded)
	if len(ended) != 1 || ended[0]["outcome"] != ConsultCancelled {
		t.Errorf("ConsultationEnded %v", ended)
	}
}

// A planted held-out test that fails a cut never reaches the advisor:
// its name, file, assertion and logged text are absent from the
// Briefing, its manifest and the advice, which carry only the count
// (ADR-0009, D-A2), end to end.
func TestConsultWithholdsAPlantedHeldOutTest(t *testing.T) {
	const heldOut = "package fx\n\nimport \"testing\"\n\n// AC-1: negatives.\nfunc TestZebraQuokkaPlanted(t *testing.T) {\n\tt.Log(\"quokka-logged-marker\")\n\tif Add(-2, 1) != -1 {\n\t\tt.Fatal(\"zebra-assertion-marker\")\n\t}\n}\n"
	script := filepath.Join(t.TempDir(), "qa.sh")
	if err := os.WriteFile(script, []byte("cat > /dev/null\nprintf '%s' '"+heldOut+"' > zq_test.go\necho 'exit: extended'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	impl := fakeClaude(t, multiturn.Scenario{Turns: []multiturn.Turn{
		{Reply: "guarded", Writes: []multiturn.File{{Path: "add.go", Content: "package fx\n\nfunc Add(a, b int) int {\n\tif a < 0 {\n\t\treturn 0\n\t}\n\treturn a + b\n}\n"}}},
	}})
	resolve := func(fast bool) *pipeline.Frozen {
		f, probs := pipeline.Resolve(nil, pipeline.Overrides{Fast: fast, Checks: []string{"go test -json ./..."},
			Tests: []string{"**/*_test.go"}, Outputs: []string{"**/*.go"}}, []string{"claude"})
		if len(probs) > 0 {
			t.Fatalf("resolve: %v", probs)
		}
		return f
	}
	fast, standard := resolve(true), resolve(false)
	for i := range standard.Stages {
		if standard.Stages[i].Role == "verifier" {
			standard.Stages[i].Agent = fake.Name // QA plants the held-out test
		}
	}
	c := openChatIn(t.Context(), t, impl, 0, fast, map[string]string{"go.mod": cutMod, "add.go": cutAdd, "add_test.go": cutAddTest})
	c.agents["fake"] = &fake.Adapter{Script: script}
	drain(t, send(t, c, "guard Add"), nil)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	res, err := c.Cut(ctx, CutParams{Frozen: standard})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Failed {
		t.Fatalf("outcome %s %v", res.Outcome, res.Why)
	}
	// The advisor lists its clone, prints its test files and echoes its
	// Briefing back, all as its advice.
	r := withAdvisor(c, advisorScript(t, "ls -aR .\ncat *_test.go\nprintf '%s\\n' \"$OGE_FAKE_TURN\"\n"))
	adv, err := c.Consult(t.Context(), "why does it fail?")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	sent := strings.Join(r.sent, "\n")
	r.mu.Unlock()
	s := recordsOf(t, c, RecConsultationStarted)[0]
	e := recordsOf(t, c, RecConsultationEnded)[0]
	if !strings.Contains(adv.Text, "add_test.go") || !strings.Contains(adv.Text, "func TestAdd(") {
		t.Errorf("the advisor didn't list its clone:\n%s", adv.Text)
	}
	if !strings.Contains(sent, "1 failing") || !strings.Contains(sent, "1 held-out test failed") {
		t.Errorf("the Briefing lacks the newest Verdict's count:\n%s", sent)
	}
	for _, where := range map[string]string{"sent": sent, "advice": adv.Text, "briefing blob": blob(t, c, s, "briefing"),
		"manifest": blob(t, c, s, "manifest"), "advice blob": blob(t, c, e, "advice")} {
		for _, never := range []string{"ZebraQuokka", "zq_test.go", "quokka-logged-marker", "zebra-assertion-marker"} {
			if strings.Contains(where, never) {
				t.Errorf("held-out %q reached the advisor:\n%s", never, where)
			}
		}
	}
}

// Only the Chat's own agent advises until #292: [advisor] naming
// another is refused before anything opens.
func TestConsultRefusesAnotherAgent(t *testing.T) {
	c := openChat(t, multiturn.Scenario{}, 0)
	r := &recorder{Adapter: &fake.Adapter{Script: advisorScript(t, "echo advice\n")}}
	c.agents["codex"] = r
	c.advisor = pipeline.AdvisorConfig{Agent: "codex"}
	if _, err := c.Consult(t.Context(), "anything?"); !errors.Is(err, ErrAdvisorAgent) || !strings.Contains(err.Error(), "#292") {
		t.Fatalf("Consult: %v", err)
	}
	if len(r.sent) > 0 || len(recordsOf(t, c, RecConsultationStarted)) > 0 {
		t.Error("a refused Consultation opened a Session or was recorded")
	}
}

// An advice turn is Öge's, outside the Task and D5's feedback budget: it
// works when the budget is spent, neither counts against it nor resets
// it, carries its Consultation id, and leaves an AdviceSent.
func TestSendAdviceIsOutsideTheTaskAndTheBudget(t *testing.T) {
	var turns []multiturn.Turn
	for range FeedbackBudget + 3 {
		turns = append(turns, multiturn.Turn{Reply: "ok"})
	}
	c := openCut(t, "", turns...)
	drain(t, send(t, c, "yo"), nil)
	for range FeedbackBudget {
		ch, err := c.SendFeedback(t.Context(), "Öge's Check failed")
		if err != nil {
			t.Fatal(err)
		}
		drain(t, ch, nil)
	}
	ch, err := c.SendAdvice(t.Context(), "cons-1", "Inject a Clock.")
	if err != nil {
		t.Fatalf("SendAdvice with the budget spent: %v", err)
	}
	drain(t, ch, nil)
	if _, err := c.SendFeedback(t.Context(), "Öge's Check failed"); !errors.Is(err, ErrFeedbackBudget) {
		t.Errorf("an advice turn reset the budget: %v", err)
	}
	c.mu.Lock()
	n := c.turns
	adviceText := c.msgs[n-1]
	isOge := c.oge[n]
	feedbacks := c.feedbacks
	c.mu.Unlock()
	if !isOge || !strings.HasPrefix(adviceText, AdvicePrefix) || feedbacks != FeedbackBudget {
		t.Errorf("oge=%v text=%q feedbacks=%d", isOge, adviceText, feedbacks)
	}
	for _, m := range c.userMsgs(n) {
		if strings.Contains(m, "Inject a Clock") {
			t.Errorf("the advice is in the Task: %q", m)
		}
	}
	var started map[string]any
	for _, r := range recordsOf(t, c, RecTurnStarted) {
		if r["consultation"] == "cons-1" {
			started = r
		}
	}
	if started == nil || started["by"] != "oge" {
		t.Errorf("TurnStarted %v", started)
	}
	sent := recordsOf(t, c, RecAdviceSent)
	if len(sent) != 1 || sent[0]["consultation"] != "cons-1" || sent[0]["how"] != "verbatim" || sent[0]["by"] != "oge" || sent[0]["turn"] != float64(n) {
		t.Errorf("AdviceSent %v", sent)
	}
	// What the user sends from the advice is theirs: in the Task, and a
	// new chain.
	ch, err = c.SendEdited(t.Context(), "cons-1", "Inject a Clock, in sched.")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch, nil)
	c.mu.Lock()
	edited := c.turns
	c.mu.Unlock()
	if m := c.userMsgs(edited); m[len(m)-1] != "Inject a Clock, in sched." {
		t.Errorf("the edited advice isn't the user's turn: %q", m)
	}
	if sent := recordsOf(t, c, RecAdviceSent); len(sent) != 2 || sent[1]["how"] != "edited" || sent[1]["by"] != "user" {
		t.Errorf("AdviceSent %v", sent)
	}
	if _, err := c.SendFeedback(t.Context(), "Öge's Check failed"); err != nil {
		t.Errorf("after the user's edited turn: %v", err)
	}
}
