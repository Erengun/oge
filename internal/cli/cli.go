// Package cli is oge's command-line entry point: commands, flags, the
// versioned exit codes (ADR-0015) and terminal rendering.
package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
	"golang.org/x/term"
)

// Exit codes used so far (ADR-0015).
const (
	ExitOK       = 0
	ExitInternal = 1
	ExitRefused  = 2 // usage, config or Preflight refusal
	ExitRejected = 3
	ExitInfra    = 11 // Infrastructure stop
)

// Env is everything the CLI takes from its process, so tests can drive it.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Dir            string // working directory
	// Interactive reports whether a human is at a terminal (stdin and stdout).
	Interactive func() bool
	LookPath    func(string) (string, error)
	// Edit opens path in the user's $EDITOR and waits for it to exit.
	Edit    func(path string) error
	GOOS    string
	Version string
	Getenv  func(string) string
	// Agents are the adapters this build can run, by agent name. Release
	// builds register none until the Claude adapter (#44) exists; test
	// builds register the scripted fake (ADR-0017).
	Agents map[string]agent.Adapter
	// CheckGoCache, when set, is a GOCACHE every Check shares instead of a
	// private one. Only tests set it (in-process, or OGE_TEST_SHARED_GOCACHE
	// in -tags ogetest builds); release builds always use a private cache.
	CheckGoCache string
}

// ProcessEnv is the Env of the running process.
func ProcessEnv(version string) Env {
	dir, _ := os.Getwd()
	return Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Dir:         dir,
		Interactive: func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) },
		LookPath:    exec.LookPath,
		Edit:        runEditor,
		GOOS:        runtimeGOOS,
		Version:     version,
		Getenv:      os.Getenv,
	}
}

// isTerminal asks the OS whether f is a terminal. A character device such
// as /dev/null is not one.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func runEditor(path string) error {
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		editor = []string{"vi"}
	}
	cmd := exec.Command(editor[0], append(editor[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// notYet are the spec's other commands. They are reserved so a one-word
// Task can't be mistaken for one once it exists.
// TODO(#39-decision): `oge <command-name>` refuses rather than running a Task
// with that text; quoting a one-word Task that equals a command name would
// need `oge run <word>`.
var notYet = map[string]bool{
	"doctor": true, "init": true, "resume": true, "cancel": true, "status": true,
	"diff": true, "apply": true, "branch": true, "receipt": true,
}

const usage = `Usage:
  oge "<task>" [flags]          run a Task (same as oge run)
  oge run [<task>] [flags]
  oge --version

Run flags:
  --dry-run                show what a Run would do, without starting any agent
                           (a first-run config proposal is still saved on yes)
  --task-file <path|->     read the Task from a file, or stdin with -
  --fast | --blind         mode: Fast (no verifier) or Blind (verifier first); default Standard
  --require <guarantee>    refuse unless the mode gives it (e.g. held-out)
  --confirm                stop at the Result gate before accepting
  --agent <stage>=<agent>[:<model>]   bind a Stage; --agent <agent> binds every Stage
  --implement, --verify <agent>[:<model>]
  --check <command>        add a Check command for this Run
  --tests <glob>           add a test glob for this Run
  --output <glob>          add an output glob for this Run
  --unattended             never prompt
  -v                       show the event stream

Environment:
  OGE_STATE_DIR            where Öge keeps private Run state (default
                           $XDG_STATE_HOME/oge, or the platform's state dir)
`

// Main runs oge with args (without the program name) and returns the exit
// code.
func Main(env Env, args []string) int {
	if len(args) > 0 {
		switch a := args[0]; {
		case a == "--version" || a == "-version":
			fmt.Fprintf(env.Stdout, "oge %s\n", env.Version)
			return ExitOK
		case a == "--help" || a == "-help" || a == "-h" || a == "help":
			fmt.Fprint(env.Stdout, usage)
			return ExitOK
		case a == "run":
			args = args[1:]
		case notYet[a]:
			fmt.Fprintf(env.Stderr, "oge: %s isn't implemented yet\n", a)
			return ExitRefused
		}
	}
	return runCommand(env, args)
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ", ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

type runFlags struct {
	taskFile   string
	dryRun     bool
	unattended bool
	verbose    bool
	o          pipeline.Overrides
}

func parseRunFlags(args []string) (runFlags, []string, error) {
	var f runFlags
	fs := flag.NewFlagSet("oge run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.taskFile, "task-file", "", "")
	fs.BoolVar(&f.dryRun, "dry-run", false, "")
	fs.BoolVar(&f.unattended, "unattended", false, "")
	fs.BoolVar(&f.verbose, "v", false, "")
	fs.BoolVar(&f.o.Fast, "fast", false, "")
	fs.BoolVar(&f.o.Blind, "blind", false, "")
	fs.BoolVar(&f.o.Confirm, "confirm", false, "")
	fs.Var((*multi)(&f.o.Agents), "agent", "")
	fs.StringVar(&f.o.Implement, "implement", "", "")
	fs.StringVar(&f.o.Verify, "verify", "", "")
	fs.Var((*multi)(&f.o.Checks), "check", "")
	fs.Var((*multi)(&f.o.Tests), "tests", "")
	fs.Var((*multi)(&f.o.Outputs), "output", "")
	fs.Var((*multi)(&f.o.Require), "require", "")

	// Flags may come before or after the Task.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return f, nil, err
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
	return f, positional, nil
}

func runCommand(env Env, args []string) int {
	f, positional, err := parseRunFlags(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(env.Stdout, usage)
		return ExitOK
	case err != nil:
		fmt.Fprintf(env.Stderr, "oge: %v\nRun oge --help for usage.\n", err)
		return ExitRefused
	case len(positional) > 1:
		fmt.Fprintf(env.Stderr, "oge: give the Task as one quoted argument (got %d arguments)\n", len(positional))
		return ExitRefused
	case len(positional) == 1 && f.taskFile != "":
		fmt.Fprintln(env.Stderr, "oge: give the Task as an argument or with --task-file, not both")
		return ExitRefused
	}
	if !f.dryRun && env.GOOS == "windows" {
		fmt.Fprintln(env.Stderr, "oge: running Tasks isn't supported on Windows yet; oge run --dry-run still works")
		return ExitRefused
	}
	attended := env.Interactive() && !f.unattended

	root, err := workspace.RepoRoot(env.Dir)
	if err != nil {
		fmt.Fprintln(env.Stderr, "oge: not inside a git repository; Öge runs on a Snapshot of one")
		return ExitRefused
	}

	cfg, cfgData, code := loadConfig(env, root, f, attended)
	if code != ExitOK {
		return code
	}
	installed := installedAgents(env)
	var registered []string
	for name := range env.Agents {
		registered = append(registered, name)
	}
	frozen, probs := pipeline.Resolve(cfg, f.o, installed, registered...)
	if len(probs) > 0 {
		reportProblems(env, "this Run isn't valid", probs)
		return ExitRefused
	}

	t, code := readTask(env, f, positional, attended)
	if code != ExitOK {
		return code
	}

	if !f.dryRun {
		return startRun(env, f, root, t, frozen, cfgData)
	}
	renderDryRun(env.Stdout, t, frozen, cfg != nil)
	return ExitOK
}

func installedAgents(env Env) []string {
	var found []string
	for _, a := range pipeline.KnownAgents {
		if _, err := env.LookPath(a); err == nil {
			found = append(found, a)
		}
	}
	return found
}

func reportProblems(env Env, what string, probs []pipeline.Problem) {
	fmt.Fprintf(env.Stderr, "oge: %s\n", what)
	for _, p := range probs {
		fmt.Fprintf(env.Stderr, "  %s\n", p)
	}
}

// loadConfig reads .oge/oge.toml from the Snapshot. With none, it offers the
// first-run proposal when attended; it returns a nil config when the CLI
// flags alone must carry the Oracle.
func loadConfig(env Env, root string, f runFlags, attended bool) (*pipeline.Config, []byte, int) {
	sf, err := workspace.ReadSnapshotFile(root, pipeline.ConfigPath)
	if errors.Is(err, workspace.ErrNotRegular) {
		fmt.Fprintf(env.Stderr, "oge: %s must be a regular file, not a symlink or directory\n", pipeline.ConfigPath)
		return nil, nil, ExitRefused
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: reading %s from the Snapshot: %v\n", pipeline.ConfigPath, err)
		return nil, nil, ExitInternal
	}
	if sf.Ignored {
		fmt.Fprintf(env.Stderr, "oge: %s exists but git ignores it, so it isn't part of the Snapshot; stop ignoring it\n", pipeline.ConfigPath)
		return nil, nil, ExitRefused
	}
	if !sf.InSnapshot {
		if len(f.o.Checks) > 0 || len(f.o.Tests) > 0 {
			return nil, nil, ExitOK // flags-only Run; Resolve says what is missing
		}
		if !attended {
			fmt.Fprintf(env.Stderr, "oge: no %s in this repository, and no terminal to review a proposed one.\n"+
				"  Run oge in a terminal to review and save a proposal, write %s yourself,\n"+
				"  or pass --check <command> and --tests <glob> for this Run.\n", pipeline.ConfigPath, pipeline.ConfigPath)
			return nil, nil, ExitRefused
		}
		if code := firstRun(env, root); code != ExitOK {
			return nil, nil, code
		}
		if sf, err = workspace.ReadSnapshotFile(root, pipeline.ConfigPath); err != nil || !sf.InSnapshot {
			fmt.Fprintf(env.Stderr, "oge: the saved %s isn't in the Snapshot (is it ignored by git?)\n", pipeline.ConfigPath)
			return nil, nil, ExitRefused
		}
	}
	cfg, probs := pipeline.Load(sf.Data)
	if len(probs) > 0 {
		reportProblems(env, pipeline.ConfigPath+" isn't valid", probs)
		return nil, nil, ExitRefused
	}
	return cfg, sf.Data, ExitOK
}

// firstRun shows the detected config and saves it only on an explicit yes
// (ADR-0019). There is no default answer.
// TODO(#39-decision): the proposal is also offered (and saved on yes) by
// --dry-run, since the file is reviewed and needed for any Run.
func firstRun(env Env, root string) int {
	p, ok := pipeline.Propose(root)
	if !ok {
		fmt.Fprintf(env.Stderr, "oge: no %s, and Öge doesn't recognise this project's toolchain (only Go so far).\n"+
			"  Write %s with at least:\n\n%s\n  or pass --check <command> and --tests <glob> for this Run.\n",
			pipeline.ConfigPath, pipeline.ConfigPath, indent(minimalTemplate, "    "))
		return ExitRefused
	}
	out := env.Stdout
	fmt.Fprintf(out, "No %s yet. Öge needs Check commands and test globs before it can accept work.\n", pipeline.ConfigPath)
	fmt.Fprintf(out, "Proposed from %s:\n\n%s\n", p.DetectedFrom, indent(p.TOML, "  "))
	in := bufio.NewReader(env.Stdin)
	for {
		fmt.Fprintf(out, "Save this as %s? [yes/no] ", pipeline.ConfigPath)
		line, err := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			path := filepath.Join(root, filepath.FromSlash(pipeline.ConfigPath))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				fmt.Fprintf(env.Stderr, "oge: %v\n", err)
				return ExitInternal
			}
			if err := os.WriteFile(path, []byte(p.TOML), 0o644); err != nil {
				fmt.Fprintf(env.Stderr, "oge: %v\n", err)
				return ExitInternal
			}
			fmt.Fprintf(out, "Saved %s. Commit it so every clone uses the same Checks.\n\n", pipeline.ConfigPath)
			return ExitOK
		case "n", "no":
			fmt.Fprintf(env.Stderr, "oge: nothing saved. Write %s yourself, or pass --check and --tests.\n", pipeline.ConfigPath)
			return ExitRefused
		}
		if err != nil {
			fmt.Fprintln(out)
			fmt.Fprintln(env.Stderr, "oge: no answer; nothing saved")
			return ExitRefused
		}
	}
}

const minimalTemplate = `schema = 1

[project]
test_globs = ["<glob matching your test files>"]

[[check.commands]]
run    = "<command that runs your tests>"
report = "junit"   # or "go-test-json"
`

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func readTask(env Env, f runFlags, positional []string, attended bool) (task.Task, int) {
	var text string
	switch {
	case len(positional) == 1:
		text = positional[0]
	case f.taskFile == "-":
		b, err := io.ReadAll(env.Stdin)
		if err != nil {
			fmt.Fprintf(env.Stderr, "oge: reading the Task from stdin: %v\n", err)
			return task.Task{}, ExitRefused
		}
		text = string(b)
	case f.taskFile != "":
		path := f.taskFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(env.Dir, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(env.Stderr, "oge: reading the Task: %v\n", err)
			return task.Task{}, ExitRefused
		}
		text = string(b)
	case attended:
		var code int
		if text, code = editTask(env); code != ExitOK {
			return task.Task{}, code
		}
	default:
		fmt.Fprintln(env.Stderr, `oge: no Task. Give it as an argument (oge "fix the login bug"), with --task-file <path>, or --task-file - for stdin.`)
		return task.Task{}, ExitRefused
	}
	text = task.StripComments(text)
	if task.IsEmpty(text) {
		fmt.Fprintln(env.Stderr, "oge: the Task is empty; nothing to do")
		return task.Task{}, ExitRefused
	}
	return task.Parse(text), ExitOK
}

func editTask(env Env) (string, int) {
	tmp, err := os.CreateTemp("", "oge-task-*.md")
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: %v\n", err)
		return "", ExitInternal
	}
	path := tmp.Name()
	defer os.Remove(path)
	_, werr := tmp.WriteString(task.EditorTemplate)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		fmt.Fprintf(env.Stderr, "oge: writing the Task template: %v\n", errors.Join(werr, cerr))
		return "", ExitInternal
	}
	if err := env.Edit(path); err != nil {
		fmt.Fprintf(env.Stderr, "oge: the editor failed: %v\n", err)
		return "", ExitRefused
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "oge: reading the Task: %v\n", err)
		return "", ExitInternal
	}
	return string(b), ExitOK
}
