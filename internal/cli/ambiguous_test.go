package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// The Ambiguous-file review (#97): new files no output or test glob
// covers are promoted or dropped, in one batch review at the end of the
// Run, before it can be Accepted (ADR-0013, ADR-0019 #2).

// strayScript fixes Add and leaves two new files no glob covers.
const strayScript = `mkdir -p docs tmp
printf '# debug notes\n' > docs/debug.md
printf '{"ok":true}\n' > tmp/result.json
` + fixScript

// ambiguousResolutions are the Run's Ambiguous-file decisions, as
// "choice files → resulting Candidate's first 7".
func ambiguousResolutions(t *testing.T, runDir string) []string {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		if r.Type != run.RecAmbiguousResolved {
			continue
		}
		var d struct {
			Choice    string   `json:"choice"`
			Files     []string `json:"files"`
			From      string   `json:"from"`
			Candidate string   `json:"candidate"`
		}
		if err := json.Unmarshal(r.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.From == "" || d.Candidate == "" || d.From == d.Candidate {
			t.Errorf("a resolution doesn't name a new Candidate: %s", r.Data)
		}
		out = append(out, d.Choice+" "+strings.Join(d.Files, ","))
	}
	return out
}

// reviewObservations are the Run's ambiguous_review Observations' counts.
func reviewObservations(t *testing.T, runDir string) []int {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, r := range recs {
		if r.Type != run.RecObservation {
			continue
		}
		var d struct {
			Kind  string `json:"kind"`
			Count int    `json:"count"`
		}
		if err := json.Unmarshal(r.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Kind == "ambiguous_review" {
			out = append(out, d.Count)
		}
	}
	return out
}

func TestAmbiguousUnattendedParks(t *testing.T) {
	f := newRunFixture(t)
	code, out, errOut := f.run(t, strayScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitParked {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"PARKED     at the Ambiguous-file Gate",
		"2 new files need a decision: docs/debug.md, tmp/result.json",
		"Unattended Runs never decide a Gate; a human must (exit 10).",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ACCEPTED") {
		t.Errorf("an unresolved Run was Accepted:\n%s", out)
	}
	dir := f.onlyRun(t)
	if got := strings.Join(gateRecords(t, dir), "|"); got != "GateOpened gate.ambiguous_file|RunParked" {
		t.Errorf("records: %s", got)
	}
	if got := reviewObservations(t, dir); len(got) != 1 || got[0] != 2 {
		t.Errorf("ambiguous_review Observations: %v", got)
	}
	var opened struct {
		Pins struct {
			Files []string `json:"files"`
		} `json:"pins"`
	}
	recordData(t, dir, run.RecGateOpened, &opened)
	if strings.Join(opened.Pins.Files, ",") != "docs/debug.md,tmp/result.json" {
		t.Errorf("GateOpened pins %+v", opened)
	}
	f.assertUntouched(t)
}

// Globs that cover every new file: no review, no Observation.
func TestAmbiguousNoReviewWhenTheGlobsCoverEverything(t *testing.T) {
	f := newRunFixture(t)
	code, out, errOut := f.run(t, strayScript, "fix Add", "--fast", "--agent", "fake", "--unattended",
		"--output", "docs/**", "--output", "tmp/*.json")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if strings.Contains(out, "Ambiguous") || strings.Contains(out, "ATTENTION") {
		t.Errorf("a review showed:\n%s", out)
	}
	dir := f.onlyRun(t)
	if got := reviewObservations(t, dir); len(got) != 0 {
		t.Errorf("ambiguous_review Observations: %v", got)
	}
	if got := gateRecords(t, dir); strings.Join(got, "|") != "RunEnded Accepted" {
		t.Errorf("records: %v", got)
	}
}

// Drop all, then the final Check, then Accepted; oge apply never delivers
// a dropped file.
func TestAmbiguousDropAllThenApply(t *testing.T) {
	f := newRunFixture(t)
	f.attended("\nd\n")
	code, out, errOut := f.run(t, strayScript, "fix Add", "--fast", "--agent", "fake", "--plain")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	t.Logf("stdout:\n%s", out)
	for _, want := range []string{
		"Ambiguous-file Gate",
		"2 new files were not covered by the declared output/test globs:",
		"  1  docs/debug.md", "  2  tmp/result.json",
		"2 new files need a decision",
		"  p  promote", "  d  drop", "  i  inspect", "  reject ", "  q  quit",
		"decision   drop · recorded at the Ambiguous-file Gate · docs/debug.md, tmp/result.json",
		"ACCEPTED   Candidate ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	// The final Check ran on the resolved Candidate.
	if n := strings.Count(out, "check      go test -json ./..."); n != 2 {
		t.Errorf("%d Checks, want 2:\n%s", n, out)
	}
	dir := f.onlyRun(t)
	if got := strings.Join(ambiguousResolutions(t, dir), "|"); got != "drop docs/debug.md,tmp/result.json" {
		t.Errorf("resolutions: %s", got)
	}
	if got := strings.Join(gateRecords(t, dir), "|"); got != "GateOpened gate.ambiguous_file|GateDecided gate.ambiguous_file drop|RunEnded Accepted" {
		t.Errorf("records: %s", got)
	}
	f.assertUntouched(t)

	code, out, errOut = f.deliver(t, "apply")
	if code != ExitOK {
		t.Fatalf("apply: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, p := range []string{"docs/debug.md", "tmp/result.json"} {
		if _, err := os.Stat(filepath.Join(f.repo, p)); err == nil {
			t.Errorf("apply delivered the dropped %s", p)
		}
	}
	if got := readFile(t, filepath.Join(f.repo, "add.go")); got != fxFixed {
		t.Errorf("add.go = %q", got)
	}
}

// helperScript fixes Add and adds helper.go, a new file no glob covers.
const helperScript = `printf 'package fx\n\nfunc helper() int { return 1 }\n' > helper.go
` + fixScript

// sawHelper is a verifier that notes, per Attempt, whether helper.go is in
// its view.
const sawHelper = `n=$(ls "$OGE_TEST_OUT" | grep -c "^verifier-" || true)
[ -e helper.go ] && touch "$OGE_TEST_OUT/qa-$n-saw-helper"
`

// attemptCauses are each AttemptStarting's "attempt cause".
func attemptCauses(t *testing.T, runDir string) string {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		if r.Type == run.RecAttemptStarting {
			var d struct{ Attempt, Cause string }
			if err := json.Unmarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d.Attempt+" "+d.Cause)
		}
	}
	return strings.Join(out, "|")
}

// Promote all: a fresh QA pass that now sees the file, then the final
// Check on exactly that Candidate, then Accepted.
func TestAmbiguousPromoteAllThenQA(t *testing.T) {
	f := newRunFixture(t)
	f.attended("p\n")
	code, out, errOut := f.run(t, verifierThen(sawHelper, helperScript), standardTask, "--agent", "fake", "--plain")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	t.Logf("stdout:\n%s", out)
	for _, want := range []string{
		"1 new file was not covered by the declared output/test globs:", "  1  helper.go",
		"  p  promote    selected/all join the Candidate: fresh QA, then the final Check",
		"decision   promote · recorded at the Ambiguous-file Gate · helper.go",
		"resolved   1 promoted · Candidate ",
		"fresh QA, then the final Check",
		"ACCEPTED   Candidate ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, selectHint) {
		t.Errorf("one file, yet the selection hint:\n%s", out)
	}
	o := filepath.Join(filepath.Dir(f.repo), "out")
	for name, want := range map[string]bool{"qa-1-saw-helper": false, "qa-2-saw-helper": true} {
		if _, err := os.Stat(filepath.Join(o, name)); (err == nil) != want {
			t.Errorf("%s: %v, want %v", name, err == nil, want)
		}
	}
	dir := f.onlyRun(t)
	if got := attemptCauses(t, dir); got != "implement#1 first|verify#1 first|verify#2 user_request" {
		t.Errorf("Attempts: %s", got)
	}
	if got := strings.Join(ambiguousResolutions(t, dir), "|"); got != "promote helper.go" {
		t.Errorf("resolutions: %s", got)
	}
	// The final Check ran on the resolved Candidate, and it's the one
	// delivered.
	var res struct{ From, Candidate string }
	recordData(t, dir, run.RecAmbiguousResolved, &res)
	var v struct{ Candidate string }
	recordNth(t, dir, run.RecVerdict, 1, &v)
	var ended struct{ Candidate string }
	recordData(t, dir, run.RecRunEnded, &ended)
	if v.Candidate != res.Candidate || ended.Candidate != res.Candidate {
		t.Errorf("Verdict on %s, Run ended on %s, resolved %s", v.Candidate, ended.Candidate, res.Candidate)
	}
	if got := reviewObservations(t, dir); len(got) != 1 || got[0] != 1 {
		t.Errorf("ambiguous_review Observations: %v", got)
	}
}

// A mixed selection: promote one, then drop the rest. The review stays
// open until every file is resolved, then one QA pass and one Check.
func TestAmbiguousMixedSelection(t *testing.T) {
	f := newRunFixture(t)
	f.attended("p 9\np 2\nd\n")
	code, out, errOut := f.run(t, verifierThen(sawHelper, strayScript+helperScript), standardTask, "--agent", "fake", "--plain")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	t.Logf("stdout:\n%s", out)
	for _, want := range []string{
		"3 new files need a decision", "  1  docs/debug.md", "  2  helper.go", "  3  tmp/result.json", selectHint,
		`"9" isn't one of the files shown`,
		"decision   promote · recorded at the Ambiguous-file Gate · helper.go",
		"2 new files need a decision",
		"decision   drop · recorded at the Ambiguous-file Gate · docs/debug.md, tmp/result.json",
		"resolved   1 promoted · 2 dropped · Candidate ",
		"ACCEPTED   Candidate ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "QA         Fresh Fake"); n != 2 {
		t.Errorf("%d QA passes, want 2:\n%s", n, out)
	}
	if n := strings.Count(out, "check      visible Oracle") + strings.Count(out, "check      go test"); n != 2 {
		t.Errorf("%d Checks, want 2:\n%s", n, out)
	}
	dir := f.onlyRun(t)
	if got := strings.Join(ambiguousResolutions(t, dir), "|"); got != "promote helper.go|drop docs/debug.md,tmp/result.json" {
		t.Errorf("resolutions: %s", got)
	}
	if got := strings.Join(gateRecords(t, dir), "|"); got != "GateOpened gate.ambiguous_file|GateDecided gate.ambiguous_file promote|GateOpened gate.ambiguous_file|GateDecided gate.ambiguous_file drop|RunEnded Accepted" {
		t.Errorf("records: %s", got)
	}
	if got := reviewObservations(t, dir); len(got) != 1 || got[0] != 3 {
		t.Errorf("ambiguous_review Observations: %v", got)
	}
}

// helperWants42 is a held-out test the promoted helper.go fails.
const helperWants42 = `[ -e helper.go ] && cat > helper_test.go <<'EOF'
package fx

import "testing"

func TestHelper(t *testing.T) {
	if helper() != 42 {
		t.Fatalf("helper() = %d, want 42", helper())
	}
}
EOF
`

// A promoted file that breaks the Check takes the normal routing: QA,
// which now sees it, adds a test it fails, and the Candidate goes back to
// the implementer. The promotion holds: the repaired Run isn't asked again.
func TestAmbiguousPromotedFileBreaksTheCheck(t *testing.T) {
	f := newRunFixture(t)
	f.attended("p\n")
	impl := `case "$OGE_FAKE_TURN" in
*"held-out test"*)
printf 'package fx\n\nfunc helper() int { return 42 }\n' > helper.go
exit 0 ;;
esac
` + helperScript
	code, out, errOut := f.run(t, verifierThen(helperWants42, impl), standardTask, "--agent", "fake", "--plain")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	t.Logf("stdout:\n%s", out)
	for _, want := range []string{
		"decision   promote · recorded at the Ambiguous-file Gate · helper.go",
		"QA         Fresh Fake · Exit extended · +1 held-out",
		"1 failed: TestHelper · fail",
		"send back  1 of 3 · Repairing automatically…",
		"ACCEPTED   Candidate ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "Ambiguous-file Gate\n"); n != 1 {
		t.Errorf("the review fired %d times:\n%s", n, out)
	}
	if got := attemptCauses(t, f.onlyRun(t)); got != "implement#1 first|verify#1 first|verify#2 user_request|implement#2 send_back|verify#3 send_back" {
		t.Errorf("Attempts: %s", got)
	}
}

// Bound exhaustion after a promotion: the normal Gate, never Accepted.
func TestAmbiguousPromotedFileExhaustsTheBound(t *testing.T) {
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	f.attended("p\nq\n")
	code, out, errOut := f.run(t, verifierThen(helperWants42, helperScript), standardTask, "--agent", "fake", "--plain")
	if code != ExitCancelled {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := strings.Join(gateRecords(t, f.onlyRun(t)), "|"); got != "GateOpened gate.ambiguous_file|GateDecided gate.ambiguous_file promote|GateOpened gate.bound_exhaustion|GateDecided gate.bound_exhaustion quit|RunEnded Cancelled" {
		t.Errorf("records: %s", got)
	}
}

// Inspect shows a file's content, safe for the terminal, and decides
// nothing; reject at the review ends the Run Rejected, never Accepted.
func TestAmbiguousInspectThenReject(t *testing.T) {
	f := newRunFixture(t)
	f.attended("i 1\ni\ni 2\nreject not mine\n")
	script := `mkdir -p docs
printf '# notes\n\033]0;pwned\007\033[31mred\033[0m\n' > docs/debug.md
printf '\000\001binary' > blob.bin
` + fixScript
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--plain")
	if code != ExitRejected {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	t.Logf("stdout:\n%s", out)
	for _, want := range []string{
		"── blob.bin", "binary, 8 bytes: not shown",
		"name the file to inspect: i 1 to i 2",
		"── docs/debug.md · 2 lines", "  # notes", "]0;pwned[31mred[0m", "── end of docs/debug.md",
		"REJECTED   Candidate ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\033\007") {
		t.Errorf("raw control bytes reached the terminal:\n%q", out)
	}
	dir := f.onlyRun(t)
	if got := strings.Join(gateRecords(t, dir), "|"); got != "GateOpened gate.ambiguous_file|GateDecided gate.ambiguous_file reject not mine|RunEnded Rejected" {
		t.Errorf("records: %s", got)
	}
	if got := ambiguousResolutions(t, dir); len(got) != 0 {
		t.Errorf("resolutions: %v", got)
	}
}
