package delivery

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// Flags name the outcome a non-Accepted delivery is allowed for
// (ADR-0015). There is no generic one.
const (
	FlagOverridden = "overridden"
	FlagRejected   = "rejected"
)

// Authorize refuses a delivery the Run's outcome doesn't allow: Accepted
// with no flag, Overridden only with --overridden, Rejected only with
// --rejected. A flag that names another outcome is refused too.
// TODO(#105): unresolved Ambiguous files in a non-Accepted Candidate need
// an explicit include or exclude here (ADR-0015). An Accepted Candidate
// has none: the Ambiguous-file review resolves each one first (#97).
func Authorize(r *Run, flag string) error {
	switch {
	case r.Candidate == "":
		return refuse("Run %s has no Candidate to deliver", r.ID)
	case run.Live(r.Dir):
		return refuse("Run %s is still running; deliver it once it ends", r.ID)
	case r.Parked:
		return refuse("Run %s is parked at a Gate and hasn't ended, so its Candidate isn't deliverable; oge diff %s shows it", r.ID, r.ID)
	case r.Outcome == "":
		return refuse("Run %s stopped without an outcome, so its Candidate isn't deliverable; oge diff %s shows it", r.ID, r.ID)
	}
	need := map[run.Outcome]string{run.Accepted: "", run.Overridden: FlagOverridden, run.Rejected: FlagRejected}
	want, ok := need[r.Outcome]
	switch {
	case !ok:
		// TODO(#54-decision): a Cancelled or Infeasible Run's Candidate
		// has no delivery flag (ADR-0015 names only Overridden and
		// Rejected), so it is never delivered; oge diff still shows it.
		return refuse("Run %s ended %s; only Accepted, Overridden (--overridden) and Rejected (--rejected) Candidates can be delivered. oge diff %s shows it", r.ID, r.Outcome, r.ID)
	case flag == want:
		return nil
	case want == "":
		return refuse("Run %s is Accepted; --%s is only for a %s Run", r.ID, flag, outcomeOf(flag))
	case flag == "":
		return refuse("Run %s is %s, not Accepted; pass --%s to deliver it anyway", r.ID, r.Outcome, want)
	}
	return refuse("Run %s is %s; --%s is only for a %s Run (use --%s)", r.ID, r.Outcome, flag, outcomeOf(flag), want)
}

func outcomeOf(flag string) string {
	if flag == FlagOverridden {
		return string(run.Overridden)
	}
	return string(run.Rejected)
}

// state is one path as a working tree or commit holds it.
type state struct {
	present bool
	mode    string // 100644, 100755 or 120000
	data    []byte // file content, or a link's target
}

func (s state) same(o state) bool {
	return s.present == o.present && (!s.present || s.mode == o.mode && bytes.Equal(s.data, o.data))
}

// action is one write the plan makes to the working tree.
type action struct {
	path string
	to   state
}

// Plan is what applying a Candidate does to a working tree: computed in
// full, and written only when nothing conflicts.
type Plan struct {
	actions []action
	// Conflicts say, per path, why the Candidate can't land there.
	Conflicts []string
	// Changed is how many paths the Candidate changes; Merged how many of
	// them were merged with edits made since the Snapshot, and Already
	// how many already hold the Candidate's content.
	Changed, Merged, Already int
}

// Writes is how many paths applying the plan changes.
func (p *Plan) Writes() int { return len(p.actions) }

// Deletes is how many of them it deletes.
func (p *Plan) Deletes() int {
	n := 0
	for _, a := range p.actions {
		if !a.to.present {
			n++
		}
	}
	return n
}

// PlanApply works out how the Candidate's change since the Snapshot lands
// on target, the user's working tree as it is now. The Snapshot holds the
// working tree's bytes, uncommitted and untracked work included, so the
// change is three-way per path: the Snapshot is the base, the working
// tree ours, the Candidate theirs. Working-tree bytes meet working-tree
// bytes, so the user's attributes (eol, filters, encodings) never come
// into it.
func PlanApply(r *Run, target string) (*Plan, error) {
	repo := r.repo()
	changes, err := candidateChanges(r)
	if err != nil {
		return nil, err
	}
	p := &Plan{Changed: len(changes)}
	folded := map[string]string{}
	for _, c := range changes {
		if err := safePath(c.Path); err != nil {
			p.Conflicts = append(p.Conflicts, fmt.Sprintf("%s: %v", c.Path, err))
			continue
		}
		if other, ok := folded[fold(c.Path)]; ok {
			p.Conflicts = append(p.Conflicts, fmt.Sprintf("%s: differs from %s only in case or Unicode normalisation, so both may be one file here", c.Path, other))
			continue
		}
		folded[fold(c.Path)] = c.Path
		base, err := blobState(repo, c.OldMode, c.OldOID)
		if err != nil {
			return nil, err
		}
		theirs, err := blobState(repo, c.NewMode, c.NewOID)
		if err != nil {
			return nil, err
		}
		if theirs.mode == "120000" {
			if why := badLink(c.Path, string(theirs.data)); why != "" {
				p.Conflicts = append(p.Conflicts, why)
				continue
			}
		}
		ours, err := treeState(target, c.Path)
		if err != nil {
			p.Conflicts = append(p.Conflicts, fmt.Sprintf("%s: %v", c.Path, err))
			continue
		}
		switch {
		case ours.same(theirs):
			p.Already++
		case ours.same(base):
			p.actions = append(p.actions, action{c.Path, theirs})
		default:
			to, why, err := merge(ours, base, theirs)
			if err != nil {
				return nil, err
			}
			if why != "" {
				p.Conflicts = append(p.Conflicts, c.Path+": "+why)
				continue
			}
			p.Merged++
			p.actions = append(p.actions, action{c.Path, to})
		}
	}
	return p, nil
}

// merge is the three-way merge of a path edited both since the Snapshot
// and by the Candidate. why says why it can't be merged.
func merge(ours, base, theirs state) (state, string, error) {
	switch {
	case !base.present:
		return state{}, "created in your working tree since the Snapshot, and the Candidate creates it differently", nil
	case !ours.present:
		return state{}, "deleted in your working tree since the Snapshot, and the Candidate changes it", nil
	case !theirs.present:
		return state{}, "changed in your working tree since the Snapshot, and the Candidate deletes it", nil
	case ours.mode == "120000" || base.mode == "120000" || theirs.mode == "120000":
		return state{}, "a symlink changed in your working tree since the Snapshot, and the Candidate changes it", nil
	case binary(ours.data) || binary(base.data) || binary(theirs.data):
		return state{}, "a binary file changed in your working tree since the Snapshot, and the Candidate changes it", nil
	}
	mode := theirs.mode
	switch {
	case ours.mode == base.mode:
	case theirs.mode == base.mode:
		mode = ours.mode
	default:
		return state{}, "its mode changed both in your working tree and in the Candidate", nil
	}
	data, clean, err := mergeFile(ours.data, base.data, theirs.data)
	if err != nil {
		return state{}, "", err
	}
	if !clean {
		return state{}, "changed in your working tree since the Snapshot, on the same lines the Candidate changes", nil
	}
	return state{present: true, mode: mode, data: data}, "", nil
}

// mergeFile runs git merge-file on plain files: no repository, so no
// attributes or merge drivers apply.
func mergeFile(ours, base, theirs []byte) ([]byte, bool, error) {
	dir, err := os.MkdirTemp("", "oge-merge-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)
	var paths []string
	for i, b := range [][]byte{ours, base, theirs} {
		p := filepath.Join(dir, fmt.Sprint(i))
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return nil, false, err
		}
		paths = append(paths, p)
	}
	cmd := exec.Command("git", append([]string{"merge-file", "-p", "-q"}, paths...)...)
	cmd.Dir = dir
	cmd.Env = append(scrubGit(os.Environ()), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out, true, nil
	case errors.As(err, &exit) && exit.ExitCode() > 0 && exit.ExitCode() < 128:
		return nil, false, nil // that many conflicts
	}
	return nil, false, fmt.Errorf("git merge-file: %v", err)
}

// binary is git's test: a NUL in the first 8000 bytes.
func binary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0
}

func blobState(repo *workspace.RunRepo, mode, oid string) (state, error) {
	if mode == "" {
		return state{}, nil
	}
	b, err := repo.Blob(oid)
	if err != nil {
		return state{}, err
	}
	return state{present: true, mode: mode, data: b}, nil
}

// safePath refuses a path that could leave the working tree or reach git's
// own directory. The Run repository never holds one; this is a backstop.
func safePath(rel string) error {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") && filepath.Separator == '\\' {
		return errors.New("not a relative path")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return errors.New("not a path Öge writes")
		}
	}
	return nil
}

// treeState reads rel in the working tree at root without following a
// link anywhere on the way: a directory that became a symlink since the
// Snapshot could otherwise lead a write outside the repository.
func treeState(root, rel string) (state, error) {
	parts := strings.Split(rel, "/")
	dir := root
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return state{}, nil
		case err != nil:
			return state{}, err
		case !fi.IsDir():
			return state{}, fmt.Errorf("%s is no longer a directory in your working tree", filepath.ToSlash(strings.TrimPrefix(dir, root+string(filepath.Separator))))
		}
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return state{}, nil
	case err != nil:
		return state{}, err
	case fi.Mode()&os.ModeSymlink != 0:
		t, err := os.Readlink(p)
		if err != nil {
			return state{}, err
		}
		return state{present: true, mode: "120000", data: []byte(filepath.ToSlash(t))}, nil
	case fi.IsDir():
		return state{}, errors.New("is a directory in your working tree")
	case !fi.Mode().IsRegular():
		return state{}, errors.New("is not a regular file in your working tree")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return state{}, err
	}
	mode := "100644"
	if fi.Mode()&0o111 != 0 {
		mode = "100755"
	}
	return state{present: true, mode: mode, data: b}, nil
}

// write makes the plan's changes. Each file is written beside its final
// place and renamed over it.
func (p *Plan) write(root string) error {
	for _, a := range p.actions {
		full := filepath.Join(root, filepath.FromSlash(a.path))
		if !a.to.present {
			if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			removeEmptyParents(root, filepath.Dir(full))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		sweepTemps(full)
		tmp := tempName(full)
		if a.to.mode == "120000" {
			if err := os.Symlink(filepath.FromSlash(string(a.to.data)), tmp); err != nil {
				return err
			}
			if err := os.Rename(tmp, full); err != nil {
				os.Remove(tmp)
				return err
			}
			continue
		}
		// A new file is created with the umask applied, as any tool's
		// would be; a replaced one keeps the user's permission bits, with
		// only the exec bit the Candidate's.
		perm := os.FileMode(0o666)
		if a.to.mode == "100755" {
			perm = 0o777
		}
		existing, err := os.Lstat(full)
		f, err2 := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err2 != nil {
			return err2
		}
		_, werr := f.Write(a.to.data)
		cerr := f.Close()
		if err == nil && existing.Mode().IsRegular() {
			keep := existing.Mode().Perm() &^ 0o111
			if a.to.mode == "100755" {
				keep |= (keep & 0o444) >> 2
			}
			werr = errors.Join(werr, os.Chmod(tmp, keep))
		}
		if err := errors.Join(werr, cerr); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, full); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}

// tempPrefix is the start of the name a file is written under beside
// its final place, before the rename.
func tempPrefix(full string) string {
	return filepath.Join(filepath.Dir(full), "."+filepath.Base(full)+".oge-")
}

func tempName(full string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return tempPrefix(full) + hex.EncodeToString(b[:])
}

// sweepTemps removes what a write killed before its rename (SIGKILL,
// power loss) left beside full.
func sweepTemps(full string) {
	prefix := tempPrefix(full)
	leftovers, _ := filepath.Glob(globEscape(prefix) + "*")
	for _, l := range leftovers {
		// Only names of exactly Öge's shape: the prefix and 12 hex digits.
		suffix := strings.TrimPrefix(l, prefix)
		if _, err := hex.DecodeString(suffix); err != nil || len(suffix) != 12 || strings.ToLower(suffix) != suffix {
			continue
		}
		if fi, err := os.Lstat(l); err == nil && fi.Mode().IsRegular() || err == nil && fi.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(l)
		}
	}
}

func globEscape(s string) string {
	r := strings.NewReplacer(`*`, `\*`, `?`, `\?`, `[`, `\[`, `\`, `\\`)
	if filepath.Separator == '\\' {
		return s
	}
	return r.Replace(s)
}

// badLink says why a Candidate symlink at rel to target is never
// delivered: it leaves the repository, or points into git's own
// directory. It is "" for a link Öge delivers.
func badLink(rel, target string) string {
	switch {
	case linkLeaves(rel, target):
		return fmt.Sprintf("%s: a symlink to %s, outside the repository, which Öge doesn't deliver", rel, target)
	case linkIntoGit(rel, target):
		return fmt.Sprintf("%s: a symlink to %s, into .git, which Öge doesn't deliver", rel, target)
	}
	return ""
}

// linkIntoGit reports whether the link's target resolves under a .git
// directory, any case.
func linkIntoGit(rel, target string) bool {
	for _, part := range strings.Split(path.Clean(path.Join(path.Dir(rel), target)), "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

// linkLeaves reports whether a symlink at rel with target escapes the
// tree: absolute, or climbing out through "..". The Snapshot never copies
// such a link (ADR-0010), and delivery never writes one.
func linkLeaves(rel, target string) bool {
	if target == "" || strings.HasPrefix(target, "/") || filepath.IsAbs(target) {
		return true
	}
	p := path.Clean(path.Join(path.Dir(rel), target))
	return p == ".." || strings.HasPrefix(p, "../")
}

func gitlink(c workspace.Change) bool { return c.NewMode == "160000" || c.OldMode == "160000" }

// candidateChanges is what the Candidate changes since the Snapshot,
// refusing what delivery can't carry.
func candidateChanges(r *Run) ([]workspace.Change, error) {
	changes, err := r.repo().Changes(r.Snapshot, r.Candidate)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		if gitlink(c) {
			return nil, refuse("%s is a submodule (a nested repository) in Candidate %s, which Öge doesn't deliver", c.Path, Short(r.Candidate))
		}
	}
	return changes, nil
}

func removeEmptyParents(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// Applied is what Apply did.
type Applied struct {
	Plan *Plan
}

// Apply applies the Run's final Candidate to the working tree at target
// (ADR-0010, ADR-0015): only when every path lands without conflict, and
// never touching the index, HEAD or any branch. The Delivery is recorded
// before the first write.
func Apply(r *Run, target, flag string) (*Applied, error) {
	if err := Authorize(r, flag); err != nil {
		return nil, err
	}
	if err := sameRepo(r, target); err != nil {
		return nil, err
	}
	unlock, err := lock(r)
	if err != nil {
		return nil, err
	}
	defer unlock()
	p, err := PlanApply(r, target)
	if err != nil {
		return nil, err
	}
	if len(p.Conflicts) > 0 {
		return &Applied{Plan: p}, &ConflictError{Conflicts: p.Conflicts}
	}
	if p.Writes() == 0 {
		return &Applied{Plan: p}, nil
	}
	if err := record(r, map[string]any{"kind": "apply", "candidate": r.Candidate, "outcome": r.Outcome, "flag": flag,
		"files": p.Writes(), "merged": p.Merged, "target": target}); err != nil {
		return nil, err
	}
	if err := p.write(target); err != nil {
		_ = recordFailed(r, err)
		return nil, fmt.Errorf("applying the Candidate: %w; some files may already be written (see git status)", err)
	}
	return &Applied{Plan: p}, nil
}

// ConflictError is an apply refused because the working tree changed in a
// way the Candidate can't land on. Nothing was written.
type ConflictError struct{ Conflicts []string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%d path(s) conflict with changes made since the Snapshot", len(e.Conflicts))
}

// sameRepo refuses a delivery into a repository other than the Run's.
// TODO(#54-decision): delivery targets only the Run's own source
// repository, from inside it; a Run id given elsewhere is refused rather
// than delivered into another checkout.
func sameRepo(r *Run, target string) error {
	if canonical(target) != canonical(r.Source) {
		return refuse("Run %s is of %s; run this inside that repository", r.ID, r.Source)
	}
	return nil
}

// lock keeps two deliveries of one Run from interleaving their Ledger
// records.
func lock(r *Run) (func(), error) {
	p := filepath.Join(r.Dir, "delivery.lock")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, refuse("another delivery of Run %s is in progress (if none is, remove %s)", r.ID, p)
	}
	if err != nil {
		return nil, err
	}
	f.Close()
	return func() { _ = os.Remove(p) }, nil
}

func record(r *Run, data map[string]any) error {
	l, err := ledger.Open(r.Dir)
	if err != nil {
		return err
	}
	defer l.Close()
	return l.Append(RecDelivery, data)
}

func recordFailed(r *Run, cause error) error {
	l, err := ledger.Open(r.Dir)
	if err != nil {
		return err
	}
	defer l.Close()
	return l.Append(RecDeliveryFailed, map[string]any{"error": cause.Error()})
}

// scrubGit drops the user's GIT_* variables that could point a git
// command at another repository, index or object store.
func scrubGit(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES", "GIT_PREFIX":
			continue
		}
		out = append(out, kv)
	}
	return out
}
