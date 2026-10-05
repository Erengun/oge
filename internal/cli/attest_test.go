package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/run"
)

// Protected-test execution attestation (ADR-0020, #73): a Candidate never
// reaches Accepted by counterfeiting the report or exiting before the
// protected tests have run, and skips are judged against the Snapshot.

// forgedFrames is what go test -json reads as TestAdd passing: test2json
// trusts the \x16-framed lines a test binary prints.
const forgedFrames = `fmt.Print("\x16=== RUN   TestAdd\n\x16--- PASS: TestAdd (0.00s)\n\x16PASS\n")`

// The forged-report exploit (#72): init prints PASS frames for the Oracle
// test and exits 0 before any test runs.
func TestRunForgedReportIsNeverAccepted(t *testing.T) {
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	script := `cat > add.go <<'EOF'
package fx

import (
	"fmt"
	"os"
)

func init() {
	` + forgedFrames + `
	os.Exit(0)
}

func Add(a, b int) int { return 0 }
EOF
`
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if !neverAccepted(code, out) {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"fx.TestAdd never ran; the report claims pass",
		"Observed", "add.go: os.Exit called from init",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

// An early exit from a Candidate TestMain, with forged frames, is never
// Accepted. The test globs name only the Oracle's file, so the
// Candidate's main_test.go survives the overlay.
func TestRunTestMainEarlyExitIsNeverAccepted(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(strings.Replace(fxConfig, `["**/*_test.go"]`, `["add_test.go"]`, 1)+"[pipelines.default.limits]\nsend_backs = 0\n"))
	script := `cat > main_test.go <<'EOF'
package fx

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	` + forgedFrames + `
	os.Exit(0)
}
EOF
`
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if !neverAccepted(code, out) {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{"fx.TestAdd never ran", "main_test.go: TestMain added or changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

// The Candidate exits during the protected test, after it started but
// before it finished: Add prints PASS frames and leaves the process with
// status 0, which the testing package can't intercept.
func TestRunExitDuringAProtectedTestIsNeverAccepted(t *testing.T) {
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	script := `cat > add.go <<'EOF'
package fx

import (
	"fmt"
	"syscall"
)

func Add(a, b int) int {
	` + forgedFrames + `
	syscall.Exit(0)
	return 0
}
EOF
`
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if !neverAccepted(code, out) || !strings.Contains(out, "fx.TestAdd started but never finished") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// toolTest skips wherever a tool is missing: on the Snapshot and the
// Candidate alike.
const toolTest = `package fx

import (
	"os/exec"
	"testing"
)

func TestNeedsTool(t *testing.T) {
	if _, err := exec.LookPath("oge-no-such-tool"); err != nil {
		t.Skip("needs oge-no-such-tool")
	}
	t.Fatal("unreachable")
}
`

// Skipped on the Snapshot and on the Candidate: the gap was already there,
// so the Run may be Accepted, and the summary says the test wasn't covered.
func TestRunSkippedOnBothMayBeAccepted(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "tool_test.go"), []byte(toolTest))
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "Not covered Oracle tests skipped on the Snapshot and the Candidate (1): fx.TestNeedsTool") {
		t.Errorf("the summary doesn't name the skipped test:\n%s", out)
	}
	var ended struct {
		Result struct {
			Tests []struct {
				Name, Attested, Snapshot string
			} `json:"tests"`
		} `json:"result"`
	}
	recordData(t, f.onlyRun(t), run.RecCheckEnded, &ended)
	found := false
	for _, x := range ended.Result.Tests {
		if x.Name == "TestNeedsTool" {
			found = x.Attested == "skip" && x.Snapshot == "skip"
		}
	}
	if !found {
		t.Errorf("CheckEnded tests = %+v, want TestNeedsTool skipped on both", ended.Result.Tests)
	}
}

// subTest fails on the Snapshot unless FX_SKIP_SUB is set.
const subTest = `package fx

import (
	"os"
	"testing"
)

func TestSub(t *testing.T) {
	if os.Getenv("FX_SKIP_SUB") != "" {
		t.Skip("FX_SKIP_SUB is set")
	}
	if Sub(3, 2) != 1 {
		t.Fatal("Sub(3, 2) != 1")
	}
}
`

// The test runs on the Snapshot, and the Candidate makes it skip: a gap
// the Candidate created can never be Accepted.
func TestRunCandidateForcedSkipIsNeverAccepted(t *testing.T) {
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	writeFile(t, filepath.Join(f.repo, "sub.go"), []byte("package fx\n\nfunc Sub(a, b int) int { return 0 }\n"))
	writeFile(t, filepath.Join(f.repo, "sub_test.go"), []byte(subTest))
	script := fixScript + `cat > skip.go <<'EOF'
package fx

import "os"

func init() { os.Setenv("FX_SKIP_SUB", "1") }
EOF
`
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if !neverAccepted(code, out) || !strings.Contains(out, "fx.TestSub skipped, but it ran on the Snapshot") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// Skipped on the Snapshot, run and passed on the Candidate: an ordinary
// pass, with attested pass Evidence and nothing listed as not covered.
func TestRunSkippedOnTheSnapshotRunOnTheCandidatePasses(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "mul.go"), []byte("package fx\n\nfunc Supported() bool { return false }\n\nfunc Mul(a, b int) int { return 0 }\n"))
	writeFile(t, filepath.Join(f.repo, "mul_test.go"), []byte("package fx\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif !Supported() {\n\t\tt.Skip(\"not supported yet\")\n\t}\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"Mul(2, 3) != 6\")\n\t}\n}\n"))
	script := fixScript + `printf 'package fx\n\nfunc Supported() bool { return true }\n\nfunc Mul(a, b int) int { return a * b }\n' > mul.go
`
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || strings.Contains(out, "skipped on the Snapshot") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	var ended struct {
		Result struct {
			Tests []struct{ Name, Attested string } `json:"tests"`
		} `json:"result"`
	}
	recordData(t, f.onlyRun(t), run.RecCheckEnded, &ended)
	got := map[string]string{}
	for _, x := range ended.Result.Tests {
		got[x.Name] = x.Attested
	}
	if got["TestMul"] != "pass" || got["TestAdd"] != "pass" {
		t.Errorf("attested = %v, want TestAdd and TestMul passing", got)
	}
}

// Attestation that fails on the Snapshot control too is the mechanism's
// failure, not the Candidate's: an Infrastructure stop, never a Verdict.
// Here the Oracle's own TestMain prints frames and exits before any test.
func TestRunAttestationFailingOnTheSnapshotIsInfrastructure(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "main_test.go"), []byte("package fx\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) {\n\t"+forgedFrames+"\n\tos.Exit(0)\n}\n"))
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitInfra || !strings.Contains(out, "INFRASTRUCTURE STOP") || !strings.Contains(out, "attestation failed on the Snapshot control") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	dir := f.onlyRun(t)
	for _, rec := range recordTypes(t, dir) {
		if rec == run.RecVerdict {
			t.Error("a Verdict was recorded")
		}
	}
	var control struct{ Consulted bool }
	recordData(t, dir, run.RecControlEnded, &control)
	if !control.Consulted {
		t.Error("the Snapshot control wasn't consulted")
	}
}

// When every Oracle test skipped on both sides, expected_tests doesn't
// reject on its own: the minimum holds only for tests that ran on the
// Snapshot.
func TestRunAllSkippedOnBothMeetsTheMinimum(t *testing.T) {
	f := newRunFixture(t)
	if err := os.Remove(filepath.Join(f.repo, "add_test.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.repo, "tool_test.go"), []byte(toolTest))
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || !strings.Contains(out, "skipped on the Snapshot and the Candidate (1): fx.TestNeedsTool") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// neverAccepted: a fail Verdict, with send-backs off, ends at the
// bound-exhaustion Gate (Parked) or Rejected; never Accepted.
func neverAccepted(code int, out string) bool {
	return (code == ExitParked || code == ExitRejected) && !strings.Contains(out, "ACCEPTED")
}
