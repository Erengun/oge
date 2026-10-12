package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/agent/claude"
	"github.com/erengun/oge/internal/pipeline"
)

// loginChecker is what the Claude adapter offers to find out which profiles
// have a login (#186). The fake agent and test Envs have none: no claim.
type loginChecker interface {
	LoginStatuses(ctx context.Context, ps []claude.Profile) []claude.Login
}

func claudeLogins(env Env) loginChecker {
	l, _ := env.Agents[claude.Name].(loginChecker)
	return l
}

// checkLogin asks Claude whether each bound Claude Stage's profile is logged
// in, before any Run state exists, and marks a Stage that isn't. Unless this
// is a dry run it returns a refusal naming the profiles that are logged in.
// An unknown answer (an older claude, a timeout) makes no claim.
func checkLogin(env Env, f *pipeline.Frozen, extra map[string]string, dryRun bool) string {
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
	ctx := context.Background()
	status := map[string]claude.Login{}
	for i, st := range l.LoginStatuses(ctx, bound) {
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
	if out.Dir == "" || dryRun {
		return ""
	}
	var rest []claude.Profile
	for _, p := range all {
		if _, ok := status[p.Dir]; !ok {
			rest = append(rest, p)
		}
	}
	for i, st := range l.LoginStatuses(ctx, rest) {
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
	return fmt.Sprintf("%s isn't logged in. Logged in: %s. Pick one with Tab, `--agent claude@<name>`, or run `claude auth login`.",
		out.Label(), strings.Join(names, ", "))
}
