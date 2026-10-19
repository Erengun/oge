package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

const resumeUsage = `Usage: oge resume <run> [--unattended] [--plain|-v]
       continue a stopped Run (parked at a Gate, or interrupted) from
       where its Ledger left it
`

// resumeCommand runs oge resume <run>: the stopped Run continues from
// where its Ledger's records prove it stood (ADR-0012).
func resumeCommand(env Env, args []string) int {
	fs := flag.NewFlagSet("oge resume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var f runFlags
	fs.BoolVar(&f.unattended, "unattended", false, "")
	fs.BoolVar(&f.verbose, "v", false, "")
	fs.BoolVar(&f.veryVerbose, "vv", false, "")
	fs.BoolVar(&f.plain, "plain", false, "")

	// Flags may come before or after the Run id.
	var positional []string
	for {
		switch err := fs.Parse(args); {
		case errors.Is(err, flag.ErrHelp):
			fmt.Fprint(env.Stdout, resumeUsage)
			return ExitOK
		case err != nil:
			fmt.Fprintf(env.Stderr, "oge resume: %v\n%s", err, resumeUsage)
			return ExitRefused
		}
		rest := fs.Args()
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			positional = append(positional, rest...)
			break
		}
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	if len(positional) != 1 {
		fmt.Fprintf(env.Stderr, "oge resume needs one Run id\n%s", resumeUsage)
		return ExitRefused
	}
	id := positional[0]
	root, _ := workspace.RepoRoot(env.Dir)
	if root == "" {
		root = env.Dir
	}
	state, code := openState(env, root)
	if code != ExitOK {
		return code
	}
	dir, err := run.FindResumable(state.Private, id)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge resume: %v\n", err)
		return ExitRefused
	}
	frozen, t, err := run.Peek(dir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge resume: %v\n", err)
		return ExitRefused
	}
	if frozen == nil {
		// The frozen Pipeline blob is unreadable; the plain view still
		// prints Resume's refusal where the live view can't draw.
		f.plain = true
	}
	v := selectView(env, f, t, frozen)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := newInterrupts(cancel)
	defer in.watch(os.Interrupt, syscall.SIGTERM)()
	out, err := v.show(ctx, in, func(ctx context.Context, observe func(run.Event)) (*run.Result, error) {
		p := run.Params{
			Agents: env.Agents, State: state, Version: env.Version, Getenv: env.Getenv,
			CacheSeedTemplate: env.CacheSeedTemplate, Observe: observe,
		}
		if !f.unattended {
			p.Gates = viewPort{v}
		}
		return run.Resume(ctx, p, dir)
	})
	if err != nil {
		if run.IsRefused(err) {
			fmt.Fprintf(env.Stderr, "oge resume: %v\n", err)
			return ExitRefused
		}
		return endCode(env, 0, err, true)
	}
	return outcomeCode(env, out)
}
