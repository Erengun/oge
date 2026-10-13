package claude

// LaunchInfo is a read-only description of a role's Launch profile, for the
// TUI's "agent launched with" view (#180). It is derived from the same
// definition the adapter launches with and hashes, so it cannot drift.
type LaunchInfo struct {
	Version        int
	Hash           string   // the profile hash recorded with each decision
	Tools          []string // the --tools whitelist
	BuiltinPlugins []string // the only plugins a launch may start with
	HomeSecrets    int      // credential locations under $HOME denied to the sandbox
	Effort         string   // the Stage's --effort level, "" for claude's default
	NetworkOff     bool     // network is off unless a Stage turns it on
}

// LaunchInfoFor describes the Launch profile of a role ("implementer" or
// "verifier") under a Stage's effort level.
func LaunchInfoFor(role, effort string) (LaunchInfo, error) {
	p, err := profileFor(role)
	if err != nil {
		return LaunchInfo{}, err
	}
	p.effort = effort
	return LaunchInfo{
		Version:        ProfileVersion,
		Hash:           p.hash(),
		Tools:          append([]string{}, p.tools...),
		BuiltinPlugins: append([]string{}, builtinPlugins...),
		HomeSecrets:    len(homeSecrets),
		Effort:         effort,
		NetworkOff:     true,
	}, nil
}
