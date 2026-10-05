package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Held back from delivery (#105, #107): unresolved Ambiguous files of a
// Run that isn't Accepted need an explicit include or exclude, and
// agent-config changes stay out unless declared as output or asked for.

// treeFiles lists a commit's files in the user's repository.
func treeFiles(t *testing.T, repo, rev string) []string {
	t.Helper()
	return strings.Fields(gitOut(t, repo, "ls-tree", "-r", "--name-only", rev))
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// A Run rejected at the Ambiguous-file review: apply and branch refuse
// and name the files until the human says to include or exclude them;
// each choice delivers exactly that set.
func TestDeliverUnresolvedAmbiguousNeedsAChoice(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.attended("reject not mine\n")
	code, out, errOut := f.run(t, strayScript, "fix Add", "--fast", "--agent", "fake", "--plain")
	if code != ExitRejected {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	f.interactive, f.stdin = false, ""

	for _, args := range [][]string{{"apply", "--rejected"}, {"branch", "--rejected"}} {
		code, out, errOut = f.deliver(t, args...)
		if code != ExitRefused {
			t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
		for _, want := range []string{
			"2 Ambiguous files nobody promoted or dropped: docs/debug.md, tmp/result.json",
			"--with-unresolved", "--without-unresolved",
		} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%v: stderr lacks %q:\n%s", args, want, errOut)
			}
		}
	}
	f.assertUntouched(t)
	if got := deliveries(t, f.onlyRun(t)); len(got) != 0 {
		t.Errorf("Delivery records: %v", got)
	}
	code, out, errOut = f.deliver(t, "apply", "--rejected", "--with-unresolved", "--without-unresolved")
	if code != ExitRefused || !strings.Contains(errOut, "not both") {
		t.Errorf("both: exit %d\nstderr:\n%s", code, errOut)
	}

	// Branches first: they leave the working tree alone.
	code, out, errOut = f.deliver(t, "branch", "with", "--rejected", "--with-unresolved")
	if code != ExitOK {
		t.Fatalf("branch --with-unresolved: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := treeFiles(t, f.repo, "with"); !has(got, "docs/debug.md") || !has(got, "tmp/result.json") {
		t.Errorf("branch with: %v", got)
	}
	code, out, errOut = f.deliver(t, "branch", "without", "--rejected", "--without-unresolved")
	if code != ExitOK {
		t.Fatalf("branch --without-unresolved: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := treeFiles(t, f.repo, "without"); has(got, "docs/debug.md") || has(got, "tmp/result.json") || !has(got, "add.go") {
		t.Errorf("branch without: %v", got)
	}
	if !strings.Contains(out, "2 Ambiguous files left out") {
		t.Errorf("stdout:\n%s", out)
	}

	code, out, errOut = f.deliver(t, "apply", "--rejected", "--without-unresolved")
	if code != ExitOK {
		t.Fatalf("apply --without-unresolved: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "2 Ambiguous files left out") {
		t.Errorf("stdout:\n%s", out)
	}
	for _, p := range []string{"docs/debug.md", "tmp/result.json"} {
		if _, err := os.Stat(filepath.Join(f.repo, p)); err == nil {
			t.Errorf("--without-unresolved delivered %s", p)
		}
	}
	if got := readFile(t, filepath.Join(f.repo, "add.go")); got != fxFixed {
		t.Errorf("add.go = %q", got)
	}
	code, out, errOut = f.deliver(t, "apply", "--rejected", "--with-unresolved")
	if code != ExitOK {
		t.Fatalf("apply --with-unresolved: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := readFile(t, filepath.Join(f.repo, "docs", "debug.md")); got != "# debug notes\n" {
		t.Errorf("docs/debug.md = %q", got)
	}
	if got := strings.Join(deliveries(t, f.onlyRun(t)), "|"); got != "branch rejected|branch rejected|apply rejected|apply rejected" {
		t.Errorf("Delivery records: %s", got)
	}

	code, out, errOut = f.deliver(t, "diff", "--plain")
	if code != ExitOK {
		t.Fatalf("diff: exit %d\nstderr:\n%s", code, errOut)
	}
	if !strings.Contains(out, "# Öge: docs/debug.md is an Ambiguous file nobody promoted or dropped; oge apply and oge branch need --with-unresolved or --without-unresolved\n") {
		t.Errorf("diff:\n%s", out)
	}
}

// An Accepted Run has no unresolved files: the flags have nothing to
// choose.
func TestDeliverAcceptedRefusesTheUnresolvedFlags(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.accepted(t, fixScript)
	for _, flag := range []string{"--with-unresolved", "--without-unresolved"} {
		code, _, errOut := f.deliver(t, "apply", flag)
		if code != ExitRefused || !strings.Contains(errOut, "has no unresolved Ambiguous files") {
			t.Errorf("%s: exit %d\nstderr:\n%s", flag, code, errOut)
		}
	}
	f.assertUntouched(t)
}
