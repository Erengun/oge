# Pi RPC mode as an architectural reference

> Moved from branch `research/pi` at commit [`f61659267c01`](https://github.com/Erengun/oge/blob/f61659267c0166d53a46b74db1f7e7eff17915a5/research/pi.md). Content unchanged.

Research for [#8](https://github.com/Erengun/oge/issues/8). Feeds [#18](https://github.com/Erengun/oge/issues/18) (AgentAdapter architecture and capability negotiation).

## Provenance

- **Canonical repo:** `earendil-works/pi` (MIT). `badlogic/pi-mono` now redirects there (`gh api repos/badlogic/pi-mono` → `earendil-works/pi`).
- **Commit read:** `200387122ca450d6387f033949423114a270b96c` (2026-10-04T02:41:25+02:00), shallow clone, read 2026-10-04.
- **Version:** `@earendil-works/pi-coding-agent` 1.0.2, released 2026-10-04 (GitHub release `v1.0.2`, 2026-10-04T00:56:36Z). 1.0.0 shipped 2026-10-01. The npm package used to be `@mariozechner/pi-coding-agent` (old changelog entries reference it).
- **Runtime:** Node.js >= 22.19.0, bin `pi` → `dist/bundle/cli.js` (`packages/coding-agent/package.json`).
- Pi is **not installed** locally. Nothing here comes from running it. Every claim cites a file in the clone; paths are relative to the repo root at the SHA above.

## 1. Integration surfaces

Pi offers four CLI modes plus an in-process SDK (`packages/coding-agent/docs/cli-integration.md`):

| Mode | Interface | Lifetime |
|---|---|---|
| Interactive | TUI | until exit |
| Print (`-p`) | final text on stdout | one invocation |
| JSON (`--mode json`) | JSONL events on stdout | one invocation; all prompts given at start |
| **RPC (`--mode rpc`)** | JSONL commands on stdin, responses + events on stdout | long-lived |
| SDK | in-process TypeScript | host process |

The docs steer non-Node hosts to RPC and Node hosts to the SDK or the shipped `RpcClient` (`docs/rpc.md`).

There is also an **experimental** remote stack: `pi-protocol` (CBOR, length-prefixed frames, protocol version 8, explicit `hello` handshake), `pi-server` (Unix-socket server with durable Sessions and several "presentation attachments" per session), `pi-client`, and `pi-durable`. Its own README says it is "experimental and has no compatibility guarantees" (`packages/protocol/README.md`, `packages/server/README.md`). In 0.85.1 (2026-09-05) these were pulled from the published npm package and made source-only (`packages/coding-agent/CHANGELOG.md` line 427). **Pi has no ACP support at this SHA**: grepping `packages/*/src`, docs and READMEs for ACP / Agent Client Protocol finds nothing.

## 2. RPC protocol mechanics

Sources: `docs/rpc.md`, `docs/rpc-commands.md`, `src/modes/rpc/rpc-mode.ts`, `src/modes/rpc/jsonl.ts`, `src/modes/rpc/rpc-types.ts` (all under `packages/coding-agent/`).

**Record families.** stdin carries commands. stdout carries `response` records, session events, and `extension_ui_request`. stdin also carries `extension_ui_response`. There is no JSON-RPC 2.0 envelope. Each record is a flat object with a `type` discriminator.

**Correlation.** Each command has an optional string `id`, and the matching `response` echoes it along with `command`, `success` and `data` or `error`. Events have no id, with one exception: `bash_execution_update` repeats the id of the `bash` command that produced it. Malformed JSON gets `{"type":"response","command":"parse","success":false}` with no id. Unknown commands get an error that carries the id (fixed in 0.79.x, CHANGELOG line 1456, after clients hung waiting).

**Concurrency.** The input loop calls `void handleInputLine(line)` without awaiting it, so several commands can be in flight at once and responses can come back out of order. The docs say: "correlate by ID rather than response order" (`rpc-mode.ts`, end of file; `rpc.md`).

**Prompt ack vs. completion.** `prompt` does not wait for the model. It answers once *preflight* finishes, with `data.disposition` set to `started`, `queued` or `handled` (added in 0.99.0, 2026-09-29; CHANGELOG line 185). Failures after that point show up only in the event stream. If a prompt arrives while a run is streaming and has no `streamingBehavior` (`steer` | `followUp`), it is rejected.

**Run lifecycle.** The event sequence is `agent_start` → `turn_start` → `message_start/update/end` → `tool_execution_*` → `turn_end` → `agent_end` → **`agent_settled`**. `agent_end` does *not* mean the run is done, because retries, overflow compaction, steering or follow-ups can still follow. `agent_settled` means Pi will not continue on its own (added in 0.80.4, 2026-07-09; CHANGELOG line 1219). The docs warn clients to subscribe *before* sending `prompt` so they don't miss a fast completion (`docs/rpc.md`, `docs/json.md`).

**Streaming payloads.** `message_update` carries only deltas (`text_delta`, `thinking_delta`, `toolcall_delta`, each with a `contentIndex`). Clients rebuild the message and then replace it with the authoritative `text_end` / `toolcall_end` / `message_end`. This was a **breaking wire change in 0.84.0 (2026-08-06)**: the old cumulative `message`/`partial` fields made output grow quadratically (CHANGELOG line 711; `docs/json.md`). Tool lifecycle events are correlated by `toolCallId`.

**Framing.** Strict LF-delimited JSONL, with an optional CR stripped. Clients must not split on U+2028/U+2029, so Node `readline` is unsafe. Pi ships its own LF-only reader (`jsonl.ts`).

**Stdout protection.** `takeOverStdout()` replaces `process.stdout.write` so that any stray write (library `console.log`, an extension, etc.) goes to **stderr**. Only `writeRawStdout` reaches the real stdout (`src/core/output-guard.ts`). The docs: "Stdout is reserved for protocol records; diagnostics ... go to stderr. Do not parse stderr."

**Backpressure.** Pi waits on stdout backpressure (`waitForRawStdoutBackpressure`, which retries on `EAGAIN`/`ENOBUFS`). A client that stops reading can stall the agent (`rpc.md`, `output-guard.ts`).

**Shutdown.** Closing stdin gives an orderly shutdown: the runtime is disposed and stdout flushed. SIGTERM exits 143 and kills tracked detached children but **skips the stdout flush**. SIGHUP (non-Windows) exits 129. An extension can ask for shutdown, which waits until the current command finishes or `agent_settled` arrives (`rpc-mode.ts` `shutdown()` / `registerSignalHandlers()`).

**No handshake, no protocol version.** RPC mode sends no initial record (JSON mode sends a session header, RPC doesn't; `docs/json.md`). `rpc-types.ts` has no version field. The shipped `RpcClient.start()` just sleeps 100 ms and checks the child hasn't exited (`rpc-client.ts` ~line 130). Each request times out after a fixed 30 s (`send()`).

**Command surface** (`docs/rpc-commands.md`):
- Prompting: `prompt`, `steer`, `follow_up`, `abort` (waits until idle), `clear_queue`.
- State: `get_state` (model, isStreaming, sessionFile, sessionId, …), `get_messages`, `get_last_assistant_text`, `get_session_stats` (tokens, cost, context usage).
- Model and thinking: `set_model`, `cycle_model`, `get_available_models`, `set_thinking_level`, …
- Queue modes, compaction (`compact`, `set_auto_compaction`), retry (`set_auto_retry`, `abort_retry`).
- `bash` (client-initiated shell command whose output is streamed and added to context unless `excludeFromContext`), `abort_bash`.
- Sessions: `new_session`, `switch_session`, `fork`, `clone`, `get_fork_messages`, `get_entries` (with a `since` cursor), `get_tree`, `set_session_name`, `export_html`.
- `get_commands` (skills, prompt templates and extension commands).

## 3. Sessions

- Sessions are append-only JSONL trees with `id`/`parentId`. The current header version is 3, and older files are auto-migrated on load (`docs/session-format.md`).
- Default location is `~/.pi/agent/sessions/--<cwd-path>--/<timestamp>_<id>.jsonl`, keyed by working directory. It can be overridden with `--session-dir`, `PI_CODING_AGENT_SESSION_DIR` or the `sessionDir` setting. `--no-session` keeps everything in memory. `--session-id <id>` opens or creates a session with a caller-chosen id (`docs/cli.md` "Sessions", `docs/sessions.md`).
- `get_entries {since}` uses entry ids as a **durable cursor that survives client restarts**, and it includes pre-compaction history and abandoned branches (`docs/rpc-commands.md` `get_entries`; added 0.80.3, 2026-06-30).
- `fork` / `clone` / `--fork` create new sessions from earlier points.

## 4. Knobs that shape a role

From `docs/cli.md` ("Tools", resource and system-prompt options) and the subagent example:

- Tools: `--tools <allowlist>` replaces the default set (`read,bash,edit,write`). Read-only built-ins are `read,grep,find,ls`. Also `--exclude-tools`, `--no-builtin-tools`, `--no-tools`.
- Resources: `--no-extensions`, `--no-skills`, `--no-context-files` (skips AGENTS.md / CLAUDE.md), `-e <extension>`.
- Prompting: `--system-prompt`, `--append-system-prompt <text|path>`, `--model provider/id[:thinking]`, `--thinking`.
- Trust: `--approve` / `--no-approve` for project trust.
- **Pi's own multi-agent example** (`examples/extensions/subagent/`) gives scout, planner, reviewer and worker roles each a separate `pi --mode json -p --no-session` process with `--tools` and `--append-system-prompt`, and chains them (`implement`: scout → planner → worker; `implement-and-review`: worker → reviewer → worker). This is close to Öge's role thesis, implemented as one-shot JSON-mode child processes.
- Structured final output: a tool can be marked `terminate: true` so the run ends on a schema-validated tool call (`examples/extensions/structured-output.ts`).

## 5. Permission model

- **Pi has no built-in per-tool approval.** "Pi can read, change, and execute files with the permissions of the account that started it, and it does not ask for approval before every tool call." Safety comes from OS, container or VM isolation (`docs/security.md`).
- The only gate is an extension hooking `tool_call` and returning `{block: true, reason}` (`examples/extensions/permission-gate.ts`). In RPC mode, `ctx.ui.select/confirm` from that hook turns into an `extension_ui_request` that the client answers with `extension_ui_response`. Dialogs can carry a `timeout`, after which Pi resolves them with a default value (`docs/rpc-extension-ui.md`, `rpc-mode.ts` `createDialogPromise`).
- **Project trust in RPC mode:** RPC can't show the trust prompt. Without `--approve`/`--no-approve`, an extension decision or a saved decision, it falls back to `defaultProjectTrust`. With `"ask"` (the default) or `"never"`, project `.pi/extensions`, `.pi/settings.json`, `.pi/mcp.json`, skills and so on are **silently skipped**. `AGENTS.md` / `CLAUDE.md` still load whatever the trust setting is. The `sessionDir` lookup happens before trust is resolved (`docs/security.md`).

## 6. Auth

- Pi runs **its own** OAuth flows (`/login`), including subscription ones: "Anthropic (Claude Pro/Max)", "OpenAI (ChatGPT Plus/Pro)", GitHub Copilot, xAI and Meta, each with `isSubscription: true`. They use client IDs hardcoded in `packages/ai/src/auth/oauth/{anthropic,openai-codex,...}.ts`, and tokens go into `~/.pi/agent/auth.json` (`docs/providers.md`). A repo-wide grep of `packages/ai/src` and `packages/coding-agent/src` for `.codex`, `.claude/` and `credentials.json` finds only Google Cloud ADC (`~/.config/gcloud/application_default_credentials.json`, ambient Vertex credentials): **Pi does not read the Codex or Claude Code CLI login stores.** A Pi user logs in to Pi separately.
- API keys from environment variables (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …) and `--api-key` are also supported.
- Pi has **credential-printing subcommands**: `pi auth print-api-key`, `pi auth print-bearer-token`, and `pi auth check --credentials`, which "write secrets to stdout". There is also a non-secret `pi auth check --provider X --json` that exits 0/1/2 for ready/not_ready/invalid (`docs/cli.md` "Credential commands").

## 7. Stability

- `rpc-types.ts` carries no protocol version, and the RPC protocol has changed behaviour several times in the last three months: `agent_settled` was added (0.80.4, 2026-07-09), `message_update` became delta-only, which is breaking (0.84.0, 2026-08-06), `toolcall_start` gained id and name (0.84.3, CHANGELOG line 570), and responses gained `disposition` (0.99.0, 2026-09-29). The CHANGELOG is the only place these changes are recorded.
- 1.0.0 (2026-10-01) makes no stated RPC compatibility promise. The 1.0.0 notes are about TUI, codemode, login and MCP (CHANGELOG lines 59–104).
- By contrast, the experimental `pi-protocol` does have an explicit version (8) and a `hello` handshake, and its envelope schemas reject unknown fields (`packages/protocol/README.md`).

## Ideas Öge should borrow

1. **Separate "accepted" from "settled".** Every adapter should report (a) whether a command was accepted, (b) that a low-level run ended, and (c) that the agent has *settled* and won't continue by itself. Öge's orchestrator should only advance roles on (c). Pi had to add `agent_settled` after the fact, so Öge should build this in from the start.
2. **Strict framing hygiene for any JSONL Öge reads or writes:** split on LF only, keep stdout for protocol records and send logs to stderr, read continuously, and honour backpressure. This applies directly to the Codex `app-server` / Claude stream-json readers too.
3. **Id-correlated, out-of-order-safe request handling.** Never assume responses come back in order.
4. **Durable cursor over an append-only event log** (`get_entries since`). A good model for Öge's own run log and for resuming after an Öge crash.
5. **Delta events plus an authoritative final record.** Store the final record as evidence and treat deltas as display-only.
6. **Role = process + tool allowlist + appended system prompt + model.** Pi's subagent example is independent evidence that the per-role-process design works without a shared context.
7. **Terminating structured-output tool** as a way to get machine-readable verdicts from verifier and reviewer roles, if an agent supports custom tools.
8. **For Öge's own future programmatic surface:** a long-lived JSONL-over-stdio mode is cheap to build and works from any language. Ship it **with a version handshake and capability list from day one**, the thing Pi's RPC mode lacks and its next-generation protocol added.

## Ideas Öge should avoid

1. **An unversioned wire protocol.** Pi's semantic breaks in RPC were documented only in the CHANGELOG. Öge's adapters should pin or detect agent versions, and Öge's own protocol should be versioned.
2. **Sleep-based readiness** (`RpcClient.start()` sleeps 100 ms) and one **fixed 30 s timeout per request**. Öge should use explicit readiness (a cheap `get_state`-style probe) and per-operation deadlines.
3. **Signal-only shutdown.** Pi's SIGTERM path skips flushing stdout. Close stdin first, wait, then signal the process group.
4. **Relying on UI dialogs for safety.** Dialogs with timeouts resolve to defaults, and Pi's docs say transcript-watching is not a security boundary. Workspace isolation stays Öge's real boundary.
5. **Leaning on an experimental transport.** `pi-server`/`pi-protocol` look promising but are explicitly unstable and not shipped on npm.

## What a Pi adapter would need (post-MVP)

- **Launch:** `pi --mode rpc --no-session` (or `--session-dir <oge-run-dir>` / `--session-id <oge-id>` when resuming), with `cwd` set to the role's worktree. Add `--no-approve`, `--no-extensions` (except Öge's own `-e`), `--no-skills`, `--tools <role allowlist>`, `--append-system-prompt <role file>` and `--model provider/id:thinking`. Decide per role whether to use `--no-context-files`. Node >= 22.19 is required on the user's machine.
- **Readiness:** send `get_state` with an id and treat the response as the handshake. Record `pi --version` and refuse or warn outside a tested range, since there is no protocol version to check.
- **Run:** subscribe, send `prompt`, check `disposition`, rebuild deltas for display, keep `message_end` and `tool_execution_end` as transcript evidence, and finish on `agent_settled`. Fetch the result with `get_last_assistant_text` or a structured terminating tool. Read usage and cost with `get_session_stats`.
- **Approvals:** Pi has none natively. Either (a) rely only on worktree or container isolation and a tool allowlist (simplest), or (b) ship a small Öge extension (`-e`) that hooks `tool_call` and turns it into `extension_ui_request` dialogs Öge answers by policy. Option (b) means Öge maintains TypeScript that runs inside Pi.
- **Cancel:** `clear_queue` then `abort`. On timeout, close stdin, then SIGTERM the process group.
- **Resume:** keep the Pi session id or file from `get_state`, relaunch with `--session <path|id>`, and catch up with `get_entries since=<last entry id>`.
- **Auth:** run `pi auth check --provider <p> --json` (status only, never `--credentials`) as a doctor or preflight step. The user logs in with Pi's own `/login`. Öge never reads `~/.pi/agent/auth.json` and never calls the `print-*` subcommands.
- **Capabilities to declare to #18:** long-lived session yes; steer/follow-up mid-run yes; native approvals **no**; tool allowlist yes; custom system prompt yes; session fork/clone yes; durable event cursor yes; structured output via a terminating tool (needs an extension); ACP no.

## Implications for other tickets

- **#18 (adapter architecture / capability negotiation):** Pi is the first researched agent with **no native approval channel**. The capability model must allow "approvals: none — isolation only" (or "via extension"). Pi also shows that a "settled" lifecycle state has to be part of the adapter contract.
- **ACP / MVP cut-line exception in #1:** Pi is **not** an ACP agent at this SHA, so it can't be the "near-free second ACP agent".
- **Auth boundary ticket:** Pi stores its own subscription OAuth tokens and exposes `print-bearer-token` / `print-api-key`. The boundary should explicitly name "never call credential-emitting subcommands of wrapped agents", and only status checks are allowed. Pi does not reuse Codex or Claude Code logins, so a Pi role needs a separate Pi login.
- **Workspace isolation:** Pi's own docs say the working folder is not a boundary and that containers or VMs are "usually the strongest practical option". This supports worktree isolation plus optional containerization rather than trusting the agent.
- **Öge's own programmatic surface (post-MVP):** use a JSONL-over-stdio mode with id correlation, accepted/settled lifecycle, a versioned handshake and a durable cursor.

## Open questions

1. Are the subscription OAuth flows in Pi (`anthropic.ts`, `openai-codex.ts`) sanctioned by the providers for third-party harnesses? Their client-ID provenance and terms are not answerable from Pi's source. This matters if Öge ever recommends Pi with a Claude or ChatGPT subscription.
2. `docs/cli.md` says explicit `-e` paths still load under `--no-extensions` ("`pi -ne -e builtin:mcp` keeps only the built-in MCP support"). Not yet verified at runtime: whether an Öge `tool_call` gate extension loaded this way reliably sees every built-in tool call, including `bash` started by codemode or MCP tools. This needs a probe once Pi is installed.
3. Would Pi accept a protocol version field or handshake in RPC mode upstream, or will RPC be replaced by the `pi-protocol` stack? There is no public statement at this SHA.
4. Concurrency limits: can `get_state` or `get_session_stats` safely run during a `prompt`? The code allows concurrent handling, but the docs don't promise it's safe.
5. Windows: SIGHUP isn't registered there, and a `powershell` tool exists. Adapter shutdown semantics on Windows are untested.
