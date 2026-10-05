package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// runIDShape is what a Run id, or the start of one, looks like; it tells
// a Run from a branch name in oge branch's arguments.
var runIDShape = regexp.MustCompile(`^\d{8}T\d{0,6}(-[0-9a-f]{0,6})?$`)

type deliverFlags struct {
	overridden, rejected bool
	plain                bool
	positional           []string
}

func (f deliverFlags) outcomeFlag() string {
	switch {
	case f.overridden:
		return delivery.FlagOverridden
	case f.rejected:
		return delivery.FlagRejected
	}
	return ""
}

func parseDeliverFlags(cmd string, args []string) (deliverFlags, error) {
	var f deliverFlags
	fs := flag.NewFlagSet("oge "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if cmd != "diff" {
		fs.BoolVar(&f.overridden, "overridden", false, "")
		fs.BoolVar(&f.rejected, "rejected", false, "")
	}
	fs.BoolVar(&f.plain, "plain", false, "")
	for {
		if err := fs.Parse(args); err != nil {
			return f, err
		}
		rest := fs.Args()
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			f.positional = append(f.positional, rest...)
			break
		}
		if len(rest) == 0 {
			break
		}
		f.positional = append(f.positional, rest[0])
		args = rest[1:]
	}
	if f.overridden && f.rejected {
		return f, errors.New("give --overridden or --rejected, not both")
	}
	return f, nil
}

const deliverUsage = `Usage:
  oge diff [<run>]                          show the Candidate's change since the Snapshot
  oge apply [<run>] [--overridden|--rejected]
                                            apply it to your working tree; nothing is committed
  oge branch [<name>] [<run>] [--overridden|--rejected]
                                            make a local branch with it as a commit; never checked out
<run> is a Run id, or its start from the date on (20261005T1204...); the default is
this repository's latest Run. oge branch takes any other argument as the branch name.
`

// deliverCommand runs oge diff, oge apply or oge branch (#54).
func deliverCommand(env Env, cmd string, args []string) int {
	f, err := parseDeliverFlags(cmd, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(env.Stdout, deliverUsage)
		return ExitOK
	case err != nil:
		fmt.Fprintf(env.Stderr, "oge %s: %v\n%s", cmd, err, deliverUsage)
		return ExitRefused
	}
	id, name, err := deliverArgs(cmd, f.positional)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge %s: %v\n%s", cmd, err, deliverUsage)
		return ExitRefused
	}
	root, rootErr := workspace.RepoRoot(env.Dir)
	if rootErr != nil && (cmd != "diff" || id == "") {
		fmt.Fprintln(env.Stderr, "oge: not inside a git repository; run this inside the Run's repository")
		return ExitRefused
	}
	r, code := findRun(env, root, id)
	if code != ExitOK {
		return code
	}
	switch cmd {
	case "diff":
		return diffCommand(env, r, f)
	case "apply":
		return applyCommand(env, r, root, f.outcomeFlag())
	default:
		return branchCommand(env, r, root, name, f.outcomeFlag())
	}
}

// deliverArgs splits the positional arguments into a Run and, for oge
// branch, a branch name. A Run id has a fixed shape, so oge branch takes
// the two in either order.
func deliverArgs(cmd string, pos []string) (id, name string, err error) {
	if cmd != "branch" {
		if len(pos) > 1 {
			return "", "", fmt.Errorf("give at most one Run (got %d arguments)", len(pos))
		}
		if len(pos) == 1 {
			id = pos[0]
		}
		return id, "", nil
	}
	switch len(pos) {
	case 0:
	case 1:
		if runIDShape.MatchString(pos[0]) {
			id = pos[0]
		} else {
			name = pos[0]
		}
	case 2:
		switch {
		case runIDShape.MatchString(pos[1]):
			name, id = pos[0], pos[1]
		case runIDShape.MatchString(pos[0]):
			id, name = pos[0], pos[1]
		default:
			return "", "", fmt.Errorf("neither %q nor %q is a Run id", pos[0], pos[1])
		}
	default:
		return "", "", fmt.Errorf("give a branch name and a Run at most (got %d arguments)", len(pos))
	}
	return id, name, nil
}

func findRun(env Env, root, id string) (*delivery.Run, int) {
	dir, err := ledger.DefaultStateDir(env.Getenv)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: %v\n", err)
		return nil, ExitRefused
	}
	repo := root
	if repo == "" {
		repo = env.Dir
	}
	state, err := ledger.OpenStateRoot(dir, repo)
	var refused *ledger.RefusedError
	switch {
	case errors.As(err, &refused):
		fmt.Fprintf(env.Stderr, "oge: %v\n", err)
		return nil, ExitRefused
	case err != nil:
		fmt.Fprintf(env.Stderr, "oge: opening the state root: %v\n", err)
		return nil, ExitInternal
	}
	r, err := delivery.Find(state.Private, root, id)
	if err != nil {
		return nil, deliveryFailed(env, err)
	}
	return r, ExitOK
}

// deliveryFailed reports err: a refusal is exit 2, anything else Öge's own
// failure.
func deliveryFailed(env Env, err error) int {
	if delivery.IsRefused(err) {
		fmt.Fprintf(env.Stderr, "oge: %v\n", err)
		return ExitRefused
	}
	fmt.Fprintf(env.Stderr, "oge: internal error: %v\n", err)
	return ExitInternal
}

func diffCommand(env Env, r *delivery.Run, f deliverFlags) int {
	patch, err := delivery.Diff(r)
	if err != nil {
		return deliveryFailed(env, err)
	}
	if r.Outcome != run.Accepted {
		fmt.Fprintf(env.Stderr, "oge: Run %s is %s, not Accepted; this is its Candidate %s\n", r.ID, outcomeWord(r), delivery.Short(r.Candidate))
	}
	if len(patch) == 0 {
		fmt.Fprintf(env.Stderr, "oge: Candidate %s changes nothing since the Snapshot\n", delivery.Short(r.Candidate))
		return ExitOK
	}
	if f.plain || !ttyOut(env) {
		if stdoutTTY(env) {
			// A terminal still never gets the agent's bytes raw.
			_, _ = io.WriteString(env.Stdout, delivery.Clean(patch))
			return ExitOK
		}
		// Not a terminal: the patch exactly as it is.
		_, _ = env.Stdout.Write(patch)
		return ExitOK
	}
	if err := showDiff(env, r, patch); err != nil {
		fmt.Fprintf(env.Stderr, "oge: showing the diff: %v\n", err)
		return ExitInternal
	}
	return ExitOK
}

func outcomeWord(r *delivery.Run) string {
	switch {
	case r.Outcome != "":
		return string(r.Outcome)
	case r.Parked:
		return "Parked"
	}
	return "unfinished"
}

// stdoutTTY reports whether output goes to a terminal at all.
func stdoutTTY(env Env) bool {
	if env.StdoutTTY != nil {
		return env.StdoutTTY()
	}
	return env.Interactive()
}

// ttyOut reports whether output goes to a terminal that can show colour
// and a pager.
func ttyOut(env Env) bool {
	t := env.Getenv("TERM")
	return env.Interactive() && t != "" && t != "dumb"
}

// showDiff pages the patch, coloured unless NO_COLOR is set (ADR-0022).
func showDiff(env Env, r *delivery.Run, patch []byte) error {
	st := newStyles(colorAllowed(env.Getenv))
	head := st.dim(fmt.Sprintf("Run %s · %s · Candidate %s against the Snapshot %s", r.ID, outcomeWord(r),
		delivery.Short(r.Candidate), delivery.Short(r.Snapshot)))
	text := head + "\n\n" + colorDiff(delivery.Clean(patch), st)
	if env.Page == nil {
		_, err := io.WriteString(env.Stdout, text)
		return err
	}
	return env.Page(text)
}

// colorDiff paints a patch: file headers bold, hunk headers in the accent,
// added lines green and removed lines red.
func colorDiff(patch string, st styles) string {
	var b strings.Builder
	header := false
	for _, l := range strings.SplitAfter(patch, "\n") {
		body := strings.TrimSuffix(l, "\n")
		nl := l[len(body):]
		switch {
		case strings.HasPrefix(body, "diff --git "):
			header = true
			b.WriteString(st.bold(body))
		case strings.HasPrefix(body, "@@"):
			header = false
			b.WriteString(st.accent(body))
		case header:
			b.WriteString(st.bold(body))
		case strings.HasPrefix(body, "+"):
			b.WriteString(st.ok(body))
		case strings.HasPrefix(body, "-"):
			b.WriteString(st.bad(body))
		default:
			b.WriteString(body)
		}
		b.WriteString(nl)
	}
	return b.String()
}

// pageText shows text in the user's pager: $PAGER, or less, which quits
// at once when the text fits and keeps colour (LESS=FRX unless set).
func pageText(text string) error {
	pager := strings.Fields(os.Getenv("PAGER"))
	if len(pager) == 0 {
		if _, err := exec.LookPath("less"); err != nil {
			_, err := io.WriteString(os.Stdout, text)
			return err
		}
		pager = []string{"less"}
	}
	cmd := exec.Command(pager[0], pager[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(text), os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if os.Getenv("LESS") == "" {
		cmd.Env = append(cmd.Env, "LESS=FRX")
	}
	return cmd.Run()
}

// banner is what a non-Accepted delivery prints first, prominently: the
// outcome, the Candidate and why it isn't Accepted. It never calls the
// result verified (ADR-0015).
func banner(w io.Writer, r *delivery.Run) {
	fmt.Fprintf(w, "%-10s Candidate %s · Run %s · NOT Accepted\n", strings.ToUpper(outcomeWord(r)), delivery.Short(r.Candidate), r.ID)
	for _, why := range r.Why {
		fmt.Fprintf(w, "%-10s %s\n", "", clean(why))
	}
}

func applyCommand(env Env, r *delivery.Run, root, flag string) int {
	if code := authorize(env, r, flag); code != ExitOK {
		return code
	}
	a, err := delivery.Apply(r, root, flag)
	var conflict *delivery.ConflictError
	switch {
	case errors.As(err, &conflict):
		fmt.Fprintf(env.Stderr, "oge: not applied: your working tree changed since the Snapshot in a way Candidate %s can't land on. Nothing was written.\n", delivery.Short(r.Candidate))
		for _, c := range conflict.Conflicts {
			fmt.Fprintf(env.Stderr, "  %s\n", clean(c))
		}
		fmt.Fprintf(env.Stderr, "Commit, stash or undo those edits and run oge apply %s again, or take it as a branch: oge branch %s\n", r.ID, r.ID)
		return ExitRefused
	case err != nil:
		return deliveryFailed(env, err)
	}
	fmt.Fprintln(env.Stdout, appliedLine(r, a.Plan))
	return ExitOK
}

// authorize refuses a delivery the Run's outcome doesn't allow. Either
// way, a non-Accepted Candidate's outcome and why it isn't Accepted are
// shown first.
func authorize(env Env, r *delivery.Run, flag string) int {
	err := delivery.Authorize(r, flag)
	nonAccepted := r.Outcome == run.Overridden || r.Outcome == run.Rejected
	switch {
	case err != nil && nonAccepted:
		banner(env.Stderr, r)
		return deliveryFailed(env, err)
	case err != nil:
		return deliveryFailed(env, err)
	case nonAccepted:
		banner(env.Stdout, r)
		fmt.Fprintf(env.Stdout, "%-10s delivering it anyway, as --%s asks\n", "", flag)
	}
	return ExitOK
}

// appliedLine is apply's one confirmation line.
func appliedLine(r *delivery.Run, p *delivery.Plan) string {
	if p.Writes() == 0 {
		return fmt.Sprintf("Candidate %s of Run %s is already in your working tree; nothing to change.", delivery.Short(r.Candidate), r.ID)
	}
	var parts []string
	if n := p.Writes() - p.Deletes(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", n))
	}
	if n := p.Deletes(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", n))
	}
	if p.Merged > 0 {
		parts = append(parts, fmt.Sprintf("%d merged with your edits", p.Merged))
	}
	return fmt.Sprintf("Applied Candidate %s of Run %s to your working tree: %s. Nothing was committed or staged.",
		delivery.Short(r.Candidate), r.ID, strings.Join(parts, " · "))
}

func branchCommand(env Env, r *delivery.Run, root, name, flag string) int {
	if code := authorize(env, r, flag); code != ExitOK {
		return code
	}
	b, err := delivery.Branch(r, root, name, flag)
	if err != nil {
		return deliveryFailed(env, err)
	}
	on := "the Snapshot's HEAD " + delivery.Short(b.Base)
	if b.Base == "" {
		on = "no parent (the repository had no commits)"
	}
	if b.SnapshotCommit != "" {
		on += ", over a commit of the Snapshot's uncommitted work"
	}
	fmt.Fprintf(env.Stdout, "Created branch %s from Run %s: Candidate %s as commit %s on %s. Not checked out; git switch %s to use it.\n",
		b.Name, r.ID, delivery.Short(r.Candidate), delivery.Short(b.Commit), on, b.Name)
	return ExitOK
}
