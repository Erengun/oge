package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/pipeline"
)

// loginChecker is what the Claude adapter offers to find out which profiles
// have a login (#186). The fake agent and test Envs have none: no claim.
type loginChecker interface {
	LoginStatuses(ctx context.Context, ps []claude.Profile) []claude.Login
}

// loginCache remembers the profiles that were found logged in, by config
// dir, for one session. A profile that was logged out or unknown is asked
// again every time, so a `claude auth login` mid-session is picked up, and a
// logged-in one costs its `claude auth status` (about a second) once.
type loginCache struct {
	mu sync.Mutex
	in map[string]bool
}

func (c *loginCache) statuses(l loginChecker, ps []claude.Profile) []claude.Login {
	out := make([]claude.Login, len(ps))
	var ask []claude.Profile
	var at []int
	if c != nil {
		c.mu.Lock()
	}
	for i, p := range ps {
		if c != nil && c.in[p.Dir] {
			out[i] = claude.LoggedIn
			continue
		}
		ask, at = append(ask, p), append(at, i)
	}
	if c != nil {
		c.mu.Unlock()
	}
	if len(ask) == 0 {
		return out
	}
	got := l.LoginStatuses(context.Background(), ask)
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.in == nil {
			c.in = map[string]bool{}
		}
	}
	for j, st := range got {
		out[at[j]] = st
		if c != nil && st == claude.LoggedIn {
			c.in[ask[j].Dir] = true
		}
	}
	return out
}

func claudeLogins(env Env) loginChecker {
	l, _ := env.Agents[claude.Name].(loginChecker)
	return l
}

// checkLogin asks Claude whether each bound Claude Stage's profile is logged
// in, before any Run state exists, and marks a Stage that isn't. Unless
// markOnly (a dry run, or a home session that shows it) it returns a refusal naming the profiles that are logged in.
// An unknown answer (an older claude, a timeout) makes no claim.
func checkLogin(env Env, f *pipeline.Frozen, extra map[string]string, markOnly, tab bool) string {
	l := claudeLogins(env)
	if l == nil {
		return ""
	}
	getenv := env.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	all := claude.Profiles(getenv("HOME"), getenv, extra)
	byName := map[string]claude.Profile{}
	for _, p := range all {
		byName[p.Name] = p
	}
	var bound []claude.Profile
	seen := map[string]bool{}
	for _, s := range f.Stages {
		if p, ok := byName[s.Profile]; ok && s.Agent == claude.Name && !seen[p.Dir] {
			seen[p.Dir] = true
			bound = append(bound, p)
		}
	}
	if len(bound) == 0 {
		return ""
	}
	status := map[string]claude.Login{}
	for i, st := range env.logins.statuses(l, bound) {
		status[bound[i].Dir] = st
	}
	var out claude.Profile
	for i := range f.Stages {
		s := &f.Stages[i]
		if p, ok := byName[s.Profile]; ok && s.Agent == claude.Name && status[p.Dir] == claude.LoggedOut {
			s.NotLoggedIn = true
			if out.Dir == "" {
				out = p
			}
		}
	}
	if out.Dir == "" || markOnly {
		return ""
	}
	var rest []claude.Profile
	for _, p := range all {
		if _, ok := status[p.Dir]; !ok {
			rest = append(rest, p)
		}
	}
	for i, st := range env.logins.statuses(l, rest) {
		status[rest[i].Dir] = st
	}
	var names []string
	for _, p := range all {
		if status[p.Dir] == claude.LoggedIn {
			names = append(names, p.Label())
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("%s isn't logged in. Run `claude auth login`.", out.Label())
	}
	pick := "Pick one with `--agent claude@<name>`"
	if tab {
		pick = "Pick one with Tab, `--agent claude@<name>`"
	}
	return fmt.Sprintf("%s isn't logged in. Logged in: %s. %s, or run `claude auth login`.",
		out.Label(), strings.Join(names, ", "), pick)
}
