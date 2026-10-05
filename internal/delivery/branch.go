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
// and never pushes. Blobs are made by git add in the user's repository,
// so its attributes (eol, filters) normalise them exactly as a commit of
// the applied files would.
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
	if _, err := g.run("", "", "check-ref-format", "--branch", name); err != nil || strings.HasPrefix(name, "-") {
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
	gitDir, err := g.run("", "", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	g.gitDir = strings.TrimSpace(gitDir)
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
	index := filepath.Join(tmp, "index")
	if r.Head != "" {
		if _, err := g.run("", index, "read-tree", r.Head); err != nil {
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
		if _, err := g.run(wt, index, "add", "-A", "-f", "--", "."); err != nil {
			return "", err
		}
		tree, err := g.run(wt, index, "write-tree")
		if err != nil {
			return "", err
		}
		tree = strings.TrimSpace(tree)
		if parent != "" {
			if pt, err := g.run("", "", "rev-parse", parent+"^{tree}"); err == nil && strings.TrimSpace(pt) == tree {
				return "", nil // nothing to commit
			}
		}
		args := []string{"commit-tree", tree, "-m", message}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		out, err := g.run("", "", args...)
		if err != nil {
			return "", fmt.Errorf("making the commit (is your git user.name and user.email set?): %w", err)
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
	cmd.Env = append(scrubGit(os.Environ()), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
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
