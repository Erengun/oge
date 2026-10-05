package delivery

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Branched is what Branch made.
type Branched struct {
	Name   string
	Commit string
	// Base is the commit the branch starts from: the user's HEAD when the
	// Snapshot was taken ("" for a repository with no commits).
	Base string
	// SnapshotCommit is the commit holding the Snapshot's uncommitted
	// work under the Candidate, or "" when there was none.
	SnapshotCommit string
}

// DefaultBranch is the branch name Branch uses when none is given.
func DefaultBranch(r *Run) string { return "oge/" + r.ID }

// Branch creates the local branch name in the user's repository at target
// with the Candidate as a commit on top of the Snapshot's HEAD. It never
// checks the branch out, never touches the index, working tree or HEAD,
// and never pushes. Blobs are made by git add with the user's attributes
// and end-of-line settings, so they are normalised as a commit of the
// applied files would be. No filter or hook ever runs: an agent-added
// .gitattributes must not execute anything. Where the user's repository
// has a clean filter for a delivered path, the commit would differ from
// the user's own, so Branch refuses and points to oge apply (ADR-0015).
// The commits are never signed; signing stays the user's act.
//
// TODO(#54-decision): when the Snapshot held uncommitted or untracked
// work, that work is its own commit under the Candidate's, so the
// Candidate commit shows only the agent's change. The user's hooks don't
// run for either commit or the branch ref (ADR-0010: Öge's commits run no
// hooks), and the commits carry the user's own git identity.
func Branch(r *Run, target, name, flag string) (*Branched, error) {
	if err := Authorize(r, flag); err != nil {
		return nil, err
	}
	if err := sameRepo(r, target); err != nil {
		return nil, err
	}
	if name == "" {
		name = DefaultBranch(r)
	}
	g := &userGit{root: target}
	if _, err := g.run("", "", "check-ref-format", "--branch", name); err != nil ||
		strings.HasPrefix(name, "-") || name == "@" || strings.HasPrefix(name, "refs/") {
		return nil, refuse("%q isn't a valid branch name", name)
	}
	if _, err := g.run("", "", "rev-parse", "--verify", "-q", "refs/heads/"+name); err == nil {
		return nil, refuse("branch %s already exists; give another name: oge branch %s <name>", name, r.ID)
	}
	if r.Head != "" {
		if _, err := g.run("", "", "cat-file", "-e", r.Head+"^{commit}"); err != nil {
			return nil, refuse("the Snapshot's HEAD %s is no longer in this repository", Short(r.Head))
		}
	}
	changes, err := candidateChanges(r)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		if c.NewMode == "120000" {
			target, err := r.repo().Blob(c.NewOID)
			if err != nil {
				return nil, err
			}
			if why := badLink(c.Path, string(target)); why != "" {
				return nil, refuse("%s", why)
			}
		}
	}
	ident, err := identity(g)
	if err != nil {
		return nil, err
	}
	unlock, err := lock(r)
	if err != nil {
		return nil, err
	}
	defer unlock()

	tmp, err := os.MkdirTemp("", "oge-branch-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	// The commits are built in a scratch repository that borrows the
	// user's objects, so nothing reaches the user's repository until they
	// are fetched in at the end. Its attributes are the user's with every
	// filter off: an agent-added .gitattributes can't run a command.
	sc, err := scratchRepo(g, filepath.Join(tmp, "scratch.git"))
	if err != nil {
		return nil, err
	}
	sc.env = ident
	index := filepath.Join(tmp, "index")
	if r.Head != "" {
		if _, err := sc.run("", index, "read-tree", r.Head); err != nil {
			return nil, err
		}
	}
	b := &Branched{Name: name, Base: r.Head}
	parent := r.Head
	commit := func(rev, message string) (string, error) {
		wt := filepath.Join(tmp, Short(rev))
		if err := r.repo().Checkout(rev, wt); err != nil {
			return "", err
		}
		// -f: the checkout holds exactly the Run's tree; nothing in it is
		// left out for being ignored here.
		if _, err := sc.run(wt, index, "add", "-A", "-f", "--", "."); err != nil {
			return "", err
		}
		tree, err := sc.run(wt, index, "write-tree")
		if err != nil {
			return "", err
		}
		tree = strings.TrimSpace(tree)
		if parent != "" {
			if pt, err := sc.run("", "", "rev-parse", parent+"^{tree}"); err == nil && strings.TrimSpace(pt) == tree {
				return "", nil // nothing to commit
			}
		}
		// Öge's commits are never signed: signing is the user's act.
		args := []string{"commit-tree", "--no-gpg-sign", tree, "-m", message}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		out, err := sc.run("", "", args...)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	}
	if r.Dirty {
		c, err := commit(r.Snapshot, fmt.Sprintf("Öge Snapshot: uncommitted work under Run %s\n\nThe working tree's uncommitted and untracked changes when Run %s took its Snapshot.\n", r.ID, r.ID))
		if err != nil {
			return nil, err
		}
		if c != "" {
			b.SnapshotCommit, parent = c, c
		}
	}
	c, err := commit(r.Candidate, commitMessage(r))
	if err != nil {
		return nil, err
	}
	if c == "" {
		return nil, refuse("the Candidate changes nothing on top of the Snapshot; there is nothing to branch")
	}
	b.Commit = c
	// Nothing has reached the user's repository yet. If their own git add
	// would run a clean filter on a delivered path, this commit differs
	// from theirs: refuse rather than run the filter or hide it.
	if err := refuseUserFilters(g, sc, r, c); err != nil {
		return nil, err
	}
	if _, err := sc.run("", "", "update-ref", "refs/heads/oge", c); err != nil {
		return nil, err
	}
	// Objects only: no ref, no FETCH_HEAD, no hooks.
	if _, err := g.run("", "", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "fetch", "-q", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", sc.root, "refs/heads/oge"); err != nil {
		return nil, err
	}
	if err := record(r, map[string]any{"kind": "branch", "candidate": r.Candidate, "outcome": r.Outcome, "flag": flag,
		"branch": name, "commit": c, "base": r.Head, "snapshot_commit": b.SnapshotCommit, "target": target}); err != nil {
		return nil, err
	}
	// Create-only: the empty old value makes this fail if the branch
	// appeared meanwhile.
	if _, err := g.run("", "", "update-ref", "-m", "oge branch "+r.ID, "refs/heads/"+name, c, ""); err != nil {
		_ = recordFailed(r, err)
		return nil, err
	}
	return b, nil
}

// commitMessage names the Run and its outcome; it never calls the result
// verified (ADR-0015).
func commitMessage(r *Run) string {
	title := r.Title
	if title == "" {
		title = "Candidate " + Short(r.Candidate)
	}
	return fmt.Sprintf("%s\n\nÖge Run %s · %s · Candidate %s · Oracle v%d\n", title, r.ID, r.Outcome, Short(r.Candidate), r.Oracle)
}

// userGit runs git in the user's repository with the user's own config,
// so their attributes and identity apply, but no hooks or fsmonitor.
type userGit struct {
	root, gitDir string
	env          []string // added to the environment
}

func (g *userGit) run(workTree, index string, args ...string) (string, error) {
	full := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}
	dir := g.root
	if workTree != "" {
		full = append(full, "--git-dir="+g.gitDir, "--work-tree="+workTree)
		dir = workTree
	}
	cmd := exec.Command("git", append(full, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(scrubGit(os.Environ()), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"), g.env...)
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// identity is the user's own author and committer, as git would use them
// for a commit of theirs.
func identity(g *userGit) ([]string, error) {
	var env []string
	for _, who := range []string{"AUTHOR", "COMMITTER"} {
		out, err := g.run("", "", "var", "GIT_"+who+"_IDENT")
		if err != nil {
			return nil, refuse("git has no identity to commit with (%v); set user.name and user.email", err)
		}
		name, rest, ok := strings.Cut(strings.TrimSpace(out), " <")
		email, _, ok2 := strings.Cut(rest, ">")
		if !ok || !ok2 {
			return nil, fmt.Errorf("git var GIT_%s_IDENT: %q", who, out)
		}
		env = append(env, "GIT_"+who+"_NAME="+name, "GIT_"+who+"_EMAIL="+email)
	}
	return env, nil
}

// scratchRepo makes a bare repository at dir whose objects fall back to
// the user's, with the user's info/attributes plus "* -filter", and the
// user's end-of-line settings.
func scratchRepo(g *userGit, dir string) (*userGit, error) {
	sc := &userGit{root: dir, gitDir: dir}
	if out, err := exec.Command("git", "init", "-q", "--bare", "--template=", dir).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git init: %v: %s", err, out)
	}
	objects, err := g.run("", "", "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "objects", "info"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "objects", "info", "alternates"), []byte(strings.TrimSpace(objects)+"\n"), 0o600); err != nil {
		return nil, err
	}
	attrs := ""
	if p, err := g.run("", "", "rev-parse", "--path-format=absolute", "--git-path", "info/attributes"); err == nil {
		if b, err := os.ReadFile(strings.TrimSpace(p)); err == nil {
			attrs = string(b) + "\n"
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "info"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "info", "attributes"), []byte(attrs+"* -filter\n"), 0o600); err != nil {
		return nil, err
	}
	// What else shapes the user's git add: their end-of-line settings,
	// a repository-local attributes file (its filters still count only
	// in the refusal check; "* -filter" above outranks it) and whether
	// the exec bit is tracked.
	for _, key := range []string{"core.autocrlf", "core.eol", "core.safecrlf", "core.checkRoundtripEncoding", "core.attributesFile", "core.fileMode"} {
		get := []string{"config", "--get", key}
		if key == "core.attributesFile" {
			get = []string{"config", "--type=path", "--get", key} // ~ expanded
		}
		if v, err := g.run("", "", get...); err == nil {
			if _, err := sc.run("", "", "config", key, strings.TrimSpace(v)); err != nil {
				return nil, err
			}
		}
	}
	return sc, nil
}

// refuseUserFilters refuses the branch when a delivered path (one the
// branch's commits add or change on top of the Snapshot's HEAD) has a
// filter attribute in the user's repository whose driver has a clean or
// process command configured. Nothing is executed to find out.
func refuseUserFilters(g, sc *userGit, r *Run, commit string) error {
	base := r.Head
	if base == "" {
		empty, err := sc.runIn("", "mktree")
		if err != nil {
			return err
		}
		base = strings.TrimSpace(empty)
	}
	out, err := sc.run("", "", "diff-tree", "-r", "-z", "--name-only", "--no-renames", "--diff-filter=d", base, commit)
	if err != nil {
		return err
	}
	paths := strings.TrimRight(out, "\x00")
	if paths == "" {
		return nil
	}
	attrs, err := g.runIn(paths+"\x00", "check-attr", "-z", "--stdin", "filter")
	if err != nil {
		return err
	}
	f := strings.Split(attrs, "\x00")
	active := map[string]bool{}
	var hit []string
	for i := 0; i+2 < len(f); i += 3 {
		path, value := f[i], f[i+2]
		if value == "unspecified" || value == "unset" || value == "set" || value == "" {
			continue
		}
		on, seen := active[value]
		if !seen {
			for _, key := range []string{"clean", "process"} {
				if v, err := g.run("", "", "config", "--get", "filter."+value+"."+key); err == nil && strings.TrimSpace(v) != "" {
					on = true
				}
			}
			active[value] = on
		}
		if on {
			hit = append(hit, path)
		}
	}
	if len(hit) == 0 {
		return nil
	}
	shown := hit
	if len(shown) > 5 {
		shown = append(shown[:5:5], fmt.Sprintf("and %d more", len(hit)-5))
	}
	return refuse("oge branch can't safely reproduce this repository's normal git transformation for %s without executing user-configured filters; use oge apply %s and commit it yourself",
		strings.Join(shown, ", "), r.ID)
}

// runIn is run with stdin, in the user's repository.
func (g *userGit) runIn(stdin string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = g.root
	cmd.Env = append(append(scrubGit(os.Environ()), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"), g.env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}
