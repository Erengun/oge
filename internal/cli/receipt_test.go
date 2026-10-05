package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
)

// receiptBlock is the Receipt in a Run's output: from its header to the
// Ledger line.
func receiptBlock(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "ÖGE RECEIPT")
	j := strings.Index(out, "\nLedger        head ")
	if i < 0 || j < i {
		t.Fatalf("no Receipt in:\n%s", out)
	}
	end := strings.IndexByte(out[j+1:], '\n')
	return out[i : j+1+end+1]
}

// The end screen is the Receipt, and oge receipt prints the same one from
// the Ledger later, as Markdown and as schema-1 JSON too (#63).
func TestReceiptEndScreenAndCommandAgree(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	end := receiptBlock(t, out)
	for _, want := range []string{
		"Result        ✓ Accepted\n", "Task          fix Add\n", "· 1 file changed · Oracle v0\n",
		"Checks        Check #1 passed · 1 of 1 Oracle tests attested passing\n",
		"Not covered   No independent tests (fast mode)", "Checks ran unsandboxed; network not blocked",
	} {
		if !strings.Contains(end, want) {
			t.Errorf("the end screen lacks %q:\n%s", want, end)
		}
	}
	if strings.Contains(end, "Öge found") {
		t.Errorf("Öge found with nothing to disagree with:\n%s", end)
	}

	code, got, errOut := f.run(t, "", "receipt")
	if code != ExitOK || got != end {
		t.Fatalf("oge receipt: exit %d\n%s\n--- the end screen's\n%s\nstderr:\n%s", code, got, end, errOut)
	}

	code, js, _ := f.run(t, "", "receipt", "--json")
	var doc struct {
		Schema     int
		Outcome    string
		LedgerHead string   `json:"ledger_head"`
		NotCovered []string `json:"not_covered"`
	}
	if err := json.Unmarshal([]byte(js), &doc); code != ExitOK || err != nil {
		t.Fatalf("--json: exit %d, %v\n%s", code, err, js)
	}
	_, head, _ := ledger.ReplayHead(f.onlyRun(t))
	if doc.Schema != 1 || doc.Outcome != "Accepted" || doc.LedgerHead != head || len(doc.NotCovered) == 0 {
		t.Errorf("--json: %+v (head %s)", doc, head)
	}

	code, md, _ := f.run(t, "", "receipt", "--md")
	if code != ExitOK || !strings.HasPrefix(md, "### Öge Receipt: ✓ Accepted\n") || !strings.Contains(md, "<details><summary>Full evidence</summary>") {
		t.Errorf("--md: exit %d\n%s", code, md)
	}

	// A delivery is part of the Receipt from then on.
	if code, out, errOut := f.run(t, "", "apply"); code != ExitOK {
		t.Fatalf("apply: exit %d\n%s\n%s", code, out, errOut)
	}
	if _, got, _ := f.run(t, "", "receipt"); !strings.Contains(got, "Delivered     applied to your working tree (1 file)\n") {
		t.Errorf("the Receipt lacks the Delivery:\n%s", got)
	}
}

// An agent that says the tests pass when they don't gets its words shown
// as a Claim, next to what Öge found.
func TestReceiptShowsTheClaimOnlyWhenTheEvidenceDisagrees(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	code, out, errOut := f.run(t, "echo 'All tests pass.'\n", "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	end := receiptBlock(t, out)
	for _, want := range []string{
		"Result        … Waiting for you: the bound-exhaustion Gate\n",
		`Agent claimed "All tests pass." (Claim, implement#1)`,
		"Öge found     Check #1 failed on its Candidate ",
		"Checks        Check #1 failed: 1 test failing (fx.TestAdd) · 0 of 1 Oracle tests attested passing\n",
	} {
		if !strings.Contains(end, want) {
			t.Errorf("the Receipt lacks %q:\n%s", want, end)
		}
	}
	if n := strings.Count(end, "All tests pass."); n != 1 {
		t.Errorf("the Claim appears %d times:\n%s", n, end)
	}
}

func TestReceiptCommandRefusals(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"receipt", "--md", "--json"}, "give --md or --json, not both"},
		{[]string{"receipt", "a", "b"}, "give at most one Run"},
		{[]string{"receipt", "--nope"}, "flag provided but not defined"},
		{[]string{"receipt"}, "no Run of this repository yet"},
		{[]string{"receipt", "20991231T000000"}, "no Run 20991231T000000"},
	} {
		code, out, errOut := f.run(t, "", c.args...)
		if code != ExitRefused || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, want %q\nstdout:\n%s\nstderr:\n%s", c.args, code, c.want, out, errOut)
		}
	}
	if code, out, _ := f.run(t, "", "receipt", "--help"); code != ExitOK || !strings.Contains(out, "oge receipt [<run>] [--md | --json]") {
		t.Errorf("--help: exit %d\n%s", code, out)
	}
}
