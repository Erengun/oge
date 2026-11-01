# The Desk: several Chats, one attention queue

Status: approved direction (user, 2026-10-31); not built yet. Evidence: [`docs/research/herdr-and-attention.md`](../research/herdr-and-attention.md). Decision record: [ADR-0024](../adr/0024-several-chats-one-attention-queue.md). It builds on the full-screen home ([`full-screen.md`](full-screen.md)).

## Why

People already run several agents at once. Tools like herdr show which pane needs you, but they leave two problems:
- The state signal comes from reading the screen. It fails exactly when it matters: an approval shows as idle and expires, or a "finished" toast fires while work is still running.
- The human still routes every request and re-checks every "done" across N contexts.

Öge's earlier answer was to let agents prompt agents: automatic feedback turns, then an automatic Advisor. That made things worse, with more output tokens and more waiting.

The Desk takes the other route:
- **Exact state** from Öge's own event streams.
- **Deterministic attention routing**, with no model in the loop.
- **A layout built against the interruption literature**, so supervising three agents feels calmer than supervising one did.

## Decisions (user, 2026-10-31)

| # | Decision |
|---|---|
| U1 | **Layout: Rail + Inbox.** A left rail lists the Chats, the focused Chat fills the middle, and the Inbox shows waiting items one card at a time. |
| U2 | **A failed Check becomes an Inbox card** with `send back` / `advise` / `open`. No automatic feedback turn, no automatic Advisor (amends chat-first D5 and advisor D-A3/D-A5). |
| U3 | **While you're typing, everything is held except a stale Gate.** Held items are released when you send or clear the input. Only a Gate open longer than T, or an approval about to expire, rings while you type. |
| U4 | **New Chats come from** `/new`, a task typed into the Inbox, or `oge add "task"` from another terminal. `oge "task"` keeps its standalone Run and exit-code contract (ADR-0015). |

## Projects (user, 2026-11-01)

The goal's "herdr side" means managing **several projects** with AI from one place. herdr-the-tool integration is not the point (#351 stays as an optional extra).

| # | Decision |
|---|---|
| P1 | **One Desk for all projects.** One `oge` holds Chats from any number of repos. The rail groups them by project (a header line per repo); the Inbox and attention routing span every project. |
| P2 | **Adding a project.** `/new` opens a picker of recent repos plus a path (`/new ~/dev/api` works); `oge add "task"` from any repo joins the open Desk as a card for that project (A7); `[desk] projects` in the user config pins projects in the rail even with no Chat open. |

What changes:
- Each Chat carries its own repo root: Snapshot, Workspace, apply/branch target, header (`oge · repo · branch` follows the focused Chat), and sibling refresh (#344) only among Chats of the same repo.
- Rail numbers stay stable and global across projects; project headers never reorder (first-opened first).
- The soft cap (`max_active`) counts working Chats across all projects: the attention limit is the person's, not the repo's.
- The Desk record (Away 3) and the `oge add` socket (#338) are **per user**, not per repo.

## Intake and resume (user, 2026-11-01)

The attention review ([`positioning.md`](../positioning.md), Identity): starting work is cheap, so committing attention must be intentional, and coming back must not mean rebuilding context.

| # | Decision |
|---|---|
| I1 | **Later: get an idea out of your head without committing to it now.** A Later item is text and nothing else: no agent, no Chat, no Workspace. It is an attention mechanism, not task management. |
| I2 | **Capture costs less than opening a Chat.** One keystroke or one command, no metadata: no priority, label, deadline, project choice or workflow state. The repo it was captured in is remembered for you, not asked. |
| I3 | **Promotion is deliberate.** A Later item never becomes a running Chat on its own: not at a free slot, not on a timer, not when the cap clears. You promote it with an explicit key or command; it then opens a Chat with that text as its first message, like `/new`. |
| I4 | **The return brief rebuilds context, it does not summarise activity.** Per Chat, in this order: **You were** (your first message: the intention), **Since you left** (events recorded while away), **Your decisions** (Gate decisions and applies, from the Ledger), **Waiting for you** (open Gates, asks, failed Checks), **Next** (the first actionable Inbox card). All deterministic and record-backed; no model writes it and no model recommends. |

Where the work stands, at a glance:

| State | On the Desk |
|---|---|
| Want to do | `◇` Later, a count in the rail fold (`◇ 4 later`); listed by `/later` |
| Started | `○` idle Chat |
| Executing | `●` working |
| Waiting for you | `▲` / `✗`, an Inbox card |
| Finished | `✓` |

Why (research): making a plan for an unfinished goal stops it intruding on your thoughts (Masicampo & Baumeister 2011; their manipulation was a specific plan, so bare capture is the weaker form, measured in the Evaluation rather than assumed), and a ready-to-resume note reduces attention residue (Leroy & Glomb 2018). Resumption cues that end at the next goal cut resumption time (Altmann & Trafton 2002; Parnin & DeLine 2010). Both serve the north star, useful agent work per minute of human attention, more than any extra parallelism does.

Rejected: Later as a backlog. Priorities, labels, deadlines, filters, sorting or states on Later items would make Öge a project-management tool and turn captured ideas back into obligations. The soft cap (`max_active`) stays experimental, validated in the Evaluation below.

## Layout

```
 oge · api · main               ▲1 needs you · ●2 working · ✓1
┌ DESK ──────┐
│1 ● parser ◀│ ╭ since you left · 4m ─────────────────────╮
│  working 3m│ │ you  make the parser accept trailing ,   │
│2 ▲ auth    │ │ ●  parser/lex.go +12 -2                  │
│  needs you │ │ ●  go test ./parser ✗ 1 failed           │
│3 ● ci-flake│ │ ✗  Check failed · s send back · a advise │
│  QA 1m     │ ╰──────────────── collapses on send/scroll ╯
│────────────│  ❯ make the parser accept trailing ,
│✓ docs  ○ 1 │  ● edit parser/lex.go
│ ^space inbx│ ╭────────────────────────────────────────╮
└────────────┘ │ ❯ also nested arrays█                  │
               ╰─ ● working · 2 held · ^space inbox (2) ╯
```

**Header.** Keeps `oge · repo · branch`, then adds the counts: needs you, working, done-unseen.

**Rail** (left, 14 columns, from 100 columns up, with two or more Chats open; toggled with the sidebar key)
- Each row has: a stable number, a static glyph, a short title (from the Chat's first turn), and a coarse state with its age.
- Glyphs:

  | Glyph | Meaning |
  |---|---|
  | `●` | working |
  | `▲` | needs you |
  | `✗` | Check failed |
  | `✓` | done, unseen |
  | `○` | idle |

- Working Chats show `working` or `QA`, never a tool name. Nothing animates.
- Done and idle Chats fold below a line into one row.
- **Rows never reorder.** A Chat keeps its number for its whole life.

**Focused Chat.** It is today's full-screen Chat: transcript, live area, input box, and the right sidebar from 110 columns up.
- Each Chat keeps its **own draft, scroll position and live area**, so switching away and back loses nothing.

**Inbox** (`ctrl+space`; `/inbox` as a fallback)
- One card at a time, ordered ▲ then ✗ then ✓, oldest first within each.
- A card shows:
  - which Chat it is, its agent, and how long it has waited;
  - **what is wanted** (the Gate, the ask, the failed criterion);
  - **the Evidence**: Check results, QA Verdict, files with `+N -M`;
  - single-key actions.
- Actions on a card:
  - Apply keeps its second typed Enter.
  - Trust-weakening words are still typed in full (ADR-0022).
  - There is no bulk apply.
- `tab` moves to the next card, `o` opens the card's Chat, `esc` closes the Inbox.
- With the Inbox open, typing a task and pressing Enter starts a new Chat with it.

**Switching**
- Click a rail row, or press `alt+1…9` when the terminal sends Meta. `/chats` opens a picker that works everywhere.
- Settled in slice 3a (#355): `alt+1…9` in the full screen; `/chats` everywhere, at 80 columns too; `/new` keeps the current Chat running and opens another; `/close` ends the focused Chat and leaves the others' numbers alone; `/clear` starts this Chat afresh in place. `/new`, `/chats` and `/close` also work during a turn. Clicking a rail row comes with the rail (3b). `ctrl+n` and `ctrl+p` are taken.

**Since-you-left card.** Shown when you focus a Chat that received events while it was unfocused:
- your last message;
- then, in time order, what changed and what ran, as one line per event with the file or command;
- ending at the open ask, if there is one.

Typing goes to the input as usual. The card collapses when you send or scroll, and it never swallows a key.

## Attention levels

The levels follow Matthews et al.'s scale. This is a fixed table; no model decides any of it.

| Event (from Öge's streams) | Level | What you see |
|---|---|---|
| Tool step, agent text | ignore | nothing outside the focused Chat |
| Turn settled, no change | change-blind | the glyph turns `○` |
| Cut Accepted, not yet seen | make aware | `✓` + an Inbox card + the header count; held while you type |
| Check failed | make aware | `✗` + an Inbox card offering send back / advise; never an automatic turn |
| Gate open, tool ask, AskUserQuestion | interrupt | `▲` + a toast at the next breakpoint (send or clear) |
| Gate or ask open longer than T (default 5 min), or an approval about to expire | demand | bell / OSC notification, even while you type |

**Where the state comes from:**

| Signal | State |
|---|---|
| `host_request`, a Gate, AskUserQuestion | blocked |
| `turn_accepted` → `turn_settled`, a background cut or Check | working |
| Accepted cut not applied | done |
| anything else | idle |

Öge knows these states exactly only for agents it drives. It is not a terminal and does not host shells, servers or watchers.

## Research alignment

Every behaviour above follows a cited principle, listed below. A change that contradicts one needs new evidence or an ADR saying why. Full citations are in the research note, section 4.

| Behaviour | Principle |
|---|---|
| Hold while composing; release on send, never on a typing pause | Interrupt at coarse breakpoints. Deferring only to fine breakpoints was *worse* than immediate delivery (Iqbal & Bailey 2008; Adamczyk & Bailey 2004; Züger 2017). |
| Badge and Inbox are pulled by you; only a stale Gate pushes | Negotiated coordination performs best (McFarlane 2002). |
| Static rail: glyph, coarse state and age; no tool names, no animation | Peripheral information stays peripheral (Matthews 2004; Weiser & Brown 1996). Live detail invites self-interruption (Jin & Dabbish 2009). |
| Rail never reorders; numbers are stable | A rearranged context destroys reorientation cues (Mark, González & Harris 2005). Priority lives in the glyph, the count and the Inbox order. |
| Since-you-left card in time order, ending at the next goal | Resumption cues doubled task completion; developers preferred chronological snippets (Parnin & DeLine 2010; Altmann & Trafton 2002; Trafton 2003). |
| Draft and scroll position per Chat | A ready-to-resume plan reduces attention residue (Leroy & Glomb 2018). |
| Soft cap of 3 working Chats | Fan-out = activity ÷ interaction time, up to 67% lower once waits count; working memory holds about 4 chunks (Olsen & Wood 2004; Cummings & Mitchell 2008; Cowan 2001). |
| Evidence on every card; no bulk apply | Complacency grows under multi-task load (Parasuraman & Manzey 2010). |
| Inbox is self-initiated triage, not a drip of notifications | Self-initiated checking beats notification-driven checking (Mark 2016; Fitz 2019). |
| Measure time and stress per task | People feel faster while being slower (METR 2025). Interruptions cost mostly in stress (Mark 2008). |

**Why this can raise fan-out.**
- Öge raises the agent's time alone, because verification and repair run without you.
- It cuts the human's time per touch, because the card carries the Evidence.

## Settings

| Key | Values (default first) | Meaning |
|---|---|---|
| `[chat] feedback` | `ask`, `on`, `off` | A failed Check asks (U2). `on` restores the automatic turn, with `feedback_budget` (default 3). |
| `[advisor] auto` | `ask`, `on`, `off` | Default changed from `on` (U2). |
| `[desk] max_active` | `3` | Soft cap. `/new` past it warns once and never blocks. |
| `[desk] stale_gate` | `5m` | T in U3. |

## Build slices

Each slice is small, fakes-first (ADR-0017), and leaves `main` shippable.

0. **Docs:** this spec, the research note, ADR-0024, the glossary, and the D5/D-A3/D-A5 amendments.
1. **Stop auto-prompting.** Add `[chat] feedback` (gating `SendFeedback` in `cutEnded`) and change the `[advisor] auto` default to `ask`. With one Chat, the "card" is the action row. Show turns and output tokens per Accepted cut in the Receipt and `/runs` (#346).
2. **Per-Chat view model** (a refactor with no UX change):
   - Move `home`'s per-Chat fields (`chat`, `turn`, `verify`, `cutGate`, `gateFor`, `stale`, `adv`, `last`, `thread`, transcript, scroll, draft) into a `chatView`.
   - Put the Chat id on every `chat*Msg` and on `verifyTickMsg`.
   - Ctrl+C reaches only the focused Chat.
3. **Desk and rail:**
   - `/new` keeps the old Chat running.
   - The rail, switching, and `/close`.
   - The header counts and the soft cap.
4. **Attention router and Inbox:**
   - The level table, the hold-while-composing rule, and the stale-Gate bell.
   - The Inbox cards and the since-you-left card.
   - `needsYou` and `runEnded` go through the router.
5. **Same repo, several Chats:**
   - A lock on the target working tree around `delivery.ApplyOnto`.
   - Siblings refresh after an Öge apply instead of reporting `RefreshOutside`.
   - A real conflict becomes a `✗` card.
6. **`oge add "task"`:** a per-user Desk socket (P1), a Chat-level `live.pid`, and a standalone fallback when no Desk is open.
7. **Optional, herdr interop:** under `HERDR_ENV=1`, report the Desk's rolled-up state with `pane report-agent --source oge`, fire-and-forget.

## Evaluation

After slice 4, run a dogfood comparison: the same tasks done with one Chat and with the Desk. Measure:
- wall time to Accepted;
- turns and output tokens per Accepted cut;
- how long Inbox items wait;
- how many interruptions happen;
- a 1–7 stress and frustration rating per session.

If the Desk is not better on both time and stress, the defaults change (for example, the cap drops to 2).

## Not doing

- An operator or routing agent, or LLM triage.
- Agent-to-agent messaging.
- Hosting shells or panes.
- A backlog: priorities, labels, deadlines or filters on Later items (I1).
- A model-written brief, summary or "what to do next" recommendation (I4).
- A daemon or detach (#55).
