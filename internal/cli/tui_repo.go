package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/erengun/oge/internal/delivery"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
	"github.com/erengun/oge/internal/workspace"
)

// repoCtx is what a Chat's repository binds (docs/ux/desk.md, Projects
// P1): one Desk holds Chats from several repos, and each Chat acts on its
// own one only. The Chats of one repo share it. chatView embeds it, so
// h.root, h.sel and the rest are the focused Chat's repo.
type repoCtx struct {
	// root is the repo's top level: the Snapshot's source, the delivery
	// target and !shell's working directory. title is the header: repo
	// and branch. setup is the setup bar, as lines.
	root  string
	title string
	setup []string
	// sel is what the setup panel edits for this repo, for the session;
	// save writes it to the user config.
	sel  *setupState
	save func() (string, error)
	// resolve reads the config again, for the Task about to start and with
	// the setup panel's choices (sel) added.
	resolve func(*setupState) (*resolved, []string)
	// startFor is how a one-shot Run starts in this repo.
	startFor func(task.Task, *resolved, *run.FollowUp) startFunc
	// hist is the repo's prompt history; shellHist its ! history, apart.
	hist      *history
	shellHist *history
	// deliver is /receipt, /diff, /apply and /branch of a named Run, and
	// runs lists the repository's recent Runs for /runs (#145).
	deliver func(cmd string, args []string) string
	runs    func(n int) ([]*delivery.Run, string)
	// label is /label and the picker's l: it names a Run beside its
	// Ledger (#177).
	label func(id, text string) (string, string)
	// diffOf reads a Run's Candidate as files, for the /diff viewer.
	diffOf func(id string) (string, []diffFile, bool)
	// listFiles reads the tracked files, for the @file picker.
	listFiles func() ([]string, error)
	// hc is the Chat a /new in this repo clones: its open, cut and
	// verdict are bound to root. nil with no Chat (OGE_CHAT=0).
	hc *homeChat
}

// envAt is env working in root: the delivery commands find their repo
// from env.Dir.
func envAt(env Env, root string) Env {
	env.Dir = root
	return env
}

// repoConfig is the config of the repo at root, for the home, and its
// setup panel's state. first is the config already resolved (startHome's);
// nil resolves it here, with Öge's proposal as the Checks when the repo
// has no project config. The problem lines say why it doesn't resolve:
// the Chat opens all the same, and says so under the box.
func repoConfig(env Env, f runFlags, root string, first *resolved) (setup *setupState, res *resolved, problem []string) {
	setup = newSetupState(env, root)
	if first != nil {
		return setup, first, nil
	}
	if noProjectConfig(root, f) && !setup.seedProposal() {
		setup.seedEmpty() // a toolchain Öge doesn't recognise: the section starts empty
	}
	res, problem = resolveHome(env, f, root, setup)
	return setup, res, problem
}

// openRepo is the repoCtx of the repo at root, on its config (repoConfig),
// with u as the view of its one-shot Runs. It is nil when the state root
// refuses the repo; problem says why.
func openRepo(env Env, f runFlags, u view, histDir, root string, setup *setupState, res *resolved) (rc *repoCtx, problem string) {
	var buf strings.Builder
	quiet := env
	quiet.Stderr = &buf
	state, code := openState(quiet, root)
	if code != ExitOK {
		return nil, clean(strings.TrimSpace(strings.TrimPrefix(buf.String(), "oge: ")))
	}
	rc = &repoCtx{
		root: root, title: homeTitle(env, root), sel: setup, save: setup.save,
		resolve:   func(s *setupState) (*resolved, []string) { return resolveHome(env, f, root, s) },
		startFor:  starter(env, root, state, u, true),
		hist:      loadHistory(historyPath(histDir, root)),
		deliver:   deliverer(envAt(env, root)),
		runs:      recentRuns(env, root),
		label:     labeller(env, root),
		diffOf:    diffLoader(envAt(env, root)),
		listFiles: func() ([]string, error) { return workspace.TrackedFiles(root) },
		hc:        homeChatFor(env, root, state),
	}
	if histDir != "" {
		rc.shellHist = loadHistory(shellHistoryFile(histDir, root))
	}
	if res != nil {
		rc.setup = setupLines(res.frozen)
		setup.init(res)
	}
	return rc, ""
}

// repoKey is root as the home tells repos apart: symlinks resolved.
func repoKey(root string) string {
	if r, err := filepath.EvalSymlinks(root); err == nil {
		return r
	}
	return filepath.Clean(root)
}

// projectRoot is the top level of the repo at path, as /new <path> names
// it: ~ is the home directory, and a relative path is from base (the
// focused Chat's repo). problem says, in plain words, why it is none.
func projectRoot(path, base string, getenv func(string) string) (root, problem string) {
	p := path
	if p == "~" || strings.HasPrefix(p, "~/") {
		home := ""
		if getenv != nil {
			home = getenv("HOME")
		}
		if home == "" {
			return "", "/new: no home directory to expand ~ in"
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	switch fi, err := os.Stat(p); {
	case errors.Is(err, os.ErrNotExist):
		return "", "/new: " + clean(path) + " doesn't exist"
	case err != nil:
		return "", "/new: " + clean(err.Error())
	case !fi.IsDir():
		return "", "/new: " + clean(path) + " is not a directory"
	}
	root, err := workspace.RepoRoot(p)
	if err != nil {
		return "", "/new: " + clean(path) + " is not inside a git repository; a Chat runs on a Snapshot of one"
	}
	return root, ""
}
