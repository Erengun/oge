package cli

import (
	"os"
	"path/filepath"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/workspace"
)

// projectInfo is where a session's cwd belongs: the main repo it is in (a
// linked worktree folds into the repo it was added to), or the cwd itself
// when it is not a repo.
type projectInfo struct {
	root string // "" when the session recorded no usable cwd
	repo bool   // root is a git work tree
	why  string // when not repo: why the Desk can't hold it
}

// projectResolver looks each cwd up once: a discovery meets about thirty
// distinct ones, and each lookup runs git.
type projectResolver struct{ seen map[string]projectInfo }

func (r *projectResolver) of(cwd string) projectInfo {
	if p, ok := r.seen[cwd]; ok {
		return p
	}
	p := resolveProject(cwd)
	if r.seen == nil {
		r.seen = map[string]projectInfo{}
	}
	r.seen[cwd] = p
	return p
}

func resolveProject(cwd string) projectInfo {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return projectInfo{why: "it recorded no working directory"}
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return projectInfo{root: cwd, why: "that directory is gone"}
	}
	root, err := workspace.MainRoot(cwd)
	if err != nil {
		return projectInfo{root: cwd, why: "not a git repo: adopt from there with claude --resume"}
	}
	return projectInfo{root: root, repo: true}
}

// sessionsInProjects keeps the native sessions that belong to one of the
// Desk's project roots, with Project set to that root. A session in a
// linked worktree belongs to the repo the worktree was added to. A session
// outside every project is left out (docs/ux/sessions.md). roots are git
// roots, as the rail holds them; both sides are resolved to their main repo
// and compared with repoKey, so a symlinked or nested cwd still lands in
// its repo.
func sessionsInProjects(all []agent.NativeSession, roots []string) []agent.NativeSession {
	var res projectResolver
	byKey := map[string]string{}
	for _, r := range roots {
		k := repoKey(r)
		if p := res.of(r); p.repo {
			k = repoKey(p.root)
		}
		byKey[k] = r
	}
	var out []agent.NativeSession
	for _, s := range all {
		p := res.of(s.Cwd)
		if !p.repo {
			continue
		}
		root := byKey[repoKey(p.root)]
		if root == "" {
			continue
		}
		s.Project = root
		out = append(out, s)
	}
	return out
}

// sessionsByProject sets Project on every session to its main repo, or to
// its cwd when that is not a repo, and says for each project the Desk
// can't hold (keyed by repoKey of the project) why not.
func sessionsByProject(all []agent.NativeSession) ([]agent.NativeSession, map[string]string) {
	var res projectResolver
	viewOnly := map[string]string{}
	out := make([]agent.NativeSession, 0, len(all))
	for _, s := range all {
		p := res.of(s.Cwd)
		s.Project = p.root
		if !p.repo {
			viewOnly[repoKey(p.root)] = p.why
		}
		out = append(out, s)
	}
	return out, viewOnly
}
