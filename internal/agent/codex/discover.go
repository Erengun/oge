package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/proc"
)

// listPage is the page size asked of thread/list; maxPages stops a server
// that never ends its cursor.
const (
	listPage = 50
	maxPages = 40
)

// methodNotFound is the JSON-RPC code of a server without the method.
// Real codex rejects an unknown method while parsing the request, so it
// answers -32600 "unknown variant `thread/list`" instead (codex-cli 0.163.0).
const methodNotFound = "(-32601)"

// noThreadList reports an error from a codex that lacks thread/list.
func noThreadList(err error) bool {
	m := err.Error()
	return strings.HasSuffix(m, methodNotFound) || strings.Contains(m, "unknown variant `thread/list`")
}

// thread is the part of a thread/list entry Öge reads. The preview is
// the user's own text and is deliberately not decoded, and neither is
// status, which is relative to the app-server that answered.
type thread struct {
	ID             string          `json:"id"`
	Name           *string         `json:"name"`
	Cwd            string          `json:"cwd"`
	UpdatedAt      int64           `json:"updatedAt"`
	Path           *string         `json:"path"`
	Source         json.RawMessage `json:"source"`
	ParentThreadID *string         `json:"parentThreadId"`
}

// subAgent reports a thread the server should not have listed under
// sourceKinds cli, vscode and appServer.
func (t thread) subAgent() bool {
	if t.ParentThreadID != nil && *t.ParentThreadID != "" {
		return true
	}
	return strings.Contains(string(t.Source), `"subAgent`)
}

// ListSessions discovers Codex's native sessions for the given project
// directories through a short-lived app-server that starts no thread and
// no turn: thread/list per cwd, newest first, paged, only the cli, vscode
// and appServer sources (no sub-agent threads). The result is newest
// first. Discovery never fails its caller: when codex is missing, too old
// to have thread/list, or errs, the sessions found so far come back with
// a one-line reason, empty otherwise.
func (a *Adapter) ListSessions(ctx context.Context, cwds []string) ([]agent.NativeSession, string) {
	path := a.Path
	if path == "" {
		var err error
		if path, err = exec.LookPath("codex"); err != nil {
			return nil, "codex isn't installed, so its sessions aren't listed"
		}
	}
	environ := a.Environ
	if environ == nil {
		environ = os.Environ
	}
	cmd := exec.Command(path, "app-server", "-c", "notify=[]")
	cmd.Env = environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "codex sessions aren't listed: " + clip(err.Error())
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "codex sessions aren't listed: " + clip(err.Error())
	}
	s := &session{
		cmd: cmd, stdin: stdin, events: make(chan agent.Event, 256), stop: make(chan struct{}), exited: make(chan struct{}),
		pending: map[int]chan reply{}, items: map[string][]string{}, prof: newProfile(agent.LaunchSpec{}),
		timeout: a.Timeout, grace: a.CloseGrace,
	}
	if s.timeout == 0 {
		s.timeout = requestTimeout
	}
	if s.grace == 0 {
		s.grace = closeGrace
	}
	cmd.Stderr = &s.stderr
	if err := proc.Start(cmd); err != nil {
		return nil, "codex sessions aren't listed: " + clip(err.Error())
	}
	s.process = agent.Process{PID: cmd.Process.Pid, PGID: proc.Group(cmd)}
	go s.read(stdout)
	defer s.Close()
	if err := s.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "oge", "title": "Öge", "version": "0"}}, nil); err != nil {
		return nil, "codex sessions aren't listed: " + clip(err.Error())
	}
	if err := s.notify("initialized", map[string]any{}); err != nil {
		return nil, "codex sessions aren't listed: " + clip(err.Error())
	}
	var out []agent.NativeSession
	seen := map[string]bool{}
	for _, cwd := range cwds {
		cursor := ""
		for page := 0; page < maxPages; page++ {
			params := map[string]any{
				"cwd": cwd, "limit": listPage, "archived": false,
				"sortKey": "updated_at", "sortDirection": "desc",
				"sourceKinds": []string{"cli", "vscode", "appServer"},
			}
			if cursor != "" {
				params["cursor"] = cursor
			}
			var res struct {
				Data       []thread `json:"data"`
				NextCursor *string  `json:"nextCursor"`
			}
			if err := s.call(ctx, "thread/list", params, &res); err != nil {
				reason := "codex sessions aren't listed: " + clip(err.Error())
				if noThreadList(err) {
					reason = "this codex has no thread/list, so its sessions aren't listed; update codex"
				}
				sortSessions(out)
				return out, reason
			}
			for _, t := range res.Data {
				if t.ID == "" || seen[t.ID] || t.subAgent() {
					continue
				}
				seen[t.ID] = true
				out = append(out, native(t))
			}
			if res.NextCursor == nil || *res.NextCursor == "" || len(res.Data) == 0 {
				break
			}
			cursor = *res.NextCursor
		}
	}
	sortSessions(out)
	return out, ""
}

func native(t thread) agent.NativeSession {
	n := agent.NativeSession{Provider: "codex", ID: t.ID, Cwd: t.Cwd}
	if t.Name != nil {
		n.Title = clip(*t.Name)
	}
	n.LastActive = time.Unix(t.UpdatedAt, 0)
	if t.Path != nil && *t.Path != "" {
		n.Path = *t.Path
		if fi, err := os.Stat(n.Path); err == nil {
			n.Written = fi.ModTime()
		}
	}
	return n
}

func sortSessions(s []agent.NativeSession) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].LastActive.After(s[j].LastActive) })
}
