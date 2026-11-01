package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// statusRuns is how many of the repository's newest Runs oge status reads.
const statusRuns = 50

const statusUsage = `Usage: oge status

Prints what this repository's Chats and Runs did since you last acted
(a message, a Gate decision, an apply): what settled, what needs you,
what failed. It is read from the records alone and starts nothing.
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
	chats, runs, err := repoLogs(private, root)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: internal error: %v\n", err)
		return ExitInternal
	}
	if len(chats) == 0 && len(runs) == 0 {
		fmt.Fprintln(env.Stdout, "nothing to report: no Chats or Runs in this repository yet")
		return ExitOK
	}
	now := time.Now()
	// Another process can't know when the human left: the window starts
	// at their last recorded act.
	from := run.LastAct(chats, runs)
	if from.IsZero() || from.After(now) {
		from = now
	}
	b := run.BuildBrief(run.BriefWindow{From: from, To: now, Basis: run.BasisLastAct}, chats, runs)
	fmt.Fprint(env.Stdout, b.Text(time.Local))
	return ExitOK
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
	recent, err := delivery.Recent(private, root, statusRuns)
	if err != nil {
		return nil, nil, err
	}
	var runs []run.BriefRun
	for _, r := range recent {
		log, err := run.ReadBriefLog(r.ID, r.Dir)
		if err != nil {
			continue
		}
		title := r.Title
		if r.Label != "" {
			title = r.Label
		}
		runs = append(runs, run.BriefRun{BriefLog: log, Title: title, Live: run.Live(r.Dir)})
	}
	return chats, runs, nil
}

func canonicalDir(p string) string {
	if c, err := filepath.EvalSymlinks(p); err == nil {
		return c
	}
	return filepath.Clean(p)
}
