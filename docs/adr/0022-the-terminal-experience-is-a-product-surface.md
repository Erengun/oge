---
status: accepted (supersedes ADR-0015's "no elaborate TUI" and "append-only lines" output rule; refines ADR-0019's terse default)
---

# The terminal experience is a core product surface: a polished live TUI over an invisible trust architecture

The product owner corrected the roadmap after M1's first slices: **a polished, Claude Code-like terminal experience is a core Öge product goal, not post-MVP decoration.** Öge should feel like a first-class coding agent in the terminal. A reader might expect one of two things this design rejects:

- the plain append-only line stream of ADR-0015 as the product's face;
- a TUI that changes trust semantics: default choices, hidden Evidence, or decisions made by keystroke shortcuts.

## Product constraint

> If Öge is dramatically slower or more annoying than native Claude/Codex workflows without proportional value, we failed.

This sits beside "Trust must earn its overhead" (#66) and the speed and interruption gate criteria in ADR-0019.

## Decisions

- **On an interactive terminal, `oge "task"` renders a live TUI.** It shows:
  - clear live stage and progress status, with elapsed time;
  - compact agent activity: the current tool or action and recent steps, collapsible;
  - clean Host-request and Gate interactions;
  - inspect views (diff, Check output, held-out source under ADR-0015's recorded-view rule);
  - the final Receipt.

  The happy path shows minimal complexity, and the trust architecture stays invisible underneath.
- **Plain text and JSON stay first-class for CI and automation.** Without a TTY, or with `--plain`, Öge emits ADR-0019's terse stage lines. The `-v`/`-vv` levels, exit codes and `--json` schemas from ADR-0015 are unchanged.
- **The TUI is a view, never a source of truth.**
  - It renders Öge's normalised events and Ledger-derived state, and reaches nothing the plain mode can't.
  - Everything ADR-0015 says the terminal never shows still holds: raw frames, credentials, held-out source outside the inspect view, forbidden Briefing content, unredacted output.
- **Decisions keep ADR-0015's rules inside the TUI.**
  - No default choice, and Enter alone does nothing.
  - Trust-weakening choices are typed as full words, with a reason.
  - The decision is durable before the TUI shows it as taken.
  - Menus and keybindings may help navigate and select, but they never shortcut a full-word entry.
- **The terminal experience is part of product acceptance.**
  - Each user-facing M1 ticket includes its terminal experience in its acceptance criteria. Golden tests cover the plain mode, and snapshot or model tests cover the TUI.
  - M1 is reached only when the full run feels like a first-class coding-agent CLI, not merely when it's correct.

## Consequences

- New M1 ticket: the TUI foundation (renderer, live stages, agent activity, plain fallback).
- The user-facing tickets gain terminal-experience criteria: #43, #44, #46, #54 and #63 in M1, and the Gate and Host-request tickets after it.
- `docs/ux/oge-run-transcript.md` stays illustrative.

## What would reverse this

- Users and the evaluation consistently prefer plain output over the TUI. Then make plain the default, and keep the TUI opt-in.
