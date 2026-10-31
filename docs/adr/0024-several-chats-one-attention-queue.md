---
status: accepted
---

# Several Chats share one attention queue: still supervision, not orchestration

Bare `oge` becomes a **Desk**: several independent Chats in one full-screen home, with one **Inbox**. Öge decides what reaches the human through a fixed table of attention levels built from its own event streams. No model takes part in that decision. A failed Check, or an exhausted feedback budget, no longer sends an agent turn by itself. It asks the human instead. Spec: [`docs/ux/desk.md`](../ux/desk.md). Evidence: [`docs/research/herdr-and-attention.md`](../research/herdr-and-attention.md).

## Context

- People run several agents at once. Parallel runners such as herdr show which pane needs attention by reading the screen. That signal fails in exactly the cases that matter: an approval shows as idle and expires, or a false "finished" appears (herdr #5546, #5490, #1217, #5024).
- Öge already drives Claude, Codex and agy over structured streams (ADR-0004, ADR-0018, ADR-0023), so it knows each agent's state exactly.
- Öge's earlier way of sparing the human was to have agents prompt agents: automatic feedback turns (chat-first D5) and an automatic Advisor (D-A3, D-A5). In use this added output tokens and waiting time. The user rejected it, and also rejected a standing "operator" agent (2026-10-31).

## Decisions

- **One `oge` process can host several Chats.** Each Chat keeps everything chat-first gave it: its own Snapshot, Workspace, Session, Ledger and Runs (`run.Chat` already supports this, `internal/run/chat.go`). Chats never message each other. This amends chat-first D1 ("one Chat per home").
- **Attention routing is deterministic.** A fixed table maps each event to one of Matthews et al.'s levels: ignore, change-blind, make aware, interrupt, demand.
  - While the input box is non-empty, everything below *demand* is held, and released on send or clear.
  - Only a Gate or ask left open longer than T, or an approval about to expire, demands attention.
- **A failed Check asks.** `[chat] feedback` defaults to `ask`, and `[advisor] auto` defaults to `ask`. The automatic paths remain available as opt-in (`on`). This amends chat-first D5 and advisor D-A3/D-A5.
- **The layout follows cited evidence.** Every Desk behaviour in `desk.md` names the principle it follows. A change that contradicts one needs new evidence or an ADR.
  - The rail order is stable and static.
  - The Inbox is triaged one card at a time, with Evidence on each card.
  - Switching to a Chat shows a resumption card.
  - Draft and scroll are kept per Chat.
  - There is a soft cap on working Chats.

## Why this is still supervision

`docs/positioning.md` says Öge is "not generic multi-agent orchestration", and that position holds:
- The Desk adds no roles, no agent-to-agent channel and no planner. It holds N independent supervised Chats.
- What it adds is attention routing for the human, which is the "Protect attention" layer positioning already names.
- User-facing copy keeps the wording rules: the Desk is described as "several Chats, one Inbox", never as "multi-agent".

## Consequences

- `home` must split its per-Chat state into a per-Chat view, and every Chat message must carry a Chat id. Ctrl+C reaches only the focused Chat.
- Two Chats can deliver to the same working tree. Öge therefore needs a lock on the target tree around `delivery.ApplyOnto`, and sibling Chats must refresh after an Öge apply instead of reporting `RefreshOutside`.
- With feedback set to `ask`, a failing chain costs no tokens until the human chooses. The trade-off is that an unattended Chat no longer repairs itself by default. `on` restores that.
- Öge knows exact state only for agents it drives. It is not a terminal, and it does not host shells, servers or watchers. herdr remains a good home for those, and Öge may report its rolled-up state there (`pane report-agent`).

## What would reverse this

- The dogfood comparison in `desk.md` shows the Desk is not better than one Chat on either wall time to Accepted or stress. In that case the cap drops, or the Desk becomes opt-in.
- Measured data shows that automatic feedback turns save more human attention than they cost in tokens and time. In that case `feedback` defaults back to `on`.
