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

// withAdvisor makes the fake running script c's advisor.
func withAdvisor(c *Chat, script string) *recorder {
	r := &recorder{Adapter: &fake.Adapter{Script: script}}
	c.agents["adv"] = r
	c.advisor = pipeline.AdvisorConfig{Agent: "adv"}
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
	r := withAdvisor(c, advisorScript(t, "echo planted > planted.go\necho > add.go\necho \"role $OGE_FAKE_ROLE\"\necho 'Inject a Clock.'\n"))
	adv, err := c.Consult(t.Context(), "is there a cleaner seam?")
	if err != nil {
		t.Fatal(err)
	}
	if adv.Text != "role advisor\nInject a Clock." || adv.Turn != 1 || adv.Effort != "high" || adv.Agent != "adv" {
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
	if !reflect.DeepEqual(s["advisor"], map[string]any{"agent": "adv", "model": "", "role": "advisor", "network": "off", "effort": "high"}) {
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
	// The advisor echoes its Briefing back as its advice.
	r := withAdvisor(c, advisorScript(t, "printf '%s\\n' \"$OGE_FAKE_TURN\"\n"))
	adv, err := c.Consult(t.Context(), "why does it fail?")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	sent := strings.Join(r.sent, "\n")
	r.mu.Unlock()
	s := recordsOf(t, c, RecConsultationStarted)[0]
	e := recordsOf(t, c, RecConsultationEnded)[0]
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
