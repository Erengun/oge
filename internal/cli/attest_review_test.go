package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review of #73: Oracles whose tests the attestation used to miss, and
// gaps attributed to the Candidate that weren't its doing.

// forgeInit is a Candidate file whose init prints PASS frames for test
// and exits 0 before any test runs.
func forgeInit(pkg, test string) string {
	return `package ` + pkg + `

import (
	"fmt"
	"os"
)

func init() {
	fmt.Print("\x16=== RUN   ` + test + `\n\x16--- PASS: ` + test + ` (0.00s)\n\x16PASS\n")
	os.Exit(0)
}
`
}

// A nested module's tests, run by a Check command of their own, are
// expected and attest like any other.
func TestRunForgedReportInANestedModuleIsNeverAccepted(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(fxConfig+`[[check.commands]]
run    = "cd sub && go test -json ./..."
report = "go-test-json"
[pipelines.default.limits]
send_backs = 0
`))
	writeFile(t, filepath.Join(f.repo, "sub", "go.mod"), []byte("module fxsub\n\ngo 1.22\n"))
	writeFile(t, filepath.Join(f.repo, "sub", "x.go"), []byte("package sub\n\nfunc X() int { return 0 }\n"))
	writeFile(t, filepath.Join(f.repo, "sub", "x_test.go"), []byte("package sub\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tif X() != 1 {\n\t\tt.Fatal(\"X() != 1\")\n\t}\n}\n"))
	script := fixScript + "cat > sub/forge.go <<'EOF'\n" + forgeInit("sub", "TestX") + "EOF\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if !neverAccepted(code, out) || !strings.Contains(out, "fxsub.TestX never ran; the report claims pass") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

const exampleTest = "package fx\n\nimport \"fmt\"\n\nfunc ExampleAdd() {\n\tfmt.Println(Add(2, 3))\n\t// Output: 5\n}\n"

// An Example with an Output comment is a protected test: it attests.
func TestRunExampleOracle(t *testing.T) {
	t.Run("forged", func(t *testing.T) {
		f := newRunFixture(t)
		f.sendBackLimit(t, 0)
		if err := os.Remove(filepath.Join(f.repo, "add_test.go")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(f.repo, "example_test.go"), []byte(exampleTest))
		script := "cat > forge.go <<'EOF'\n" + forgeInit("fx", "ExampleAdd") + "EOF\n"
		code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
		if !neverAccepted(code, out) || !strings.Contains(out, "fx.ExampleAdd never ran") {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
	// The example returns, and once go test has restored stdout, before
	// it compares the output, a Candidate goroutine exits 0.
	t.Run("exit before the comparison", func(t *testing.T) {
		f := newRunFixture(t)
		f.sendBackLimit(t, 0)
		if err := os.Remove(filepath.Join(f.repo, "add_test.go")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(f.repo, "example_test.go"), []byte(exampleTest))
		script := `cat > add.go <<'EOF'
package fx

import (
	"os"
	"runtime"
	"syscall"
)

func Add(a, b int) int {
	out := os.Stdout
	go func() {
		for os.Stdout == out { // go test restores stdout, then compares
			runtime.Gosched()
		}
		syscall.Exit(0)
	}()
	return 0
}
EOF
`
		code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
		if !neverAccepted(code, out) || !strings.Contains(out, "fx.ExampleAdd started but never finished") {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
	t.Run("unordered", func(t *testing.T) {
		f := newRunFixture(t)
		if err := os.Remove(filepath.Join(f.repo, "add_test.go")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(f.repo, "example_test.go"), []byte("package fx_test\n\nimport (\n\t\"fmt\"\n\n\t\"fx\"\n)\n\nfunc ExampleAdd() {\n\tfmt.Println(fx.Add(2, 3))\n\tfmt.Println(\"x\")\n\t// Unordered output:\n\t// x\n\t// 5\n}\n"))
		code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
		if code != ExitOK {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
	t.Run("fixed", func(t *testing.T) {
		f := newRunFixture(t)
		if err := os.Remove(filepath.Join(f.repo, "add_test.go")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(f.repo, "example_test.go"), []byte(exampleTest))
		code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
		if code != ExitOK {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
}

// An Oracle with Go test files but nothing Öge can attest can't vouch
// for anything: an Oracle-validity stop, not a pass.
func TestRunOracleWithNothingToAttestStops(t *testing.T) {
	f := newRunFixture(t)
	if err := os.Remove(filepath.Join(f.repo, "add_test.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.repo, "bench_test.go"), []byte("package fx\n\nimport \"testing\"\n\nfunc BenchmarkAdd(b *testing.B) {}\n"))
	script := "cat > forge.go <<'EOF'\n" + forgeInit("fx", "TestAdd") + "EOF\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitInfra || !strings.Contains(out, "the Oracle's Go test files declare no test Öge can attest") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// A skip forced inside a subtest is a gap the Candidate made.
func TestRunCandidateForcedSubtestSkipIsNeverAccepted(t *testing.T) {
	f := newRunFixture(t)
	f.sendBackLimit(t, 0)
	writeFile(t, filepath.Join(f.repo, "sub.go"), []byte("package fx\n\nfunc Sub(a, b int) int { return 0 }\n"))
	writeFile(t, filepath.Join(f.repo, "sub_test.go"), []byte(`package fx

import (
	"os"
	"testing"
)

func TestSubtests(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		if os.Getenv("FX_SKIP_SUB") != "" {
			t.Skip("FX_SKIP_SUB is set")
		}
		if Sub(3, 2) != 1 {
			t.Fatal("Sub(3, 2) != 1")
		}
	})
}
`))
	script := fixScript + "cat > skip.go <<'EOF'\npackage fx\n\nimport \"os\"\n\nfunc init() { os.Setenv(\"FX_SKIP_SUB\", \"1\") }\nEOF\n"
	code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if !neverAccepted(code, out) || !strings.Contains(out, "fx.TestSubtests/sub skipped, but it ran on the Snapshot") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// The Oracle's own TestMain runs no tests: the Snapshot control shows the
// same absence, so it's an Oracle-validity stop, not the Candidate's fail.
func TestRunOracleTestMainThatRunsNothingIsInfrastructure(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "main_test.go"), []byte("package fx\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(0) }\n"))
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitInfra || !strings.Contains(out, "fx.TestAdd never ran on the Snapshot control either") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// Baseline-relative buildability (ADR-0020): the Snapshot control, never
// the Candidate, decides which protected tests count.

const taggedTest = "//go:build oge_integration\n\npackage fx\n\nimport \"testing\"\n\nfunc TestTagged(t *testing.T) {}\n"

// Excluded on the Snapshot by build tags: not covered, with the reason.
func TestRunBuildTaggedProtectedTestIsNotCovered(t *testing.T) {
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "tagged_test.go"), []byte(taggedTest))
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK || !strings.Contains(out, `Oracle test files this machine doesn't build (1): tagged_test.go — requires build constraint "oge_integration"`) {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// Protected source exists, but nothing protected runs here: fail closed.
func TestRunOnlyExcludedProtectedTestsFailClosed(t *testing.T) {
	f := newRunFixture(t)
	if err := os.Remove(filepath.Join(f.repo, "add_test.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.repo, "tagged_test.go"), []byte(taggedTest))
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitInfra || !strings.Contains(out, "no protected test runs on this machine: tagged_test.go") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// The Candidate can't make a Snapshot-runnable test excluded: a
// //go:build ignore line or a platform-suffixed rename never passes.
func TestRunCandidateCannotExcludeAProtectedTest(t *testing.T) {
	for name, script := range map[string]string{
		"build ignore": "printf '//go:build ignore\\n\\n' | cat - add_test.go > x && mv x add_test.go\n",
		"rename":       "mv add_test.go add_windows_test.go\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newRunFixture(t)
			f.sendBackLimit(t, 0)
			code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
			if !neverAccepted(code, out) || !strings.Contains(out, "TestAdd") {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
		})
	}
}

// The Snapshot doesn't compile: the statically eligible tests are
// required once the Candidate builds, and excluded ones aren't covered.
func TestRunSnapshotThatDoesNotCompile(t *testing.T) {
	setup := func(t *testing.T) *runFixture {
		f := newRunFixture(t)
		f.sendBackLimit(t, 0)
		writeFile(t, filepath.Join(f.repo, "add.go"), []byte("package fx\n\nfunc Add(a, b int) int { return }\n"))
		writeFile(t, filepath.Join(f.repo, "tagged_test.go"), []byte(taggedTest))
		return f
	}
	t.Run("fixed", func(t *testing.T) {
		f := setup(t)
		code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
		if code != ExitOK || !strings.Contains(out, "tagged_test.go — requires build constraint") {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
	t.Run("forged", func(t *testing.T) {
		f := setup(t)
		script := "printf 'package fx\\n\\nfunc Add(a, b int) int { return 0 }\\n' > add.go\ncat > forge.go <<'EOF'\n" + forgeInit("fx", "TestAdd") + "EOF\n"
		code, out, errOut := f.run(t, script, "fix Add", "--fast", "--agent", "fake", "--unattended")
		if !neverAccepted(code, out) || !strings.Contains(out, "fx.TestAdd never ran") {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
}
