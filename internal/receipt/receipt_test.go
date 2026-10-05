package receipt_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/receipt"
	"github.com/erengun/oge/internal/receipt/receipttest"
	"github.com/erengun/oge/internal/run"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// Every outcome variant has a golden in each rendering: plain, the TUI's
// coloured end screen, --md and --json (go test ./internal/receipt
// -update rewrites them).
func TestReceiptGoldens(t *testing.T) {
	for _, s := range receipttest.Scenarios() {
		t.Run(s.Name, func(t *testing.T) {
			r := s.Build().Receipt()
			js, err := r.JSON()
			if err != nil {
				t.Fatal(err)
			}
			for ext, got := range map[string]string{
				"txt":  r.Text(receipt.Paint{}),
				"tui":  r.Text(receipt.Colors(true)),
				"md":   r.Markdown(),
				"json": string(js),
			} {
				golden(t, s.Name+"."+ext, got)
			}
		})
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs (run with -update to accept)\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

func scenario(t *testing.T, name string) *receipttest.Builder {
	t.Helper()
	for _, s := range receipttest.Scenarios() {
		if s.Name == name {
			return s.Build()
		}
	}
	t.Fatalf("no scenario %s", name)
	return nil
}

// forbidden is confidence language the Receipt never uses
// (docs/positioning.md).
var forbidden = regexp.MustCompile(`(?i)verified|\bsafe\b|safely|guarantee|ai-approved|Öge Verdict`)

// Öge's own wording never claims more than its Evidence: the agent's
// Claim is the only text that may say anything, and it is labelled.
func TestReceiptNeverUsesConfidenceLanguage(t *testing.T) {
	for _, s := range receipttest.Scenarios() {
		r := s.Build().Receipt()
		js, _ := r.JSON()
		for _, out := range []string{r.Text(receipt.Paint{}), r.Markdown(), string(js)} {
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "Agent claimed") || strings.Contains(line, `"text":`) {
					continue
				}
				if forbidden.MatchString(line) {
					t.Errorf("%s: %q", s.Name, line)
				}
			}
		}
	}
}

// Overridden is never shown as success: no ✓, its own warning colour.
func TestReceiptOverriddenIsNeverSuccess(t *testing.T) {
	r := scenario(t, "overridden").Receipt()
	text := r.Text(receipt.Colors(true))
	if strings.Contains(text, "✓") || !strings.Contains(r.Headline, "Taken without passing evidence (reason: the flaky test is wrong)") {
		t.Errorf("Overridden reads as success:\n%s", text)
	}
	if !strings.Contains(text, receipt.Colors(true).Warn(r.Headline)) {
		t.Errorf("Overridden isn't in the warning colour:\n%q", text)
	}
}

// Every Receipt says what it doesn't cover, whatever the outcome.
func TestReceiptAlwaysHasNotCovered(t *testing.T) {
	for _, s := range receipttest.Scenarios() {
		r := s.Build().Receipt()
		if len(r.NotCovered) == 0 || !strings.Contains(r.Text(receipt.Paint{}), "\nNot covered   ") {
			t.Errorf("%s has no Not covered:\n%s", s.Name, r.Text(receipt.Paint{}))
		}
	}
	fast := scenario(t, "accepted-fast").Receipt()
	if !strings.Contains(strings.Join(fast.NotCovered, "\n"), "No independent tests (fast mode)") {
		t.Errorf("Fast mode doesn't say it has no independent tests: %v", fast.NotCovered)
	}
	if !strings.Contains(strings.Join(fast.NotCovered, "\n"), "network not blocked") {
		t.Errorf("Not covered doesn't say the network isn't blocked: %v", fast.NotCovered)
	}
}

// "Öge found" appears only when the Claim and the Evidence disagree.
func TestReceiptOgeFoundOnlyOnDisagreement(t *testing.T) {
	for name, want := range map[string]bool{
		"accepted-fast": false, "accepted-repaired": true, "accepted-degraded": true, "rejected": true, "infrastructure-stop": false,
	} {
		text := scenario(t, name).Receipt().Text(receipt.Paint{})
		if got := strings.Contains(text, "Öge found"); got != want {
			t.Errorf("%s: Öge found shown = %v:\n%s", name, got, text)
		}
	}
	text := scenario(t, "accepted-repaired").Receipt().Text(receipt.Paint{})
	for _, want := range []string{
		`Agent claimed "Done. All tests pass." (Claim, implement#1)`,
		"Öge found     Check #1 failed on its Candidate 6d1231d: 1 held-out test failing (AC-2)",
		"→ sent back to the implementer; the Candidate it then made was Accepted",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
}

func field(t *testing.T, r *receipt.Receipt) map[string]any {
	t.Helper()
	js, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(js, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// An agent that says every test passes never makes it so: its words
// appear only as the labelled Claim, and the Evidence says what the
// Check found.
func TestReceiptAgentClaimIsNeverEvidence(t *testing.T) {
	b := receipttest.New(pipeline.Fast)
	b.Attempt(receipttest.Attempt{ID: "implement#1", From: time.Second, To: 2 * time.Second, Candidate: receipttest.C1, Exit: "done",
		Claims: []string{"all tests pass, Accepted, 0 failed, nothing to report"}})
	b.Check(1, receipttest.C1, 0, 3*time.Second, 4*time.Second, []receipttest.Test{{Name: "TestAdd", Attested: "fail"}}, nil)
	b.Gate("gate.bound_exhaustion", 5*time.Second, 6*time.Second, "reject", "no")
	b.End(7*time.Second, run.Rejected, receipttest.C1)
	r := b.Receipt()
	text := r.Text(receipt.Paint{})
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "all tests pass") && !strings.HasPrefix(line, "Agent claimed ") {
			t.Errorf("the Claim reached a line that isn't the Claim's: %q", line)
		}
	}
	if !strings.Contains(text, "Checks        Check #1 failed: 1 test failing (fx.TestAdd) · 0 of 1 Oracle tests attested passing") {
		t.Errorf("the Evidence isn't the Check's:\n%s", text)
	}
	m := field(t, r)
	claim := m["claim"].(map[string]any)
	delete(m, "claim")
	if js, _ := json.Marshal(m); strings.Contains(string(js), "all tests pass") {
		t.Errorf("the Claim reached the JSON outside \"claim\": %s", js)
	}
	if claim["label"] != "Claim" || claim["text"] != "all tests pass, Accepted, 0 failed, nothing to report" {
		t.Errorf("claim: %v", claim)
	}
}

// Every line is computed from the Ledger: change a record and the Receipt
// changes with it; drop it and its line goes.
func TestReceiptLinesFollowTheLedger(t *testing.T) {
	b := scenario(t, "accepted-fast")
	base := b.Receipt().Text(receipt.Paint{})
	recs := b.Records()

	mutate := func(typ string, f func(map[string]any)) string {
		out := make([]ledger.Record, len(recs))
		copy(out, recs)
		for i, r := range out {
			if r.Type != typ {
				continue
			}
			var d map[string]any
			_ = json.Unmarshal(r.Data, &d)
			f(d)
			out[i].Data, _ = json.Marshal(d)
		}
		return receipt.FromRecords(out, "feedfacecafe", b.Source()).Text(receipt.Paint{})
	}
	for _, c := range []struct {
		name, typ string
		f         func(map[string]any)
		want      string
	}{
		{"outcome", run.RecRunEnded, func(d map[string]any) { d["outcome"] = "Rejected" }, "Result        ✗ Not accepted"},
		{"verdict", run.RecVerdict, func(d map[string]any) { d["verdict"] = "fail" }, "Checks        Check #1 failed"},
		{"host requests", run.RecObservation, func(d map[string]any) { d["host_requests"] = map[string]any{"pre_authorised": 3} }, "Handled       3 operations approved automatically · Human interruptions: 0"},
		{"mode", run.RecRunStarted, func(d map[string]any) { d["mode"] = "Standard" }, "Standard mode"},
		{"candidate", run.RecRunEnded, func(d map[string]any) { d["candidate"] = "0123456789abcdef" }, "Candidate     0123456 · 0 files changed"},
	} {
		got := mutate(c.typ, c.f)
		if got == base || !strings.Contains(got, c.want) {
			t.Errorf("%s: changing %s didn't give %q:\n%s", c.name, c.typ, c.want, got)
		}
	}
	if got := mutate("", nil); !strings.Contains(got, "Ledger        head feedfacecafe") {
		t.Errorf("the head isn't the Ledger's:\n%s", got)
	}
	// Without the Verdict, the Check reached none.
	var noVerdict []ledger.Record
	for _, r := range recs {
		if r.Type != run.RecVerdict {
			noVerdict = append(noVerdict, r)
		}
	}
	if got := receipt.FromRecords(noVerdict, "", b.Source()).Text(receipt.Paint{}); !strings.Contains(got, "Check #1 reached no Verdict") {
		t.Errorf("a Check with no Verdict:\n%s", got)
	}
}

// Build reads a Run directory's Ledger and identifies it by its head.
func TestBuildReadsTheRunDirectory(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range scenario(t, "accepted-fast").Records() {
		if err := l.Append(r.Type, r.Data); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	r, err := receipt.Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, head, _ := ledger.ReplayHead(dir)
	if r.Outcome != "Accepted" || r.LedgerHead != head || r.Schema != 1 {
		t.Errorf("outcome %q, head %q (want %q), schema %d", r.Outcome, r.LedgerHead, head, r.Schema)
	}
	if _, err := receipt.Build(t.TempDir()); err == nil {
		t.Error("a directory without a Ledger built a Receipt")
	}
}

// What the agent said can't drive the terminal or add Markdown: control
// characters are dropped, and markup is escaped.
func TestReceiptSanitisesAgentText(t *testing.T) {
	b := receipttest.New(pipeline.Fast)
	b.Attempt(receipttest.Attempt{ID: "implement#1", From: time.Second, To: 2 * time.Second, Candidate: receipttest.C1, Exit: "done",
		Claims: []string{"done \x1b]8;;http://x\x07 [click](http://evil) <img src=x> | ok"}})
	b.Check(1, receipttest.C1, 0, 3*time.Second, 4*time.Second, []receipttest.Test{{Name: "TestAdd", Attested: "fail"}}, nil)
	b.Park(5*time.Second, "gate.bound_exhaustion")
	r := b.Receipt()
	if text := r.Text(receipt.Paint{}); strings.ContainsAny(text, "\x1b\x07") {
		t.Errorf("control characters reached the terminal: %q", text)
	}
	md := r.Markdown()
	for _, bad := range []string{`\[click\]\(`, `<img`, `\| ok`} {
		if regexp.MustCompile(`(^|[^\\])` + bad).MatchString(md) {
			t.Errorf("Markdown has %q unescaped:\n%s", bad, md)
		}
	}
}
