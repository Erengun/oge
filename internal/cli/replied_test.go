package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

// A turn that changed nothing ends as Replied (#254): no QA, no Check, no
// Verdict, exit 0, nothing to deliver.
const replyScript = "echo \"yo, Add sums two ints\"\necho \"exit: done\"\n"

func ledgerTypes(t *testing.T, runDir string) map[string]int {
	t.Helper()
	recs, err := ledger.Replay(runDir)
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, r := range recs {
		n[r.Type]++
	}
	return n
}

func TestRepliedRunSkipsQAAndCheck(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, replyScript, "yo", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "yo, Add sums two ints") || !strings.Contains(out, repliedLine) {
		t.Errorf("the reply and the summary line are not printed:\n%s", out)
	}
	if strings.Contains(out, "ÖGE RECEIPT") || strings.Contains(out, "oge apply") {
		t.Errorf("a Replied Run printed a Receipt or an action:\n%s", out)
	}
	dir := f.onlyRun(t)
	types := ledgerTypes(t, dir)
	for _, typ := range []string{run.RecCheckStarted, run.RecVerdict} {
		if types[typ] != 0 {
			t.Errorf("a Replied Run wrote %d %s records", types[typ], typ)
		}
	}
	if types[run.RecScopeObserved] == 0 {
		t.Error("the scope observation is not recorded")
	}
	ended := records(t, dir, run.RecRunEnded)
	if len(ended) != 1 || ended[0]["outcome"] != "Replied" {
		t.Errorf("RunEnded %v", ended)
	}
	id := filepath.Base(dir)

	// There is nothing to apply or branch.
	for _, cmd := range []string{"apply", "branch"} {
		args := []string{cmd, id}
		if cmd == "branch" {
			args = append(args, "x")
		}
		code, _, errOut := f.deliver(t, args...)
		if code == ExitOK || !strings.Contains(errOut, "replied and changed nothing") {
			t.Errorf("oge %s: exit %d, stderr %q", cmd, code, errOut)
		}
	}
	// The Receipt says so, with no Checks section.
	code, out, _ = f.deliver(t, "receipt", id)
	if code != ExitOK || !strings.Contains(out, "Replied (no changes)") || strings.Contains(out, "Checks ") {
		t.Errorf("receipt: exit %d\n%s", code, out)
	}
}

func TestRepliedIgnoresExcludedChanges(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	script := "mkdir -p .claude\necho x > .claude/notes.md\n" + replyScript
	code, out, errOut := f.run(t, script, "yo", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if got := records(t, f.onlyRun(t), run.RecRunEnded); len(got) != 1 || got[0]["outcome"] != "Replied" {
		t.Errorf("RunEnded %v", got)
	}
}

func TestRealChangeIsNotReplied(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, fixScript, "fix Add", "--fast", "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if strings.Contains(out, repliedLine) {
		t.Errorf("a changing turn was Replied:\n%s", out)
	}
	if got := records(t, f.onlyRun(t), run.RecRunEnded); len(got) != 1 || got[0]["outcome"] != "Accepted" {
		t.Errorf("RunEnded %v", got)
	}
}

func TestHomeRepliedShowsReplyAndNoReceipt(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--agent", "fake")
	s.composer()
	script := filepath.Join(filepath.Dir(f.repo), "agent.sh")
	if err := os.WriteFile(script, []byte(replyScript), 0o644); err != nil {
		t.Fatal(err)
	}
	s.type_("yo\r")
	s.expect(repliedLine)
	s.mark = len(s.out.String())
	s.composer()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	got := s.out.String()
	if !strings.Contains(got, "yo, Add sums two ints") {
		t.Errorf("the reply is not shown:\n%s", got)
	}
	for _, bad := range []string{"ÖGE RECEIPT", "↓ actions"} {
		if strings.Contains(got[strings.Index(got, "yo, Add"):], bad) {
			t.Errorf("a Replied Run showed %q:\n%s", bad, got)
		}
	}
}

func TestRepliedNeverBecomesTheThreadTip(t *testing.T) {
	prev := &thread{}
	res := &run.Result{Outcome: run.Replied, Candidate: "abc", Dir: "d"}
	if got := nextThread(prev, task.Task{}, res, runFlags{}); got != prev {
		t.Errorf("a Replied Run replaced the thread: %v", got)
	}
	if got := nextThread(nil, task.Task{}, res, runFlags{}); got != nil {
		t.Errorf("a Replied Run started a thread: %v", got)
	}
}
