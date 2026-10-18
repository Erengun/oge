# Chat first, verify when there's a change

Status: design approved by the user on 2026-10-13. It blocks M1 (#68), and the attended acceptance run (#114) is re-run on it.

## Why

A bare `oge` session where the user typed "yo" ran the whole Standard pipeline:
- preflight;
- implement, with 0 files changed;
- QA;
- a `go test ./...` Check lasting several minutes.

Claude's reply was never shown.

The cause is that every message is a Task, and every Task is a Run. Native Claude Code feels like a conversation, while Öge feels like a CI job. Two rules in `AGENTS.md` say this is the interface to fix:
- trust must earn its overhead;
- a normal task must not feel the trust layer.

## Model

Bare `oge` opens a **Chat**. A Chat owns:
- one Snapshot;
- one Run repository (`State.Private/chats/<id>/repo.git`);
- one implementer Workspace;
- one long-lived implementer Session;
- an append-only Chat log.

Each message is a **turn**, one `Send` to the implementer Session, recorded as a Chat Attempt.

When a turn settles with Promoted or Ambiguous changes (against the last cut, or against the Snapshot if there is none), and no ordinary follow-up is already queued, Öge **cuts** an immutable Candidate. A **Run** is that cut taken through the frozen Pipeline to a Verdict. A Chat hosts many Runs.

One-shot `oge "task"`, `oge run` and `oge resume` are unchanged.

## What the user sees

- **"yo":** the reply streams in within about 1 to 2 seconds. There are no stages, no Receipt and no action row.
- **A question:** tool steps show as dim one-liners, and the answer stays in the transcript. Follow-ups reach the same Session, so it remembers.
- **A fix:** the reply, then `1 file changed · verifying…`, and the composer stays live. When the Verdict lands: `✓ Accepted · 1 file · 412 passed · /apply /diff`. A diff of 20 lines or fewer shows inline.
- **Multi-file work:** queued follow-up messages continue work before cutting; their changes are verified when the conversation settles. A newer cut cancels background verification, and only the newest cut is verified. A superseded Check is cancelled: it yields no Verdict and doesn't count as a Recheck. A failing Verdict prints `✗ 2 criteria failing`, followed by a dim `Öge → agent: …` feedback turn. Esc interrupts the running turn.
- **Delivery:** `/apply` and `/branch` deliver the newest *verified* cut.
  - If newer, unchecked edits exist, Öge asks `newer edits, unchecked: apply verified / wait`.
  - A three-line summary prints, and the full Receipt is behind `/receipt`.
  - Applying doesn't end the Chat.
- **Gates** never interrupt a turn. Tamper and Ambiguous-file Gates show once, at delivery.
  - Keeping every Ambiguous file keeps the Verdict.
  - Dropping one re-cuts and requires a Recheck first (ADR-0010).

## When the trust pipeline runs

- **After every turn:** a hash comparison for scope and tamper. Edits to the Oracle are reverted and recorded silently. The Chat log records the comparison, so "0 files changed, no Check" is Evidence.
  - The check runs while the Session is alive (ADR-0007, as amended). Each turn's commit restores protected paths from the Snapshot, so it can't carry a race.
  - A protected write between the comparison and the commit is a late Tamper event on that turn. A write after the commit is caught by the next turn's check and attributed to that turn. When the Session ends between turns, a final check records one as a late Tamper event on the last turn.
  - After a reopen, `SessionReopened` (pid, pgid) is the process record in place of `ProcessStarted`.
- **No Promoted or Ambiguous change:** no QA, no Check, no Verdict, no Receipt, and no waiting on preflight. Excluded files never count.
- **Changes present:** Öge cuts mechanically; the agent's `Exit:` is only a Claim.
  - The cut freezes the Pipeline, the config and the current Shift+Tab mode.
  - In the background, on disposable clones: the Oracle version, QA in a fresh Session (Standard mode), the Check, the Verdict.
  - Preflight and the Snapshot control start once, when the Chat opens. A cut fails closed until both have finished.
- Verdicts are cached by (Candidate tree, Oracle version).

## Session mechanics

- One `claude -p` stream-json process per Chat:
  - it keeps `--no-session-persistence` and never uses `--resume`;
  - an idle timeout (about 30 minutes) replaces `StageTimeout`;
  - after an idle timeout or a crash, the next message reopens the Session through the #185 re-brief path, which stays as the fallback. The work survives that; the conversation's memory doesn't.
- **Briefing:** Öge builds a light chat Briefing once, without `Exit: done`. Each later user message is sent verbatim and gets its own Briefing-manifest entry (its hash and `session_reused=true`).
- **Nothing enters a running turn:** messages queue until it settles, or Esc interrupts it.
- **Permissions:** the implementer runs in Claude Code's auto permission mode. Its classifier approves safe actions and asks about or blocks risky ones. Öge's hard denies apply on top: paths outside the Workspace, and Oracle and held-out internals. An ask from auto mode reaches the composer as a one-line question.
- **Agents without an auto mode** (OpenCode/ACP) fall back to running read-only commands and the Check commands without asking.
- **Send-back** is an Öge-built feedback turn in the live Session:
  - it carries a count plus criterion ids, never held-out text;
  - it is budgeted at 3 per chain of cuts;
  - it is dropped if a newer cut exists, and queues behind a user turn in flight.
- **QA** always starts fresh, and its text is never streamed. The transcript is never agent context.
- **Snapshot refresh:** Öge retakes the Snapshot, with a Ledger record, after an apply and whenever the Workspace has no unchecked edits. Otherwise it shows one line, `repo changed outside Öge; not merged`. It never merges silently.
  - *Implemented in #277 (after-apply trigger only):* the retake happens only when the user's repo and the Workspace both equal the commit applied (an exact-tree gate), so nothing of the user's is merged and no undelivered edit is judged against the new base. A cut in flight keeps its old base and the retake waits for it. The clean-Workspace trigger needs a Workspace sync and is a follow-up.

## Ledger and Receipt

- **Chat log** (written ahead):
  - `ChatStarted`, then `SnapshotTaken` or `SnapshotRefreshed`;
  - per turn: `TurnStarted` (the message hash), `AttemptEnded` (the events blob, the reply, Usage), and the scope or tamper observation;
  - `HeldOutViewed`. Its `turn` is the last turn started when the user viewed the held-out tests (0 before the first).
- **Each cut gets a Run directory** with today's Ledger, plus:
  - `chat` and the turn range;
  - `follows`, set to the previous cut;
  - a reference to the Chat repo.

  Today's Receipt builder doesn't change.
- **The Task** is the user's turns since the last delivery, verbatim and hashed. Agent text is never summarized into QA's Briefing, so the judged party never writes its own exam.
- **Receipt disclosures:**
  - `Task from N turns; refers to agent text`, when that applies;
  - `HeldOutViewed`;
  - the number of verify and send-back cycles.
- `oge runs` lists cuts only.

## Decisions (user, 2026-10-13)

| # | Decision |
|---|---|
| Scope | The redesign blocks M1; #114 is re-run on the Chat UX. |
| D1 | **Implementer Session reuse** is the default in bare `oge`. It is disclosed in the manifest and the Receipt. Reusing a verifier or QA Session is a hard error. `oge "task"` stays fresh. |
| D2 | **Permissions** use Claude Code's auto mode, with Öge's hard denies on top. Other agents fall back to read-only commands plus the Check commands. |
| D3 | **Verification runs in the background:** a newer cut cancels stale verification, and Verdicts are cached. |
| D4 | **Cuts are automatic:** Öge cuts settled Promoted or Ambiguous changes once queued ordinary follow-ups finish, including when the final follow-up changes nothing. |
| D5 | **A failed Check** gets an automatic feedback turn (a count plus ids), with a budget of 3 per chain. |
| D6 | **Held-out sampling** is disclosed (`held_out_viewed` and the cycle count), with no cap yet. |
| D7 | **The Task** is the user's turns since the last delivery, verbatim and hashed. It is never summarized by the agent. |
| D8 | **Interactive `oge` exits 0** on a clean quit, which is not a trust statement. Only the one-shot commands carry the ADR-0015 contract. |
| D9 | **The term is "Chat".** A Session is still one agent conversation. |
| D10 | **The Snapshot is retaken** after an apply and when the Workspace is clean. Otherwise there is a one-line note, and never a silent merge. |

## Invariants

**Kept:**
- Only a Check issues a Verdict.
- An apply requires a Verdict pinned to the exact Candidate.
- Agents work only in Workspaces.
- Held-out tests never reach the implementer.
- QA starts fresh and never sees the agent's narrative.
- The Ledger is written ahead.
- Gates never resolve themselves.
- Scope and tamper are checked on every turn.
- The one-shot exit-code contract stands.

**Weakened.** Each change is disclosed and lands with the slice that realizes it, together with its ADR amendment:
1. **The implementer Session is reused** in bare `oge` (ADR-0009, ADR-0013).
2. **User text reaches the Session after the Briefing.** Each turn is manifested (ADR-0009).
3. **The freeze moves from Run start to the cut,** and a Chat hosts many Runs (ADR-0007, ADR-0013).
4. **The Chat continues during QA and the Check.** Turns are Chat Attempts outside the Run (ADR-0012).
5. **The Task may refer to agent text that QA can't see.** This is disclosed and never filled in (ADR-0016).
6. **Repeated cuts sample held-out feedback.** This is disclosed and counted, with no cap yet.
7. **The interactive exit code is not a trust statement** (ADR-0015).
8. **Blind mode is one-shot only** (ADR-0016, ADR-0019).
9. **Implementer text streams; QA text never does** (ADR-0022).

## Build slices

The first two ship value on today's `run.go`, before any new architecture. The Chat path stays behind a hidden flag until a change can be cut and verified.

1. **Show the agent's reply** (Sonnet).
   - The implementer's Claim text becomes persistent transcript lines instead of `m.steps` activity, which `model.finish` wipes (`tui.go`, `runcmd.go`).
   - QA text never streams.
   - Lands the ADR-0022 amendment.
2. **A zero-change short-circuit** (Sonnet, Opus review).
   - In `run.go`, after the implementer's Candidate: if it has no Promoted or Ambiguous changes, end with a `Replied` outcome.
   - No QA, no Check, no Verdict, and no wait on preflight. The scope observation is still recorded.
   - The home prints no Receipt and no action row, and drops empty Runs from the follow-up thread.
3. **A multi-turn fake agent** (Sonnet).
   - Turns are read line by line from stdin, with an end-of-turn marker.
   - It covers turns with and without changes, tool Claims, Usage with cost, and a non-fatal interrupt.
4. **Chat Session lifetime** (Opus).
   - `run.Chat` owns one long-lived implementer Session and Workspace, with one `Send` per turn.
   - An idle timeout, the chat Briefing, and per-turn manifest entries.
   - Claude's auto mode: verify the CLI flag, and how asks surface in `-p` stream-json.
   - Tests on the fake agent and on `fakeclaude`.
   - Lands the ADR-0009 and ADR-0013 amendments and the GLOSSARY entry for Chat.
   - Behind the hidden flag.
5. **The Chat log, plus scope and tamper per turn** (Opus).
   - The Chat log, the Chat-owned `repo.git`, and a per-turn hash comparison with revert and record.
   - The #185 re-brief as the fallback after an idle timeout or a crash.
6. **A cut as a Run, blocking first** (Opus).
   - Commit the diff since the last cut as the Candidate, freeze at the cut, and enter the existing walk at QA.
   - The Task comes from the user's turns.
   - One Run directory per cut.
   - Lands the ADR-0007 amendment.
7. **Home wiring, plus permissions** (Sonnet for the wiring, Opus for the policy).
   - `startHome` opens a Chat, and Enter becomes `Send`.
   - Auto mode with Öge's hard denies, and the fallback profile.
   - The wiring and permissions ship together, so the trust layer is never visible on day one.
   - The flag is flipped here.
   - Deferred (#259): the fallback profile ships with the second agent (Codex, #60). Until then an agent without auto mode runs each home message as a one-shot Run, and `OGE_CHAT=0` does the same for claude.
8. **Background verification** (Opus).
   - The composer stays live during QA and the Check.
   - Superseded cuts are cancelled, Verdicts are cached, and a status line shows `verifying…`.
   - Lands the ADR-0012 amendment.
9. **Delivery UX** (Sonnet).
   - A compact Verdict line, and the inline small diff.
   - The newest-verified rule, with the stale-edit prompt.
   - Gates at `/apply`, with a Recheck when a file is dropped.
   - The Snapshot retaken after an apply, and the Receipt behind `/receipt`.
10. **Send-back as a feedback turn,** with its budget and the Receipt disclosures (Opus).

## Risks

1. **The cost of background Checks.** A full `go test ./...` per cut churns when edits come fast. Cancel-on-supersede and the cache are required; package-targeted Checks may need to come earlier.
2. **The Task drifts from QA.** "Yes, do option 2" makes QA verify a guess. Measure the false-reject rate before trusting disclosure alone.
3. **Lost memory and a visible trust layer.**
   - A crash loses the conversation's memory; the re-brief fallback recovers only the work.
   - A permission prompt in chat makes the trust layer visible, so the permissions ship with the home wiring.

## Acceptance (M1 exit, re-run of #114)

Attended, bare `oge`, real Claude, on a scratch clone:
- **A greeting** gets a reply in under 3 seconds, with no stages.
- **A one-file fix:** the composer stays live while the cut verifies in the background, the Verdict shows `✓ Accepted`, and `/apply` lands exactly the checked tree.
- **A follow-up** ("now also add tests") remembers the context and re-cuts.
- **A failing Check** triggers one automatic feedback turn, visible as `Öge → agent: …`.
- **The first reply** to each turn arrives within 2 seconds of native `claude`'s. Time to a Verdict is recorded against ADR-0019's ≤2× gate (informational).
