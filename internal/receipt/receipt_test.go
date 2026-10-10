package receipt_test

import (
	"encoding/json"
	"flag"
	"fmt"
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
	"github.com/erengun/oge/internal/workspace"
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
	if strings.Contains(text, "✓") || !strings.Contains(unmarked(r.Headline), "Taken without passing evidence (reason: the flaky test is wrong)") {
		t.Errorf("Overridden reads as success:\n%s", text)
	}
	if !strings.Contains(text, receipt.Colors(true).Warn(unmarked(r.Headline))) {
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
// characters are dropped, and in Markdown every value is a code span, so
// nothing in it can link, mention, reference an issue or add markup.
func TestReceiptSanitisesAgentText(t *testing.T) {
	const bad = "done \x1b]8;;http://x\x07 [click](http://evil) <img src=x> | ok @Erengun https://evil.example/x www.evil.com #12 `tick` ``two``"
	b := receipttest.New(pipeline.Fast)
	b.Attempt(receipttest.Attempt{ID: "implement#1", From: time.Second, To: 2 * time.Second, Candidate: receipttest.C1, Exit: "done",
		Claims: []string{bad}, Changed: []string{"@Erengun/www.evil.com.go"}})
	b.Check(1, receipttest.C1, 0, 3*time.Second, 4*time.Second, []receipttest.Test{{Name: "TestA_@Erengun_#12_https://evil.example/x", Attested: "fail"}}, nil)
	b.Park(5*time.Second, "gate.bound_exhaustion", "why @Erengun #12")
	r := b.Receipt()
	if text := r.Text(receipt.Paint{}); strings.ContainsAny(text, "\x1b\x07\x0e\x0f") {
		t.Errorf("control characters reached the terminal: %q", text)
	}
	md := r.Markdown()
	outside := withoutCodeSpans(md)
	for _, danger := range []string{"@Erengun", "https://", "www.", "#12", "[click]", "<img", "http://evil", "tick"} {
		if strings.Contains(outside, danger) {
			t.Errorf("Markdown has %q outside a code span:\n%s\n--- outside code spans:\n%s", danger, md, outside)
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "|") && strings.Count(strings.ReplaceAll(line, `\|`, ""), "|") != 3 {
			t.Errorf("a value broke the table: %q", line)
		}
	}
	if !strings.Contains(md, "@Erengun") {
		t.Errorf("the Claim is gone:\n%s", md)
	}
}

// withoutCodeSpans is md with every code span removed (CommonMark: a run
// of n backticks closes at the next run of exactly n).
func withoutCodeSpans(md string) string {
	var b strings.Builder
	for i := 0; i < len(md); {
		if md[i] != '`' {
			b.WriteByte(md[i])
			i++
			continue
		}
		n := 0
		for i+n < len(md) && md[i+n] == '`' {
			n++
		}
		fence := strings.Repeat("`", n)
		j := i + n
		closed := -1
		for k := j; k < len(md); {
			if md[k] != '`' {
				k++
				continue
			}
			m := 0
			for k+m < len(md) && md[k+m] == '`' {
				m++
			}
			if m == n {
				closed = k
				break
			}
			k += m
		}
		if closed < 0 {
			b.WriteString(fence)
			i = j
			continue
		}
		i = closed + n
	}
	return b.String()
}

// Every character that makes text read other than it is shows escaped in
// each rendering, never as itself.
func TestReceiptEscapesBidiAndInvisibleRunes(t *testing.T) {
	for _, c := range []struct {
		name string
		r    rune
	}{
		{"embedding", 0x202a}, {"override", 0x202e}, {"isolate", 0x2066}, {"pop isolate", 0x2069},
		{"LRM", 0x200e}, {"RLM", 0x200f}, {"ALM", 0x061c}, {"zero-width space", 0x200b}, {"ZWJ", 0x200d},
		{"line separator", 0x2028}, {"paragraph separator", 0x2029}, {"BOM", 0xfeff},
	} {
		t.Run(c.name, func(t *testing.T) {
			hidden := "a" + string(c.r) + "b"
			b := receipttest.New(pipeline.Fast)
			b.Attempt(receipttest.Attempt{ID: "implement#1", From: time.Second, To: 2 * time.Second, Candidate: receipttest.C1, Exit: "done",
				Claims: []string{hidden}, Reverted: []workspace.Revert{{Path: "x" + hidden + "_test.go", Change: "modified", Class: run.ClassOracleTest, Tamper: true}}})
			b.Check(1, receipttest.C1, 0, 3*time.Second, 4*time.Second, []receipttest.Test{{Name: "Test" + hidden, Attested: "fail"}}, nil)
			b.End(5*time.Second, run.Rejected, receipttest.C1, "why "+hidden)
			r := b.Receipt()
			js, _ := r.JSON()
			want := fmt.Sprintf("<U+%04X>", c.r)
			for name, out := range map[string]string{"text": r.Text(receipt.Paint{}), "md": r.Markdown(), "json": string(js)} {
				if strings.ContainsRune(out, c.r) || !strings.Contains(out, "a"+want+"b") {
					t.Errorf("%s: %q isn't escaped:\n%s", name, c.r, out)
				}
			}
		})
	}
}

// unmarked is a model string without its value marks.
func unmarked(s string) string { return strings.NewReplacer("\x0e", "", "\x0f", "").Replace(s) }

// A torn last line (a crash mid-write) leaves the records before it: the
// Receipt is of those, and says where the Ledger stopped. A broken chain
// is refused.
func TestBuildFromATornLedger(t *testing.T) {
	dir := t.TempDir()
	if err := scenario(t, "accepted-fast").WriteRun(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ledger.LedgerFile)
	good, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(append([]byte(nil), good...), `{"format":1,"seq":`...), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := receipt.Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != "Accepted" || !strings.Contains(r.Text(receipt.Paint{}), "the Ledger is unreadable after record ") {
		t.Errorf("torn tail:\n%s", r.Text(receipt.Paint{}))
	}
	broken := strings.Replace(string(good), `"seq":2,`, `"seq":7,`, 1)
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := receipt.Build(dir); err == nil {
		t.Error("a broken chain built a Receipt")
	}
}

// The JSON drops only Öge's own value marks: a value holding the text
// "\u000e" keeps it.
func TestReceiptJSONKeepsEscapedText(t *testing.T) {
	b := receipttest.New(pipeline.Fast)
	b.Attempt(receipttest.Attempt{ID: "implement#1", From: time.Second, To: 2 * time.Second, Candidate: receipttest.C1, Exit: "done", Claims: []string{`say \u000e and \u000f`}})
	b.Check(1, receipttest.C1, 0, 3*time.Second, 4*time.Second, []receipttest.Test{{Name: "TestAdd", Attested: "fail"}}, nil)
	b.End(5*time.Second, run.Rejected, receipttest.C1, `why \u000e`)
	m := field(t, b.Receipt())
	if got := m["claim"].(map[string]any)["text"]; got != `say \u000e and \u000f` {
		t.Errorf("claim text %q", got)
	}
	if got := m["why"].([]any)[0]; got != `why \u000e` {
		t.Errorf("why %q", got)
	}
}

// The count is the funcs in new test files that match the test globs plus
// the funcs added to existing Oracle test files (#119); the last
// ScopeObserved record of the Attempt behind the Candidate is the one
// that counts.
func TestImplementerTestsCount(t *testing.T) {
	r := scenario(t, "accepted-implementer-tests").Receipt()
	if r.ImplementerTests != 2 { // TestExtraA, TestExtraB; not helper, not Testing
		t.Errorf("new file: %d", r.ImplementerTests)
	}
	if r := scenario(t, "accepted-kept-additions").Receipt(); r.ImplementerTests != 2 || len(r.Protected.Kept) != 1 {
		t.Errorf("kept: %d, %+v", r.ImplementerTests, r.Protected.Kept)
	}
	if r := scenario(t, "accepted-implementer-tests-both").Receipt(); r.ImplementerTests != 2 {
		t.Errorf("both: %d", r.ImplementerTests)
	}
	if r := scenario(t, "accepted-fast").Receipt(); r.ImplementerTests != 0 || strings.Contains(r.Text(receipt.Paint{}), "implementer-authored") {
		t.Errorf("none: %d", r.ImplementerTests)
	}

	// A late record drops a kept file written after the comparison.
	b := scenario(t, "accepted-kept-additions")
	recs := b.Records()
	var late []ledger.Record
	for _, rec := range recs {
		late = append(late, rec)
		if rec.Type == run.RecScopeObserved {
			d, _ := json.Marshal(map[string]any{"attempt": "implement#1", "role": "implementer", "state": "late", "reverted": []any{}, "kept": []any{}, "tamper": 0, "enforcement": workspace.RevertOnly})
			late = append(late, ledger.Record{Format: rec.Format, Seq: rec.Seq, At: rec.At, Type: run.RecScopeObserved, Data: d})
		}
	}
	if r := receipt.FromRecords(late, "", b.Source()); r.ImplementerTests != 0 || len(r.Protected.Kept) != 0 {
		t.Errorf("late: %d, %+v", r.ImplementerTests, r.Protected.Kept)
	}

	got := scenario(t, "accepted-kept-additions").Receipt().Text(receipt.Paint{})
	if !strings.Contains(got, "no test changes reverted · 2 test additions kept in add_test.go") || strings.Contains(got, "no test changes kept") {
		t.Errorf("Protected line:\n%s", got)
	}
}
