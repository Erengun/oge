package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/briefing"
	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/redact"
	"github.com/erengun/oge/internal/task"
)

// A Consultation is one request for advice inside a Chat
// (docs/ux/advisor.md): a fresh, read-only advisor Session in a
// disposable clone of the newest settled turn's commit, briefed by Öge,
// asked once and closed. Its advice is a Claim: it never creates
// Evidence, issues a Verdict, chooses an edge or resolves a Gate, and it
// reaches the implementer only as a turn someone sends (slice 2).

// Chat log records of a Consultation, written ahead like a turn's.
const (
	RecConsultationStarted = "ConsultationStarted"
	RecConsultationEnded   = "ConsultationEnded"
	// RecAdviceSent: a Consultation's advice became a turn, verbatim
	// (Öge's) or edited (the user's).
	RecAdviceSent = "AdviceSent"
)

// Consultation outcomes, on ConsultationEnded.
const (
	ConsultAdvised   = "advised"
	ConsultCancelled = "cancelled"
	ConsultFailed    = "failed"
)

// Read-only enforcement, on ConsultationStarted (ADR-0018's rule: never
// called sandbox enforcement where it's only discard).
const (
	// ReadOnlyTools: the adapter's advisor profile withholds every tool
	// that writes, and the clone is discarded too.
	ReadOnlyTools = "tools_withheld"
	// ReadOnlyDiscard: what the Session writes is only discarded.
	ReadOnlyDiscard = "discard_only"
)

// maxAdviceDiff bounds the diff in an Advisor's Briefing; the Advisor
// can read the rest in its clone.
const maxAdviceDiff = 64 << 10

// Advice is a Consultation's answer: a Claim, never checked.
type Advice struct {
	ID   string // the Consultation's
	Text string
	// Turn is the turn whose commit the Advisor read; 0 for the Snapshot.
	Turn                 int
	Agent, Model, Effort string
	CostUSD              *float64
	Duration             time.Duration
}

// ErrAdvisorAgent is why a Consultation was refused: [advisor] names an
// agent other than the Chat's.
var ErrAdvisorAgent = errors.New("only the Chat's own agent advises")

// chatVerdict is a Chat's newest cut Verdict, as an Advisor is told it.
type chatVerdict struct {
	turn     int // the last turn it checked
	run      string
	outcome  Outcome
	failures string // through the held-out filter; empty when it passed
}

// advisorFor is who advises: [advisor] over the implementer's agent, its
// strongest model and DefaultAdvisorEffort (D-A1). The profile is the
// implementer's, so the auth is unchanged.
func (c *Chat) advisorFor() (agent.Adapter, pipeline.Stage, error) {
	st := pipeline.Stage{Name: "advise", Role: "advisor", Network: "off"}
	for _, s := range c.frozen.Stages {
		if s.Role == c.role {
			st.Agent, st.Profile, st.Model = s.Agent, s.Profile, s.Model
		}
	}
	cfg := c.advisor
	if cfg.Agent != "" && cfg.Agent != st.Agent {
		// Cross-vendor advice is opt-in after #60 (docs/ux/advisor.md).
		return nil, st, fmt.Errorf("%w: [advisor] agent %q differs from the Chat's %s; a different agent advises from #292", ErrAdvisorAgent, cfg.Agent, st.Agent)
	}
	ad := c.agents[st.Agent]
	if ad == nil {
		return nil, st, fmt.Errorf("%w for the advisor, %s", ErrNoAdapter, st.Agent)
	}
	if s, ok := ad.(agent.Strongest); ok {
		st.Model = s.StrongestModel()
	}
	if cfg.Model != "" {
		st.Model = cfg.Model
	}
	st.Effort = pipeline.DefaultAdvisorEffort
	if cfg.Effort != "" {
		st.Effort = cfg.Effort
	}
	return ad, st, nil
}

// Consult asks a fresh advisor Session question about the Chat and
// returns its advice (docs/ux/advisor.md, slice 1). It never waits on a
// turn, and the implementer goes on meanwhile. The Advisor reads a
// disposable clone of the newest settled turn's commit, never the live
// Workspace, and its Briefing is Öge's: the question, the Task, the
// diff, the newest Verdict through the held-out filter, and the
// implementer's last reply labelled a Claim (D-A2). Every ask from its
// Session is denied without reaching the human. Cancelling ctx cancels
// the Consultation; its records stay in the Chat log either way.
func (c *Chat) Consult(ctx context.Context, question string) (*Advice, error) {
	// Close waits for every Consultation it lets start, and refuses the
	// rest, so ConsultationEnded never lands on a closed log.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrChatClosed
	}
	c.consults.Add(1)
	c.mu.Unlock()
	defer c.consults.Done()
	ad, st, err := c.advisorFor()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	id := newID()
	brief, items, commit, n := c.adviceBriefing(question)
	dir := filepath.Join(c.work, "advice", id)
	defer oracle.RemoveAll(dir)
	ws := filepath.Join(dir, "repo")
	if err := c.repo.Checkout(commit, ws); err != nil {
		return nil, fmt.Errorf("cloning turn %d for the advisor: %w", n, err)
	}
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, err
	}
	readOnly := ReadOnlyDiscard
	if r, ok := ad.(agent.ReadOnly); ok && r.AdvisorReadOnly() {
		readOnly = ReadOnlyTools
	}
	// The live Workspace holds a turn's in-flight edits: the Advisor
	// reads only its clone.
	denyRead := append(append([]string(nil), c.deny...), c.Workspace)
	if c.instr != "" {
		items = append(items, map[string]any{"item": "repo_instructions", "source": "the Snapshot's CLAUDE.md", "sha256": sha256Hex(c.instr)})
	}
	manifest := map[string]any{"chat": c.ID, "consultation": id, "role": "advisor", "items": items,
		"briefing": sha256Hex(brief), "deny_read": denyRead, "session": "fresh", "session_reused": false}
	mJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	mBlob, err := c.blobs.Put(mJSON)
	if err != nil {
		return nil, err
	}
	bBlob, err := c.blobs.Put(redact.Redact([]byte(brief)))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	last := c.turns
	c.mu.Unlock()
	started := time.Now()
	// TODO(#287-decision): the auth source is known only from the
	// Session's system/init, after the Briefing is sent, so it is on
	// ConsultationEnded, not here.
	if err := c.log(RecConsultationStarted, map[string]any{"chat": c.ID, "consultation": id,
		"trigger": "user", "requested_by": "user", "turn": last, "read_turn": n, "commit": commit,
		"question": sha256Hex(question), "manifest": mBlob, "briefing": bBlob,
		"advisor": launchProfile(st), "permissions": "launch_profile", "read_only": readOnly}); err != nil {
		return nil, err
	}
	end := func(outcome, why string, rec *adviceRecord) error {
		ended := map[string]any{"chat": c.ID, "consultation": id, "outcome": outcome,
			"duration_ms": time.Since(started).Milliseconds()}
		if why != "" {
			ended["failure"] = string(redact.Redact([]byte(why)))
		}
		if rec != nil {
			rec.fill(ended, c)
		}
		return c.log(RecConsultationEnded, ended)
	}

	spec := agent.LaunchSpec{Role: "advisor", Model: st.Model, Effort: st.Effort, Profile: st.Profile,
		Workspace: ws, Network: st.Network, RunID: c.ID + "/" + id, RepoInstructions: c.instr,
		DenyRead: denyRead, Cache: cache,
		StateRoots: c.spec.StateRoots}
	sess, err := ad.Open(ctx, spec)
	if err != nil {
		return nil, errors.Join(err, end(ConsultFailed, "open_failed: "+err.Error(), nil))
	}
	defer sess.Close()
	rec := &adviceRecord{}
	text, outcome, why := c.advise(ctx, sess, st, id, brief, rec)
	if err := end(outcome, why, rec); err != nil {
		return nil, err
	}
	switch outcome {
	case ConsultCancelled:
		return nil, ctx.Err()
	case ConsultFailed:
		return nil, fmt.Errorf("advisor unavailable: %s", why)
	}
	return &Advice{ID: id, Text: text, Turn: n, Agent: st.Agent, Model: st.Model, Effort: st.Effort,
		CostUSD: rec.cost(), Duration: time.Since(started)}, nil
}

// adviceRecord is what a Consultation's Session reported, for its
// ConsultationEnded.
type adviceRecord struct {
	mu      sync.Mutex
	stream  []byte
	text    string
	usage   *agent.Event
	session *agent.SessionInfo
	proc    agent.Process
}

func (r *adviceRecord) cost() *float64 {
	if r.usage == nil {
		return nil
	}
	return r.usage.CostUSD
}

// fill adds the advice blob, Usage, the process and what the Session
// reported of its auth and residue to ended.
func (r *adviceRecord) fill(ended map[string]any, c *Chat) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ended["pid"], ended["pgid"] = r.proc.PID, r.proc.PGID
	if blob, err := c.blobs.Put(redact.Redact(r.stream)); err == nil {
		ended["events"] = blob
	}
	if r.text != "" {
		if blob, err := c.blobs.Put(redact.Redact([]byte(r.text))); err == nil {
			ended["advice"] = blob
		}
	}
	if u := r.usage; u != nil {
		ended["usage"] = map[string]any{"cost_usd": u.CostUSD, "tokens": u.Tokens}
	}
	if s := r.session; s != nil {
		ended["auth_source"] = s.AuthSource
		ended["agent_version"] = s.AgentVersion
		if s.Residue != nil {
			ended["residue"] = s.Residue
		}
	}
}

// advise sends brief to sess and reads its one turn.
func (c *Chat) advise(ctx context.Context, sess agent.Session, st pipeline.Stage, id, brief string, rec *adviceRecord) (text, outcome, why string) {
	rec.proc = sess.Process()
	if err := c.log(RecProcessStarted, map[string]any{"chat": c.ID, "consultation": id,
		"pid": rec.proc.PID, "pgid": rec.proc.PGID, "launch_profile": launchProfile(st)}); err != nil {
		return "", ConsultFailed, "chat_log_failed: " + err.Error()
	}
	if err := sess.Send(agent.Turn{Text: brief}); err != nil {
		return "", ConsultFailed, "send_failed: " + err.Error()
	}
	var reply []string
	settle := func(o, w string) (string, string, string) {
		t := strings.Join(reply, "\n")
		rec.mu.Lock()
		rec.text = t
		rec.mu.Unlock()
		return t, o, w
	}
	for {
		select {
		case <-ctx.Done():
			_ = sess.Interrupt()
			return settle(ConsultCancelled, "")
		case ev, ok := <-sess.Events():
			if !ok {
				return settle(ConsultFailed, "the advisor Session ended")
			}
			rec.mu.Lock()
			if b, err := json.Marshal(ev); err == nil {
				rec.stream = append(append(rec.stream, b...), '\n')
			}
			switch {
			case ev.Kind == agent.Usage:
				rec.usage = &ev
			case ev.Kind == agent.SessionOpened && ev.Session != nil:
				rec.session = ev.Session
			}
			rec.mu.Unlock()
			if ev.Ask != nil && ev.Host != nil && ev.Host.Decision == "ask" {
				// An ask from the advisor never surfaces: it would make
				// the trust layer visible (docs/ux/advisor.md).
				_ = ev.Ask.Answer(agent.Answer{Reason: "Nothing can be approved during a Consultation: answer from what you can read."})
			}
			switch {
			case ev.Kind == agent.Claim && ev.Tool == "":
				reply = append(reply, ev.Text)
			case ev.Kind == agent.TurnSettled && ev.Stop != "":
				return settle(ConsultFailed, ev.Stop)
			case ev.Kind == agent.TurnSettled && ev.Failure != "":
				return settle(ConsultFailed, ev.Failure)
			case ev.Kind == agent.TurnSettled:
				return settle(ConsultAdvised, "")
			}
		}
	}
}

// adviceBriefing is an Advisor's Briefing and its manifest items, and the
// commit its directory is cloned from: the newest settled turn's (n),
// never the Workspace a turn may be writing to.
func (c *Chat) adviceBriefing(question string) (string, []map[string]any, string, int) {
	c.scopeMu.Lock()
	commit, n := c.last, c.lastTurn
	c.scopeMu.Unlock()
	c.supersedeMu.Lock()
	delivered := c.delivered
	c.supersedeMu.Unlock()
	c.mu.Lock()
	var msgs []string
	var turns []int
	for i, m := range c.msgs {
		t := i + 1
		if t <= delivered || c.oge[t] || c.unsent[t] {
			continue // delivered, Öge's (D7) or never sent
		}
		msgs, turns = append(msgs, m), append(turns, t)
	}
	verdict, reply, replyTurn := c.verdict, c.lastReply, c.replyTurn
	c.mu.Unlock()

	a := briefing.Advice{Question: question, Turn: n}
	items := []map[string]any{{"item": "question", "source": "the user's question, verbatim", "sha256": sha256Hex(question)}}
	if len(msgs) > 0 {
		a.Task = task.Task{Text: msgs[0], FollowUps: msgs[1:]}
		items = append(items, map[string]any{"item": "task", "source": "the user's turns since the last delivery, verbatim (D7)",
			"turns": turns, "sha256": sha256Hex(strings.Join(msgs, "\n"))})
	}
	if n > 0 {
		if d, err := c.diff(c.Snapshot, commit); err == nil && len(d) > 0 {
			a.Diff = string(d)
			if len(a.Diff) > maxAdviceDiff {
				a.Diff = a.Diff[:maxAdviceDiff] + "\n… (cut short: read the rest in the current directory)"
			}
			items = append(items, map[string]any{"item": "diff", "source": "the newest turn's commit against the Snapshot",
				"from": c.Snapshot, "to": commit, "turn": n, "sha256": sha256Hex(a.Diff)})
		}
	}
	if verdict != nil {
		a.Verdict, a.VerdictTurn = verdict.failures, verdict.turn
		if a.Verdict == "" {
			a.Verdict = fmt.Sprintf("It ended %s; no Check failure to report.", verdict.outcome)
		}
		items = append(items, map[string]any{"item": "verdict", "run": verdict.run, "turn": verdict.turn,
			"source": "the newest Verdict's failures, through the held-out filter a feedback turn uses", "sha256": sha256Hex(a.Verdict)})
	}
	if strings.TrimSpace(reply) != "" {
		a.Reply, a.ReplyTurn = reply, replyTurn
		items = append(items, map[string]any{"item": "reply", "turn": replyTurn, "label": "claim",
			"source": "agent text (a Claim): the implementer's last reply, verbatim (D-A2)", "sha256": sha256Hex(reply)})
	}
	items = append(items, map[string]any{"item": "workspace", "source": "a disposable clone of the newest settled turn's commit, discarded after",
		"commit": commit, "turn": n})
	return briefing.Advisor(a), items, commit, n
}

// diff is the patch from one commit to another, as delivery shows it:
// text files as text, a binary one as git's one-line stanza. The Chat
// repository marks every path -diff, so each is told which it is.
func (c *Chat) diff(from, to string) ([]byte, error) {
	changes, err := c.repo.Changes(from, to)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, ch := range changes {
		text := true
		for _, oid := range []string{ch.OldOID, ch.NewOID} {
			if oid == "" {
				continue
			}
			b, err := c.repo.Blob(oid)
			// A gitlink's oid is a commit, not a blob: git's stanza.
			text = text && err == nil && bytes.IndexByte(b[:min(len(b), 8000)], 0) < 0
		}
		patch, err := c.repo.PathDiff(from, to, ch.Path, text)
		if err != nil {
			return nil, err
		}
		out.Write(patch)
	}
	return out.Bytes(), nil
}

// Advisor is who a Consultation would ask: the agent and model, for the
// status line shown while it runs.
func (c *Chat) Advisor() (agentName, model string, err error) {
	_, st, err := c.advisorFor()
	return st.Agent, st.Model, err
}
