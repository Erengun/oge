# Away: the Desk while you're gone

Status: approved (user, 2026-11-01; decisions A1–A8 in #354, recorded below). Evidence: [`docs/research/herdr-and-attention.md`](../research/herdr-and-attention.md) (§1 Duet, §2 herdr, §5 principles) and [`docs/research/competitor-pain-points.md`](../research/competitor-pain-points.md) (2.11, G6). Builds on [`desk.md`](desk.md) and [ADR-0024](../adr/0024-several-chats-one-attention-queue.md). It reframes part of [#55](https://github.com/Erengun/oge/issues/55) and takes G6 from [#342](https://github.com/Erengun/oge/issues/342).

## Why

The goal (user, 2026-11-01) is that Öge becomes a mix of Claude Duet and herdr:
- Duet runs work in the background and notifies you "when a task finishes or needs review".
- herdr lets you detach (`ctrl+b q`) while work keeps running, and shows exact agent state.

Today an Öge Desk does neither. Closing the terminal breaks it:
- **SIGHUP is not handled.** `tui_home.go` watches only `os.Interrupt` and `SIGTERM`. A hangup takes Go's default action, which is instant death, with no interrupt, no scope check and no Checkpoint.
- **Agents outlive Öge.** They run in their own process group (`proc_unix.go`, `Setpgid`), so the terminal's hangup never reaches them. They keep writing into the Workspace until their stdin hits EOF. The Chat log ends torn.
- **Verified work is lost.** A Chat's cut waits at its delivery Gate until `/apply` (`cutGates` in `tui_chat.go`). When Öge dies, the wait's context is cancelled, `run/gates.go` writes `GateAbandoned`, and the Run ends as an Infrastructure stop. A `✓` you hadn't applied yet cannot be recovered.
- **Nothing comes back.** The next `oge` starts empty. The Chats, their numbers and their transcripts are gone from view, although the logs are on disk.

These are pains 2.11 (session restore and silent death), G6 (Desk lost on reboot), rank 2 / 2.1 (a needs-you agent missed while you're away) and rank 7 / 2.4 (re-entry cost).

**Approach.** This spec follows the judged winner, "host-kept Desk: restore, not detach":
- Öge builds no detach, no daemon and no second process.
- Something you already run keeps the `oge` process alive while you're gone: herdr's server, tmux, or screen (which ships with macOS).
- Öge adds four things a host can't know:
  - a clean exit;
  - a full restore;
  - a return brief;
  - a way to reach you off the terminal.

**What each user gets:**

| Your setup | Terminal closed, machine awake | Lid closed, sleep or reboot |
|---|---|---|
| `oge` inside herdr, tmux or screen | **Continuity.** Turns you already sent finish, cuts verify, Gates wait, and you are pinged. | Nothing runs while the machine sleeps. After a reboot, `oge` restores. |
| No host | **Restore, not continuity.** Every Chat stops cleanly (interrupt, Checkpoint), and nothing runs. | `oge` restores. |
| Anyone | | Duet's "your devices don't need to stay on" **is not matched**. The honest equivalent is `oge` under herdr `--remote` or ssh+tmux on a machine that stays on. Öge documents this and does not build it (G14). |

Under the U2 default (`[chat] feedback = ask`), "keeps working while you're away" means **minutes, not hours**: the turns you already sent, one cut each, and its QA and Checks. Then the Chat waits at `✓`, `✗` or `▲`. Decision A4 asks whether to go further.

## Decisions for the user

| # | Question | a | b | c | Recommendation |
|---|---|---|---|---|---|
| A1 | What does "detach and resume" (#55 comment) mean? | **Host-kept.** Öge builds no detach. herdr, tmux or screen keeps the process alive. desk.md's "Not doing: a daemon or detach" stays. | **Desk server.** `oge` becomes a client of a per-repo server that owns the Chats (needs ADR-0025 and a large refactor of `home`). | **Headless in place.** The same process stops drawing on hangup and a socket proxy reattaches it (a small terminal multiplexer, close to G15). | **a.** It reads the "herdr half" of the goal as "run oge inside herdr", which the herdr reporter (#339) just made worthwhile. Revisit b only if dogfood shows that users without a host lose work they cared about. |
| A2 | No host, terminal closed with work in flight | Stop now: interrupt, grace period, kill, scope check, Checkpoint. | Finish in place: stay headless until the in-flight turns and cuts settle, under a hard deadline, with no new turns. Then park and exit. | Show a confirm dialog on quit. | **a** by default. Offer **b** as opt-in `[desk] on_close = finish` once dogfood shows it is wanted. Never c, because a closed window can't answer a dialog. |
| A3 | Does `/quit` keep Chats until `/close`? Today, quitting ends a Chat and deletes its Workspace. | Yes. Every open Chat restores, and Workspaces are kept. | Only Chats that ended uncleanly restore (hangup, crash, reboot). `/quit` closes them as today. | Ask on `/quit` when Chats are open. | **a**, closer to Duet's "one ongoing thread". `/close` removes the Workspace. An auto-close rule (for example, applied and idle for 7 days) is deferred and named here so the disk cost is known. |
| A4 | Under a host while you're away, may a failed Check be sent back without you? | No. U2 holds: a `✗` card waits. | A typed, pinned `/away` grant turns on `send back` with `feedback_budget` per Chat, a same-failure stop, no Advisor, and a Desk-wide cap counted on input, cache and output tokens. | Turned on implicitly on detach. | **a now.** This is Öge sending turns on its own, which the 2026-10-31 direction pulled back from (even though the send-back text is deterministic). Build b only if dogfood shows `✗` cards waiting overnight are the felt problem. Never c. |
| A5 | Vendor Session resume (`claude --resume`, codex `thread/resume`) on restore | Re-brief only: a fresh Session plus Öge's re-brief, as the idle reopen does today. | A spike with a per-Chat private config dir. This reverses #44 ("never set `CLAUDE_CONFIG_DIR`", `--no-session-persistence`), Codex's `ephemeral: true`, and ADR-0016's deferral. | Use the user's own Claude/Codex persistence. | **a.** The work survives; the conversation's nuance does not. Spike b only if measured re-brief quality is the complaint. |
| A6 | A restored Chat whose settings changed | Read-only, plus `n` for a new Chat. | Also allow a typed `reopen`: continue under the new settings, recorded as a pinned human decision. | Continue silently. | **b.** Never c (that is herdr D1080). |
| A7 | `oge add "task"` (#338) reaching a Desk while you're away | Starts at once, as #338 is written. | Lands as a card. Its first turn starts only when a human focuses it or presses Enter. | Refused while away. | **b.** A supervised agent runs as your user and can run `oge add` itself. Hard denies are path-based, so nothing else stops that fan-out. This amends #338's first acceptance criterion. |
| A8 | What off-terminal pushes may carry | The Chat number, counts and the repo name only. | Opt-in `[notify] titles = true` adds Chat titles. | Always include titles. | **a** by default, **b** as opt-in. Titles come from your first message, which is Task text, and notify.go's rule keeps that out of third-party pushes (G11 wants the title). |

### Decided (user, 2026-11-01, #354)

| # | Decision |
|---|---|
| A1 | **Host-kept.** herdr, tmux or screen keeps `oge` alive; Öge builds no daemon or detach. |
| A2 | **Stop cleanly now** with no host: interrupt, grace, kill, scope check, Checkpoint. |
| A3 | **Ask on `/quit`** when Chats are open: keep them for restore or close them. A signal (SIGHUP, SIGTERM) can't ask, so it **keeps** them. |
| A4 | **No send-back while away.** The `✗` card waits; U2 holds. |
| A5 | **Re-brief only** on restore; no vendor session resume. |
| A6 | A restored Chat whose settings changed is **read-only**, with `n` and a typed `reopen`. |
| A7 | `oge add` while away **lands as a card**; it starts only when focused. |
| A8 | Pushes carry the **Chat number, counts and repo** only; `[notify] titles = true` opts in to titles. |

The table above keeps the options and the reasoning.

## Model

**Processes.**
- There is one `oge` process per repo Desk, as today. It owns every Chat's implementer Session and every cut's QA and Check processes.
- The host (herdr server, tmux server, screen) owns only the pty.
- There is no daemon, attach protocol or second process.

**Desk record.** It lives under the state root's Private tree (0700, never the temp dir, so it survives a reboot), at `<Private>/desks/<repo-key>/`:
- **`desk.log`**, written ahead, with these records:
  - `DeskOpened{pid, start identity, host: herdr|tmux|screen|none, TMUX/STY/HERDR_PANE_ID, tmux session}`;
  - `ChatAdded{chat, n, title}`;
  - `ChatClosed{chat}`;
  - `Seen{chat, upto}`, which keeps `✓` unread across restarts (G1);
  - `Away{from}`, `Back{at}` and `Slept{from, to}`;
  - `DeskClosed{clean}`.
- **`lock`**: a flock released by the OS when its holder dies, the same layering as ADR-0012's per-Run lock.
- **`drafts/<chat>`**: mode 0600, replaced atomically, debounced at 1 s, deleted on send.

**Chat log additions.** Everything else needed is already durable: `ChatStarted` with its frozen blob, each turn's scope-checked commit at `refs/oge/chat/turns/<n>`, `AttemptEnded` blobs (enough to redraw the transcript), `ProcessStarted` / `SessionReopened` with `launch_profile` and `permissions`, and the cut Runs' Ledgers. The new records are:
- `TurnInterrupted{turn, reason: hup|int|found_on_restore, checkpoint}`;
- `SessionRestored{tier: rebrief, from: refs/oge/chat/turns/<n>}`.

**Exit.** Every way out takes the same path: SIGHUP, a tty read that returns EIO, SIGTERM, a second Ctrl-C, or `/quit` with work in flight. That path is ADR-0012's interrupt, applied to every Chat:
1. Interrupt the turn in flight. After the grace period, kill its process group.
2. Scope- and tamper-check the Workspace. If the result is safe, it becomes a Checkpoint, never a turn.
3. Write `TurnInterrupted`.
4. Mark a verifying cut interrupted in its Ledger.
5. Write `DeskClosed{clean:false}`.

Three rules keep this path from destroying the state that restore needs:
- **Keep the Workspace.** The exit path never calls `Chat.Close`, because Close removes the Workspace (`oracle.RemoveAll`). It uses a new Chat stop that keeps it, and the home never cancels the Chats' context on hangup.
- **Survive a dead tty.** stdio is redirected to a 0600 `runner.log` in Private, so the exit path never writes to a dead tty.
- **Park open Gates.** An open cut Gate, the delivery Gate included, ends through the **parked** path (`errParked` in `run/gates.go`), with its pins recorded. It never ends through `ErrNoDecision` or a cancelled context, which write `GateAbandoned` and an Infrastructure stop. A park is never Rejected (ADR-0008). While the process lives, a Gate port never gives up because the view blinked (a pty resize, a reattach).

**Away.** Away is a deterministic signal. It starts on a terminal focus-out report or after 10 minutes without a key, and ends on focus-in, resize or a key. It decides only two things:
- whether off-terminal pushes fire;
- whether the "while you were away" line shows.

It never changes trust, spend or Gates.

**Restore.** When `oge` starts in a repo whose Desk record has open Chats and whose lock is free, it:
1. reads each Chat log;
2. sweeps residue: it kills recorded process groups whose identity still matches, and records anything that escaped as residue (ADR-0012). This happens **before** it touches a Workspace;
3. rebuilds a missing Workspace from the last turn ref. A dirty Workspace with no `TurnInterrupted` gets the scope check and a `found_on_restore` Checkpoint;
4. draws the rail and transcripts.

**No agent starts.** Focusing a Chat runs the trust check (below). If it passes, focus opens one Session through the existing `reopen()` + re-brief path, ahead of your first send, and sends nothing.

**Trust check on focus.** It compares the last `ProcessStarted` / `SessionReopened` with what would launch now:
- the Launch profile;
- the permission mode;
- the recomputed hard-deny set;
- the adapter and its MinVersion.

The Chat's own frozen blob stays authoritative. Öge does not compare raw `graph_hash` against the current `oge.toml`, because then an unrelated Check edit or an Öge upgrade would strand every Chat. Any mismatch leaves the Chat read-only and shows a `settings changed` card (A6).

## What the user sees

### Leave

**With a host.** You detach as usual: herdr `ctrl+b q`, tmux `prefix d`, or screen `ctrl+a d`. Nothing changes on screen, and no key is added. The `oge` process, its Sessions and its cuts keep running.

**Without a host.** The first time work goes in flight on a machine with no `TMUX`, `STY` or `HERDR_ENV`, one hint shows once per machine and is never repeated:

```
tip · closing this terminal stops the Chats · to keep them working, run oge inside herdr, tmux or screen (screen -S oge oge)
```

While work is in flight, the footer carries one quiet line. There is no dialog:

```
 2 working · closing stops them · oge brings them back
```

Closing the window, pressing Ctrl-C twice or typing `/quit` all take the exit path above. If work was in flight, the shell gets one line:

```
oge · 2 Chats stopped · run oge here to pick up
```

**Under a host, to end everything.** `/stop` interrupts every working turn (ADR-0012 grace, kill and Checkpoint), parks open Gates, and exits. Closing a herdr pane or tmux window does the same through SIGHUP.

### Away (with a host)

- Chats work as if you were present but not looking. Turns you sent settle, each is cut, and QA and Checks run.
- A Gate, tool ask or AskUserQuestion waits as `▲`. A failed Check waits as `✗` (U2). An idle Session closes after 30 minutes.
- **Three messages reach you off the terminal, and nothing else:**

  | When | Text (fixed) |
  |---|---|
  | Something needs you: sent at once, then coalesced for 15 minutes. A Gate still open past `[desk] stale_gate` re-sends once. | `oge · api · Chat 2 needs you` / `oge · api · 2 Chats need you` |
  | Every Chat has settled (done, waiting or failed), sent once | `oge · api · settled · 2 ready · 1 needs you · 1 failed` |
  | The Desk stopped (SIGHUP or TERM while you were away) | `oge · api · the Desk stopped · run oge to restore` |

  Never per tool step, per cut or as progress pings.
- **Channels:**
  - Under herdr, the existing reporter carries it (blocked / working / idle), and herdr's own alerts and remote attach reach you there. Öge's OSC stays off, as `notifierFor` does today.
  - Under tmux or screen, an OSC to a detached client goes nowhere. An optional `[notify] command` argv carries the text instead: no shell, bounded, fire-and-forget, for example `["ntfy", "publish", "my-topic"]` or `terminal-notifier`.
- **From any terminal or over ssh**, `oge status` prints the brief so far as plain text, without attaching.

### Return (with a host)

You reattach (`herdr`, `tmux attach -t work` or `screen -r oge`). The first focus-in, resize or key ends away. The header gains one line. Nothing modal appears and no key is swallowed:

```
 oge · api · main      while you were away · 2h14m · ▲1 ✗1 ✓2 · ^space
```

`ctrl+space` opens the Inbox. Its first card is the brief, built deterministically from the logs (no model):

```
╭ while you were away · 2h 14m ──────────────────────────╮
│ ✓ 2 ready   ▲ 1 needs you   ✗ 1 failed                 │
│ 1 parser    ✓ Accepted · +40 -6                        │
│ 2 auth      ▲ plan Gate since 14:31 · pushed 14:31     │
│ 3 ci-flake  ✗ go test ./ci · 1 failed                  │
│ 4 docs      ✓ Accepted · +12 -0                        │
│ machine asleep 01:10–06:40 (nothing ran)               │
╰ enter: first card (▲ → ✗ → ✓) · esc: desk ─────────────╯
```

Then come the usual cards, each with its Evidence. Apply keeps its second Enter, and there is no bulk apply. Focusing a Chat that changed shows its since-you-left card: your last message, each event in time order, ending at the open ask.

### Reboot, crash or plain close (any user)

You run `oge` in the repo. Under herdr, layout restore puts the pane back in the right directory; it's the same command. The Desk comes back within normal startup time. No agent starts.

```
┌ DESK ─────────┐
│1 ○ parser     │
│  stopped 9h   │   ← a turn was cut off; distinct from ○ idle
│2 ▲ auth       │
│  parked 9h    │
│3 ✗ ci-flake   │
│────────────── │
│✓ docs  ○ 1    │
└───────────────┘
```

- **Rows keep their numbers and titles.** States are honest: `stopped` is never shown as `idle`, and `✓` stays unread until seen (`Seen`).
- **The brief opens with how it ended.** When the recorded pid identity is dead and there is no `DeskClosed`: `away ended unexpectedly · last activity 03:12 (machine restarted or oge was killed)`.
- **An interrupted turn gets a card.** Nothing is resent by itself (ADR-0012):

  ```
  Chat 1 · turn 7 stopped when oge exited 9h ago · ↵ resend · o open
  ```

- **A cut that was waiting at a Gate**, including a verified result waiting for `/apply`, was parked with its pins. Until #55's Gate re-entry exists, its card says so plainly:

  ```
  Chat 4 · verified, parked at the result Gate when oge exited · c cut again
  ```

  Cutting again re-cuts the same turn commit. A Check this tree already passed on the same Oracle is reused (`VerdictReused`). Once #55 lands, the card offers the same Gate again, with the same pins, and nothing re-runs.
- **A cut interrupted mid-Check** shows `Check interrupted · c check again`. A killed Check had no Verdict, so this is not a Recheck.
- **A host request open at exit** died with its Session. It is never shown as answered: `the agent asked to run X; the Session ended before you answered`.
- **Focusing a Chat** passes the trust check, then starts one Session in the background while you read. Your first send carries the re-brief.
- **If settings changed**, the card names the exact difference:

  ```
  Chat 2 ran under claude@work · auto; this config gives claude · auto
  n new Chat · type reopen to continue under the new settings
  ```

### A second terminal while the Desk lives under a host

A second `oge` finds the lock held. It restores nothing and points to the live Desk:

```
this repo's Desk is open in tmux session "work": tmux attach -t work
```

For herdr it names the pane, and otherwise the pid. `oge add "task"` still reaches the live Desk (A7).

## Trust

**Gates.**
- No Gate, host request or tool ask resolves itself while you are away (ADR-0008).
- Away is **not** `--unattended`:
  - optional Gates are never removed;
  - host requests are held, never denied.
- Open Gates park on exit and are never abandoned or Rejected. A parked Gate re-opens only through Ledger replay with unchanged pins (#55).
- Off-terminal channels only inform. They can never approve. Phone approval stays herdr's (G14).

**Held requests: a precondition.** Before any copy promises that "Gates wait while you're detached", tests must prove that neither protocol expires a pending request over a long hold:
- a long-held `can_use_tool` case in the Claude fake and recorded testdata;
- Codex `held_approval.jsonl`.

ADR-0008 already demands this. If an adapter does expire a request:
- Öge adds a deny-only timeout just below the expiry;
- the needs-you push fires first;
- the card says `the approval expired at 14:02 · the agent was told no`.

**Spend bounds.**

| Situation | Spend |
|---|---|
| Away under a host (U2 default) | Turns you already sent or queued, one cut per settled turn, and QA and Checks within the frozen Pipeline. A newer cut supersedes the older one. Then nothing. |
| Idle Session | Closes after 30 minutes (`DefaultIdleTimeout`), so an idle Chat costs nothing. |
| Active-time limits | Pause at Gates (ADR-0012), so a night away never exhausts a Run. |
| Restore | Starts zero agents. Each focus starts at most one Session and sends nothing. The first token is your own send. It never respawns N agents at once (parallel-code #293). |
| Interrupted turns and cuts | Never resent or re-run by themselves. |
| No host, terminal closed | Zero (A2 a). |

**Never while you're away, and never on restore:**
- applying to your tree or branch, any Gate decision, typing a trust-weakening word, or a bulk apply;
- resending an interrupted turn, or re-running an interrupted Check;
- an Advisor turn, or a send-back turn (unless A4 b is chosen);
- continuing a Chat whose Launch profile, permissions, hard denies or adapter changed, unless a human types `reopen`;
- resuming a vendor Session outside ADR-0012's three conditions;
- reporting the inner `claude` or `codex` session ids to herdr or any host. herdr could then resume the raw agent outside Öge's hard denies, which is D1080's silent downgrade.

**Integrity.**
- The residue sweep runs, with an identity check, before any Workspace is touched.
- On Linux, agents also get `Pdeathsig: SIGKILL`, set from a goroutine holding `runtime.LockOSThread`.
- Every adapter's and every Check's process group differs from `oge`'s, and a test asserts it. A hangup therefore reaches only `oge`, and the interrupt path owns the shutdown.
- The Desk lock stops two `oge` processes from resuming the same Sessions.

**The #338 socket.** It exposes only two things: "where is the live Desk" and `oge add`. It never exposes Gate, Ask, apply or keystroke methods.
- Peers are checked by uid, but a supervised agent has your uid. So `oge add` is treated as possible agent-initiated spend (A7), and rate-limited.
- Do not rely on vendor sandboxes to block `AF_UNIX` connects; that is unverified.

**Disk.** Drafts may hold pasted secrets. They live 0600 in Private, like the Chat log that already stores messages, and are deleted on send.

## Research alignment

| Behaviour | Principle (herdr-and-attention.md §5) or evidence |
|---|---|
| Off-terminal: needs-you pushes; done and failed wait for the digest | 2. Negotiated interruption (McFarlane 2002) |
| One header line plus the Inbox on return; no modal, no auto-open | 8. Self-initiated triage beats a drip (Mark 2016; Fitz 2019); 1. coarse breakpoints (Iqbal & Bailey 2008): the return is the coarsest one |
| Three fixed messages on the same level table | 3. Fixed level table (Matthews 2004) |
| Restored rail keeps numbers, titles and order | 4. Stable spatial order (Mark, González & Harris 2005) |
| Brief and since-you-left in time order, ending at the open ask | 5. Resumption cues (Parnin & DeLine 2010; Altmann & Trafton 2002; Trafton 2003) |
| Drafts persist across restarts | 6. A ready-to-resume plan (Leroy & Glomb 2018) |
| Evidence on every card after a long absence; no bulk apply | 9. Complacency under load (Parasuraman & Manzey 2010) |
| A4 b, if chosen: bounded repair while away | 7. Raise the agent's time alone (Olsen & Wood 2004; Cummings & Mitchell 2008) |
| Measure wait time after return, not felt speed | 10. METR 2025; Mark 2008 |
| Fail closed when settings changed; never pass inner ids to a host | 2.11, herdr D1080 ("came back as plain resumed `claude`, silently downgraded") |
| The Desk record, not the host layout, says which Chats exist | 2.11, herdr #4414 ("restored 12 workspace layouts but resumed only three"), G6 |
| SIGHUP is an interrupt; `stopped` is distinct from idle; a "Desk stopped" push | 2.11, claude-code #63023 (no completion notice, work lost) |
| Lazy resume on focus, never N at once | G6, parallel-code #293 |
| `✓` stays unread across restarts | 2.3, G1 |
| Chat number in every push; titles opt-in | 2.3, G11 |
| Needs-you reaches you while detached | Rank 2 / 2.1 |
| Background work, notified "when a task finishes or needs review" | Duet (§1), within the machine-awake limit |
| Detach keeps work running | herdr `ctrl+b q` (§2), by running inside it rather than copying it |
| Remote and phone approval; pane hosting | G14, G15: not ours |

## Build slices

Each slice is small (about 45 min / 150k), fakes-first (ADR-0017), and leaves `main` shippable.
- Slices 1–4 land on today's single-Chat home, before #335/#336.
- Slice 1 fixes a present bug and should go first, whatever is decided.
- Slices 6 and 7 use #336's cards and Inbox.

0. **Docs.**
   - This spec.
   - An ADR-0012 Chat amendment: the turn ref is the Chat's Checkpoint, and `found_on_restore` is an interruption reason.
   - A Restore section in desk.md. Its "Not doing: detach" line stays under A1 a.
   - Reconcile #55's acceptance criteria ("no Checkpoints, no vendor-session resume") with the Chat turn ref.
   - `GLOSSARY.md` entries: Away, Stopped, Restore, Host.
1. **Exit is an interrupt.**
   - SIGHUP joins the signal watch, and a tty EIO counts as hangup.
   - stdio is redirected to a 0600 `runner.log`.
   - A Chat stop that interrupts, kills the group, scope-checks, writes `TurnInterrupted` with its Checkpoint, and keeps the Workspace.
   - The no-host hint and the footer line.
   - Files: `internal/cli/tui_home.go`, `internal/cli/interrupt.go`, `internal/cli/tui.go`, `internal/run/chat.go`, `internal/cli/tui_home_unix_test.go`.
   - Fake-agent test: SIGHUP mid-turn leaves a well-formed log, an intact Workspace, and no live process group.
2. **Gates park on exit; process groups are owned.**
   - Add an exported park result the Gate port can return, so `cutGates` and the live Gate view park on exit instead of abandoning. The port waits through view blips.
   - A test asserting that the pgid differs from `oge`'s for every adapter (fakes included) and for Checks.
   - Linux `Pdeathsig` with `LockOSThread`. This is the #55 acceptance criterion, reused.
   - Files: `internal/run/gates.go`, `internal/cli/tui_chat.go`, `internal/cli/tui_gate.go`, `internal/cli/gates.go`, new `internal/proc/proc_linux.go`, `internal/proc/proc_test.go`.
   - Test: exit at a delivery Gate leaves the Run Parked with its pins, and no `GateAbandoned`.
3. **Desk record and lock.**
   - `desk.log` (`DeskOpened` with the host, `ChatAdded`, `ChatClosed`, `Seen`, `DeskClosed`) and the OS-released lock.
   - A second `oge` prints where the live Desk is.
   - `ChatClosed` behaviour per A3.
   - Files: new `internal/run/desk.go`, `internal/run/live.go` (the #338 Chat-level `live.pid`), `internal/run/chat.go`, `internal/cli/runcmd.go`, `internal/cli/tui_home.go`.
4. **Restore the list, without agents.**
   - Bare `oge` with open Chats redraws the rail and transcripts from `AttemptEnded` blobs, with honest states (`stopped`, `✓` from `Seen`, `parked`, `✗`).
   - Includes the identity-checked residue sweep (#55's sweep, reused) and the interrupted-turn, parked-Gate and Check-interrupted cards.
   - Starts nothing.
   - Files: new `internal/run/chat_restore.go`, `internal/run/sweep_unix.go`, `internal/cli/tui_chat.go`, `internal/cli/tui_chatview.go`, `internal/cli/tui_home.go`. TUI snapshot tests.
   - This is G6 from #342.
5. **Lazy reopen on focus.**
   - Rebuild a missing Workspace from `refs/oge/chat/turns/<n>`, and run the `found_on_restore` scope check.
   - The semantic trust check against the last `ProcessStarted` / `SessionReopened`.
   - Focus opens one Session through `reopen()` with re-brief. Record `SessionRestored`.
   - The `settings changed` card with `n` and typed `reopen` (A6).
   - Files: `internal/run/chat_restore.go`, `internal/run/chat.go`, `internal/cli/tui_chat.go`.
   - E2E on the fake agent: `kill -9` oge mid-turn, run `oge`, focus, resend; the turn settles.
6. **Held-request precondition.**
   - Long-held `can_use_tool` (Claude) and `held_approval.jsonl` (Codex) contract tests.
   - An expiry card and a deny-only timeout only if one expires.
   - Files: `internal/agent/claude/testdata/`, `internal/agent/claude/*_test.go`, `internal/agent/codex/*_test.go`.
   - Must land before slice 8's copy says Gates wait.
7. **Return brief.**
   - A pure builder over (Chat logs, Run Ledgers, away window), with the sleep gap from wall-clock versus monotonic time (`Slept`) and "ended unexpectedly" from the pid identity.
   - The away signal (Bubble Tea focus reports, 10-minute fallback), the header line, and the brief as the first Inbox card.
   - `oge status` prints it as plain text. This is the status part of #55.
   - Files: new `internal/run/brief.go`, `internal/run/desk.go`, `internal/cli/tui_home.go`, `internal/cli/cli.go`. Golden tests on fake Ledgers.
8. **Off-terminal reach.**
   - The three-message policy on the herdr reporter and an optional `[notify] command` argv.
   - Fixed texts with the Chat number and repo name. Titles only by A8 opt-in.
   - Files: `internal/cli/notify.go`, `internal/pipeline/userconfig.go`, `internal/herdr/herdr.go` (messages only). Fake exec and fake clock tests.
9. **`/stop`.** End everything under a host: interrupt, park Gates, exit. Files: `internal/cli/tui_slash.go`, `internal/cli/tui_home.go`.
10. **`oge add` while away.** #338 with the socket limited to locate and add, rate-limited, landing as a card per A7. Files: those #338 names.

**Behind a decision or dogfood evidence, not scheduled:**
- herdr `pane report-agent-session --source oge --agent oge` with the Desk id (never inner ids), plus an upstream request for an `oge` integration whose resume argv is `oge` in the pane's directory. herdr 0.9 resumes only official integrations, so until then this is harmless metadata.
- A5 b: the vendor-resume spike.
- A2 b: finish in place on close.
- A4 b: the `/away` repair grant.
- Keep-awake while a turn or cut runs (`caffeinate -i -w <pid>`, `systemd-inhibit --what=idle`). It prevents idle sleep only, not lid-close sleep.

**Fits with other work.** Build #336's attention router and card state UI-free, consumed in-process by `home`. That costs little now, and if dogfood ever shows that users without a host need continuity (A1 b), detach becomes a transport change rather than a rewrite.

## Not doing

- Öge's own detach, daemon, attach socket or terminal proxy (A1 a).
- Hosting shells or panes, or per-client views (G15).
- Remote or multi-machine supervision, or approving from a phone (G14; herdr carries it).
- Matching Duet's VM: nothing runs while the machine sleeps or is off.
- Respawning N agents on restore.
- Resending, re-running or resuming anything automatically after a crash (ADR-0012).
- An Advisor turn or agent-to-agent prompting while away.
- A model-written brief or notification.
- Handing vendor session ids to a host.
- Bulk apply, also after a long absence.