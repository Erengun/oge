# Codex app-server: current protocol and auth

> Moved from branch `research/codex` at commit [`27bbcf3b3b57`](https://github.com/Erengun/oge/blob/27bbcf3b3b5719cbf69f34285f0ceba7897efe96/research/codex.md). Content unchanged.

Research for [#4](https://github.com/Erengun/oge/issues/4). Researched 2026-10-04.

## Sources and versions

| Tag | Source | Version / date |
|---|---|---|
| **[BIN]** | Installed `codex` (`/opt/homebrew/bin/codex`): `--help`, `app-server --help`, `generate-json-schema`, `generate-ts`, `login status`, `doctor --json`, two probe sessions | `codex-cli 0.155.1` (upstream tag `rust-v0.155.1` = `4e21628f9e`) |
| **[SCHEMA]** | `codex app-server generate-json-schema --out …` (and `--experimental`) from that binary | 0.155.1 |
| **[UP]** | `openai/codex` on GitHub, `main` | `afb436df8b` (2026-10-04T07:12Z). Latest release `rust-v0.160.0` (2026-10-01) |
| **[DOCS]** | Official docs, "Codex App Server": https://learn.chatgpt.com/docs/app-server (308-redirected from `developers.openai.com/codex/app-server`) | fetched 2026-10-04, page has no version stamp |
| **[AUTHDOC]** | https://learn.chatgpt.com/docs/auth (from `developers.openai.com/codex/auth`) | fetched 2026-10-04 |
| **[EXECDOC]** | https://learn.chatgpt.com/docs/non-interactive-mode | fetched 2026-10-04 |
| **[SDKDOC]** | https://learn.chatgpt.com/docs/codex-sdk | fetched 2026-10-04 |
| **[PROBE]** | Two throwaway sessions against `codex app-server` (stdio) in a scratch git repo, 2026-10-04 | 0.155.1 |

The installed binary is 5 minor versions behind the latest release. The docs track `main`, so some shapes in them are ahead of (or differ from) 0.155.1. Where they differ, this note trusts the binary's own schema and the probe.

**What the probe did and did not cover.** The probe's model turn failed on an auth error (see [Auth](#auth)). So these were **seen on the wire**: handshake, the `Not initialized` error, `account/read`, `thread/start`, `thread/started`, `thread/status/changed`, `turn/start`, `turn/started`, the `userMessage` item, the `error` notification, `turn/completed` (failed), `thread/fork` and `turn/steer` error responses, `thread/delete`, and a clean exit on stdin EOF. **Everything about commandExecution / fileChange items, approval round-trips, `turn/diff/updated`, `turn/plan/updated` and review comes from the schema and docs, not from the wire.**

---

## 1. Transport, framing, stability

- **JSON-RPC 2.0 with the `"jsonrpc":"2.0"` field left out on the wire** [DOCS "Protocol"]. Requests are `{id, method, params}`. Responses are `{id, result}` or `{id, error:{code,message}}`. Notifications are `{method, params}` [DOCS "Message schema"]. Seen in [PROBE].
- **Transports** (`--listen`): `stdio://` (the default; newline-delimited JSON), `ws://IP:PORT` ("experimental and unsupported"; one message per text frame), `unix://[PATH]` (WebSocket over a Unix control socket), and `off` [BIN `app-server --help`, DOCS]. WebSocket auth uses `--ws-auth capability-token|signed-bearer-token`, and the docs warn that "Non-loopback WebSocket listeners currently allow unauthenticated connections by default during rollout" [DOCS]. When the WebSocket queues are full the server answers `-32001 "Server overloaded; retry later."` [DOCS].
- There is also a **shared local daemon** (`codex app-server daemon start|stop|version|…`, `codex app-server proxy`, `codex agents`) that the TUI and remote-control use [BIN]. Öge does not need it: it can spawn its own stdio child.
- **Stability.**
  - `codex --help` lists `app-server` as **`[experimental]`**, and `generate-ts` / `generate-json-schema` are each marked `[experimental]` too [BIN].
  - The docs say: *"The app-server command and WebSocket transport are experimental and aren't supported for production workloads."* [DOCS, "Connect a remote Code Mode host"].
  - Even so, it is the **only structured surface left**. `codex mcp-server` "and standalone codex-mcp-server binary have been removed. Use the Codex app server for existing integrations." [SDKDOC]. On 0.155.1, `codex mcp-server --help` falls through to the top-level help [BIN]. The Python SDK "controls the local Codex app-server over JSON-RPC" [SDKDOC]. VS Code uses it [DOCS intro].
- **The `experimentalApi` gate.** A client sends `initialize.params.capabilities.experimentalApi: true` to unlock experimental methods and fields. Without it, the server rejects them with `"<descriptor> requires experimentalApi capability"` [DOCS]. In the 0.155.1 schema, these methods are **experimental-only** (from diffing `generate-json-schema` with and without `--experimental`) [SCHEMA]: `process/*`, `thread/queue/*`, `thread/settings/update`, `turn/settings/update`, `thread/turns/list`, `thread/search`, `thread/timeline/list`, `thread/backgroundTerminals/*`, `thread/memoryMode/set`, `thread/realtime/*` (client side), `project/*`, `environment/*`, `collaborationMode/list`, `memory/*`, `remoteControl/*`, `userVerification/*`, `plugin/search`, `mcpServer/event/stream/*`, `account/bedrock/*`, `server/diagnostics`, plus the `currentTime/read` server request. Upstream marks these in code with `#[experimental("…")]` attributes [UP `codex-rs/app-server-protocol/src/protocol/common.rs`]. Some fields are experimental on otherwise stable methods (for example `dynamicTools` and `permissions` on `thread/start`, and `additionalPermissions` on approvals) [DOCS].
- **Versioning.** There is no protocol version number in the handshake. The method namespace is called "v2". The generated schema has a `v1/` folder that holds only `InitializeParams/Response`, and a `v2/` folder with 273 types [SCHEMA]. The old v1 server requests `applyPatchApproval` / `execCommandApproval` are still in the union, marked `/// DEPRECATED APIs below … used for Turns started via the legacy APIs` [UP common.rs L1837-1848]. Compatibility is tied to the binary: "Each output is specific to the Codex version you ran, so the generated artifacts match that version exactly" [DOCS].

## 2. Code generation (for Go or Rust)

- `codex app-server generate-json-schema --out DIR [--experimental]` writes draft-07 JSON Schema. You get one bundle per side (`codex_app_server_protocol.schemas.json` and `codex_app_server_protocol.v2.schemas.json`), plus one file per type, plus the method unions `ClientRequest.json`, `ServerRequest.json`, `ServerNotification.json` and `ClientNotification.json` (each a `oneOf` keyed on `method`) [BIN, SCHEMA].
- `codex app-server generate-ts --out DIR [--experimental] [--prettier BIN]` writes `ts-rs` TypeScript, 627 files under `v2/` [BIN].
- Upstream commits the same output under `codex-rs/app-server-protocol/schema/{json,typescript,precomputed}` [UP], so you can diff it per tag without installing a binary.
- **Rust** can depend directly on the `codex-app-server-protocol` crate (serde types) by git tag. The archived Zed `codex-acp` pinned `openai/codex` crates by tag (`rust-v0.137.0`), but it linked `codex-core` and did not use app-server [github.com/zed-industries/codex-acp Cargo.toml]. **Go** has to run codegen from the JSON Schema.
- **Caveat: the schema can lag the binary.** The real `thread/start` response contained `thread.environments[]` and `thread.extra`, and neither is in the generated `Thread` schema [PROBE vs SCHEMA]. Notifications also carry a top-level `emittedAtMs` next to `params` [PROBE]. Parsers must therefore tolerate unknown fields and unknown notification methods. For example, an unsolicited `remoteControl/status/changed` arrives right after `initialize` [PROBE].

## 3. Lifecycle and method shapes

Primitives are **Thread** (a conversation) → **Turn** (one user request plus the agent work after it) → **Item** (a unit of input or output) [DOCS "Core primitives"].

### Initialization
- `initialize {clientInfo:{name, title?, version}, capabilities?:{experimentalApi?, optOutNotificationMethods?, requestAttestation?, mcpServerOpenaiFormElicitation?, extensions?}}` returns `{userAgent, codexHome, platformFamily, platformOs}`. The client then sends the notification `initialized` [SCHEMA, PROBE].
- Any request sent before this gets `{code:-32600, message:"Not initialized"}` [PROBE]. A second `initialize` gets "Already initialized" [DOCS].
- `clientInfo.name` feeds OpenAI's Compliance Logs. OpenAI asks enterprise integrations to get onto a known-clients list [DOCS]. The name also shows up in the upstream user-agent: `oge_probe/0.155.1 (Mac OS …) … (oge_probe; 0.0.0)` [PROBE].
- `optOutNotificationMethods` turns off individual notification methods (exact match only), for example `item/agentMessage/delta` [DOCS].

### Threads
- `thread/start {cwd?, model?, modelProvider?, approvalPolicy?, approvalsReviewer?, sandbox?, config?, baseInstructions?, developerInstructions?, ephemeral?, personality?, serviceName?, …}` returns `{thread, model, modelProvider, cwd, approvalPolicy, approvalsReviewer, sandbox, reasoningEffort?, instructionSources?}`, followed by the `thread/started` notification. Starting a thread auto-subscribes the connection to that thread's events [SCHEMA, DOCS, PROBE].
- `thread/resume {threadId, …same overrides…, excludeTurns?}` has the same response shape [SCHEMA, DOCS].
- `thread/fork {threadId, lastTurnId?, ephemeral?, excludeTurns?, …overrides}` copies stored history into a new thread id, emits `thread/started`, and returns `forkedFromId` [DOCS]. Probe: `{ephemeral:true}` on a fresh thread was rejected with `-32600 "ephemeral paginated thread/fork requires excludeTurns: true"` [PROBE].
- Thread `status` is one of `notLoaded | idle | systemError | active{activeFlags:[waitingOnApproval|waitingOnUserInput]}`, and changes arrive as `thread/status/changed` [SCHEMA, PROBE].
- Also available: `thread/read`, `thread/list` (cursor; filters include `cwd`), `thread/loaded/list`, `thread/unsubscribe`, `thread/archive|unarchive|delete`, `thread/compact/start`, `thread/inject_items` (append raw Responses-API items without a turn), `thread/name/set`, `thread/goal/*`, and `thread/rollback` (deprecated) [SCHEMA, DOCS]. Probe: `thread/delete` returned `{}` plus `thread/deleted` [PROBE].
- Thread and turn ids are **UUIDv7 strings** (e.g. `01a106d0-cdbe-7532-…`), not `thr_123` as the docs examples show. `thread.sessionId` is the root of the fork tree [PROBE, DOCS].
- Drift: the probe thread came back with `historyMode:"paginated"`, but the docs say paginated thread creation "isn't supported yet" [PROBE vs DOCS].

### Turns
- `turn/start {threadId, input:[UserInput], cwd?, approvalPolicy?, approvalsReviewer?, sandboxPolicy?, model?, effort?, summary?, personality?, outputSchema?, toolOutput?, clientUserMessageId?, …}` returns `{turn:{id, status:"inProgress", items:[], error:null}}`. Overrides stick for later turns on the same thread. `outputSchema` applies to the current turn only [SCHEMA, DOCS, PROBE].
- `UserInput` is `text | image(url) | localImage(path) | skill(name, path) | …` [DOCS].
- **Steering:** `turn/steer {threadId, expectedTurnId, input}` returns `{turnId}`. It adds input to the in-flight turn, emits no new `turn/started`, and does not accept overrides. If nothing is running it fails with `-32600 "no active turn to steer"` [SCHEMA, DOCS, PROBE]. `CodexErrorInfo` also has `activeTurnNotSteerable{turnKind}` [SCHEMA].
- **Interrupt:** `turn/interrupt {threadId, turnId}` returns `{}`, and the turn then ends with `status:"interrupted"` [DOCS].
- Turn notifications:
  - `turn/started {threadId, turn}`
  - `turn/completed {threadId, turn}`, where `turn.status` is `completed | interrupted | failed` and failures carry `turn.error {message, codexErrorInfo?, additionalDetails?}` [SCHEMA, PROBE]
  - `turn/diff/updated {threadId, turnId, diff}`, "the latest aggregated unified diff across every file change in the turn"
  - `turn/plan/updated {threadId, turnId, explanation?, plan:[{step, status: pending|inProgress|completed}]}`
  - `thread/tokenUsage/updated`
  - `model/rerouted`
  - `hook/started|completed` [DOCS, SCHEMA]

### Items (the `ThreadItem` tagged union, discriminated on `type`) [SCHEMA, DOCS]
- `userMessage{content}` (seen in [PROBE])
- `agentMessage{text, phase?: commentary|final_answer}`
- `plan{text}` (plan mode; "Treat the final plan item from item/completed as authoritative")
- `reasoning{summary, content}`
- `commandExecution{command, cwd, status, commandActions, aggregatedOutput?, exitCode?, durationMs?, processId?}`
- **`fileChange{changes:[{path, kind: add|delete|update{move_path?}, diff}], status}`**: patches are reported **as structured data**, with a unified diff for each file
- `mcpToolCall`, `dynamicToolCall`, `collabAgentToolCall`, `subAgentActivity`, `webSearch`, `imageView`, `imageGeneration`, `functionCallOutput`, `hookPrompt`, `sleep`
- `enteredReviewMode{review}` / `exitedReviewMode{review}`
- `contextCompaction`

Lifecycle: `item/started` carries the full item, and `item/completed` carries the final item ("authoritative") [DOCS]. Deltas: `item/agentMessage/delta`, `item/plan/delta`, `item/reasoning/summaryTextDelta|summaryPartAdded|textDelta`, `item/commandExecution/outputDelta`, and `item/fileChange/patchUpdated`. The legacy `item/fileChange/outputDelta` is no longer emitted [DOCS, SCHEMA].

### Approvals (server → client JSON-RPC requests) [SCHEMA, DOCS — not observed live]
- `item/commandExecution/requestApproval {threadId, turnId, itemId, startedAtMs, reason?, command?, cwd?, commandActions?, proposedExecpolicyAmendment?, networkApprovalContext?, …}` → `{decision: accept | acceptForSession | acceptWithExecpolicyAmendment{…} | applyNetworkPolicyAmendment{…} | decline | cancel}`.
- `item/fileChange/requestApproval {threadId, turnId, itemId, startedAtMs, reason?, grantRoot?}` → `{decision: accept | acceptForSession | decline | cancel}`.
- `item/permissions/requestApproval`: the agent's `request_permissions` tool asks for extra network or filesystem access. The client grants a subset, scoped to `turn` or `session`.
- Also `item/tool/requestUserInput` (with `autoResolutionMs`), `mcpServer/elicitation/request`, and `item/tool/call` (client-executed dynamic tools, experimental).
- Order of messages: `item/started` (pending item) → request → client response → `serverRequest/resolved` → `item/completed` with `status: completed|failed|declined`. The server also sends `serverRequest/resolved` when it clears a pending request itself (turn start/complete/interrupt) [DOCS].
- **Policy knobs:**
  - `approvalPolicy` (`AskForApproval`) is `"untrusted" | "on-request" | "never" | {granular:{sandbox_approval, rules, skill_approval?, request_permissions?, mcp_elicitations}}`.
  - **The docs prose and examples use `"unlessTrusted"` / `"onRequest"`, and 0.155.1 rejects those:** `Invalid request: unknown variant 'unlessTrusted', expected one of 'untrusted', 'on-request', 'granular', 'never'` [PROBE]. Upstream `main`'s `AskForApproval.ts` agrees with the binary [UP].
  - `approvalsReviewer: user | auto_review | guardian_subagent` routes approvals to an automatic reviewer instead of the client [SCHEMA]. This is what `codex --approve-for-me` sets [BIN].
  - Admin `requirements.toml` can restrict the allowed policies and sandbox modes, and `configRequirements/read` reports the restrictions [DOCS].

### Sandbox and working directory
- **Two spellings for one concept.** `thread/start|resume|fork.sandbox` is the `SandboxMode` string `read-only | workspace-write | danger-full-access` (kebab-case, even though the docs example says `"workspaceWrite"`). `turn/start.sandboxPolicy` and `command/exec.sandboxPolicy` take the tagged object `{type: readOnly{networkAccess?, access?} | workspaceWrite{writableRoots?, networkAccess?, readOnlyAccess?, excludeTmpdirEnvVar?, excludeSlashTmp?} | dangerFullAccess | externalSandbox{networkAccess: restricted|enabled}}` [SCHEMA, DOCS, PROBE]. Probe result for `sandbox:"workspace-write"`: `{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,…}` [PROBE].
- `externalSandbox` means "you already sandbox the server process and want Codex to skip its own sandbox enforcement" [DOCS].
- `cwd` can be set on the thread and overridden per turn. `writableRoots` and `readableRoots` add further directories [DOCS].
- Experimental: named permission profiles (`permissions`, `permissionProfile/list`) replace `sandbox` [DOCS].
- Escape hatches that **bypass the sandbox**: `thread/shellCommand` ("runs outside the sandbox with full access") and `process/*` (experimental). `command/exec` runs a single argv under a sandbox policy without creating a thread [DOCS].

### Review mode
- `review/start {threadId, target, delivery?: inline|detached}`, where `target` is `uncommittedChanges | baseBranch{branch} | commit{sha, title?} | custom{instructions}`. It returns `{turn, reviewThreadId}`. With `detached`, the review runs in a new forked thread, which gets its own `thread/started`. The stream contains an `enteredReviewMode` item, then an `exitedReviewMode{review: <final text>}` item [SCHEMA, DOCS]. CLI equivalents: `codex review`, `codex exec review` [BIN].

### Errors and shutdown
- JSON-RPC errors seen: `-32600` (invalid request / not initialized / precondition), `-32601` (unsupported, e.g. paginated `historyMode`), `-32603` (internal; upstream 401s show up here), `-32001` (overloaded) [PROBE, DOCS].
- Turn failures: an `error {threadId, turnId, willRetry, error:{message, codexErrorInfo, additionalDetails}}` notification, then `turn/completed{status:"failed"}` [SCHEMA, PROBE]. `codexErrorInfo` is one of `contextWindowExceeded | usageLimitExceeded | rateLimitExceeded | sessionBudgetExceeded | serverOverloaded | unauthorized | badRequest | sandboxError | internalServerError | cyberPolicy | misalignmentPolicyViolation | threadRollbackFailed | other`, or `{httpConnectionFailed | responseStreamConnectionFailed | responseStreamDisconnected | responseTooManyFailedAttempts: {httpStatusCode?}}`, or `{activeTurnNotSteerable}` [SCHEMA]. Non-fatal problems arrive as `warning` / `configWarning` / `deprecationNotice`.
- Shutdown: when stdin closed, the stdio server exited with code 0 inside 15 s [PROBE]. No `shutdown` method exists [SCHEMA]. `thread/unsubscribe` unloads a thread after a grace period (`thread/closed`) [DOCS].

## 4. Auth

- **Modes** [DOCS "Auth endpoints"]:
  - `chatgpt`: "Codex owns the ChatGPT OAuth flow, persists tokens, and refreshes them automatically".
  - `apikey`.
  - `chatgptAuthTokens` (experimental): the host app supplies the tokens and must answer `account/chatgptAuthTokens/refresh` server requests.
  - Bedrock, `agentIdentity`, and `personalAccessToken`.
- **Reuse with no API key.** App-server reads the same cached login as the CLI, IDE extension and desktop app ("The CLI and extension share the same cached login details") [AUTHDOC]. The client sends nothing auth-related. `initialize` reports `codexHome` (`~/.codex` here), and the server picks up stored credentials from there [PROBE]. `CODEX_HOME` moves the whole store [AUTHDOC].
- **Where credentials live** [AUTHDOC]: "in a plaintext file at `~/.codex/auth.json` or in your OS-specific credential store". The location is set by `cli_auth_credentials_store = file | keyring | auto | ephemeral`. Admins can enforce the store. (This research did not open either location.)
- **Login from a client.** `account/login/start {type:"chatgpt"}` returns `{loginId, authUrl}` and app-server hosts the localhost callback. `{type:"chatgptDeviceCode"}` returns `{verificationUrl, userCode}`. Completion arrives as `account/login/completed` and `account/updated {authMode, planType}`. Also available: `account/logout` and `account/login/cancel` [DOCS]. Öge should not drive login. It should tell the user to run `codex login`.
- **Policy statement** [DOCS, verbatim]: *"If you've built a local or open-source application using Codex app-server authentication, you can continue using it, though we recommend migrating to Sign in with ChatGPT … App-server authentication has never been permitted for commercial or hosted services."* Öge (local, open-source, the user's own subscription, Öge never holds the credential) fits the permitted case. A hosted or commercial Öge would not.
- **Detecting "logged in" without touching tokens. Key finding: presence is not validity.**
  - During this research the user's stored ChatGPT login was **unusable**. Every upstream call failed with `refresh_token_reused` / `token_expired` 401s, and the probe turn failed with `codexErrorInfo:"unauthorized"` [PROBE]. At the same moment:
    - `codex login status` printed `Logged in using ChatGPT` and exited 0 [BIN];
    - `codex doctor --json` reported `auth.credentials: ok — "auth is configured"` [BIN];
    - app-server `account/read {refreshToken:false}` returned `{account:{type:"chatgpt", email, planType:"plus"}, requiresOpenaiAuth:true}` [PROBE].
  - Only these calls exposed the problem [PROBE]:
    - `account/read {refreshToken:true}` returned `{account:null, requiresOpenaiAuth:true}`. It does not log the user out: `login status` still said logged in afterwards.
    - `account/rateLimits/read` returned JSON-RPC `-32603` with the upstream 401 text.
  - Neither call uses model quota.
  - Known upstream cause class: concurrent Codex clients racing on token refresh. See openai/codex#10332 (closed; an OpenAI maintainer says the server allows reuse of a refresh token within a window of "on the order of an hour"), plus the open #19803 / #38679 / #39803. The user needs to run `codex login` again.

## 5. Alternatives

| Surface | What it is | Fit for Öge |
|---|---|---|
| `codex app-server` (stdio JSON-RPC) | Full bidirectional protocol: threads, turns, steer, interrupt, approvals as requests, structured diffs, review, plan, auth status | **Best fit.** Labelled experimental, but every first-party integration and the maintained ACP adapter build on it |
| `codex exec --json` | One-shot JSONL on stdout: `thread.started`, `turn.started`, `turn.completed{usage}`, `turn.failed`, `item.*` with **snake_case** item types (`command_execution`, `agent_message`), `error`. Also `--output-schema FILE`, `-o/--output-last-message`, `exec resume <id>/--last`, `exec review`, `--ephemeral`, `--sandbox`. Read-only sandbox by default; refuses to run outside a git repo unless `--skip-git-repo-check` [EXECDOC, BIN] | Good for fire-and-forget roles. No approvals channel (policy must be set up front), and no steer or interrupt beyond killing the process. Its event vocabulary differs from app-server's |
| TS SDK `@openai/codex-sdk` | "wraps the `codex` CLI … spawns the CLI and exchanges JSONL events over stdin/stdout" [UP `sdk/typescript/README.md`] | Node only. Shows the exec-JSONL path is supported for automation |
| Python SDK `openai-codex` | "controls the local Codex app-server over JSON-RPC"; stable; pins a Codex runtime [SDKDOC] | A reference client for app-server, useful for reading |
| `codex mcp-server` | **Removed** [SDKDOC, BIN] | Not an option |
| ACP: `agentclientprotocol/codex-acp` | TypeScript stdio ACP agent that "starts the Codex App Server, translates ACP requests into Codex operations". v2.1.1, 2026-10-01; npm `@agentclientprotocol/codex-acp` bundles `@openai/codex` (override with `CODEX_PATH`). Supports ChatGPT auth, approvals, plan, review, diffs [github.com/agentclientprotocol/codex-acp README] | Adds a Node dependency and a translation layer on top of app-server. Its predecessor `zed-industries/codex-acp` (Rust, linked `codex-core`) is **archived** (2026-07) |

## Implications for Öge

- **The Codex adapter should talk app-server over stdio directly**, with one `codex app-server` child per role or session that Öge spawns, owns and kills. Pin a minimum Codex version, and generate types from `generate-json-schema` (Go) or the protocol crate / schema (Rust) for that version. Tolerate unknown fields and methods. Keep a recorded-transcript test corpus, because the schema does not fully describe the wire.
- **Stay on the stable surface** (no `experimentalApi`) unless a specific feature needs it. Everything Öge's MVP needs is stable: threads, fork, turns, steer, interrupt, approvals, review, plan, diff, `account/read`.
- **The role mapping is native:**
  - The implementer runs in a `workspace-write` thread rooted at its worktree `cwd`.
  - The verifier/reviewer gets a `read-only` sandbox, an `ephemeral` thread, or `thread/fork`, plus `review/start {delivery:"detached"}`.
  - Planners can use `outputSchema` for structured plans, and `turn/plan/updated` gives the todo list.
  - `fileChange.changes[].diff` and `turn/diff/updated` give structured diffs. These are still **claims**. Öge's evidence should come from `git diff` in the worktree.
- **Approvals.** Öge can be the approval authority, since requests arrive as JSON-RPC requests it must answer. It can also set `approvalPolicy:"never"` with a tight sandbox for headless roles. Do not forward `approvalsReviewer:auto_review` by default, because that has Codex grading itself.
- **Auth boundary:**
  - Öge never touches `auth.json` or the keyring.
  - Preflight with `codex login status` (presence), then app-server `account/read{refreshToken:true}` or `account/rateLimits/read` (validity). Do not trust `login status` or `doctor` alone.
  - Map `codexErrorInfo:"unauthorized"` to "run `codex login`".
  - Running several app-server processes at once shares one refresh token and has a known race. Prefer one app-server process with many threads, or serialise process start-up, until this is validated.
- **Workspace isolation.** `cwd` and `writableRoots` are per thread or turn, and the Seatbelt (macOS) and Landlock-class sandboxes are Codex's own. `thread/shellCommand` and `process/*` bypass the sandbox, so Öge must never call them.
- **The ACP question** (#13 / ACP research). For Codex, ACP is a TS layer over app-server, so using ACP means an extra hop and a Node runtime. Its value depends on whether the Claude Code side and post-MVP agents make ACP the common denominator.

## Open questions

1. A live **approval round-trip, fileChange items, `turn/diff/updated`, steer, interrupt and review** were not observed, because the stored login was dead. Re-run a tiny probe after `codex login` to capture a golden transcript (it also makes a good test fixture).
2. Does one app-server process safely run **several concurrent threads with different `cwd`s and sandboxes**? That would avoid the multi-process refresh race. Not tested.
3. How far apart are the docs (`unlessTrusted`, `workspaceWrite`, `thr_` ids, "paginated unsupported") and 0.155.1 / 0.160? The docs follow `main`. Pick and test a minimum supported Codex version.
4. Does `thread/resume` work across app-server restarts for threads in `historyMode:"paginated"`? The docs say resume "fail[s] closed" for paginated records.
5. How should Öge set `clientInfo.name`? It is used for OpenAI compliance logging, and "known clients" registration is offered for enterprise use.
6. Does the "experimental / not supported for production" label carry any deprecation or compat promise? None was found. Track releases.
