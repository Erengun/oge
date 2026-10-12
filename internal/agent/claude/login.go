package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Login is whether a Claude profile has a login, as `claude auth status`
// reports it (#186). Unknown makes no claim: an older claude without the
// command, a timeout, or output that isn't the expected JSON.
type Login int

const (
	LoginUnknown Login = iota
	LoggedIn
	LoggedOut
)

// loginTimeout bounds one `claude auth status`: it is a local call, so a
// slow one is a claude that can't answer, not a login that is missing.
var loginTimeout = 5 * time.Second

// parseAuthStatus reads only the loggedIn boolean and the authMethod from
// `claude auth status` output, never anything else (ADR-0006). It returns
// the method for a login, "" otherwise. A claude that is logged out may exit
// non-zero but still prints the JSON, so the exit code is not consulted.
func parseAuthStatus(out []byte) (Login, string) {
	var st struct {
		LoggedIn   *bool  `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &st); err != nil || st.LoggedIn == nil {
		return LoginUnknown, ""
	}
	if !*st.LoggedIn {
		return LoggedOut, ""
	}
	return LoggedIn, st.AuthMethod
}

// LoginStatus asks claude whether profile p is logged in, running
// `claude auth status` with that profile's CLAUDE_CONFIG_DIR (the default
// profile keeps the parent's). It spends no model quota. In an empty config
// dir claude may create .claude.json or backups; that is harmless, because
// discovery only offers dirs that already hold a config.
func (a *Adapter) LoginStatus(ctx context.Context, p Profile) Login {
	path := a.Path
	if path == "" {
		var err error
		if path, err = exec.LookPath("claude"); err != nil {
			return LoginUnknown
		}
	}
	environ := a.Environ
	if environ == nil {
		environ = os.Environ
	}
	var env []string
	for _, kv := range environ() {
		name, _, _ := strings.Cut(kv, "=")
		if stripped(name) || (p.Name != "" && name == "CLAUDE_CONFIG_DIR") {
			continue
		}
		env = append(env, kv)
	}
	if p.Name != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+p.Dir)
	}
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "auth", "status")
	cmd.Env = env
	cmd.WaitDelay = time.Second
	out, _ := cmd.Output() // a non-zero exit may still carry the JSON
	if ctx.Err() != nil {
		return LoginUnknown
	}
	l, _ := parseAuthStatus(out)
	return l
}

// LoginStatuses checks every profile in parallel, in the order given.
func (a *Adapter) LoginStatuses(ctx context.Context, ps []Profile) []Login {
	out := make([]Login, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = a.LoginStatus(ctx, p)
		}()
	}
	wg.Wait()
	return out
}
