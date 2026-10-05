# Claude Code: structured modes, control protocol and auth

> Moved from branch `research/claude` at commit [`739e6cab128b`](https://github.com/Erengun/oge/blob/739e6cab128ba94bdeacec9b3347468c35b6ea21/research/claude.md). Content unchanged.

Research for [Erengun/oge#5](https://github.com/Erengun/oge/issues/5). Researched 2026-10-04.

**Versions and sources**

| Source | Version / date |
| - | - |
| Installed `claude` binary (`~/.local/bin/claude`) | `2.1.289 (Claude Code)` |
| Official docs, fetched as Markdown from `https://code.claude.com/docs/en/<page>.md` | fetched 2026-10-04 (pages carry no version; claims below quote the "requires vX" notes they contain) |
| `anthropics/claude-agent-sdk-python` (MIT) | `9c69ce7aced5cdf2aa1ac86fe62e877b4962de8b` (2026-10-03), package `0.2.163`, bundles CLI `2.1.289` (`_cli_version.py`) |
| `anthropics/claude-agent-sdk-typescript` | `16cf0a783143406b1ad5c4a2b0dbf0af2be04b04` (2026-10-03). The repo holds README, CHANGELOG and examples only, no source. `LICENSE.md`: "© Anthropic PBC. All rights reserved. Use is subject to Anthropic's Commercial Terms of Service." |
| Anthropic Consumer Terms | "Effective October 8, 2025" |
| Probes | 2 throwaway `claude -p` stream-json sessions on `--model haiku` in a scratch git repo, 2026-10-04 (log excerpts below) |

Doc pages cited by short name: `headless` (Run Claude Code programmatically), `cli-reference`, `sessions`, `authentication`, `legal-and-compliance`, `permission-modes`, `sandboxing`, `agent-sdk/overview`, `agent-sdk/typescript`, `agent-sdk/permissions`, `agent-sdk/claude-code-features`, `env-vars`.

---

## 1. Answer in brief

- `claude -p --input-format stream-json --output-format stream-json --verbose` is a long-lived, bidirectional NDJSON session over stdin/stdout. Multiple user turns are just more `{"type":"user",...}` lines on stdin. Each turn ends with its own `result` message. **Verified by probe.**
- The same pipe carries a **control protocol**: `control_request` / `control_response` / `control_cancel_request` envelopes in both directions. The CLI sends permission prompts as `can_use_tool` control requests when started with `--permission-prompt-tool stdio`, which is what the official SDKs do. **Verified by probe** (allow round-trip, interrupt receipt).
- The Agent SDKs (Python and TS) are thin wrappers that spawn the `claude` CLI and speak this protocol. There is **no standalone protocol spec**, but the TS SDK reference explicitly addresses clients that "parse the wire protocol yourself" and "drive the CLI's control protocol directly", and gives a `capabilities` array for feature detection. The MIT Python SDK is the readable reference implementation. A Go or Rust client can speak the protocol directly. No SDK is needed.
- **Auth:** the CLI uses whatever login the user already has (OAuth from `/login`, or an API key, etc.). `claude auth status --json` reports login state without exposing credentials.
- **Terms of use. This is a real constraint, see §7.** Anthropic's docs say subscription OAuth is "intended exclusively for … ordinary use of Claude Code and other native Anthropic applications". They also say third-party developers may not "offer claude.ai login or rate limits for their products, including agents built on the Claude Agent SDK". At the same time they state this does not prevent "an end user from signing in to the unmodified Claude Code binary with their own Claude subscription". Öge sits in that gap. Read literally the text allows it, but the case is ambiguous and Anthropic reserves enforcement "without prior notice".
- **Roadmap risk:** `--bare` never reads OAuth or the keychain, and the docs say `--bare` "will become the default for `-p` in a future release". If that ships without an opt-out, subscription-authenticated `claude -p` stops working.
- **Context isolation:** a default `-p` session loads the user's whole setup: plugins, hooks, MCP servers including claude.ai connectors, skills, CLAUDE.md and auto memory. `--setting-sources=` (empty), `--strict-mcp-config` and `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` remove most of it while keeping subscription auth. Some things remain (§8). `CLAUDE_CONFIG_DIR` cannot be used for isolation because it also moves the credential lookup.

---

## 2. Structured modes (`-p`)

From `claude --help` (2.1.289) and `headless`:

- `--output-format text|json|stream-json` (only with `--print`). `json` gives a single result object (`result`, `session_id`, `total_cost_usd`, usage…). With `--json-schema '<schema>'` it also gives `structured_output` (headless "Get structured output"; an invalid schema is an error since v2.1.205).
- `--input-format text|stream-json`. stream-json means "realtime streaming input".
- `stream-json` output requires `--verbose`. The SDK always passes `--output-format stream-json --verbose … --input-format stream-json` (python `subprocess_cli.py` L574, L793).
- `--include-partial-messages` emits `stream_event` token deltas. `--include-hook-events` emits all hook lifecycle events. `--replay-user-messages` echoes stdin user messages back. `--forward-subagent-text` forwards subagent text and thinking blocks.
- `--no-session-persistence` (with `-p`) writes no transcript.
- `--max-budget-usd`, `--fallback-model`, `--model`, `--effort` all apply in `-p`.
- `-p` skips the workspace trust dialog, and "settings files that fail validation are silently ignored in this mode" (`--help`). Without `--bare`, a `-p` session "runs the hooks in a project's `.claude/settings.json` and connects the servers in its `.mcp.json`, even in a folder you've never trusted" (`headless` "Start faster with bare mode").
- Slash commands and skills work in `-p` when sent as `/name` in the prompt. `/model x`, `/effort`, and `/config key=value` take arguments. Terminal-only commands such as `/login` do not work (`headless`, v2.1.205+).

### Message shapes observed (probe, CLI 2.1.289)

Stdin, one JSON per line:

```json
{"type":"control_request","request_id":"req_init","request":{"subtype":"initialize","hooks":null}}
{"type":"user","uuid":"00000000-0000-4000-8000-000000000001","session_id":"","parent_tool_use_id":null,
 "message":{"role":"user","content":[{"type":"text","text":"…"}]}}
```

Stdout, in observed order for a turn that used one tool:

| `type` / `subtype` | Notes |
| - | - |
| `system/hook_started`, `system/hook_response` | User `SessionStart` hooks ran before `init`. In probe 1 one injected a large `additionalContext` block (a user plugin's "superpowers" preamble). This is a concrete example of context leaking in. |
| `control_response` (to `initialize`) | `response` keys: `commands, agents, output_style, available_output_styles, models, account, pid, current_permission_mode, fast_mode_state, session_state, capabilities, …`, plus `pending_permission_requests: []` on the wrapper. `account` has `email, organization, subscriptionType, apiProvider`. **This is PII, so do not log it.** |
| `command_lifecycle` | `{command_uuid, state: queued→started→completed}` keyed to the user message `uuid`. Advertised as capability `msg_lifecycle_v1`. |
| `system/init` | `session_id, cwd, model, permissionMode, tools[], mcp_servers[{name,status}], plugins[], agents[], skills, slash_commands, apiKeySource, claude_code_version, capabilities[], memory_paths`. **Re-emitted at the start of every turn**, not just once. |
| `system/thinking_tokens` | Progress estimate. |
| `assistant` | Full Anthropic message per content block (`thinking` with signature only, `text`, `tool_use{id,name,input}`). `parent_tool_use_id` is `null` for the main thread and the spawning tool-use id for subagents. |
| `control_request` `can_use_tool` | See §3. |
| `user` (tool result) | `message.content[].tool_result` plus a structured **`tool_use_result`** (below). |
| `rate_limit_event` | `rate_limit_info: {status:"allowed", resetsAt, rateLimitType:"five_hour", overageStatus, …}`. Gives a subscription-quota signal. |
| `result` | `subtype: success|error_during_execution|…`, `is_error`, `result` (text), `session_id`, `num_turns`, `total_cost_usd` (a client-side estimate, also reported under subscription), `usage`, `modelUsage`, `permission_denials[]`, `terminal_reason` (`completed`, `aborted_streaming`), `user_message_uuid(s)`, `result_index`, `subagent_stats`. One per user message. |

`capabilities` reported by 2.1.289: `interrupt_receipt_v1, interrupt_cancel_queued_v1, interrupt_send_now_v1, msg_lifecycle_v1, sdk_mcp_tools_list_changed, sdk_mcp_manifests, mcp_read_resource_v1, mcp_tool_ui_meta_v1, ui_surface_v1`. The docs say to feature-detect on this list and ignore unknown values (`headless` "Read session metadata"; requires v2.1.205+).

The full TS union `SDKMessage` (`agent-sdk/typescript` "Message Types") also lists `stream_event` partials, `compact_boundary`, `status`, `tool_progress`, `auth_status`, `task_started/progress/updated/notification`, `background_tasks_changed`, `session_state_changed`, `permission_denied`, `api_retry`, `notification`, `files_persisted`, `tool_use_summary`, `prompt_suggestion`, `informational`, `conversation_reset`, and others.

### File-edit events: yes, structured diffs

The tool-result `user` message carries `tool_use_result`:

- **Edit** (probe 2): `{"filePath":…, "oldString":"hi", "newString":"hello", "originalFile":"hi", "structuredPatch":[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-hi","\\ No newline at end of file","+hello","\\ No newline at end of file"]}], "userModified":false, "replaceAll":false}`
- **Write**, new file (probe 1): `{"type":"create","filePath":…,"content":"hi","structuredPatch":[],"originalFile":null,"userModified":false}`
- **Read**: `{"type":"text","file":{"filePath","content","numLines","startLine","totalLines"}}`

These shapes are not documented as a stable contract. For evidence, Öge should diff the worktree itself (git) and treat these events as hints.

### Subagents, tasks, todos, plan mode

- Subagent messages arrive as `assistant`/`user` with `parent_tool_use_id` set to the spawning Agent/Task tool call. Nesting can be rebuilt from those ids. Subagent text and thinking appear only with `--forward-subagent-text` (v2.1.211+; nested subagents v2.1.219+) (`headless` "Follow subagent messages"). Background subagents and Bash tasks emit `system/task_started|task_progress|task_updated|task_notification` (`agent-sdk/typescript` `SDKTaskStartedMessage`). The `result` carries `subagent_stats`.
- `-p` waits for background subagents or workflows (10-minute idle ceiling, `CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`). Background Bash is killed about 5 s after the final result once stdin closes (`headless` "Background tasks at exit").
- Todos are not a separate event type. They are tool calls (`TaskCreate/TaskGet/TaskUpdate/TaskList`, or `TodoWrite` with `CLAUDE_CODE_ENABLE_TASKS=0`). On newer models these are opt-in only (`agent-sdk/typescript` "TodoWrite" note). The probe's `init.tools` on Haiku 4.5 included the four Task tools.
- **Plan mode** (`--permission-mode plan`): read-only tools run. File edits, and since v2.1.212 shell commands that modify files, always go to the permission handler regardless of allow rules (`agent-sdk/permissions` "Plan mode"). `ExitPlanMode` is a tool call, so the host sees the plan as tool input and approves or denies it. The TS option `planModeInstructions` can replace the plan-mode workflow body. Mode can be switched mid-session with the `set_permission_mode` control request.

---

## 3. Control protocol

Envelope (python SDK `_internal/query.py`, confirmed by probe):

```json
// either direction
{"type":"control_request","request_id":"<id>","request":{"subtype":"…", …}}
{"type":"control_response","response":{"subtype":"success","request_id":"<id>","response":{…}}}
{"type":"control_response","response":{"subtype":"error","request_id":"<id>","error":"…"}}
// CLI → host: abandon a pending request it sent
{"type":"control_cancel_request", …}
```

**Host → CLI subtypes** (python `query.py` L341–L870): `initialize` (hooks with `hookCallbackIds`, `agents`, `skills`, `excludeDynamicSections`, `systemPromptSnapshot`, `forwardSubagentText`), `interrupt` (optionally with `cancel_queued: true`), `set_permission_mode`, `set_model`, `rewind_files`, `mcp_status`, `mcp_reconnect`, `mcp_toggle`, `get_context_usage`, `stop_task`.

**CLI → host subtypes** (python `query.py` L581–L660): `can_use_tool`, `hook_callback` (for hooks registered in `initialize`), `mcp_message` (in-process SDK MCP servers).

### Permission prompts

The SDK maps `canUseTool` to `--permission-prompt-tool stdio` (python `types.py` ~L1927–1943: "returns a copy with `permission_prompt_tool_name="stdio"`"). Observed request:

```json
{"type":"control_request","request_id":"3c4cfb77-…","request":{"subtype":"can_use_tool",
 "tool_name":"Edit","display_name":"Edit",
 "input":{"file_path":"…/hello.txt","old_string":"hi","new_string":"hello","replace_all":false},
 "description":"hello.txt",
 "permission_suggestions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}],
 "tool_use_id":"toolu_01LmN…"}}
```

Reply used in the probe (the tool then ran):

```json
{"type":"control_response","response":{"subtype":"success","request_id":"3c4cfb77-…",
 "response":{"behavior":"allow","updatedInput":{…original input…}}}}
```

Deny is `{"behavior":"deny","message":"…","interrupt":true?}`. Allow may add `updatedPermissions` (python `query.py` L615–L637). Other optional request fields: `agent_id`, `blocked_path`, `decision_reason`, `title`.

Related knobs:
- `--permission-prompt-tool <mcp tool>` sends prompts to an MCP tool instead of stdio.
- `--permission-prompts none` (v2.1.259+): nobody answers. Anything that would prompt is denied, `AskUserQuestion` is removed, denials show as `permission_denied` system messages and in `result.permission_denials` (`headless`).
- `--permission-mode` choices (2.1.289 help): `acceptEdits, auto, bypassPermissions, manual, dontAsk, plan`. `default` was also accepted and echoed as `permissionMode:"default"` (probe), presumably an alias of `manual`. With no mode set, the built-in starting mode "can be `auto`" (a classifier), so **Öge should always pass a mode explicitly** (`headless` "Auto-approve tools").
- `--allowedTools` / `--disallowedTools` take permission-rule syntax, e.g. `Bash(git diff *)`. `--tools` restricts the built-in tool set itself (`""` means none).
- After a transport gap, the `initialize` response's `pending_permission_requests` re-delivers unanswered prompts (v2.1.268+). Handle them idempotently by `request_id` (`agent-sdk/typescript` `SDKControlInitializeResponse`).

### Interrupt / cancel

Probe 2: a user message was sent, then `{"subtype":"interrupt"}` 1.5 s later. The CLI answered `{"still_queued":[]}` (the receipt, capability `interrupt_receipt_v1`), then emitted a synthetic `user` message `"[Request interrupted by user]"`, then `result` with `subtype:"error_during_execution"`, `is_error:true`, `terminal_reason:"aborted_streaming"`. The process exited with **code 1** after stdin closed, because the last result was an error. Docs: the receipt lists queued messages that will still run. `cancel_queued:true` cancels them (v2.1.219+, `interrupt_cancel_queued_v1`) (`agent-sdk/typescript` `SDKControlInterruptResponse`).

Signals (`headless` "Stop a run with SIGTERM"): SIGTERM gives exit 143, leaves the turn unfinished with no result, kills Bash process trees, and runs `SessionEnd` hooks. To end a turn cleanly, send SIGINT or the `interrupt` control request first. Closing stdin cancels a pending permission prompt.

### Hooks

Two paths:
1. Settings-file hooks (`hooks` in settings JSON, also loadable via `--settings <file-or-json>`). These run shell commands.
2. Host callbacks: the host registers matchers with `hookCallbackIds` in `initialize`, and the CLI sends a `hook_callback` control request when they fire. No settings file is touched. A repeated `initialize` over stdin replaces the hooks (`hooks_applied`, `agent-sdk/typescript`).

`--include-hook-events` streams the lifecycle of all hooks. `--bare` and `--safe-mode` skip settings and plugin hooks.

### Sandbox

The OS-enforced Bash sandbox (macOS, Linux, WSL2; Windows native runs unsandboxed) is off by default and turned on with `sandbox.enabled` in settings. It covers only shell commands. File tools, MCP servers and hooks run outside it. Reads default to "most of the machine, including credential files" unless `filesystem.denyRead` or `credentials` are set. Network goes through a local allowlist proxy (`sandboxing`). Öge can pass it per session via `--settings '{"sandbox":{…}}'`. The runtime is open source (`@anthropic-ai/sandbox-runtime`).

---

## 4. Sessions

- Every `-p` session has a UUID (`system/init.session_id`, `result.session_id`). `--session-id <uuid>` sets it up front. `--resume <id|path-to-.jsonl>` continues it, `--continue` continues the most recent in the cwd, and `--fork-session` (with resume or continue) branches to a new id. Since v2.1.223 `--resume <id>` finds the session from any directory (`headless` "Continue conversations"; `sessions`).
- A forked process starts without the parent's "allow for this session" grants (`sessions`).
- Resuming the same session in two processes without forking interleaves both into one transcript (`sessions`). Öge must not do this.
- Transcripts: `<config dir>/projects/<cwd with non-alphanumerics→'-'>/<session-id>.jsonl`. On this machine the config dir is `~/.claude-work` because `CLAUDE_CONFIG_DIR` is set (`claude auth status` reports `projectsDirectory`). **"The entry format is internal to Claude Code and changes between versions"**, so do not parse it (`sessions` "Where transcripts are stored"). Knobs: `--no-session-persistence`, `cleanupPeriodDays` (30-day default), `CLAUDE_CODE_PROJECT_DIR_NAME` (only together with `CLAUDE_CONFIG_DIR`).
- An interrupted turn stays interrupted on resume unless `CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1` is set (`headless`).

---

## 5. Relationship to the Agent SDK

- `headless`: "The Agent SDK gives you the same tools, agent loop, and context management that power Claude Code. It's available as a CLI … or as Python and TypeScript packages." The CLI `-p` mode is the SDK.
- The Python SDK resolves a **bundled** `claude` binary first (`subprocess_cli.py` `_find_bundled_cli`, L255–352), spawns it with the flags above, and sets `CLAUDE_CODE_ENTRYPOINT=sdk-py` and `CLAUDE_AGENT_SDK_VERSION` (L814–824). It strips `CLAUDECODE` from the inherited env so a child does not think it runs inside Claude Code. Agents go through `initialize`, not `--agents`.
- **There is no standalone protocol spec.** The TS reference documents the payloads (`SDKControlInitializeResponse`, `SDKControlInterruptResponse`, `SDKMessage` union) and explicitly addresses direct wire clients: "if you parse the wire protocol yourself, treat a missing field as an older CLI" and "A client that drives the CLI's control protocol directly, rather than through `interrupt()`, can set `cancel_queued: true`" (`agent-sdk/typescript`). The shapes are versioned by CLI release, and the `capabilities` array exists for direct clients to feature-detect.
- Licences: the Python SDK is MIT, so it can be read and ported with attribution. The TS SDK is proprietary (Commercial ToS) with no source in the repo. The `claude` binary is Anthropic's, and the legal page requires it to be run unmodified (§7).

---

## 6. Auth: login reuse and detection

- Precedence (`authentication` "Authentication precedence"): cloud provider env, then `ANTHROPIC_AUTH_TOKEN`, then `ANTHROPIC_API_KEY` (always used in `-p` when present), then `apiKeyHelper`, then `CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`, a one-year token), then Anthropic profile, then **subscription OAuth from `/login`** (the Pro/Max/Team/Enterprise default).
- A child `claude` spawned with the user's normal env and config dir uses the user's existing login. **Verified:** the probes ran on the subscription (`apiKeySource:"none"`, `rate_limit_event` five_hour) and Öge-side code never touched credentials.
- Storage: macOS Keychain (falling back to `~/.claude/.credentials.json` 0600), Linux `~/.claude/.credentials.json`. **"If you've set `CLAUDE_CONFIG_DIR` … keys the macOS Keychain entry to that directory too, so a session with a different `CLAUDE_CONFIG_DIR` reads a different entry"** (`authentication`). Consequence: Öge must launch with the user's own `CLAUDE_CONFIG_DIR` (or none). A private config dir per session would show as logged out.
- `--bare` never reads OAuth or the keychain and ignores `CLAUDE_CODE_OAUTH_TOKEN`. It needs `ANTHROPIC_API_KEY` or `apiKeyHelper` (`headless`, `--help`). The docs say it "will become the default for `-p` in a future release".
- **Detecting "authenticated" without reading credentials:**
  - `claude auth status` (JSON by default, `--text` available) returns `loggedIn`, `authMethod` (e.g. `"claude.ai"`), `apiProvider` (`firstParty`), `subscriptionType`, `configDirectory`, `projectsDirectory`, plus `email/orgId/orgName`. Exit 0 when logged in. Öge should keep only the non-PII fields.
  - At runtime: `system/init.apiKeySource`, the `initialize` response `account.subscriptionType/apiProvider`, `system/auth_status` messages, and `api_retry` with `error:"authentication_failed"` or `oauth_org_not_allowed`. An expired login fails each request with "Login expired · Please run /login".
  - Login itself has to happen in the user's terminal (`claude auth login` or `/login`). `/login` is unavailable in `-p`.

---

## 7. Terms of use for subscription auth by a third-party tool

Verbatim, `legal-and-compliance` (fetched 2026-10-04):

> **OAuth authentication** is intended exclusively for purchasers of Claude Free, Pro, Max, Team, and Enterprise subscription plans and is designed to support ordinary use of Claude Code and other native Anthropic applications.

> **Developers** building products or services that interact with Claude's capabilities, including those using the Agent SDK, should use API key authentication … Anthropic does not permit third-party developers to offer Claude.ai login into their own applications, or to route requests through Free, Pro, or Max plan credentials on behalf of their users. Moreover, developers may not collect, store, or intermediate Claude.ai credentials or session tokens — sign-in to a Claude account must complete through Anthropic's own flow.

> … Nor does it prevent an end user from signing in to the unmodified Claude Code binary with their own Claude subscription, including where a platform hosts Claude Code as described under *Can customers offer Claude Code in their products?* above.

> Anthropic reserves the right to take measures to enforce these restrictions and may do so without prior notice.

> Advertised usage limits for Pro and Max plans assume ordinary, individual usage of Claude Code and the Agent SDK.

Products that run Claude Code must not modify the binary, must not "remove, disable, or restrict any authentication method built into it", and must not "pay for, resell, or intermediate Claude usage" (`legal-and-compliance` "Can customers offer Claude Code in their products?"). Product names may not include "Claude Code" or "Anthropic" or suggest endorsement.

`agent-sdk/overview`:

> Unless previously approved, Anthropic does not allow third party developers to offer claude.ai login or rate limits for their products, including agents built on the Claude Agent SDK.

Consumer Terms (effective 2025-10-08), prohibited use:

> Except when you are accessing our Services via an Anthropic API Key or where we otherwise explicitly permit it, to access the Services through automated or non-human means, whether through a bot, script, or otherwise.

**How Öge's design lines up against this text**

| Fact about Öge | Bearing |
| - | - |
| Spawns the user's installed, unmodified `claude` binary | Matches the "end user … unmodified Claude Code binary with their own Claude subscription" carve-out. |
| User logs in through Anthropic's own flow; Öge never reads, stores or forwards tokens | Satisfies "may not collect, store, or intermediate … credentials". |
| Local, single user, the user's own quota, no resale | Not "on behalf of their users". Öge pays for and intermediates nothing. |
| Öge speaks the same wire protocol as the Agent SDK and automates multi-role runs | This is where the "offer … rate limits for their products, including agents built on the Agent SDK" and "ordinary, individual usage" language bites. Headless `-p` is explicitly documented and permitted, but an orchestrator running several roles in parallel is less obviously "ordinary". |

**Verdict:** the text permits it literally but the case is **ambiguous**, and enforcement can come without notice. This research cannot settle the question. It is a product and risk decision for the map. Defensive posture that does not depend on the outcome:

- Make API-key / cloud-provider auth an equal first-class path. Öge passes the user's own env through and never manages keys, so a user can choose `ANTHROPIC_API_KEY` without Öge changing.
- Never touch credential stores, never relocate `CLAUDE_CONFIG_DIR`, and never build a login UI.
- Do not imitate a native client. Leave `CLAUDE_CODE_ENTRYPOINT` alone or set it to something truthful; do not claim `cli`.
- Do not market "use your Claude subscription". Name Claude Code only as something Öge "runs".
- Optionally, ask Anthropic for written confirmation (the legal page points to sales).

---

## 8. Context and config isolation

**Probe comparison** (same cwd and prompt type, CLI 2.1.289):

| | Default `-p` | `--setting-sources=` + `--strict-mcp-config` + `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` |
| - | - | - |
| Plugins | 13 (user-installed and org) | 4 (apparently organisation-managed) |
| MCP servers | 9 (2 plugin servers + 7 claude.ai connectors, 6 `needs-auth`) | 0 |
| Slash commands / skills | 134 / 99 | 54 / 19 |
| SessionStart hook injecting context | yes | no |
| `memory_paths.auto` in init | present | absent |
| First request input tokens | 25,154 | 19,393 |
| Auth | subscription | subscription (unchanged) |

What controls what (`agent-sdk/claude-code-features`, `cli-reference`, `env-vars`):

- `--setting-sources user,project,local` (empty means none) gates settings.json and its hooks, CLAUDE.md and rules, skills, commands and subagents at each level. The SDK passes `--setting-sources=<list>`.
- **Not** gated by setting sources: managed policy (endpoint and server-managed, which cannot be disabled from the client), `~/.claude.json` (always read), auto memory (needs `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` or `autoMemoryEnabled:false`), claude.ai MCP connectors (need `--strict-mcp-config`, `disableClaudeAiConnectors`, or `ENABLE_CLAUDEAI_MCP_SERVERS=false`), and sandbox credential deny entries in user settings.
- The docs warn: "Do not rely on default `query()` options for multi-tenant isolation … set `settingSources: []` plus `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`."
- System prompt: `--system-prompt` (replace), `--append-system-prompt` (add), and `-file` variants. `--system-prompt-snapshot on` (the default) records the prompt on the first request and reuses it verbatim on resume, even if later launches pass different text. `--exclude-dynamic-system-prompt-sections` moves cwd, git status and similar into the first user message.
- Heavier switches:
  - `--bare`: also skips the keychain, so no subscription.
  - `--safe-mode`: disables all customizations (CLAUDE.md, skills, plugins, hooks, MCP, agents, memory…). Auth and permissions stay normal and managed policy still applies. Documented as a troubleshooting mode.
  - `--restricted`: removes code-running tools and WebFetch unless named in `--tools`, ignores user, project and local settings, confines file tools to working dirs, and refuses `bypassPermissions`. Documented for "an evaluation harness drives `claude` on a shared machine".
- Leftover per-machine surface in the probe's init even when isolated: `messaging_socket_path` (cross-session messaging) and tools such as `SendMessage`, `ListAgents`, `RemoteTrigger`, `PushNotification`, `EnterWorktree`, `Workflow`. Öge should cut these with `--tools` or `--disallowedTools` per role.

---

## 9. Implications for Öge

1. **Speak the protocol directly from Go or Rust.** Spawn `claude -p --input-format stream-json --output-format stream-json --verbose --permission-prompt-tool stdio --permission-mode <explicit> …`, send `initialize`, then stream `user` lines. Use the MIT Python SDK (`query.py`, `subprocess_cli.py`, `types.py`) as the porting reference and the TS reference docs for payload types. No Node or Python runtime and no SDK dependency is needed.
2. **Pin and feature-detect.** Record `claude_code_version` and `capabilities` from `system/init` in run evidence. Gate behaviour on capabilities, not version strings. Keep recorded NDJSON fixtures (like the probes here) for adapter tests without spending quota.
3. **Permissions are the role boundary.** Per role: an explicit `--permission-mode`, a `--tools` whitelist, `--allowedTools`/`--disallowedTools` rules, and every remaining prompt answered by Öge through `can_use_tool`. The planner can run in `plan` mode, where edits always reach Öge. The verifier and reviewer get no Edit/Write.
4. **Evidence and interception:** register PreToolUse/PostToolUse hooks through `initialize` (`hook_callback`), so no settings files are written into the repo. Treat `tool_use_result.structuredPatch` as a hint and use git diff as the evidence.
5. **Isolation recipe that keeps subscription auth:** `--setting-sources=` (or `project` only if the repo's CLAUDE.md is wanted), `--strict-mcp-config` with an explicit `--mcp-config` if any, `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`, `--append-system-prompt` for the role prompt, `--no-session-persistence` or Öge-owned `--session-id`, and `--tools` trimming. Accept and document the residue: managed policy and plugins, `~/.claude.json`. Never relocate `CLAUDE_CONFIG_DIR`.
6. **Lifecycle:** one long-lived process per role session. Multiple turns go over stdin. Cancel with the `interrupt` control request (plus `cancel_queued`), never SIGTERM first. Expect exit code 1 after an interrupted last turn. Resume with `--resume <id>`, and never run two processes on one session id.
7. **Auth boundary:** use `claude auth status` (filter PII) as the preflight. Pass the user's env through untouched. Treat the API-key path as first class. Surface `rate_limit_event` and `api_retry` to the user.
8. **Workspace:** `-p` skips the trust dialog and runs project hooks and `.mcp.json`. Öge's worktree isolation should assume repository-supplied config executes unless `--setting-sources` excludes `project`.

---

## 10. Open questions

- **ToS ambiguity (§7):** does Anthropic consider a local orchestrator driving the user's own `claude -p` on a subscription "ordinary, individual usage"? Only Anthropic can answer. This needs a map-level decision.
- **`--bare` becoming the `-p` default:** when, and will there be an opt-out that keeps OAuth? If it ships as stated, subscription use of `-p` breaks, and Öge's Claude adapter would have to fall back to API key or `CLAUDE_CODE_OAUTH_TOKEN` (which `--bare` also ignores). Re-check the release notes before implementation.
- Wire stability: the protocol is versioned only implicitly, through CLI releases and the `capabilities` list. How often do shapes change? Track the TS SDK CHANGELOG.
- Organisation-managed plugins survived `--setting-sources=` on the probe machine. Does any residue remain on a personal account with no organisation? Re-probe on one.
- `--permission-mode default` versus `manual`: the alias was accepted, but which spelling is canonical going forward?
- Not probed: subagent and Task event streams, plan-mode `ExitPlanMode` round-trip, `hook_callback`, `--resume` / `--fork-session` within stream-json, `--permission-prompts none`, sandbox settings via `--settings`. All are documented, but a prototype ticket should exercise them.
