package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/erengun/oge/internal/receipt"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// endScreen is how a Run ends on the terminal: its Receipt, read back
// from the Ledger the Run just wrote (#63), so the end screen and
// oge receipt are the same text. A Run Preflight refused before it existed
// has no Ledger, and its refusal is on stderr.
func (r *renderer) endScreen(res *run.Result) {
	if res == nil || res.Dir == "" || res.Outcome == run.Refused {
		return
	}
	rc, err := receipt.Build(res.Dir)
	if err != nil {
		r.p("")
		r.p("oge: the Receipt can't be read: %v", err)
		return
	}
	r.receipt(rc, res)
}

// receipt prints rc, then what isn't the Run's own record: the
// configuration warning, and that nothing was written yet.
func (r *renderer) receipt(rc *receipt.Receipt, res *run.Result) {
	r.p("")
	fmt.Fprint(r.w, rc.Text(r.paint))
	if r.warn != "" {
		r.p("! %s", r.warn)
	}
	if len(rc.Deliveries) == 0 && (!r.applying || res.Outcome != run.Accepted) {
		r.p("Nothing was written to your repository.")
	}
}

const receiptUsage = `Usage:
  oge receipt [<run>] [--md | --json]
                 a Run's Receipt: why it ended the way it did, from its Ledger
  --md           Markdown, for a PR or an issue
  --json         JSON (schema 1)
<run> is a Run id, or its start from the date on; the default is this
repository's latest Run.
`

// receiptCommand runs oge receipt [run] [--md|--json] (#63).
func receiptCommand(env Env, args []string) int {
	var asMD, asJSON bool
	var pos []string
	fs := flag.NewFlagSet("oge receipt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&asMD, "md", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	for {
		err := fs.Parse(args)
		switch {
		case errors.Is(err, flag.ErrHelp):
			fmt.Fprint(env.Stdout, receiptUsage)
			return ExitOK
		case err != nil:
			fmt.Fprintf(env.Stderr, "oge receipt: %v\n%s", err, receiptUsage)
			return ExitRefused
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
	switch {
	case asMD && asJSON:
		fmt.Fprintf(env.Stderr, "oge receipt: give --md or --json, not both\n%s", receiptUsage)
		return ExitRefused
	case len(pos) > 1:
		fmt.Fprintf(env.Stderr, "oge receipt: give at most one Run (got %d arguments)\n%s", len(pos), receiptUsage)
		return ExitRefused
	}
	id := ""
	if len(pos) == 1 {
		id = pos[0]
	}
	root, err := workspace.RepoRoot(env.Dir)
	if err != nil && id == "" {
		fmt.Fprintln(env.Stderr, "oge: not inside a git repository; give a Run id, or run this inside the Run's repository")
		return ExitRefused
	}
	r, code := findRun(env, root, id)
	if code != ExitOK {
		return code
	}
	rc, err := receipt.Build(r.Dir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: %v\n", err)
		return ExitInternal
	}
	switch {
	case asJSON:
		b, err := rc.JSON()
		if err != nil {
			fmt.Fprintf(env.Stderr, "oge: %v\n", err)
			return ExitInternal
		}
		_, _ = env.Stdout.Write(b)
	case asMD:
		fmt.Fprint(env.Stdout, rc.Markdown())
	default:
		tty := env.StdoutTTY
		if tty == nil {
			tty = env.Interactive
		}
		fmt.Fprint(env.Stdout, rc.Text(receipt.Colors(tty() && colorAllowed(env.Getenv))))
	}
	return ExitOK
}
