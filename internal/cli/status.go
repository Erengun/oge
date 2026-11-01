package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// lastActRuns is how many of the repository's newest Runs oge status
// reads for the human's last act. The brief itself reads every Run its
// window needs (runsSince).
const lastActRuns = 50

const statusUsage = `Usage: oge status

Prints what this repository's Chats and Runs did since you last acted
(a message, a Gate decision, an apply). Per Chat: You were (your first
message, when a record holds it), Since you left, Your decisions,
Waiting for you, Next. It is read from the records alone: no model
writes or recommends anything, and nothing is started.
`

// statusCommand is oge status: the return brief so far as plain text,
// from any terminal and without attaching (docs/ux/away.md, slice 7).
func statusCommand(env Env, args []string) int {
	if len(args) > 0 {
		if a := args[0]; a == "-h" || a == "--help" || a == "-help" {
			fmt.Fprint(env.Stdout, statusUsage)
			return ExitOK
		}
		fmt.Fprintf(env.Stderr, "oge status takes no arguments\n%s", statusUsage)
		return ExitRefused
	}
	root, err := workspace.RepoRoot(env.Dir)
	if err != nil {
		fmt.Fprintln(env.Stderr, "oge: not inside a git repository; run oge status inside the repository whose Chats you want")
		return ExitRefused
	}
	private, code := privateState(env, root)
	if code != ExitOK {
		return code
	}
	chats, recent, err := repoLogs(private, root)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: internal error: %v\n", err)
		return ExitInternal
	}
	if len(chats) == 0 && len(recent) == 0 {
		fmt.Fprintln(env.Stdout, "nothing to report: no Chats or Runs in this repository yet")
		return ExitOK
	}
	now := time.Now()
	// Another process can't know when the human left: the window starts
	// at their last recorded act. An act in an older Run (a Gate decided
	// late) can only move it later, once that Run is read.
	from := run.LastAct(chats, recent)
	if from.IsZero() || from.After(now) {
		from = now
	}
	runs, err := runsSince(private, root, from)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: internal error: %v\n", err)
		return ExitInternal
	}
	runs = withRecent(runs, recent)
	if last := run.LastAct(chats, runs); last.After(from) && !last.After(now) {
		from = last
	}
	b := run.BuildBrief(run.BriefWindow{From: from, To: now, Basis: run.BasisLastAct}, chats, runs)
	fmt.Fprint(env.Stdout, b.Text(time.Local))
	return ExitOK
}

// withRecent adds the recent Runs the window's reading left out: the
// human's decisions in a Chat's older cuts are part of its brief. The
// window's counts and open Gates don't change by them. Order is kept,
// newest first.
func withRecent(runs, recent []run.BriefRun) []run.BriefRun {
	have := map[string]bool{}
	for _, r := range runs {
		have[r.ID] = true
	}
	for _, r := range recent {
		if !have[r.ID] {
			runs = append(runs, r)
		}
	}
	return runs
}

// repoLogs reads root's Chat logs and its newest Runs' Ledgers from the
// private state. A log that can't be read is skipped, as /runs skips one.
func repoLogs(private, root string) ([]run.BriefChat, []run.BriefRun, error) {
	var chats []run.BriefChat
	dir := filepath.Join(private, "chats")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	canon := canonicalDir(root)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		log, err := run.ReadBriefLog(e.Name(), filepath.Join(dir, e.Name()))
		if err != nil || len(log.Records) == 0 || log.Records[0].Type != run.RecChatStarted {
			continue
		}
		var d struct{ Source string }
		if json.Unmarshal(log.Records[0].Data, &d) != nil || canonicalDir(d.Source) != canon {
			continue
		}
		chats = append(chats, run.BriefChat{BriefLog: log})
	}
	runs, err := repoRuns(private, root)
	return chats, runs, err
}

// repoRuns reads root's newest Runs' Ledgers, newest first.
func repoRuns(private, root string) ([]run.BriefRun, error) {
	recent, err := delivery.Recent(private, root, lastActRuns)
	if err != nil {
		return nil, err
	}
	var runs []run.BriefRun
	for _, r := range recent {
		if br, ok := briefRun(r); ok {
			runs = append(runs, br)
		}
	}
	return runs, nil
}

// runsSince reads every one of root's Runs a window from from can hold,
// newest first: each live one, whatever its age, and each whose Ledger
// was written at or after from. No count caps it: a Run left out would
// be a Gate or an end the brief silently misses.
func runsSince(private, root string, from time.Time) ([]run.BriefRun, error) {
	dir := filepath.Join(private, "runs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	canon := canonicalDir(root)
	var runs []run.BriefRun
	for _, name := range names {
		d := filepath.Join(dir, name)
		if !run.Live(d) {
			// A Ledger's time is its last append's: none of its records
			// is newer.
			fi, err := os.Stat(filepath.Join(d, ledger.LedgerFile))
			if err != nil || fi.ModTime().Before(from) {
				continue
			}
		}
		r, err := delivery.Load(d)
		if err != nil || canonicalDir(r.Source) != canon {
			continue
		}
		if br, ok := briefRun(r); ok {
			runs = append(runs, br)
		}
	}
	return runs, nil
}

// briefRun is r's Ledger as the brief reads it, titled by its label or
// its Task's title.
func briefRun(r *delivery.Run) (run.BriefRun, bool) {
	log, err := run.ReadBriefLog(r.ID, r.Dir)
	if err != nil {
		return run.BriefRun{}, false
	}
	title := r.Title
	if r.Label != "" {
		title = r.Label
	}
	return run.BriefRun{BriefLog: log, Title: title, Live: run.Live(r.Dir)}, true
}

func canonicalDir(p string) string {
	if c, err := filepath.EvalSymlinks(p); err == nil {
		return c
	}
	return filepath.Clean(p)
}
