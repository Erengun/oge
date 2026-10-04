---
status: accepted
---

# ACP is Öge's generic client path, not the transport for Codex and Claude

ACP is the standard way to drive coding agents, so a reader may expect Öge to speak ACP to everything. It does not ([#17](https://github.com/Erengun/oge/issues/17)). Codex and Claude Code stay on their native protocols (Codex app-server, Claude stream-json) for as long as those expose capabilities Öge needs that ACP does not: native review, typed sandbox and approval policy, stable fork and subagent events. Codex and Claude reach ACP only through adapters (`codex-acp`, `claude-agent-acp`) that lose these features and bundle their own agent binaries ([#3](https://github.com/Erengun/oge/issues/3)). ACP is the default integration path for agents without a materially richer native interface. Öge is an ACP **client** only.

## Considered options

- **ACP for every agent, Codex and Claude included.** Rejected. Öge would lose the native features it relies on.
- **A generic ACP adapter in the MVP.** Rejected. The only candidates were Gemini, which lost consumer login on 2026-06-18, and OpenCode, which needs its own install and provider login. The MVP cut line's "second ACP agent near-free" exception is dropped.
- **Öge as an ACP agent driven by editors.** Not designed for, and not ruled out. An ACP session is one conversation with one agent. An Öge run (several roles, Evidence, verdict) would flatten into a single text stream. Öge's core run API stays independent of transport, with the CLI as one front end.
- **Öge as an ACP proxy**, whether in a conductor chain or passing editor sessions through. Rejected. A conductor is a straight chain in front of one agent and saves Öge no orchestration work.

## Consequences

- **MVP.** No ACP code. The adapter interface ([#18](https://github.com/Erengun/oge/issues/18)) is checked on paper to confirm that an ACP v1 agent could sit behind it. Features that only some agents have are optional capabilities, not assumptions in the interface.
- **Post-MVP.** The first ACP target is OpenCode. The smallest ACP v1 client is built per ADR-0001, with these rules:
  - Never call `authenticate` speculatively.
  - Negotiate capabilities per agent and per session.
  - Do not build important Öge semantics on surfaces that ACP v2 drops: `session/load`, `set_mode`, client `fs/*` and `terminal/*`.
- **Client capabilities.** Öge does not advertise the ACP client `fs/*` and `terminal/*` capabilities. Leaving them out is **not** Öge's security boundary. Workspace isolation, process isolation and execution policy belong to their own decisions.

## What would reverse this

- A future ACP version reaches functional parity with the native Codex and Claude protocols and materially lowers maintenance cost. Codex and Claude routing is then re-evaluated.
- The native protocols are deprecated in favour of ACP.
- The user requires OpenCode, or another ACP-only agent, in the MVP.
