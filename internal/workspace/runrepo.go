package workspace

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/ledger"
)

// Preflight lists why the repository at root can't be snapshotted
// (ADR-0010): submodules, Git LFS, an unmerged index, or a merge, rebase,
// cherry-pick or bisect in progress. It writes nothing.
func Preflight(root string) ([]string, error) {
	var why []string
	staged, err := git(root, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, err
	}
	for _, e := range bytes.Split(staged, []byte{0}) {
		if bytes.HasPrefix(e, []byte("160000 ")) {
			why = append(why, "the repository has submodules, which Öge doesn't support yet")
			break
		}
	}
	files, err := snapshotPaths(root)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if out, err := linkLeavesTree(root, f); err != nil {
			return nil, err
		} else if out {
			why = append(why, f+" is a symlink that points outside the repository (absolute or through ..), which Öge doesn't copy; make it relative and inside the repository, or remove it")
		}
	}
	for _, f := range files {
		base := filepath.Base(filepath.FromSlash(f))
		if f == ".gitmodules" && !contains(why, "submodules") {
			why = append(why, "the repository has submodules, which Öge doesn't support yet")
		}
		if base == ".gitattributes" {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
			if err == nil && bytes.Contains(b, []byte("filter=lfs")) {
				why = append(why, "the repository uses Git LFS ("+f+"), which Öge doesn't support yet")
				break
			}
		}
	}
	if u, err := git(root, "ls-files", "-u"); err != nil {
		return nil, err
	} else if len(bytes.TrimSpace(u)) > 0 {
		why = append(why, "the index has unmerged paths; resolve the conflict first")
	}
	for _, op := range []struct{ path, name string }{
		{"MERGE_HEAD", "a merge"}, {"rebase-merge", "a rebase"}, {"rebase-apply", "a rebase"},
		{"CHERRY_PICK_HEAD", "a cherry-pick"}, {"BISECT_LOG", "a bisect"},
	} {
		p, err := git(root, "rev-parse", "--git-path", op.path)
		if err != nil {
			return nil, err
		}
		path := strings.TrimSpace(string(p))
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if _, err := os.Lstat(path); err == nil && !contains(why, op.name) {
			why = append(why, op.name+" is in progress; finish or abort it first")
		}
	}
	return why, nil
}

// linkLeavesTree reports whether the Snapshot path rel under root is a
// symlink whose target is absolute or resolves outside root. A relative,
// dangling link is judged by its text alone.
func linkLeavesTree(root, rel string) (bool, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(p)
	if os.IsNotExist(err) || (err == nil && fi.Mode()&os.ModeSymlink == 0) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	target, err := os.Readlink(p)
	if err != nil {
		return false, err
	}
	if filepath.IsAbs(target) {
		return true, nil
	}
	if outside(filepath.Join(filepath.Dir(filepath.FromSlash(rel)), target)) {
		return true, nil
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false, nil // dangling: the text above stayed inside
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, err
	}
	r, err := filepath.Rel(realRoot, resolved)
	return err != nil || outside(r), nil
}

// outside reports whether a cleaned relative path climbs out of its base.
func outside(rel string) bool {
	rel = filepath.Clean(rel)
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func contains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// snapshotPaths lists the Snapshot's paths: tracked files plus untracked
// files git doesn't ignore.
func snapshotPaths(root string) ([]string, error) {
	out, err := git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// SnapshotInfo describes where a Snapshot came from.
type SnapshotInfo struct {
	Head      string // HEAD commit, "" when the branch has no commits
	Branch    string
	Modified  int
	Untracked int
}

func (s SnapshotInfo) String() string {
	head := "no commits"
	if s.Head != "" {
		head = "HEAD " + s.Head[:7]
		if s.Branch != "" {
			head += " (" + s.Branch + ")"
		}
	}
	var dirty []string
	if s.Modified > 0 {
		dirty = append(dirty, fmt.Sprintf("%d modified", s.Modified))
	}
	if s.Untracked > 0 {
		dirty = append(dirty, fmt.Sprintf("%d untracked", s.Untracked))
	}
	if len(dirty) == 0 {
		return head
	}
	return head + " + " + strings.Join(dirty, ", ")
}

// RunRepo is the Öge-owned Run repository: bare, no remotes, hooks and
// fsmonitor off. Öge makes every commit with its own index.
type RunRepo struct {
	Dir string
}

// Ref names in the Run repository.
const (
	SnapshotRef = "refs/oge/snapshot"
)

// ByteExactAttributes switches off every content-changing git attribute.
const ByteExactAttributes = "* -text -eol -ident -working-tree-encoding -filter -diff -merge\n"

// InitRunRepo creates the Run repository at dir.
func InitRunRepo(dir string) (*RunRepo, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &RunRepo{Dir: dir}
	if _, err := r.git("", "", nil, "init", "-q", "--bare", "--template=", dir); err != nil {
		return nil, err
	}
	for _, kv := range [][2]string{
		{"core.hooksPath", os.DevNull}, {"core.fsmonitor", "false"},
		{"core.autocrlf", "false"}, {"core.fsync", "committed"}, {"core.symlinks", "true"}, {"gc.auto", "0"},
	} {
		if _, err := r.git("", "", nil, "config", kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	// Every git operation on the Run repository is byte-exact: no
	// .gitattributes in a Snapshot or Workspace may convert, filter or
	// re-encode content (info/attributes takes precedence over them).
	if err := os.MkdirAll(filepath.Join(dir, "info"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "info", "attributes"), []byte(ByteExactAttributes), 0o600); err != nil {
		return nil, err
	}
	return r, nil
}

// TakeSnapshot copies the Snapshot of the user's repository at root into
// dir (which becomes the implementer's Workspace) and commits it to the Run
// repository as the Snapshot. After it returns, nothing reads root again.
func (r *RunRepo) TakeSnapshot(root, dir string) (commit string, info SnapshotInfo, err error) {
	paths, err := snapshotPaths(root)
	if err != nil {
		return "", info, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", info, err
	}
	for _, p := range paths {
		if err := copyPath(filepath.Join(root, filepath.FromSlash(p)), filepath.Join(dir, filepath.FromSlash(p)), filepath.FromSlash(p)); err != nil {
			return "", info, err
		}
	}
	info, err = describe(root)
	if err != nil {
		return "", info, err
	}
	commit, err = r.commitDir(dir, "", "Snapshot "+info.String(), true, nil)
	if err != nil {
		return "", info, err
	}
	if _, err := r.git("", "", nil, "update-ref", SnapshotRef, commit); err != nil {
		return "", info, err
	}
	if err := os.WriteFile(filepath.Join(dir, ledger.WorkspaceMarker), nil, 0o600); err != nil {
		return "", info, err
	}
	return commit, info, nil
}

// copyPath copies one Snapshot path, keeping the exec bit and copying a
// symlink as a symlink. A path deleted from the working tree is skipped.
func copyPath(src, dst, rel string) error {
	fi, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		// Preflight refused links that leave the tree; this catches one
		// made since.
		if filepath.IsAbs(target) || outside(filepath.Join(filepath.Dir(rel), target)) {
			return fmt.Errorf("%s is a symlink that points outside the repository", filepath.ToSlash(rel))
		}
		return os.Symlink(target, dst)
	case fi.Mode().IsRegular():
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		mode := os.FileMode(0o644)
		if fi.Mode()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}
	return nil // directories (e.g. an empty untracked one) carry no content
}

func describe(root string) (SnapshotInfo, error) {
	var s SnapshotInfo
	if h, err := git(root, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		s.Head = strings.TrimSpace(string(h))
	}
	if b, err := git(root, "symbolic-ref", "-q", "--short", "HEAD"); err == nil {
		s.Branch = strings.TrimSpace(string(b))
	}
	out, err := git(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return s, err
	}
	for _, e := range strings.Split(string(out), "\x00") {
		switch {
		case strings.HasPrefix(e, "??"):
			s.Untracked++
		case len(e) > 3 && e[2] == ' ':
			s.Modified++
		}
	}
	return s, nil
}

// CommitCandidate commits the Workspace dir as a Candidate on top of
// parent, starting from parent's index so tracked-but-ignored files stay.
func (r *RunRepo) CommitCandidate(dir, parent, ref, message string) (string, error) {
	commit, err := r.commitDir(dir, parent, message, false, nil)
	if err != nil {
		return "", err
	}
	if _, err := r.git("", "", nil, "update-ref", ref, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// commitDir commits dir's contents with a private index. force adds every
// file (the Snapshot copy is already filtered); otherwise the Workspace's
// .gitignore applies on top of parent's tree. fix, when set, may change
// the index before its tree is written.
func (r *RunRepo) commitDir(dir, parent, message string, force bool, fix func(index string) error) (string, error) {
	idx, err := os.CreateTemp(r.Dir, "index-*")
	if err != nil {
		return "", err
	}
	idx.Close()
	os.Remove(idx.Name()) // git wants to create it
	defer os.Remove(idx.Name())
	if parent != "" {
		if _, err := r.git(dir, idx.Name(), nil, "read-tree", parent); err != nil {
			return "", err
		}
	}
	args := []string{"add", "-A"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, "--", ".", ":(exclude)"+ledger.WorkspaceMarker, ":(exclude,glob)**/.git")
	if _, err := r.git(dir, idx.Name(), nil, args...); err != nil {
		return "", &AddError{err}
	}
	if fix != nil {
		if err := fix(idx.Name()); err != nil {
			return "", err
		}
	}
	tree, err := r.git(dir, idx.Name(), nil, "write-tree")
	if err != nil {
		return "", err
	}
	ct := []string{"commit-tree", "--no-gpg-sign", strings.TrimSpace(string(tree)), "-m", message}
	if parent != "" {
		ct = append(ct, "-p", parent)
	}
	out, err := r.git("", "", nil, ct...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Checkout writes commit's files into dir, which must not hold a .git.
func (r *RunRepo) Checkout(commit, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Checkouts can run concurrently (a Check beside the Snapshot
	// control), so each gets its own index.
	tmp, err := os.CreateTemp(r.Dir, "index-checkout-*")
	if err != nil {
		return err
	}
	idx := tmp.Name()
	tmp.Close()
	os.Remove(idx) // git writes it fresh; an empty file isn't an index
	defer os.Remove(idx)
	if _, err := r.git(dir, idx, nil, "read-tree", commit); err != nil {
		return err
	}
	_, err = r.git(dir, idx, nil, "checkout-index", "-a", "-f")
	return err
}

// ChangedFiles lists paths that differ between two commits.
func (r *RunRepo) ChangedFiles(from, to string) ([]string, error) {
	out, err := r.git("", "", nil, "diff-tree", "-r", "--name-only", "-z", "--no-renames", from, to)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// Show returns the content of path at commit, or ok=false if absent.
func (r *RunRepo) Show(commit, path string) (data []byte, ok bool, err error) {
	out, err := r.git("", "", nil, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return nil, false, err
	}
	if len(out) == 0 {
		return nil, false, nil
	}
	b, err := r.git("", "", nil, "cat-file", "blob", commit+":"+path)
	return b, err == nil, err
}

// Files lists every file path in commit.
func (r *RunRepo) Files(commit string) ([]string, error) {
	out, err := r.git("", "", nil, "ls-tree", "-r", "-z", "--name-only", commit)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// Remotes lists the Run repository's remotes (there must be none).
func (r *RunRepo) Remotes() (string, error) {
	out, err := r.git("", "", nil, "remote")
	return strings.TrimSpace(string(out)), err
}

// git runs git against the Run repository, isolated from the user's git
// config so no hook, signing or filter setting applies. workTree and index
// are optional.
func (r *RunRepo) git(workTree, index string, stdin io.Reader, args ...string) ([]byte, error) {
	full := append([]string{"--git-dir=" + r.Dir}, args...)
	if workTree != "" {
		full = append([]string{"--work-tree=" + workTree}, full...)
	}
	if args[0] == "init" {
		full = args
	}
	cmd := exec.Command("git", full...)
	cmd.Dir = r.Dir
	if workTree != "" {
		cmd.Dir = workTree
	}
	cmd.Env = gitEnv()
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// gitEnv is the environment Öge's own git commands run with.
func gitEnv() []string {
	return append(scrubGitEnv(os.Environ()),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0",
		"GIT_AUTHOR_NAME=Öge", "GIT_AUTHOR_EMAIL=oge@localhost",
		"GIT_COMMITTER_NAME=Öge", "GIT_COMMITTER_EMAIL=oge@localhost",
		"GIT_TERMINAL_PROMPT=0")
}

// InitWorkspaceGit gives a Workspace holding the Snapshot a minimal
// Öge-owned .git (ADR-0010, #44), so the agent's git diff and git status
// work: the Snapshot as HEAD and index, copied in by a fetch that writes
// no FETCH_HEAD, no remote, hooks off, no reflog, nothing that names
// Öge's private state. Candidates never take its contents.
func (r *RunRepo) InitWorkspaceGit(ws string) error {
	gd := filepath.Join(ws, ".git")
	run := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"--git-dir=" + gd, "--work-tree=" + ws}, args...)...)
		cmd.Dir, cmd.Env = ws, gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("init", "-q", "--template="); err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{"core.hooksPath", os.DevNull}, {"core.fsmonitor", "false"}, {"core.logAllRefUpdates", "false"},
		{"core.autocrlf", "false"}, {"core.symlinks", "true"}, {"gc.auto", "0"},
	} {
		if err := run("config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	// Öge's own marker isn't the agent's change.
	if err := os.MkdirAll(filepath.Join(gd, "info"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gd, "info", "exclude"), []byte("/"+ledger.WorkspaceMarker+"\n"), 0o644); err != nil {
		return err
	}
	steps := [][]string{
		{"fetch", "-q", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", r.Dir, "+" + SnapshotRef + ":refs/heads/oge"},
		{"symbolic-ref", "HEAD", "refs/heads/oge"},
		{"read-tree", "HEAD"},
		{"update-index", "-q", "--refresh"},
	}
	for _, a := range steps {
		if err := run(a...); err != nil && a[0] != "update-index" {
			return err
		}
	}
	return nil
}

// scrubGitEnv drops the user's GIT_* variables, which could redirect a git
// command to another repository, index or config.
func scrubGitEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}

// AddError is git failing to add a Workspace's files, which the Workspace's
// content can cause (an unreadable file, say).
type AddError struct{ Err error }

func (e *AddError) Error() string { return e.Err.Error() }
func (e *AddError) Unwrap() error { return e.Err }
