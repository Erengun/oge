package cli

import (
	"path/filepath"

	"github.com/erengun/oge/internal/agent"
)

// sessionsInProjects keeps the native sessions whose recorded cwd is in
// one of the Desk's project roots, with Project set to that root. A session
// outside every project is left out (docs/ux/sessions.md). roots are git
// roots, as the rail holds them; cwd is resolved with gitRoot and compared
// with repoKey, so a symlinked or nested cwd still lands in its repo.
func sessionsInProjects(all []agent.NativeSession, roots []string) []agent.NativeSession {
	byKey := map[string]string{}
	for _, r := range roots {
		byKey[repoKey(r)] = r
	}
	rootOf := map[string]string{} // cwd -> project root, "" for none
	var out []agent.NativeSession
	for _, s := range all {
		root, seen := rootOf[s.Cwd]
		if !seen {
			if s.Cwd != "" && filepath.IsAbs(s.Cwd) {
				if g := gitRoot(s.Cwd); g != "" {
					root = byKey[repoKey(g)]
				}
			}
			rootOf[s.Cwd] = root
		}
		if root == "" {
			continue
		}
		s.Project = root
		out = append(out, s)
	}
	return out
}
