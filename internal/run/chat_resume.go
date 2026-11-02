package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/workspace"
)

// Lazy reopen (docs/ux/away.md, build slice 5): a restored Chat has no
// Session. Only a message to it brings one back, and then exactly one,
// through the same reopen() an idle Chat uses, so the turn carries the
// re-brief (A5): the work survives, the conversation's nuance doesn't.

// RecSessionRestored is the Chat log's record that a restored Chat got a
// Session again: what the trust check found, and whether the human typed
// reopen over a change.
const RecSessionRestored = "SessionRestored"

// ErrRestoreRefused is a Chat that can't be resumed: its records or its
// repository don't allow it. It stays read-only.
var ErrRestoreRefused = errors.New("this Chat can't be reopened")

// ResumeParams is what a restored Chat reopens from.
type ResumeParams struct {
	Chat   *RestoredChat
	Agents map[string]agent.Adapter
	State  *ledger.StateRoot
	Getenv func(string) string
	// Config is the .oge/oge.toml the cuts of the Chat are held to.
	Config   []byte
	Advisor  pipeline.AdvisorConfig
	Feedback pipeline.ChatConfig
	// Now is what the settings resolve to now; nil compares nothing.
	Now *pipeline.Frozen
	// Reopen is the human's typed reopen: continue under Now's settings.
	Reopen bool
	// Shown is the change list the human saw on the card when they typed
	// reopen. A different list now means they approved something else.
	Shown       []string
	IdleTimeout time.Duration
}

// resumeLog is what a Chat log says about the Chat, for building it again.
type resumeLog struct {
	frozenBlob string
	source     string
	role       string
	snapshot   string
	turns      int
	texts      map[int]string
	oge        map[int]bool
	advised    map[int]bool
	settled    map[int]bool
	interrupt  map[int]bool
	delivered  int
	launch     map[string]string
	perms      string
	deny       string
	// version is the agent version the last Session reported; "" when it
	// hasn't yet, or the log is older than the field.
	version string
}

func readResume(dir string, blobs *ledger.Blobs) (*resumeLog, error) {
	recs, err := ledger.Replay(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: its log can't be trusted: %v", ErrRestoreRefused, err)
	}
	r := &resumeLog{texts: map[int]string{}, oge: map[int]bool{}, advised: map[int]bool{},
		settled: map[int]bool{}, interrupt: map[int]bool{}}
	pending := ""
	for _, rec := range recs {
		switch rec.Type {
		case RecChatStarted:
			var d struct{ Frozen, Source, Role string }
			_ = json.Unmarshal(rec.Data, &d)
			r.frozenBlob, r.source, r.role = d.Frozen, d.Source, d.Role
		case RecSessionRestored:
			// An accepted reopen made its settings the Chat's own.
			var d struct {
				Frozen   string
				Accepted bool
			}
			// It is the Chat's own only once a Session was launched under
			// it (SessionReopened below): until then the launch profile
			// the trust check compares is still the older one.
			if json.Unmarshal(rec.Data, &d) == nil && d.Accepted && d.Frozen != "" {
				pending = d.Frozen
			} else {
				pending = ""
			}
		case RecSnapshotTaken, RecSnapshotRefreshed:
			var d struct{ Commit string }
			if json.Unmarshal(rec.Data, &d) == nil && d.Commit != "" {
				r.snapshot = d.Commit
			}
		case RecTurnStarted:
			var d struct {
				Turn         int
				By, Text     string
				Consultation string
			}
			if json.Unmarshal(rec.Data, &d) != nil || d.Turn <= 0 {
				continue
			}
			r.turns = max(r.turns, d.Turn)
			r.oge[d.Turn] = d.By == "oge"
			r.advised[d.Turn] = d.By == "oge" && d.Consultation != ""
			if d.Text != "" {
				if b, err := blobs.Get(d.Text); err == nil {
					r.texts[d.Turn] = string(b)
				}
			}
		case RecAgentVersion:
			var d struct {
				AgentVersion string `json:"agent_version"`
			}
			if json.Unmarshal(rec.Data, &d) == nil {
				r.version = d.AgentVersion
			}
		case RecAttemptEnded:
			var d struct{ Turn int }
			if json.Unmarshal(rec.Data, &d) == nil {
				r.settled[d.Turn] = true
			}
		case RecTurnInterrupted:
			var d struct{ Turn int }
			if json.Unmarshal(rec.Data, &d) == nil {
				r.interrupt[d.Turn] = true
			}
		case RecDelivered:
			var d struct{ Turn int }
			if json.Unmarshal(rec.Data, &d) == nil {
				r.delivered = max(r.delivered, d.Turn)
			}
		case RecProcessStarted, RecSessionReopened:
			if rec.Type == RecSessionReopened && pending != "" {
				r.frozenBlob, pending = pending, ""
			}
			r.version = "" // a new process: its own version follows once it opens
			var d struct {
				LaunchProfile map[string]string `json:"launch_profile"`
				Permissions   string
				Deny          string
			}
			if json.Unmarshal(rec.Data, &d) == nil {
				if d.LaunchProfile != nil {
					r.launch = d.LaunchProfile
				}
				r.perms, r.deny = d.Permissions, d.Deny
			}
		}
	}
	if r.frozenBlob == "" || r.snapshot == "" {
		return nil, fmt.Errorf("%w: its log has no start", ErrRestoreRefused)
	}
	return r, nil
}

func denyHash(paths []string) string {
	p := slices.Clone(paths)
	sort.Strings(p)
	sum := sha256.Sum256([]byte(strings.Join(p, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// launchNow is the Launch profile the Chat's Session starts under.
func (c *Chat) launchNow() map[string]string { return launchProfile(c.stage) }

func chatDeny(state *ledger.StateRoot, work string) []string {
	return []string{state.Private, filepath.Join(work, qaDir), filepath.Join(work, cutsDir)}
}

func implementerStage(f *pipeline.Frozen, role string) (pipeline.Stage, bool) {
	for _, s := range f.Stages {
		if s.Role == role {
			return s, true
		}
	}
	return pipeline.Stage{}, false
}

// RestoreChanges is the semantic trust check (A6): what would launch now,
// against the Launch profile, permission mode and hard-deny set the last
// Session of the Chat started under. It is empty when nothing relevant
// changed. A field its record never held (an older log) isn't compared.
// An unrelated oge.toml edit, or a new Öge, is no change.
func RestoreChanges(p ResumeParams) ([]string, error) {
	c := p.Chat
	blobs, err := ledger.OpenBlobs(c.Dir)
	if err != nil {
		return nil, err
	}
	r, err := readResume(c.Dir, blobs)
	if err != nil {
		return nil, err
	}
	frozen, err := loadFrozen(blobs, r.frozenBlob)
	if err != nil {
		return nil, err
	}
	if p.Now != nil {
		frozen = p.Now
	}
	stage, ok := implementerStage(frozen, orDefault(r.role, "implementer"))
	if !ok || p.Agents[stage.Agent] == nil {
		return []string{"the agent " + stage.Agent + " isn't available"}, nil
	}
	var out []string
	now := launchProfile(stage)
	for _, k := range slices.Sorted(maps.Keys(now)) {
		was, had := r.launch[k]
		if r.launch != nil && was != now[k] || r.launch != nil && !had {
			out = append(out, fmt.Sprintf("%s: %s -> %s", k, orDash(was), orDash(now[k])))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(r.launch)) {
		if _, ok := now[k]; !ok {
			out = append(out, fmt.Sprintf("%s: %s -> -", k, r.launch[k]))
		}
	}
	if r.perms != "" && r.perms != string(agent.AutoPermissions) {
		out = append(out, fmt.Sprintf("permission mode: %s -> %s", r.perms, agent.AutoPermissions))
	}
	if r.deny != "" && r.deny != denyHash(chatDeny(p.State, c.Work)) {
		out = append(out, "the hard-deny set changed (Öge's state directories moved)")
	}
	// The agent's version: a different one may behave differently under
	// the same settings. An older log, or an agent that can't say, isn't
	// compared; nothing is launched unless the log has a version.
	if v, ok := p.Agents[stage.Agent].(agent.Versioned); ok && r.version != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		now, err := v.AgentVersion(ctx)
		cancel()
		if now = strings.TrimSpace(now); err == nil && now != "" && now != r.version {
			out = append(out, fmt.Sprintf("agent version: %s -> %s", r.version, now))
		}
	}
	return out, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func orDash(s string) string { return orDefault(s, "-") }

func loadFrozen(blobs *ledger.Blobs, id string) (*pipeline.Frozen, error) {
	b, err := blobs.Get(id)
	if err != nil {
		return nil, fmt.Errorf("%w: its frozen settings are gone", ErrRestoreRefused)
	}
	var f pipeline.Frozen
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w: its frozen settings can't be read", ErrRestoreRefused)
	}
	return &f, nil
}

// Resumed is what ResumeChat did besides opening the Chat.
type Resumed struct {
	Chat *Chat
	// Rebuilt: the Workspace was gone and was checked out again from the
	// last turn ref.
	Rebuilt bool
	// Found: an unsettled turn's Workspace was scope-checked and its
	// edits kept as that turn's commit (TurnInterrupted found_on_restore).
	Found bool
	// Outside: the user's repository isn't what the Chat started from.
	// Nothing was merged; it is the Chat's own Snapshot that stays its base.
	Outside bool
	Changes []string
}

// ResumeChat builds the Chat again from its records, with no Session: its
// first Send reopens one, with the re-brief. The Workspace is rebuilt from
// the last turn ref when it is gone, and a dirty one an unsettled turn left
// gets the scope check (found_on_restore). It starts no agent. The caller
// has run RestoreChanges and, when it found any, the human typed reopen.
func ResumeChat(ctx context.Context, p ResumeParams) (res *Resumed, err error) {
	rc := p.Chat
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv(ChatFlag) == "0" {
		return nil, ErrChatDisabled
	}
	if _, err := os.Stat(rc.Repo); err != nil {
		return nil, fmt.Errorf("%w: the repo %s isn't there", ErrRestoreRefused, rc.Repo)
	}
	blobs, err := ledger.OpenBlobs(rc.Dir)
	if err != nil {
		return nil, err
	}
	rl, err := readResume(rc.Dir, blobs)
	if err != nil {
		return nil, err
	}
	frozen, err := loadFrozen(blobs, rl.frozenBlob)
	if err != nil {
		return nil, err
	}
	changes, err := RestoreChanges(p)
	if err != nil {
		return nil, err
	}
	if len(changes) > 0 {
		if !p.Reopen || p.Now == nil {
			return nil, fmt.Errorf("%w: its settings changed", ErrRestoreRefused)
		}
		if !slices.Equal(changes, p.Shown) {
			return nil, fmt.Errorf("%w: its settings changed again since you were asked", ErrRestoreRefused)
		}
		frozen = p.Now // A6: continue under the new settings, as the human decided
	}
	role := orDefault(rl.role, "implementer")
	stage, ok := implementerStage(frozen, role)
	adapter := p.Agents[stage.Agent]
	if !ok || adapter == nil {
		return nil, fmt.Errorf("%w for %s", ErrNoAdapter, stage.Agent)
	}
	repo, err := workspace.OpenRunRepo(filepath.Join(rc.Dir, "repo.git"))
	if err != nil {
		return nil, fmt.Errorf("%w: its repository is gone", ErrRestoreRefused)
	}
	msgs := make([]string, rl.turns)
	for n := 1; n <= rl.turns; n++ {
		msgs[n-1] = rl.texts[n]
		if rl.texts[n] == "" && !rl.oge[n] {
			return nil, fmt.Errorf("%w: turn %d's message isn't in its log", ErrRestoreRefused, n)
		}
	}
	c := &Chat{ID: rc.ID, role: role, stage: stage, idle: p.IdleTimeout, stop: make(chan struct{}),
		frozen: frozen, adapter: adapter, ctx: ctx, reverted: map[int][]workspace.Revert{},
		source: rc.Repo, config: p.Config, unsent: map[int]bool{}, oge: rl.oge, advised: rl.advised,
		state: p.State, agents: p.Agents, getenv: getenv, advisor: p.Advisor, feedback: p.Feedback,
		Dir: rc.Dir, work: rc.Work, blobs: blobs, repo: repo, Snapshot: rl.snapshot,
		turns: rl.turns, msgs: msgs, delivered: rl.delivered, rebrief: true}
	if c.idle <= 0 {
		c.idle = DefaultIdleTimeout
	}
	// The Session ended with oge: a Send reopens it the way an idle one is.
	c.err = ErrChatLost
	c.pumped = make(chan struct{})
	close(c.pumped)
	c.timer = time.AfterFunc(time.Hour, c.idleOut)
	c.timer.Stop()
	c.Workspace = filepath.Join(c.work, "implement")
	c.last = rl.snapshot
	for n := rl.turns; n >= 1; n-- {
		if commit, ok := repo.RefCommit(fmt.Sprintf("refs/oge/chat/turns/%d", n)); ok {
			c.last, c.lastTurn = commit, n
			break
		}
	}
	res = &Resumed{Chat: c, Changes: changes}
	if c.l, err = ledger.Open(c.Dir); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRestoreRefused, err)
	}
	defer func() {
		if err != nil {
			c.l.Close()
		}
	}()
	if _, serr := os.Stat(c.Workspace); serr != nil {
		if err = repo.Checkout(c.last, c.Workspace); err != nil {
			return nil, err
		}
		if err = repo.InitWorkspaceGit(c.Workspace); err != nil {
			return nil, err
		}
		res.Rebuilt = true
	}
	m, mBlob, err := oracle.NewV0(repo, rl.snapshot, frozen, blobs)
	if err != nil {
		return nil, err
	}
	c.v0, c.protected = mBlob, implementerScope(m, frozen)
	if c.instr, err = repoInstructions(repo, rl.snapshot); err != nil {
		return nil, err
	}
	var checks []string
	for _, ch := range frozen.Checks {
		checks = append(checks, ch.Run)
	}
	cache := filepath.Join(c.work, "cache", stage.Name)
	if err = os.MkdirAll(cache, 0o700); err != nil {
		return nil, err
	}
	c.deny = chatDeny(p.State, c.work)
	c.spec = agent.LaunchSpec{
		Role: role, Model: stage.Model, Effort: stage.Effort, Profile: stage.Profile, Workspace: c.Workspace,
		Network: stage.Network, RunID: c.ID, RepoInstructions: c.instr, CheckCommands: checks,
		DenyRead: c.deny, Cache: cache, Permissions: agent.AutoPermissions,
		StateRoots: []string{p.State.Dir, p.State.Private, p.State.Work},
	}
	// found_on_restore: a turn that never settled may have left edits. They
	// get the scope check, and are kept as that turn's commit: a Checkpoint.
	if n := rl.turns; n > 0 && !rl.settled[n] && !rl.interrupt[n] && !res.Rebuilt {
		a := &Attempt{ID: fmt.Sprintf("chat#%d", n), Role: role}
		before := c.last
		if _, _, err = c.checkScope(a, n); err != nil {
			return nil, err
		}
		if a.Failure != "" {
			// Not recorded as interrupted: the next restore checks again.
			return nil, fmt.Errorf("%w: the stopped turn's edits couldn't be scope-checked: %v", ErrRestoreRefused, a.Failure)
		}
		if err = c.log(RecTurnInterrupted, map[string]any{"chat": c.ID, "turn": n, "why": "found_on_restore", "commit": c.last}); err != nil {
			return nil, err
		}
		// Only edits the turn really left are claimed: an unchanged tree
		// is committed too, but there is nothing to say about it.
		moved, cerr := repo.ChangedFiles(before, c.last)
		if cerr != nil {
			return nil, cerr
		}
		res.Found = len(moved) > 0 || len(a.Reverted) > 0
	}
	res.Outside = c.outsideSince()
	rec := map[string]any{"chat": c.ID, "turns": rl.turns, "workspace_rebuilt": res.Rebuilt, "found_on_restore": res.Found,
		"outside": res.Outside, "changed": append([]string{}, changes...), "accepted": len(changes) > 0}
	if len(changes) > 0 {
		// The accepted settings are the Chat's frozen blob from here on.
		fj, jerr := json.Marshal(frozen)
		if jerr != nil {
			return nil, jerr
		}
		if rec["frozen"], err = blobs.Put(fj); err != nil {
			return nil, err
		}
	}
	if err = c.log(RecSessionRestored, rec); err != nil {
		return nil, err
	}
	c.prepare(m, rl.snapshot)
	c.mu.Lock()
	c.unctx = context.AfterFunc(ctx, func() { c.Close() })
	c.mu.Unlock()
	return res, nil
}

// outsideSince reports whether the user's repository holds something other
// than the Snapshot the Chat started from. Nothing is merged or moved, as
// RefreshOutside: the Chat's own base stays, and the human is told.
func (c *Chat) outsideSince() bool {
	if why, err := workspace.Preflight(c.source); err != nil || len(why) > 0 {
		return false
	}
	scratch := filepath.Join(c.work, "retake")
	defer oracle.RemoveAll(scratch)
	scratchRepo, err := workspace.InitRunRepo(filepath.Join(c.work, "retake.git"))
	if err != nil {
		return false
	}
	defer oracle.RemoveAll(filepath.Join(c.work, "retake.git"))
	commit, _, err := scratchRepo.TakeSnapshot(c.source, scratch)
	if err != nil {
		return false
	}
	// Trees are compared by id; the scratch repo hashes the same bytes.
	have, err := scratchRepo.TreeOf(commit)
	if err != nil {
		return false
	}
	want, err := c.repo.TreeOf(c.Snapshot)
	if err != nil {
		return false
	}
	return have != want
}
