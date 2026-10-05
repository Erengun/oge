# Competitive landscape: multi-agent coding harnesses

> Moved from branch `research/competitors` at commit [`c3a5da90c22c`](https://github.com/Erengun/oge/blob/c3a5da90c22c5378a103bfc2cdeb095e85197782/research/competitors.md). Content unchanged.

Research for [Erengun/oge#9](https://github.com/Erengun/oge/issues/9). Snapshot taken **2026-10-04**.

**Question.** Who already does what Öge proposes, and does anyone ship the full combination of the nine properties below?

**Short answer.** No surveyed project has all nine. The closest is **zeroshot** (`the-open-engine/zeroshot`). It drives the native `claude` and `codex` binaries over structured protocols, reuses their local logins, runs implementer and independent reviewers as a typed graph, and lets each node use a different harness, so Codex can review Claude's work. It lacks two of Öge's properties. Its verifiers are LLM agents, and its graph format cannot express a command, so the harness never runs tests itself; the only deterministic gate is external CI in `--pr`/`--ship` delivery. It also has no advisor. The cross-vendor review niche is crowded: OpenAI's own `codex-plugin-cc` has about 33.8k stars. Nobody combines **harness-owned deterministic evidence** with a **provider-agnostic advisor**.

Method: candidates came from web search. Each was then checked against its own README/docs, fetched with `gh api repos/<owner>/<repo>/readme` and GitHub GraphQL metadata on 2026-10-04. First-party features were checked against official docs: code.claude.com, learn.chatgpt.com, and installed `codex-cli 0.155.1` / Claude Code `2.1.289`. Stars, dates and SHAs are as of 2026-10-04.

## The nine properties (scoring rubric)

| # | Property | Scored ✓ when… |
|---|---|---|
| P1 | Native coding-CLI reuse | It drives the user's real `claude` / `codex` (etc.) binaries rather than reimplementing an agent. |
| P2 | Subscription/OAuth reuse | It works on the CLI's existing login (ChatGPT / Claude subscription) with no API key required. |
| P3 | Independent agent sessions | Each role runs as its own agent process or session. |
| P4 | Configurable engineering roles | The user can define or bind roles (planner / implementer / verifier / reviewer…) to agents. Fixed hard-coded pairs score ◐. |
| P5 | Context isolation | Roles do not share conversation history; each gets a fresh or controlled context. |
| P6 | Independent verification | The agent that wrote the change is not the one that accepts it, and this is a structural rule, not a prompt suggestion. |
| P7 | Harness-owned deterministic evidence | The harness itself runs tests/commands and gates on their results; agent claims are not evidence. |
| P8 | Cross-agent review | The review is done by a different vendor's agent than the implementer, e.g. Codex reviews Claude. |
| P9 | Provider-agnostic advisor | A consultable "second, stronger model" that can come from any provider/CLI. |

Legend: ✓ has it · ◐ partial / expressible but not the default / caveated · ✗ absent · n/a not applicable.

## Property × project matrix

| Project | Arch | P1 | P2 | P3 | P4 | P5 | P6 | P7 | P8 | P9 |
|---|---|---|---|---|---|---|---|---|---|---|
| **zeroshot** | structured (Claude `--print` stream-json; Codex `app-server`) | ✓ | ✓ | ✓ | ✓ | ✓¹ | ✓ | ◐² | ✓ | ✗ |
| **OpenRig** | tmux/PTY + hooks | ✓ | ✓ | ✓ | ✓ | ✓ | ◐³ | ✗ | ✓ | ✗ |
| **CLI Agent Orchestrator (AWS)** | tmux/PTY | ✓ | ✓ | ✓ | ✓ | ✓ | ◐³ | ✗ | ◐ | ✗ |
| **Agent Orchestrator (Untrivial)** | desktop daemon; chat or native TUI | ✓ | ◐ | ✓ | ◐ | ✓ | ◐ | ◐⁴ | ◐ | ✗ |
| **acpx** (toolkit) | structured (ACP) | ✓⁵ | ✓⁵ | ✓ | ◐ | ✓ | ◐ | ◐⁶ | ◐ | ✗ |
| **openai/codex-plugin-cc** | structured (Codex `app-server`) | ✓ | ✓ | ✓ | ✗ | ◐ | ◐⁷ | ✗ | ✓ | ◐⁸ |
| **PAL MCP `clink`** (stale) | subprocess CLIs via MCP | ✓ | ✓ | ✓ | ◐ | ✓ | ◐ | ✗ | ✓ | ◐⁹ |
| **cross-review** | subprocess (`codex exec review`, `/code-review`) | ✓ | ✓ | ✓ | ✗ | ✓ | ✓ | ◐¹⁰ | ✓ | ✗ |
| **omc-codex** | Claude Code plugin | ✓ | ✓ | ✓ | ✓ | ◐ | ✓ | ✗ | ✓ | ◐ |
| **Every Code** (Codex fork) | in-agent; spawns other CLIs | ◐ | ✓ | ✓ | ◐ | ◐ | ◐ | ✗ | ✓ | ✗ |
| **Claude Code first-party** (subagents, agent teams, advisor, hooks) | is the agent | n/a | ✓ | ✓ | ✓ | ✓ | ◐ | ◐¹¹ | ✗ | ✗¹² |
| **Codex first-party** (subagents, `codex review`) | is the agent | n/a | ✓ | ✓ | ✓ | ✓ | ◐ | ✗ | ✗ | ✗ |
| **Parallel runners** (claude-squad, Parallel Code, Crystal/Nimbalyst, vibe-kanban, dmux, Emdash, Superset, Sculptor, Conductor) | mostly PTY/tmux or embedded terminals | ✓ | ✓ | ✓ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ |
| **Öge (proposed)** | structured protocols | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

Footnotes:
1. zeroshot nodes run in separate provider sessions (`sessionScope: execution | node_instance`), but "Writers share the run's workspace, including reviewers" ([execution.md](https://github.com/the-open-engine/zeroshot/blob/main/docs/concepts/execution.md)). Conversation is isolated; the filesystem is not.
2. Verifier nodes are LLM workers emitting finite-enum signals. The graph contract says graphs "cannot represent a typed command, executable path, endpoint…" ([graph.md](https://github.com/the-open-engine/zeroshot/blob/main/docs/reference/cluster/graph.md)). The only deterministic gate is CI in `--pr`/`--ship` delivery ("waits for required CI").
3. Independence depends on the lead agent's prompt (e.g. OpenRig's owner is *asked* to "ask dev-check… to check the exact candidate"). The harness does not enforce it.
4. AO observes CI and PR state and routes failures back to the worker. That is external CI, not a local harness-run gate.
5. Via ACP adapters (`@agentclientprotocol/codex-acp`, `@agentclientprotocol/claude-agent-acp`). Login reuse is whatever the adapter supports. The Claude adapter is built on the Claude Agent SDK, whose subscription-auth terms belong to the auth ticket, not this one.
6. acpx flows have a runtime-owned `action` node with `context.runShell` ("A deterministic runtime-owned step — typically a shell command"). This is expressible in user-authored TypeScript flows, not a built-in policy.
7. Optional "review gate" uses a Claude `Stop` hook to run a Codex review and block the stop if issues are found.
8. `/codex:rescue` delegates to a different vendor's agent mid-task. It is close to a cross-vendor "advisor", but user-invoked and fixed to Codex.
9. PAL `consensus`/`chat` consult arbitrary providers, but via **API keys** (Gemini/OpenAI/OpenRouter…). Only `clink` uses CLI logins.
10. cross-review records "deterministic metrics" (precision/agreement) and source-verifies findings. It does not run tests.
11. Agent-team hooks `TaskCompleted` / `TeammateIdle` can "Exit with code 2 to prevent completion and send feedback". A user can wire a test run there as a deterministic gate, but it is user-built and Claude-only.
12. Claude Code's advisor is Claude-models-only and "requires the Anthropic API".

## Per-project entries

### zeroshot: closest competitor
- **Repo:** [the-open-engine/zeroshot](https://github.com/the-open-engine/zeroshot) (formerly `covibes/zeroshot`). MIT. 1,919★. Release v10.10.0 (2026-10-03), HEAD `d5061b85d9` (2026-10-03). Native Rust binary since v8, distributed via npm.
- **What:** "The agent that writes the code should not be the one that decides it works." Turns a goal into an explicit multi-agent graph: worker implements, acceptance and code reviewers check in parallel, rejected work goes to a bounded repair loop, delivery optionally to branch/PR/merge. Durable SQLite event ledger, resume from checkpoints, web UI, Docker target, and a paid **Zeroshot Cloud**. [README](https://github.com/the-open-engine/zeroshot/blob/main/README.md)
- **Agents/auth:** harnesses `codex`, `claude`, `copilot`. "Local `codex`/`openai`, `claude`/`anthropic`, and `copilot`/`github` runs reuse the harness's native login and configuration". Contained/Docker targets need API keys. [runtimes-and-connections.md](https://github.com/the-open-engine/zeroshot/blob/main/docs/concepts/runtimes-and-connections.md)
- **Architecture:** structured. The Claude adapter builds `--print --input-format … --output-format stream-json [--resume <id>]` (`zeroshot/src/native_v2_claude/command.rs`). The Codex adapter launches `app-server` (`zeroshot/src/native_v2_codex/permissions.rs`). The same choice Öge is heading toward.
- **Roles:** fully configurable graph (`step`, `verifier`, `seq`, `par`, `loop`, `map`, `choice`), per-node harness/model via `--runtime-config`, saved profiles. Experimental: expose a graph *as* an ACP agent (`zeroshot acp`).
- **Gaps vs Öge:** verification is agent-judged (P7 ◐, footnote 2); reviewers share the writer's workspace; no advisor; scope includes cloud/team queue (Öge's non-goals).

### OpenRig
- **Repo:** [mvschwarz/openrig](https://github.com/mvschwarz/openrig). Apache-2.0. 4,819★. v0.6.5 (2026-10-04), HEAD `23a562f0bd`.
- **What:** "Define your agent team in YAML, boot it with one command. Claude Code and Codex in the same rig." Local daemon + CLI + TUI + MCP server on **tmux**. Starter `first-project-mixed` = "Claude owner + Codex checker". "Reuse an explicit choice; no second subscription is required." [README](https://github.com/mvschwarz/openrig/blob/main/README.md)
- **Architecture:** long-lived interactive CLI sessions in tmux, with Claude/Codex activity hooks relaying events to the daemon. Writes trust settings and hooks into `~/.claude.json` / `~/.codex/config.toml`.
- **Gaps:** verification is a conversation between agents; no harness-run evidence; PTY-based.

### CLI Agent Orchestrator (CAO), AWS Labs
- **Repo:** [awslabs/cli-agent-orchestrator](https://github.com/awslabs/cli-agent-orchestrator). Apache-2.0. 1,378★. v2.5.1 (2026-09-11), HEAD `d3bea05dba` (2026-10-04).
- **What:** supervisor delegates to specialist worker agents "in isolated terminal sessions"; "agents remain full CLI processes with their native authentication". Supports Kiro (default), Claude Code, Codex, Antigravity, Copilot, OpenCode, Cursor, Grok and more. [README](https://github.com/awslabs/cli-agent-orchestrator/blob/main/README.md)
- **Architecture:** `cao-server` + tmux (PTY). Python.
- **Gaps:** no enforced independent verification, no deterministic evidence.

### Agent Orchestrator (AO), Untrivial (formerly ComposioHQ)
- **Repo:** [Untrivial-ai/agent-orchestrator](https://github.com/Untrivial-ai/agent-orchestrator) (the `ComposioHQ/agent-orchestrator` URL redirects here). Apache-2.0. 12,718★. v0.13.3 (2026-10-01).
- **What:** desktop workspace + daemon: a project "orchestrator" agent plans and spawns workers, one worktree per worker, live Kanban driven by session/PR/CI/review facts, "send CI and review feedback back to the same agent". "Any harness (Claude code, codex, +25 more)". [README](https://github.com/Untrivial-ai/agent-orchestrator/blob/main/README.md)
- **Gaps:** GUI-first; evidence is external CI; roles are orchestrator/worker rather than configurable engineering roles.

### acpx: substrate, not a product competitor
- **Repo:** [openclaw/acpx](https://github.com/openclaw/acpx). MIT. 3,312★. v0.19.4 (2026-10-01). Pre-1.0.
- **What:** headless ACP client: persistent named sessions, `exec`, NDJSON output (`--format json`), permission modes, `compare` (one prompt across agents), and experimental **flows**. Flows are TypeScript graphs of `acp` / `action` (runtime-owned shell) / `compute` / `decision` / `checkpoint` nodes, persisted under `~/.acpx/flows/runs/`. [README](https://github.com/openclaw/acpx/blob/main/README.md), [flows.md](https://github.com/openclaw/acpx/blob/main/docs/flows.md)
- **Agents:** `codex` → `npx @agentclientprotocol/codex-acp`; `claude` → `npx @agentclientprotocol/claude-agent-acp`; Pi, OpenClaw, custom. [agents.md](https://github.com/openclaw/acpx/blob/main/docs/agents.md)
- **Why it matters:** you could assemble most of Öge from acpx flows. It is the closest *building block*, and its `action` node shows the harness-owned-command pattern. It has no opinionated role/evidence policy.

### openai/codex-plugin-cc: first-party cross-vendor review
- **Repo:** [openai/codex-plugin-cc](https://github.com/openai/codex-plugin-cc). Apache-2.0. 33,828★. v1.0.6 (2026-07-08); no push since 2026-07-08.
- **What:** Claude Code plugin by OpenAI: `/codex:review` (read-only, `--base <ref>`), `/codex:adversarial-review` (steerable challenge review), `/codex:rescue` (delegate a task to Codex), **`/codex:transfer`** (hand a Claude session to Codex via "Codex's external-agent session importer"), status/result/cancel, and an optional **review gate** that "uses a `Stop` hook to run a targeted Codex review… If that review finds issues, the stop is blocked". [README](https://github.com/openai/codex-plugin-cc/blob/main/README.md)
- **Auth/arch:** "wraps the Codex app server… uses your local Codex CLI authentication". ChatGPT subscription or API key.
- **Gaps:** Claude-hosted and fixed pairing (Claude implements, Codex reviews); no harness evidence; no role model.

### PAL MCP Server (`clink`), formerly Zen MCP
- **Repo:** [BeehiveInnovations/pal-mcp-server](https://github.com/BeehiveInnovations/pal-mcp-server). LICENSE file is Apache-2.0 text (GitHub reports NOASSERTION). 11,767★. v9.8.2. **Last push 2025-12-15 (stale for ~10 months).**
- **What:** MCP server. `clink` "launches isolated Codex instances… delegate to Gemini… run specialized Claude agents" as CLI subagents with role presets (planner, codereviewer). `consensus` / `codereview` / `planner` tools call other models **via API keys**. Ships CLIs with relaxed flags (Codex `--dangerously-bypass-approvals-and-sandbox`). [clink.md](https://github.com/BeehiveInnovations/pal-mcp-server/blob/main/docs/tools/clink.md)
- **Relevance:** popularised "consult another provider" (the advisor idea), but through API keys and inside the host agent's control.

### cross-review
- **Repo:** [brettchangus/cross-review](https://github.com/brettchangus/cross-review). MIT. 0★. v1.0.0 (2026-09-19), HEAD `8234489ee5` (2026-10-03).
- **What:** runs Claude `/code-review` and `codex exec review` **independently and concurrently** ("neither can see the other's output"), then the non-host model compares and the host adjudicates every finding against source. Records precision/agreement metrics per run. Read-only. [README](https://github.com/brettchangus/cross-review/blob/main/README.md)
- **Relevance:** the cleanest design for *blind* dual review plus reviewer-effectiveness metrics, which matches Öge's "measuring verifier independence" fog. Tiny adoption.

### omc-codex
- **Repo:** [jhcdev/omc-codex](https://github.com/jhcdev/omc-codex). Apache-2.0. 2★. Last push 2026-04-03.
- **What:** oh-my-claudecode extension with a role table (Claude plans/implements, Codex reviews/adversarial-reviews), "blind TDD", cross-model fallback on rate limits, mixed teams (`/omcx:team 3:claude:executor,2:codex`). [README](https://github.com/jhcdev/omc-codex/blob/main/README.md)
- **Relevance:** shows demand for role → vendor binding and quota fallback. Not maintained at scale.

### Every Code (just-every/code)
- **Repo:** [just-every/code](https://github.com/just-every/code). Apache-2.0. 4,033★. v0.6.196 (2026-09-29).
- **What:** community fork of `openai/codex` with "Multi-agent commands – `/plan`, `/code` and `/solve` coordinate multiple CLI agents" (Claude, Gemini, GPT "consensus"/"race"), Auto Drive and Auto Review. [README](https://github.com/just-every/code/blob/main/README.md)
- **Relevance:** a forked agent as host, not a harness above agents. Fixed command set.

### Claude Code first-party (v2.1.289 installed)
- **Subagents:** "Each subagent starts with a fresh, isolated context window"; `isolation: worktree`; tool allow/deny lists; models are Claude aliases/IDs only. A subagent cannot be an external CLI. [sub-agents](https://code.claude.com/docs/en/sub-agents)
- **Agent teams:** experimental (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`). Separate Claude Code instances with a shared task list and mailbox; "The lead's conversation history does not carry over". Quality-gate hooks `TaskCompleted` / `TeammateIdle` ("Exit with code 2 to prevent completion"). Claude-only; no resume of in-process teammates. [agent-teams](https://code.claude.com/docs/en/agent-teams)
- **Advisor:** `/advisor`, `--advisor`, `advisorModel`. Experimental server-side tool; "The advisor receives the full conversation, including every tool call and result". Accepted advisors are Claude models ranked ≥ the main model. "Requires the Anthropic API" (not Bedrock/Vertex/Foundry). Works on subscription plans. [advisor](https://code.claude.com/docs/en/advisor)
- **Takeaway:** single-vendor, so it covers P3–P5 and part of P6/P7 inside Claude Code, but not P8/P9. Öge's provider-agnostic advisor differs from this one mainly in being cross-vendor and harness-invoked.

### Codex first-party (codex-cli 0.155.1 installed)
- **`codex review`:** "Run a code review non-interactively", with `--uncommitted`, `--base <BRANCH>`, custom instructions (`codex review --help`).
- **Subagents:** enabled by default in current releases; built-in `default` / `worker` / `explorer`, custom agents as TOML in `~/.codex/agents/`; intermediate output moved to separate threads; OpenAI models only. [subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents)
- **`codex app-server`** (experimental) is the structured surface that codex-plugin-cc, zeroshot and the new codex-acp all build on.

### Parallel runners (grouped: parallelism plus human review, not role orchestration)
All create one isolated worktree per agent/task, run the user's own CLIs on their own logins, and leave review and testing to the **human**. None enforce roles, independent verification or harness evidence.

| Project | License | ★ | Latest | Notes |
|---|---|---|---|---|
| [smtg-ai/claude-squad](https://github.com/smtg-ai/claude-squad) | AGPL-3.0 | 8,567 | v1.0.20 (2026-08-20) | tmux + worktrees; Claude Code, Codex, OpenCode, Amp |
| [johannesjo/parallel-code](https://github.com/johannesjo/parallel-code) | MIT | 1,033 | v3.1.0 (2026-09-26) | GUI; Claude/Codex/Gemini; "AI Arena — race agents head-to-head"; optional Docker sandbox |
| [stravu/crystal](https://github.com/stravu/crystal) | MIT | 3,123 | v0.3.5 (2026-02-26) | "Crystal is now Nimbalyst"; repo inactive since Feb 2026 |
| [BloopAI/vibe-kanban](https://github.com/BloopAI/vibe-kanban) | Apache-2.0 | 28,259 | push 2026-09-19 | Kanban of agent tasks, diff review, PRs |
| [standardagents/dmux](https://github.com/standardagents/dmux) (was formkit/dmux) | MIT | 1,793 | v5.11.1 (2026-08-16) | tmux pane + worktree per task |
| [generalaction/emdash](https://github.com/generalaction/emdash) | Apache-2.0 | 5,905 | v1.2.7 (2026-09-27) | YC W26 "Agentic Development Environment" |
| [superset-sh/superset](https://github.com/superset-sh/superset) | custom (NOASSERTION) | 14,864 | desktop-v1.35.0 (2026-10-02) | "Run any agent with your own subscription"; terminals + diff + browser |
| [imbue-ai/sculptor](https://github.com/imbue-ai/sculptor) | MIT | 235 | v0.48.0 (2026-09-21) | experimental; container backend |
| Conductor ([docs](https://www.conductor.build/docs/)) | closed, macOS | – | – | "run Claude Code, Codex, Cursor, and OpenCode in parallel"; per-task workspace + review path |

### Substrates and status changes worth knowing
- **coder/agentapi: deprecated and archived.** [coder/agentapi](https://github.com/coder/agentapi), MIT, archived (last push 2026-09-13; last release v0.12.2 2026-05-27). README: "AgentAPI is deprecated and no longer maintained." It was a PTY/terminal-emulation HTTP wrapper.
- **zed-industries/codex-acp: archived.** "Development has moved to [agentclientprotocol/codex-acp]… The new adapter is built on the new Codex App Server". New repo: v2.1.1 (2026-10-01), 429★. Claude side: [agentclientprotocol/claude-agent-acp](https://github.com/agentclientprotocol/claude-agent-acp) v0.85.1 (2026-10-02), Apache-2.0. ACP spec repo: schema-v1.24.1 (2026-09-30).
- **h5i: pivoted.** [h5i-dev/h5i](https://github.com/h5i-dev/h5i) (Apache-2.0, 673★) shipped a Claude/Codex ensemble in mid-2026 ("h5i team"/"orchestra": blind sandboxes, peer review, neutral verifier that "replays every candidate and merges only the one that passes"; commits `662d189cad` 2026-07-02, `ef0354992a` 2026-07-13). As of v0.4.8 (2026-10-01) the README is "The Agent-Native Web Security Workspace" and MANUAL.md no longer documents team/ensemble. The neutral-replay-verifier idea was the closest match to P7; it is no longer a maintained product feature.

Seen in search results but **not verified** against a primary source (excluded from the matrix): Jockey (ACP orchestrator), claw-orchestrator, OmniAgent, CC Mirror, mark-dingwall/zeroshot fork.

## Gaps: what nobody ships

1. **Harness-owned deterministic evidence as the acceptance gate (P7).** Every surveyed orchestrator lets an LLM decide "verified". Deterministic signals exist only as external CI (zeroshot `--ship`, AO), user-wired hooks (Claude `TaskCompleted`), or toolkit primitives (acpx `action`). None makes "the harness ran the tests, captured exit codes and output, and that record is the evidence" the default contract. This is Öge's clearest differentiator.
2. **Provider-agnostic advisor (P9).** Claude's advisor is Claude-only and Anthropic-API-only. PAL's consensus is API-key-based and stale. codex-plugin-cc `/codex:rescue` is user-invoked and Codex-only. No one offers "consult any logged-in CLI as advisor".
3. **All nine properties in a local, single-user CLI.** zeroshot is the only one at 7/9, and it is drifting toward cloud/team (Zeroshot Cloud, hosted merge plans). The parallel runners cover P1–P3 and P5 but none of the engineering discipline.
4. **Workspace-level independence for verifiers.** zeroshot reviewers share the writer's workspace. The h5i-style "fresh clean box, replay the candidate" verifier no longer ships.

## Ideas worth adopting

- **Typed, bounded graph with finite-enum verifier signals** (zeroshot): verifiers return a closed enum plus a typed diagnostic, loops have `maxIterations`, model output "cannot add an edge or raise a bound". This fits Öge's "agent claims are suggestions" stance.
- **Session scope per role** (zeroshot `sessionScope: execution | node_instance`): explicit choice between fresh context per run and a reused session across repair iterations.
- **Validate-only dry run** (`zeroshot run --validate-only`, `rig up --plan`, `rig setup --dry-run`).
- **Durable run ledger + checkpoint resume** (zeroshot SQLite ledger, `resume --from-checkpoint`; acpx `~/.acpx/flows/runs/`). Feeds Öge's failure/resume ticket.
- **Runtime-owned shell action with strict timeout/cancel/cleanup semantics** (acpx `runShell`: nonzero exit as data, `timedOut`, process-tree cleanup, `maxBufferBytes`). A good spec for Öge's evidence runner.
- **Blind concurrent dual review + reviewer metrics** (cross-review): start both reviews before either writes, adjudicate findings against source, record precision/agreement per reviewer. A concrete answer to "measuring verifier independence".
- **Handoff via the vendor's own importer** (`/codex:transfer` uses Codex's external-agent session importer). Check this before building a custom handoff format.
- **Transparency about machine changes** (OpenRig's "What OpenRig changes on your machine" table): Öge should say exactly which config/trust files it touches, ideally none.
- **Don't relax agent sandboxes by default** (counter-example: PAL clink ships `--dangerously-bypass-approvals-and-sandbox`).

## Sources (primary)

READMEs fetched 2026-10-04 via `gh api -H "Accept: application/vnd.github.raw" repos/<repo>/readme`; metadata via GitHub GraphQL the same day. Additional files:
- zeroshot: [docs/concepts/runtimes-and-connections.md](https://github.com/the-open-engine/zeroshot/blob/main/docs/concepts/runtimes-and-connections.md), [docs/concepts/execution.md](https://github.com/the-open-engine/zeroshot/blob/main/docs/concepts/execution.md), [docs/reference/cluster/graph.md](https://github.com/the-open-engine/zeroshot/blob/main/docs/reference/cluster/graph.md), `zeroshot/src/native_v2_claude/command.rs`, `zeroshot/src/native_v2_codex/permissions.rs` @ `d5061b85d9`
- acpx: [docs/flows.md](https://github.com/openclaw/acpx/blob/main/docs/flows.md), [docs/agents.md](https://github.com/openclaw/acpx/blob/main/docs/agents.md) @ `27efb1b57b`
- PAL: [docs/tools/clink.md](https://github.com/BeehiveInnovations/pal-mcp-server/blob/main/docs/tools/clink.md) @ `7afc7c1cc9`
- h5i: [docs/MANUAL.md](https://github.com/h5i-dev/h5i/blob/main/docs/MANUAL.md) @ `564a8b736c`; commit search `repo:h5i-dev/h5i team`
- Claude Code docs: [advisor](https://code.claude.com/docs/en/advisor), [agent-teams](https://code.claude.com/docs/en/agent-teams), [sub-agents](https://code.claude.com/docs/en/sub-agents)
- Codex docs: [subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents); local `codex review --help` (codex-cli 0.155.1)
- Conductor: [conductor.build/docs](https://www.conductor.build/docs/)
