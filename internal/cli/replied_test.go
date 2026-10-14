package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// homeTurn types one message into a bare-oge session whose agent runs
// script, waits until text is on screen, leaves, and returns the Run dir.
func homeTurn(t *testing.T, f *runFixture, script, text string, args ...string) string {
	t.Helper()
	s := startHomeSession(t, f, append([]string{"--agent", "fake"}, args...)...)
	s.composer()
	path := filepath.Join(filepath.Dir(f.repo), "agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	s.type_("yo\r")
	s.expect(text)
	s.mark = len(s.out.String())
	s.type_("\x03")
	s.wait()
	return f.onlyRun(t)
}

func endedOutcome(t *testing.T, dir string) string {
	t.Helper()
	got := records(t, dir, run.RecRunEnded)
	if len(got) != 1 {
		return ""
	}
	o, _ := got[0]["outcome"].(string)
	return o
}

func TestHomeRepliedSkipsQAAndCheck(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--agent", "fake")
	s.composer()
	path := filepath.Join(filepath.Dir(f.repo), "agent.sh")
	if err := os.WriteFile(path, []byte(replyScript), 0o644); err != nil {
		t.Fatal(err)
	}
	s.type_("yo\r")
	s.expect(repliedLine)
	// The composer is ready again, drawn after the line: the home isn't
	// waiting. It may come in the same write as the line.
	s.mark = strings.LastIndex(s.out.String(), repliedLine)
	s.composer()
	s.type_("\x03")
	if code := s.wait(); code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s.out.String(), s.errOut.String())
	}
	got := s.out.String()
	at := strings.Index(got, "yo, Add sums two ints")
	if at < 0 {
		t.Fatalf("the reply is not shown:\n%s", got)
	}
	// No Check or QA row, spinning, failed or "not run" (#259).
	for _, l := range strings.Split(got[at:], "\n") {
		if l = strings.TrimSpace(l); strings.Contains(l, "check ") && !strings.HasPrefix(l, "· check ") {
			t.Errorf("a Replied Run's check row isn't just pending: %q", l)
		}
	}
	for _, bad := range []string{"ÖGE RECEIPT", "↓ actions", "✗", "not run"} {
		if strings.Contains(got[at:], bad) {
			t.Errorf("a Replied Run showed %q:\n%s", bad, got)
		}
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
	if o := endedOutcome(t, dir); o != "Replied" {
		t.Fatalf("outcome %q", o)
	}
	id := filepath.Base(dir)

	// There is nothing to apply or branch.
	for _, args := range [][]string{{"apply", id}, {"branch", id, "x"}} {
		code, _, errOut := f.deliver(t, args...)
		if code == ExitOK || !strings.Contains(errOut, "nothing to "+args[0]+": Run "+id+" replied without changes") {
			t.Errorf("oge %s: exit %d, stderr %q", args[0], code, errOut)
		}
	}
	// The Receipt says so, with no Checks section.
	code, out, _ := f.deliver(t, "receipt", id)
	if code != ExitOK || !strings.Contains(out, "Replied (no changes)") || strings.Contains(out, "Checks ") {
		t.Errorf("receipt: exit %d\n%s", code, out)
	}
}

func TestHomeRepliedIgnoresExcludedChanges(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	script := "mkdir -p .claude\necho x > .claude/notes.md\n" + replyScript
	if o := endedOutcome(t, homeTurn(t, f, script, repliedLine)); o != "Replied" {
		t.Errorf("outcome %q", o)
	}
}

// homeUntil runs one home turn with script (attended, so a Gate waits
// for the human) and returns the Run dir once the Ledger has a record of
// type typ. The session is left open; the test's cleanup closes it.
func homeUntil(t *testing.T, f *runFixture, script, typ string, args ...string) (string, *homeSession) {
	t.Helper()
	s := startHomeSession(t, f, append([]string{"--agent", "fake"}, args...)...)
	s.composer()
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.repo), "agent.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	s.type_("yo\r")
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if dirs := runDirs(t, f); len(dirs) == 1 {
			// The Run dir exists a moment before its Ledger does.
			if _, err := os.Stat(filepath.Join(dirs[0], "ledger.jsonl")); err != nil {
				continue
			}
			if len(records(t, dirs[0], typ)) > 0 {
				return dirs[0], s
			}
		}
	}
	t.Fatalf("no %s record:\n%s", typ, s.out.String())
	return "", nil
}

// A Tamper event keeps today's path: the turn is never Replied, even
// though nothing of the Candidate is left to verify.
func TestHomeTamperOnlyTurnIsNotReplied(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	_, s := homeUntil(t, f, cheatScript, run.RecTamperEvent, "--fast")
	time.Sleep(time.Second)
	if strings.Contains(s.out.String(), repliedLine) {
		t.Errorf("a Tamper turn was Replied:\n%s", s.out.String())
	}
}

// oge "task" never ends Replied: a no-change turn is checked and sent back.
func TestOneShotNoChangeIsNotReplied(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.sendBackLimit(t, 1)
	_, out, _ := f.run(t, replyScript, "yo", "--fast", "--agent", "fake", "--unattended")
	if strings.Contains(out, repliedLine) {
		t.Errorf("a one-shot Run was Replied:\n%s", out)
	}
	dir := f.onlyRun(t)
	if ledgerTypes(t, dir)[run.RecCheckStarted] == 0 || endedOutcome(t, dir) == "Replied" {
		t.Errorf("the one-shot no-change turn skipped its Check (outcome %q)", endedOutcome(t, dir))
	}
}

// A home send-back Attempt that changes nothing isn't Replied either: the
// first turn's change fails its Check and is sent back.
func TestHomeSendBackWithNoChangeIsNotReplied(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	f.sendBackLimit(t, 1)
	_, s := homeUntil(t, f, wrongScript, run.RecVerdict, "--fast")
	time.Sleep(time.Second)
	if strings.Contains(s.out.String(), repliedLine) {
		t.Errorf("a send-back turn was Replied:\n%s", s.out.String())
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

// A reply doesn't wait for Preflight: a failing (and slow) setup still
// leaves it Replied, and quickly.
func TestHomeRepliedDoesNotWaitForPreflight(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	writeFile(t, filepath.Join(f.repo, "setup.sh"), []byte("sleep 20\nexit 1\n"))
	cfg := fxConfig + "[setup]\nrun = \"sh setup.sh\"\n"
	writeFile(t, filepath.Join(f.repo, ".oge", "oge.toml"), []byte(cfg))
	start := time.Now()
	dir := homeTurn(t, f, replyScript, repliedLine, "--fast")
	if o := endedOutcome(t, dir); o != "Replied" {
		t.Errorf("outcome %q", o)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("the reply waited for setup: %v", d)
	}
}

// A Replied follow-up keeps the earlier Accepted Run's actions.
func TestHomeRepliedKeepsEarlierRunsActions(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	s := startHomeSession(t, f, "--fast", "--agent", "fake")
	s.composer()
	s.type_("fix Add\r")
	s.ended()
	path := filepath.Join(filepath.Dir(f.repo), "agent.sh")
	if err := os.WriteFile(path, []byte(replyScript), 0o644); err != nil {
		t.Fatal(err)
	}
	s.type_("thanks\r")
	s.expect(repliedLine)
	s.mark = len(s.out.String())
	s.composer()
	s.actions() // the Accepted Run can still be taken
}
