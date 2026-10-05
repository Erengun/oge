# ACP: spec, Go/Rust SDKs, and the proxy/conductor model

> Moved from branch `research/acp` at commit [`c7817b2f87ec`](https://github.com/Erengun/oge/blob/c7817b2f87ecdceabc1bd0c1e812ed91fa236356/research/acp.md). Content unchanged.

Research for [Erengun/oge#3](https://github.com/Erengun/oge/issues/3). Gathered 2026-10-04.

## Sources

Every claim below cites one of these snapshots. Repos were downloaded as tarballs at the listed HEAD.

| Ref | Repo | HEAD SHA | Commit date |
| --- | --- | --- | --- |
| SPEC | [agentclientprotocol/agent-client-protocol](https://github.com/agentclientprotocol/agent-client-protocol/tree/9cd7f4d7fe19397ddea4386926e733822a0a1925) | `9cd7f4d` | 2026-10-04 |
| RUST | [agentclientprotocol/rust-sdk](https://github.com/agentclientprotocol/rust-sdk/tree/65347cfbf5d2e01c0d2b6badbb739668df4e30d2) | `65347cf` | 2026-10-02 |
| GO | [coder/acp-go-sdk](https://github.com/coder/acp-go-sdk/tree/0845a3bb9eddda5bfc22a94dd3598c90cb842451) | `0845a3b` | 2026-06-02 |
| GO2 | [ironpark/acp-go](https://github.com/ironpark/acp-go/tree/d984fe2f13defa9216c979a5f7465dca97792070) | `d984fe2` | 2026-10-02 |
| CLA | [agentclientprotocol/claude-agent-acp](https://github.com/agentclientprotocol/claude-agent-acp/tree/a44c486019e98ad478c549c4902e7092f1418d0b) | `a44c486` | 2026-10-02 |
| CXA | [agentclientprotocol/codex-acp](https://github.com/agentclientprotocol/codex-acp/tree/ca1d97173ad37b471d5a4e5847725a4657d34e29) | `ca1d971` | 2026-10-02 |
| REG | [agentclientprotocol/registry](https://github.com/agentclientprotocol/registry/tree/50f1621eb1e1283bb93d324e5497aeae8a4d935f) | `50f1621` | 2026-10-04 |

Release dates and download counts come from the GitHub releases API and the crates.io API, both queried 2026-10-04.

Local probes ran in a scratch directory with `codex-cli 0.155.1`, `Claude Code 2.1.289`, `gemini 0.46.0`, `agy 1.2.5`, and Node `v26.8.2`. Two kinds of probe were used: an `initialize`-only handshake, and `initialize` followed by `session/new`. Neither sends a prompt, so neither uses model quota.

Housekeeping: the ACP repos moved from `zed-industries` to the `agentclientprotocol` GitHub org. Zed's `claude-code-acp` is now `claude-agent-acp`, published as npm `@agentclientprotocol/claude-agent-acp`.

---

## 1. Spec: version, stability, surface

### Versioning: three numbers, keep them apart

- **Wire protocol version**: an integer negotiated in `initialize`. The stable version is **`1`**. Version `2` is an "unstable draft" that is only available behind the `unstable_protocol_v2` feature ([SPEC `agent-client-protocol-schema/src/version.rs`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/agent-client-protocol-schema/src/version.rs)). The same file says the version is "only bumped for breaking changes", and that non-breaking changes come through capabilities.
- **Repo/crate release**: `v1.10.2`, released 2026-10-01. `v1.0.0` shipped 2026-06-24. The last 0.x release was `v0.14.0` on 2026-06-18 ([SPEC `CHANGELOG.md`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/CHANGELOG.md)).
- **Published JSON Schema tags**: `schema-v1.24.1` and `schema-v2.0.0-alpha.7`, both from 2026-09-30 (GitHub releases).

The project is moving fast. The changelog shows about ten minor releases between June and October 2026. Each new feature goes through an RFD process with these stages: Draft, Preview, Active, Completed, and To be removed ([SPEC `docs/docs.json`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/docs/docs.json)). The repo also has a conformance kit, [`agentclientprotocol/acp-tck`](https://github.com/agentclientprotocol/acp-tck). It launches an agent over stdio and reports which requirements pass. codex-acp 2.0.0 lists "resolve ACP v1 conformance failures found by acp-tck" ([CXA `CHANGELOG.md`](https://github.com/agentclientprotocol/codex-acp/blob/ca1d97173ad37b471d5a4e5847725a4657d34e29/CHANGELOG.md)).

### v1 stable surface

Source: [SPEC `schema/v1/meta.json`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/schema/v1/meta.json) and `schema/v1/schema.json`.

- **Transport**: JSON-RPC 2.0, normally over the agent subprocess's stdio. The agent is a child process of the client.
- **Agent methods (client → agent)**:
  - `initialize`, `authenticate`, `logout`
  - `session/new`, `session/load`, `session/resume`, `session/list`, `session/close`, `session/delete`
  - `session/prompt`, `session/cancel`
  - `session/set_mode`, `session/set_config_option`
- **Client methods (agent → client)**:
  - `session/update` (a notification)
  - `session/request_permission`
  - `fs/read_text_file`, `fs/write_text_file`
  - `terminal/create`, `terminal/output`, `terminal/wait_for_exit`, `terminal/kill`, `terminal/release`
  - `elicitation/create`, `elicitation/complete`
- **Protocol method**: `$/cancel_request`. Each side must answer a cancelled request with error `-32800` ([SPEC `docs/protocol/v1/cancellation.mdx`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/docs/protocol/v1/cancellation.mdx)).
- **Lifecycle**:
  - `session/load` requires the `loadSession` capability. The agent **MUST** replay the whole conversation as `session/update` notifications before it responds.
  - `session/resume` requires `sessionCapabilities.resume`. It reconnects without replaying history ([SPEC `docs/protocol/v1/session-setup.mdx`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/docs/protocol/v1/session-setup.mdx)).
  - The `session/prompt` response ends the turn with a `stopReason`: `end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, or `cancelled`.
- **Streaming (`session/update` variants)**:
  - Messages: `user_message_chunk`, `agent_message_chunk`, `agent_thought_chunk`
  - Tool calls: `tool_call`, `tool_call_update`
  - Plans: `plan`, a whole-list replacement of entries
  - Session state: `available_commands_update`, `current_mode_update`, `config_option_update`, `session_info_update`, `usage_update`
- **Tool calls**:
  - `ToolKind` values: `read`, `edit`, `delete`, `move`, `search`, `execute`, `think`, `fetch`, `switch_mode`, `other`.
  - `ToolCallStatus` values: `pending`, `in_progress`, `completed`, `failed`.
  - Tool-call content can be text, a diff (path plus old/new text), or a terminal reference. File edits therefore arrive as *agent-reported* diffs on `edit` tool calls.
- **Permissions**: `session/request_permission` offers options with a `PermissionOptionKind` of `allow_once`, `allow_always`, `reject_once`, or `reject_always`. The client picks one or returns `cancelled`.
- **Modes and config**: `session/set_mode` and `session/set_config_option` are generic, agent-defined selects, such as "mode", "model", "effort". The protocol has no typed approval-policy or sandbox field.
- **Client-side fs/terminal**: when the client advertises `fs`/`terminal` capabilities, the agent *may* delegate file reads/writes and command execution to the client. Whether it does is up to the agent.
- **Extensibility**: every type carries `_meta`. Methods whose names start with `_` are extension methods, and unknown extension notifications must be ignored.

### Unstable v1 surface (opt-in, in `schema.unstable.json`)

Source: [SPEC `schema/v1/meta.unstable.json`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/schema/v1/meta.unstable.json).

- New methods: `session/fork`, `providers/*`, `mcp/message` (MCP-over-ACP), `nes/*` (next-edit suggestions, scheduled for removal), and `document/*`.
- New update variants: `plan_update`, `plan_removed`, `notice`, `compaction_update`, `compaction_summary_chunk`, `subagent_update`, `session_message`, `session_message_chunk`.
- Several RFDs that matter to Öge are still at **Draft**: `session-fork`, `proxy-chains`, `mcp-over-acp`, `subagents` (the subagents schema landed in v1.10.0 on 2026-09-30), `plan-operations`, and `end-turn-token-usage`.

### v2 draft: what is coming

[SPEC `docs/protocol/v2/migration.mdx`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/docs/protocol/v2/migration.mdx) describes v2 as a "consolidation release", and it is breaking:

- **Prompt lifecycle**: the `session/prompt` response only acknowledges acceptance. Completion and the stop reason arrive later in a `state_update` (`running` / `idle` / `requires_action`).
- **Updates become upserts by ID**: `tool_call` is removed, `tool_call_update` creates and updates, and `plan` is replaced by `plan_update`.
- **Removed**: `session/load`, which is replaced by `session/resume` with `replayFrom`.
- **Removed**: `session/set_mode` and `current_mode_update`. Modes become config options.
- **Removed**: the entire client `fs/*` and `terminal/*` surface. The guide says to "use client-provided MCP servers when the Agent needs Client-side tools".
- **Now required**: `session/list`, `session/close`, and `session/resume`.

The migration guide tells implementers to support v1 and v2 side by side, negotiated per connection, with "v2 behind feature flags until it stabilizes". It gives no stabilization date.

---

## 2. What Öge needs that ACP does not provide

| Need | Stable v1 | Status |
| --- | --- | --- |
| **Native review** (Codex `/review`) | None | codex-acp exposes `/review`, `/review-branch`, `/review-commit` as *slash commands*, so their output arrives as an ordinary agent message ([CXA `README.md`](https://github.com/agentclientprotocol/codex-acp/blob/ca1d97173ad37b471d5a4e5847725a4657d34e29/README.md)). There are no structured review findings. |
| **Fork** | None | `session/fork` is unstable/Draft. Both adapters already advertise `sessionCapabilities.fork` (probe below). |
| **Subagent events** | None | `subagent_update` is unstable/Draft. Without negotiation, both adapters flatten subagents into tool calls on the parent session. Today, native subagent sessions are negotiated through JetBrains' `_meta.jetbrains.air.capabilities.nativeSubagentSessions` until SDKs carry the draft field ([CLA `README.md`](https://github.com/agentclientprotocol/claude-agent-acp/blob/a44c486019e98ad478c549c4902e7092f1418d0b/README.md)). |
| **Sandbox / approval policy** | Only through agent-defined modes or config options | codex-acp maps Codex approval + sandbox onto four presets: `read-only`, `workspace-write`, `agent`, `agent-full-access` ([CXA `src/AgentMode.ts`](https://github.com/agentclientprotocol/codex-acp/blob/ca1d97173ad37b471d5a4e5847725a4657d34e29/src/AgentMode.ts)). The Claude adapter exposes Claude permission modes: `default`, `acceptEdits`, `plan`, `auto`, `bypassPermissions`. Fine-grained policy (writable roots, network) is reachable only through codex-acp's `CODEX_CONFIG` env var JSON, not through ACP. |
| **Authoritative file changes** | Edits are agent-reported diffs on tool calls | Both adapters add a negotiated "agent file-change report" and "diff patch" through `_meta` AIR extensions. Öge's evidence model should not trust either and should diff the worktree itself. |
| **Structured errors** | JSON-RPC error codes only | AIR "session failure extension" through `_meta`. |
| **Token/cost usage** | `usage_update` (stable since 0.13.6) | End-turn token usage RFD is still Draft. |
| **Background tasks, goals, steering** | None | `_meta` extensions only (AIR async tasks, goal; `_meta.steering`). |

A large share of the practically useful surface lives in **vendor `_meta` extensions** rather than the spec. Those extensions are the "AIR" extensions, named for JetBrains' AIR client. Each adapter documents them in its `docs/air-extensions.md`.

---

## 3. Go SDKs

### `coder/acp-go-sdk`

This is the SDK the issue named. It appears on the spec's *community* library page, not as an official SDK ([SPEC `docs/libraries/community.mdx`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/docs/libraries/community.mdx)). The official SDKs are TypeScript, Python, Rust, Kotlin, and Java.

- **Version and staleness**:
  - Latest release is `v0.13.5`, from 2026-06-02. The last commit is from the same day.
  - It is generated from spec schema `0.13.5` ([GO `schema/version`](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/schema/version)). That is four months and about twelve spec releases behind.
  - The wire version is still `1`, and the spec changelog between `v0.13.5` and `v1.0.0` contains no breaking v1 change. So the SDK *talks* to current agents. What it lacks are later-stabilized fields and RFDs: stable message IDs, `session/delete` and `usage_update` stabilization, tool-call name, session notices, and the subagents schema.
- **Coverage**: it implements every v1 method plus most of the unstable ones, including `session/fork`, `session/resume`, `session/list`, and `providers/*` ([GO `constants_gen.go`](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/constants_gen.go)). It has no v2.
- **API shape**:
  - You implement the `acp.Client` interface, plus the optional `ClientTerminal`.
  - You wrap the agent's stdio with `acp.NewClientSideConnection(client, stdin, stdout)` and call `Initialize`, `NewSession`, and `Prompt`.
  - Extension methods go through `CallExtension` and `NotifyExtension` ([GO `README.md`](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/README.md)).
  - It is small: about 15k lines, mostly generated. It targets Go 1.21 and has no external dependencies.
- **Tests**: unit tests cover JSON parity, cancellation, notification barriers, and the example agent and client.
- **Maintenance**: this is the main weakness.
  - One main maintainer (ThomasK33, 19 commits; nobody else has more than one).
  - 14 open PRs from 2026-05 to 2026-09 are unreviewed. They include a schema bump to `1.20.0` (#53), request/notification ordering fixes (#56), and data-race fixes (#59; issues #57/#58).
- **Concrete hazard for an orchestrator**: the connection uses a bounded notification queue of `1024`. When the queue overflows, the SDK **closes the connection** ([GO `connection.go` L19, L446](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/connection.go#L446)). PRs #40, #50, and #60 to make this configurable are still open.
- **Who uses it**: GitHub code search for `coder/acp-go-sdk` in `go.mod` finds about 40 repos, including `docker/docker-agent`, `jaegertracing/jaeger`, `blacktop/ipsw`, and various agent orchestrators/wrappers.

### `ironpark/acp-go`

A Go alternative that is actively maintained.

- **Schema currency**: tracks spec schema **`1.24.1`** (v1) and **`2.0.0-alpha.7`** (v2 draft). It has packages `acp1`, `acp2`, `acphttp`, `acpmcp`, a router, middleware, and session/file stores ([GO2 `README.md`](https://github.com/ironpark/acp-go/blob/d984fe2f13defa9216c979a5f7465dca97792070/README.md)).
- **Maturity**: MIT license. Requires **Go 1.27** (`encoding/json/v2`). Effectively one maintainer, 32 stars, only tag `v0.1.0`, about 160 commits.
- It has more surface and is current, but its bus factor is as low as coder's.

**Go verdict**: there is no official Go SDK. Both Go options are single-maintainer community projects. v1 is small: about 13 methods and a dozen update variants. A Go Öge could realistically generate its own types from `schema/v1/schema.json` and own the JSON-RPC layer, using either SDK as a reference.

---

## 4. Official Rust SDK and the proxy/conductor model

### SDK

- **Crate**: [`agent-client-protocol`](https://crates.io/crates/agent-client-protocol) `2.2.0`, released 2026-09-18. About 4.9M downloads in total, 1.9M recent. The crate's semver is unrelated to the wire version. It depends on `agent-client-protocol-schema =1.9.1` ([RUST `Cargo.toml`](https://github.com/agentclientprotocol/rust-sdk/blob/65347cfbf5d2e01c0d2b6badbb739668df4e30d2/Cargo.toml)). Authors are listed as Zed, Apache-2.0, MSRV 1.88, edition 2024.
- **Workspace crates**: core, `-derive`, `-http` (HTTP/SSE/WebSocket transports), `-rmcp` (MCP integration), `-conductor`, `-polyfill`, `-trace-viewer`, `-test`, `-cookbook`, and the example client `yopo`.
- **v2**: `.v2()` builder variants exist behind `unstable_protocol_v2`.
- **Client API shape**:
  - `Client.builder().connect_with(component, |cx| …)`, then `cx.build_session(cwd).start_session()`, `session.send_prompt(..)`, and a `session.read_update()` loop. Permission requests are answered through a `responder` ([RUST `src/yopo/src/lib.rs`](https://github.com/agentclientprotocol/rust-sdk/blob/65347cfbf5d2e01c0d2b6badbb739668df4e30d2/src/yopo/src/lib.rs)).
  - `AcpAgent` / `AcpAgentConfig` launch an agent subprocess. They handle stdio wiring, bounded stderr capture, and a shutdown grace period ([RUST `src/agent-client-protocol/src/acp_agent.rs`](https://github.com/agentclientprotocol/rust-sdk/blob/65347cfbf5d2e01c0d2b6badbb739668df4e30d2/src/agent-client-protocol/src/acp_agent.rs)).
- **Coordination is left to the application**: the SDK docs say the multi-session coordination example is "a cookbook prototype, not a public SDK coordinator", with "no timeouts, automatic retries, … or prompt scheduling" ([RUST `md/session-operation-coordination.md`](https://github.com/agentclientprotocol/rust-sdk/blob/65347cfbf5d2e01c0d2b6badbb739668df4e30d2/md/session-operation-coordination.md)).

### Proxies and the conductor: what they actually are

- **A proxy** sits *between one client and one agent*. It can rewrite prompts, inject context, filter tools, or transform responses. It talks to its successor through the `_proxy/successor` extension method ([SPEC `docs/rfds/proxy-chains.mdx`](https://github.com/agentclientprotocol/agent-client-protocol/blob/9cd7f4d7fe19397ddea4386926e733822a0a1925/docs/rfds/proxy-chains.mdx), RFD status **Draft**).
- **The conductor** (`agent-client-protocol-conductor`, 2.2.0, about 3.4k downloads) spawns a **linear chain**, `Client → Conductor → P1 → … → Pn → Agent`, and presents the whole chain to the editor as a single ACP agent. "Errors in any component bring down the entire chain" ([RUST `src/agent-client-protocol-conductor/README.md`](https://github.com/agentclientprotocol/rust-sdk/blob/65347cfbf5d2e01c0d2b6badbb739668df4e30d2/src/agent-client-protocol-conductor/README.md); [RUST `md/conductor.md`](https://github.com/agentclientprotocol/rust-sdk/blob/65347cfbf5d2e01c0d2b6badbb739668df4e30d2/md/conductor.md)).
- **What they are designed for**: the RFD's motivating use is *agent extensions*, a portable replacement for AGENTS.md, hooks, and plugins that editors install the way they install MCP servers. Symposium (Rust-crate-aware proxies) is the flagship example.

**Does it reduce Öge's orchestration work?** No.

- A chain is one client in front of one agent. It does not fan out to multiple agents, assign roles, sequence planner → implementer → verifier, isolate context between sessions, or gather evidence. Öge would still have to write all of that.
- It would matter only if Öge chose to **sit inside someone else's ACP chain**, for example as a proxy in Zed or JetBrains AIR in front of a user's agent. Öge could also use a proxy to inject role context into an agent. Both are post-MVP possibilities, not MVP requirements.
- The Rust pieces that *do* help a standalone orchestrator are mundane: subprocess launch (`AcpAgent`), typed messages, builder-based sessions, the HTTP transport, and the `rmcp` integration.

---

## 5. Which agents speak ACP natively vs via adapters

From the [ACP registry](https://github.com/agentclientprotocol/registry/tree/50f1621eb1e1283bb93d324e5497aeae8a4d935f) (`*/agent.json`; 47 agents listed):

| Agent | How | Registry version |
| --- | --- | --- |
| Claude Code | **Adapter**: `@agentclientprotocol/claude-agent-acp`. A TypeScript ACP server on `@anthropic-ai/claude-agent-sdk 0.3.287` | 0.85.1 |
| Codex | **Adapter**: `@agentclientprotocol/codex-acp`. A TypeScript translator to the **Codex App Server** JSON-RPC protocol | 2.1.1 |
| Gemini CLI | **Native**: `gemini --acp` | 0.62.0 |
| OpenCode | **Native**: `opencode acp` | 1.18.34 |
| Cursor | **Native**: `cursor-agent acp` | 2026.10.01 |
| Goose | **Native**: `goose acp` | 1.53.0 |
| Antigravity | **Separate Google binary**: `agy-acp-server`. The installed `agy 1.2.5 --help` shows no ACP flag | 1.3.0 |
| Pi | **Community adapter**: `pi-acp` | 0.0.34 |

### How the adapters work, and what that means for Öge

**codex-acp**

- It "starts the Codex App Server, translates ACP requests into Codex operations, and maps Codex events back" ([CXA `README.md`](https://github.com/agentclientprotocol/codex-acp/blob/ca1d97173ad37b471d5a4e5847725a4657d34e29/README.md); spawn in [CXA `src/CodexJsonRpcConnection.ts`](https://github.com/agentclientprotocol/codex-acp/blob/ca1d97173ad37b471d5a4e5847725a4657d34e29/src/CodexJsonRpcConnection.ts)).
- It bundles its own `@openai/codex ^0.159.1`. The `CODEX_PATH` env var overrides it.
- **It is strictly a subset of `codex app-server`.** It cannot expose anything the app server lacks, and it flattens some features (review becomes a slash command; approval/sandbox become four presets).

**claude-agent-acp**

- It runs the Claude Agent SDK, which launches a native `claude` binary shipped as an SDK optional dependency. The `CLAUDE_CODE_EXECUTABLE` env var overrides it ([CLA `src/acp-agent.ts` `claudeCliPath()`](https://github.com/agentclientprotocol/claude-agent-acp/blob/a44c486019e98ad478c549c4902e7092f1418d0b/src/acp-agent.ts)).
- It is likewise a subset of what the Agent SDK / `claude -p --input-format stream-json --output-format stream-json` can do.
- It also adds work of its own: permission-mode mapping, `/mcp` handling, compaction metadata, and subagent transcripts.

**Feature loss**: the evidence is structural. Both adapter READMEs list a dozen AIR `_meta` extensions: diff patches, file-change reports, session failure, permission presentation, native subagent sessions, async tasks, goals, and steering. Those extensions exist because stable ACP cannot carry these things. A plain ACP client that does not negotiate the extensions gets the flattened, legacy representation.

**Maintenance**: both adapters are very active.

- claude-agent-acp: 0.85.1, released 2026-10-01. Top contributors are `benbrandt` (Zed, 297 commits), `agu-z`, `nikita-ashihmin`, `ConradIrwin`.
- codex-acp: 2.1.1, released 2026-10-01. Top contributors are `AlexandrSuhinin`, `ishulgin` (JetBrains-heavy).
- Both pin to a specific upstream release (`claude-agent-sdk 0.3.287`, `codex ^0.159.1`). Each upstream release therefore needs an adapter release.

### Probe results (2026-10-04, scratch dir, no prompts)

`initialize` (client advertised `fs` + `terminal`):

- **claude-agent-acp 0.85.1**: `protocolVersion: 1`, `loadSession`. `sessionCapabilities` includes `resume`, `list`, `close`, `delete`, `fork`, `additionalDirectories`, and `subagents`. Also `_meta.claudeCode.promptQueueing` and `_meta.steering`. `authMethods` was empty because the probe did not advertise the `terminal-auth` client capability. The adapter builds "Claude Subscription" and "Anthropic Console" login methods only when the client advertises it (`src/acp-agent.ts` around L2570–2700).
- **codex-acp 2.1.1**: the same session capabilities, plus `authMethods`: `api-key` and `chat-gpt`.
- **gemini 0.46.0 `--acp`** (native): `loadSession`, image/audio/embedded context, and `authMethods` (Google login, API key, Vertex, gateway). It advertises no `sessionCapabilities`.

`session/new` (existing CLI logins, no prompt):

- **claude-agent-acp**: **succeeded**. The existing Claude Code login was reused. The response listed:
  - modes: `default`, `acceptEdits`, `plan`, `auto`, `bypassPermissions`
  - config options: `mode`, `model`, `effort`, `fast`
- **codex-acp with its bundled codex**: **failed** with `-32000 Authentication required`, even though `codex login status` reports "Logged in using ChatGPT".
- **codex-acp with `CODEX_PATH` set to the installed `codex` 0.155.1**: **succeeded**. It returned a session ID, models, and the four modes.
- The probe did not establish the cause of that difference, because reading credential files was out of scope. The practical takeaway is that **"reuse the user's CLI login" means reusing the credential store through *a binary the adapter picks*.** Version skew between the bundled binary and the user's installed CLI can break it. Öge should point adapters at the user's installed binaries (`CODEX_PATH`, `CLAUDE_CODE_EXECUTABLE`).

The claude adapter has a `--hide-claude-auth` flag that *refuses* claude.ai-subscription billing, added for an integrator in [CLA #1079](https://github.com/agentclientprotocol/claude-agent-acp/issues/1079), 2026-09-03. The **default** behavior is to accept the subscription login.

---

## 6. Concurrent sessions across agent processes

- **Protocol level: yes.**
  - Every session-scoped message carries a `sessionId`. One connection, meaning one agent subprocess, can host several sessions.
  - A client can hold any number of connections, one per subprocess.
  - Requests flow in both directions on each connection, for example permission requests arriving while a prompt is pending. Cancellation is per request (`$/cancel_request`) or per turn (`session/cancel`).
- **v1 caveat**: a turn's end is the `session/prompt` response, and notifications carry no prompt identity. Under v2, idle/running state is session-wide and "updates have no prompt identity" ([RUST `md/session-operation-coordination.md`](https://github.com/agentclientprotocol/rust-sdk/blob/65347cfbf5d2e01c0d2b6badbb739668df4e30d2/md/session-operation-coordination.md)). One session per role per process keeps attribution simple.
- **SDK level**:
  - Rust supports many connections and sessions but deliberately ships no coordinator.
  - coder's Go SDK supports one `ClientSideConnection` per process. Two of its hazards matter here: the 1024-notification overflow that closes the connection, and the unmerged request-ordering fix (#56). Under load, a slow consumer can kill a busy agent's connection.
- **Workspace isolation**: `session/new` takes a `cwd` and, since 0.13.5, `additionalDirectories`. Separate worktrees per role map naturally onto separate sessions or processes.

---

## Implications for Öge

1. **ACP is a viable *common* transport, but not a sufficient one for the MVP agents.**
   - Codex and Claude Code both reach ACP only through adapters. Each adapter is a lossy translation of a richer native protocol: Codex App Server, and Claude Agent SDK / stream-json.
   - Öge's differentiators (native review, sandbox/approval control, structured file-change evidence, subagent visibility, fork) are exactly the parts that are unstable in ACP or only available through `_meta` extensions.
   - The likely shape is **native adapters for Codex and Claude Code, with ACP as the generic adapter** for Gemini, OpenCode, Cursor, Goose, and others. Under that shape, a second ACP agent (Gemini, native `--acp`, already installed) is close to free once a generic ACP adapter exists. That is relevant to the MVP cut-line exception in #1.
2. **Build against stable v1, and isolate anything v2 removes.**
   - Do not make client-side `fs/*` or `terminal/*` load-bearing, even though they would let Öge observe or execute agent I/O. The same applies to `session/set_mode` and `session/load`. v2 removes all of them.
   - Expect a v1/v2 dual stack within the project's life.
3. **Go vs Rust (feeds #15 / #17)**:
   - Rust has the official, actively released SDK (2.2.0, with a v2 draft) and the only conductor.
   - Go has two single-maintainer community SDKs. coder's is stalled at schema 0.13.5 and has a connection-killing overflow. ironpark's is current but needs Go 1.27 and has one maintainer.
   - This is a real point in Rust's favour **only if ACP is central**. If ACP is a secondary generic adapter, the v1 surface is small enough that generating Go types from the JSON schema is a modest cost.
   - The proxy/conductor model does **not** count for Rust as an orchestration accelerator.
4. **Credential boundary**:
   - Adapters bundle their own agent binaries. Öge should always pin them to the user's installed CLI (`CODEX_PATH`, `CLAUDE_CODE_EXECUTABLE`) so it reuses the existing login and version.
   - The codex-acp probe showed that the bundled binary did not see a valid login.
5. **Evidence**: ACP file edits and tool results are agent claims. Öge's deterministic evidence (worktree diff, test runs) must come from Öge itself, which matches the trust model.
6. **Testing**: `acp-tck` plus the Rust `agent-client-protocol-test` fixtures and `testy` agent mean fake ACP agents already exist. That partly answers the "test adapters without burning quota" item in #1.

## Open questions

- Why the codex-acp bundled `codex ^0.159.1` returned `Authentication required` while the installed 0.155.1 (same user, same machine) did not. Possible causes are a credential-store change, a `CODEX_HOME` difference, or account-read behavior. Resolving it needs a careful look by the auth ticket without reading secrets.
- Exact feature deltas between `codex app-server` and codex-acp, and between stream-json / Agent SDK and claude-agent-acp, such as review output structure, rollout and fork semantics, and hooks. These belong to the Codex and Claude integration research tickets. This doc only establishes that the adapters are subsets.
- When ACP v2 stabilizes, and when the Draft RFDs Öge cares about (fork, subagents, end-turn usage) reach Completed. No dates are published.
- Whether coder/acp-go-sdk will be revived (14 open PRs), or whether the ecosystem converges on another Go SDK.
- Whether Anthropic's terms allow third-party orchestrators to drive a claude.ai subscription through the Agent SDK or the adapter. The adapter's `--hide-claude-auth` flag exists so that an integration can refuse subscription billing. The source does not say why an integrator would need that. This belongs to the auth/credential ticket.
