# OpenCode and Gemini CLI: ACP and headless surfaces

> Moved from branch `research/opencode-gemini` at commit [`ea6f2396f236`](https://github.com/Erengun/oge/blob/ea6f2396f23663df69cdc9ea39ce7d5956781893/research/opencode-gemini.md). Content unchanged.

Research for [Erengun/oge#6](https://github.com/Erengun/oge/issues/6). Date: 2026-10-04.

**Question.** What structured integration surfaces do **OpenCode** and **Gemini CLI** offer today, and would either cost Öge almost nothing to add once it has an ACP client?

**Short answer.**

- **Gemini CLI** has a native, fairly complete ACP agent (`gemini --acp`). Öge can't use it, though, and the reason is auth, not the protocol. Google stopped serving "Login with Google" in Gemini CLI for the free individual tier, Google AI Pro and Google AI Ultra on **2026-06-18**. Only Gemini Code Assist Standard/Enterprise licences still work. The local probe confirms this: `initialize` succeeds, then `session/new` fails with `IneligibleTierError` / `UNSUPPORTED_CLIENT`. The remaining options are an API key or Vertex AI, and Öge rules out API keys. So for Öge's main user (a solo developer on their own subscriptions), Gemini CLI is **not** near-free.
- **OpenCode** has the most complete ACP session lifecycle seen so far: new/load/list/resume/fork/close, cancel, permission requests with diffs, and usage updates. As far as the protocol goes, it is close to free. Two catches. First, it uses **its own** provider logins (`opencode auth login`, which offers ChatGPT Plus/Pro, Copilot, GitLab Duo and others), not the user's `codex`/`claude` logins. Second, it can't use Claude Pro/Max at all. And it is not installed locally.

## Sources and versions

| Thing | Version / ref | Date |
|---|---|---|
| Local `gemini` binary (`/opt/homebrew/bin/gemini`) | `0.46.0` | probed 2026-10-04 |
| google-gemini/gemini-cli `main` | `fb972b2f87fe7d5b06d37eac711490162d98de2c` | committed 2026-10-02 |
| gemini-cli latest release | `v0.62.0` | 2026-09-29 |
| Google deprecation notice ([code-assist-individuals](https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals)) | page "last updated 2026-09-02" | fetched 2026-10-04 |
| OpenCode repo (moved from `sst/opencode` to **`anomalyco/opencode`**; default branch `dev`; MIT) | `907b3bc518fa48e90e8ec24dd327d13eee71c36c` | committed 2026-10-03 |
| OpenCode latest release | `v1.18.34` | 2026-09-30 |

In the citations below, `gemini-cli:<path>` means the path at `fb972b2f` and `opencode:<path>` means the path at `907b3bc5`.

Note: the local `gemini` (0.46.0) is 16 minor versions behind upstream. The auth failure happens on the server, so the version doesn't change the conclusion (see the deprecation notice).

---

## Gemini CLI

### Surfaces

- **ACP, native.** `gemini --acp` starts ACP mode. `--experimental-acp` still works but is marked "deprecated, use --acp instead" (local `gemini --help`, 0.46.0). Transport is JSON-RPC 2.0 over stdio, ndjson (`gemini-cli:docs/cli/acp-mode.md`, `gemini-cli:packages/cli/src/acp/README.md`). The docs disagree with each other: `gemini-cli:docs/cli/cli-reference.md` still lists only `--experimental-acp` ("Agent Code Pilot ... Experimental").
- Uses `@agentclientprotocol/sdk` **0.16.1** (`gemini-cli:packages/cli/package.json`). Gemini CLI is listed in the ACP Agent Registry (`acp-mode.md`).
- **Headless.** `-p/--prompt` or a non-TTY stdin, with `-o/--output-format text|json|stream-json` (`--help`; `gemini-cli:docs/cli/headless.md`).
  - `json` output is one object: `{response, stats, error?}`.
  - `stream-json` is JSONL with event types `init` (session id, model), `message`, `tool_use`, `tool_result`, `error` and `result`.
  - Exit codes: 0 ok, 1 general/API error, 42 input error, 53 turn limit. The probe also saw **55** (untrusted folder, below).
  - **There is no permission event in headless mode.** Under the policy engine, `ask_user` "In non-interactive mode, this is treated as `deny`" (`gemini-cli:docs/reference/policy-engine.md` §decisions). So headless only supports pre-declared policy (`--approval-mode`, `--policy`, `-y`). **ACP is Gemini's only surface where a client can approve or deny individual tool calls.**
- **Folder trust gate.** Headless in a new directory exits 55 with "not running in a trusted directory ... use `--skip-trust`, set `GEMINI_CLI_TRUST_WORKSPACE=true`, or trust this directory" (probe). ACP mode in an untrusted folder logs "Skipping project agents ... Project hooks disabled because the folder is not trusted" but continues (probe stderr). Fresh per-role worktrees will hit this.

### ACP capabilities (source plus probe)

The probe's `initialize` returned `protocolVersion: 1` with these fields:

- `agentInfo {name: "gemini-cli", version: "0.46.0"}`
- `agentCapabilities {loadSession: true, promptCapabilities {image, audio, embeddedContext}, mcpCapabilities {http, sse}}`
- `authMethods`: `oauth-personal` (Log in with Google), `gemini-api-key`, `vertex-ai`, `gateway`

The source matches (`gemini-cli:packages/cli/src/acp/acpRpcDispatcher.ts` `initialize`).

- **Methods:** `initialize`, `authenticate`, `session/new`, `session/load`, `session/prompt`, `session/cancel`, `session/set_mode`, `unstable_setSessionModel` (`acpRpcDispatcher.ts`). It does **not** advertise list/resume/fork/close session capabilities.
- **Modes:** `default`, `auto_edit`, `yolo`, `plan`, returned from new/load with `currentModeId` (`acpUtils.ts` `buildAvailableModes`; `acpSessionManager.ts`).
- **Streaming updates:** `agent_message_chunk`, `agent_thought_chunk`, `user_message_chunk` (history replay), `tool_call` and `tool_call_update` (edit tools carry `{type: 'diff'}` content), `available_commands_update`, `usage_update` (`gemini-cli:packages/cli/src/acp/acpSession.ts`).
- **Permissions:** `session/request_permission` is sent for tools whose `shouldConfirmExecute` asks. If the outcome is cancelled or rejected, the tool fails with "canceled by the user" (`acpSession.ts` ~L700–805).
- **Cancellation:** `session/cancel` aborts the pending prompt, and the response carries `stopReason: 'cancelled'`. Other stop reasons are `end_turn`, `max_turn_requests` and `max_tokens` (`acpSession.ts`).
- **File-change reporting:** `diff` content on tool calls. If the client advertises `fs.readTextFile/writeTextFile`, file I/O is proxied through the client via `AcpFileSystemService`, with a local fallback (`acpFileSystemService.ts`). Öge could use this proxy to see or veto writes, but Öge will diff the worktree anyway (#23).
- **Sessions/resume:** `session/load` resolves the session from the project store, calls `resumeChat`, and **streams the history back** as update notifications (`acpSessionManager.ts` `loadSession`). On disk, sessions live in `~/.gemini/tmp/<project_hash>/chats/` and are scoped per project. CLI flags: `--resume latest|<idx>|<uuid>`, `--session-id <uuid>` (start a session with a chosen id), `--session-file`, `--list-sessions` (`docs/cli/session-management.md`; `--help`).

### Auth

- **The individual Google login is gone.** Per Google's notice, Gemini Code Assist IDE extensions stopped serving the *Gemini Code Assist for individuals*, *Google AI Pro* and *Google AI Ultra* tiers, and so did Gemini CLI's "Login with Google", effective **2026-06-18**. Users with *Gemini Code Assist Standard or Enterprise* keep access. Consumer users are told to "migrate to the Antigravity family of products" ([deprecation page](https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals), updated 2026-09-02). Upstream's own `docs/get-started/authentication.mdx` and README at `fb972b2f` still recommend Sign in with Google for individuals, so the docs lag the policy.
- **Probe result.** On this machine (cached `oauth-personal`, free tier), `session/new` returned JSON-RPC error `-32000` "This client is no longer supported for Gemini Code Assist for individuals ... migrate to the Antigravity suite". Stderr showed `IneligibleTierError`, `reasonCode: 'UNSUPPORTED_CLIENT'`, `tierId: 'free-tier'`. Headless `--skip-trust -p ... -o stream-json` exited 1, with **nothing on stdout** and the error only on stderr. **Ineligibility shows up only at `session/new` or the first headless call, not at `initialize`.**
- **ACP `authenticate` has side effects.** If the requested method differs from the stored one, it **clears the cached credential file** and persists `security.auth.selectedType` to **User** settings (`acpRpcDispatcher.ts` `authenticate`). An orchestrator that calls `authenticate` speculatively can wipe the user's login.
- **New sessions use the stored login.** `session/new` uses the stored `selectedType`. If nothing is stored, it falls back to `USE_GEMINI` (API key) or `GATEWAY`, so ACP can't bootstrap a Google login on its own (`acpSessionManager.ts` `newSession`). Headless mode also "will use your existing authentication method, if an existing authentication credential is cached" (`authentication.mdx` §headless).
- Upstream has an open request for an alternative individual login: google-gemini/gemini-cli#29613 "Add 'Developer Login' (AI Studio OAuth)", opened 2026-10-02.

---

## OpenCode

### Surfaces

- **ACP, native.** `opencode acp [--cwd]` starts "ACP (Agent Client Protocol) server" over stdio, ndjson (`opencode:packages/opencode/src/cli/cmd/acp.ts`; `opencode:packages/web/src/content/docs/acp.mdx`). Internally it **starts OpenCode's local HTTP server (`Server.listen`) and translates ACP onto its own SDK** (`acp.ts`, `src/acp/service.ts`), so ACP is a thin layer over the server API. Uses `@agentclientprotocol/sdk` **0.21.0** (`opencode:packages/opencode/package.json`). Listed in the Zed ACP registry. Docs: "works the same via ACP as it does in the terminal", except slash commands like `/undo` and `/redo` (`acp.mdx`).
- **HTTP server.** `opencode serve` (`--port/--hostname/--mdns/--cors`; basic auth via `OPENCODE_SERVER_PASSWORD`). The OpenAPI 3.1 spec is served at `/doc`, and an SSE stream at `/event` (plus `/global/event`).
  - Session endpoints include create/list/status/abort/fork/revert/unrevert/summarize, **`GET /session/:id/diff` → `FileDiff[]`**, and `POST /session/:id/permissions/:permissionID` (`opencode:packages/web/src/content/docs/server.mdx`).
  - A generated JS SDK exists (`docs/sdk.mdx`).
- **Headless one-shot.** `opencode run [message]` with `--format json` ("raw JSON events"), `-c/--continue`, `-s/--session`, `--fork`, `--attach <server>`, `--dir`, `-m provider/model`, `--agent` and `--auto` ("Auto-approve permissions that are not explicitly denied") (`docs/cli.mdx` §run). The JSON event schema isn't documented beyond that one phrase.

### ACP capabilities (source)

`initialize` returns (`opencode:packages/opencode/src/acp/service.ts` L94–139):

- `protocolVersion: 1`
- `loadSession: true`
- `mcpCapabilities {http, sse}`
- `promptCapabilities {embeddedContext, image}`
- **`sessionCapabilities {close, fork, list, resume}`**
- a single `authMethods` entry: id `opencode-login`, "Login with opencode", description "Run `opencode auth login` in the terminal"

If the client sends `clientCapabilities._meta["terminal-auth"] = true`, the auth method also carries `_meta["terminal-auth"] = {command: "opencode", args: ["auth","login"]}`. `authenticate` itself is a no-op that only validates the method id.

- **Methods:** initialize, authenticate, new/load/list/resume/close session, `unstable_forkSession`, `setSessionConfigOption`, `setSessionMode`, `unstable_setSessionModel`, prompt, cancel (`src/acp/agent.ts`). Model, variant and mode (OpenCode "agents" such as build/plan) are exposed as `configOptions`.
- **load vs resume:** `session/load` fetches all messages and **replays** them as updates. `session/resume` fetches the last 20 for state and does **not** replay (`service.ts` L211–335).
- **Streaming updates:** `agent_message_chunk`, `agent_thought_chunk`, `user_message_chunk`, `tool_call` and `tool_call_update` (with `kind`, `locations`, and `{type:"diff"}` content for edits), `available_commands_update`, `config_option_update`, `usage_update` (`src/acp/event.ts`, `src/acp/tool.ts`).
- **Permissions:** OpenCode permission events become `session/request_permission` with options `once` (allow_once), `always` (allow_always) and `reject` (reject_once). Edit permissions carry diff content. If the client lacks `requestPermission`, or the request errors or is cancelled, OpenCode **auto-rejects** (`src/acp/permission.ts`). Caveat: OpenCode's **defaults are permissive**. "Most permissions default to `allow`"; only `doom_loop` and `external_directory` default to `ask` (`docs/permissions.mdx` §defaults). Öge only sees approval requests if it sets `permission` rules, e.g. through `OPENCODE_PERMISSION` (inline JSON) or `OPENCODE_CONFIG_CONTENT` (`docs/cli.mdx` §env vars).
- **Cancellation:** `session/cancel` → `sdk.session.abort`, and the prompt resolves with `stopReason: "cancelled"` (`service.ts` L336–360, ~L860).
- **File-change reporting:** `diff` and `locations` on tool calls. When an edit is approved and the client advertises `writeTextFile`, OpenCode also mirrors the edit through the client (`permission.ts` `writeProposedEdit`). The server's `/session/:id/diff` is an extra source.

### Auth

- **OpenCode keeps its own credential store.** `opencode auth login` stores credentials in `~/.local/share/opencode/auth.json`. It also reads provider keys from env and the project `.env` (`docs/cli.mdx` §auth).
- **Subscription logins are first-party OAuth flows in OpenCode:** "ChatGPT Plus/Pro" (browser OAuth), GitHub Copilot (device flow) and GitLab Duo, plus OpenCode's own Zen/Go plans (`docs/providers.mdx`).
- **No Claude subscription path.** "There are plugins that allow you to use your Claude Pro/Max models with OpenCode. Anthropic explicitly prohibits this. Previous versions ... came bundled with these plugins but that is no longer the case as of 1.3.0" (`docs/providers.mdx` §Anthropic). The step text just above that note still mentions a "Claude Pro/Max option", so the docs contradict themselves.
- In short, Öge can launch `opencode acp` and reuse whatever the user set up with `opencode auth login`, with no API key, via ChatGPT/Copilot OAuth. That is still a **separate login from Codex's and Claude Code's**, and the OpenAI login is a separate OpenCode-held OAuth token, not the `codex` CLI's.
- **Installed state:** not installed locally. Nothing above was probed; it comes from source and docs only.

---

## Comparison

| | Gemini CLI | OpenCode |
|---|---|---|
| ACP entry point | `gemini --acp` (native) | `opencode acp` (native, wraps local HTTP server) |
| ACP SDK | 0.16.1 | 0.21.0 |
| load / list / resume / fork / close | load only | all five |
| Per-tool approval over ACP | yes (`request_permission`) | yes, but only for rules set to `ask` (defaults permissive) |
| Approval in headless | no (`ask_user` → deny) | no (`--auto` or config) |
| Cancel | `session/cancel` → `cancelled` | `session/cancel` → abort → `cancelled` |
| File-change reporting | `diff` content, optional fs proxy | `diff` + `locations`, server `/session/:id/diff` |
| Other structured surface | `-o json/stream-json` | HTTP server + OpenAPI + SSE, `run --format json` |
| Existing-login reuse, no API key | **Only for Code Assist Standard/Enterprise.** Consumer/AI Pro/Ultra Google login is dead since 2026-06-18 | **Yes, via OpenCode's own** `opencode auth login` (ChatGPT Plus/Pro, Copilot, …); no Claude Pro/Max |

## Implications for Öge

1. **Is a second ACP agent near-free (#31's exception)?** OpenCode: mostly yes on the protocol side. Gemini: no, because of auth. If #31 uses the exception, OpenCode is the only candidate of the two, under these conditions:
   - It needs an install (`opencode` isn't on the machine).
   - It needs a provider login done in OpenCode (`opencode auth login`).
   - Its value as a *different model family* only holds if the user has e.g. Copilot or ChatGPT through OpenCode. For a user with only Codex + Claude subscriptions, OpenCode just routes to the same OpenAI account through a second token, or to OpenCode Zen.
   - Since the MVP agents are already Codex + Claude Code, the extra independence OpenCode would add for the verifier is small. This argues for keeping OpenCode **post-MVP but cheap**, not in the MVP.
2. **Gemini CLI should drop out of the "later adapters" list in its current form.** Google is moving consumer users to Antigravity, so #7 is now the Google-side question. Gemini CLI is only worth an adapter for users with Code Assist Standard/Enterprise. The adapter would be nearly the same as any ACP adapter, so it is cheap if the ACP client is generic. Just don't plan around it.
3. **ACP client design (#3, #17, #18).**
   - The two agents use SDK 0.16.1 vs 0.21.0 and advertise different `sessionCapabilities`. Öge's client must negotiate capabilities rather than assume them, e.g. fall back from `session/resume` to `session/load`, and handle no `list`.
   - Treat history replay on `load` as a stream to drain or ignore.
   - Don't depend on `unstable_*` methods.
4. **Auth boundary (#19).**
   - Öge must **never call ACP `authenticate` speculatively.** On Gemini it can clear cached credentials and rewrite user settings.
   - Treat an `authRequired`/`-32000` error at `session/new` as "tell the user to log in with the agent's own CLI".
   - OpenCode's `terminal-auth` `_meta` (the agent hands back the login command to run in a terminal) fits Öge's "controls the process, not the credential" stance well. Öge can show the command and run nothing on its own.
5. **`oge doctor` (#28).** Eligibility can't be checked at `initialize`. Doctor needs a `session/new` probe, which costs no tokens, to catch Gemini's `IneligibleTierError` or a missing OpenCode provider.
6. **Approval gates (#24).** For both agents, only ACP gives per-call approval; headless is policy-only. For OpenCode, Öge has to *inject* `ask` rules (env `OPENCODE_PERMISSION`), otherwise it never sees a request. That is useful: Öge can set role-specific permission policy, e.g. a read-only verifier, entirely through env without touching user config.
7. **Workspace isolation (#12, #22).** Gemini's folder-trust gate fires in fresh worktrees: headless exits 55, and in ACP, project hooks/agents are silently disabled. Öge has to pick between `--skip-trust`/`GEMINI_CLI_TRUST_WORKSPACE=true` and accepting reduced project config, and record the choice.
8. **Evidence (#23).** Both report diffs per tool call, which is useful for the timeline. Öge's own git diff of the worktree remains the evidence, as agent-reported diffs are claims.

## Open questions

- Does OpenCode's `session/new` fail cleanly (an auth error) when no provider is configured, or does it silently fall back to a free OpenCode Zen model? This needs a probe with `opencode` installed.
- What schema does `opencode run --format json` emit? It's only documented as "raw JSON events". Read `src/cli/cmd/run.ts` if headless OpenCode ever matters. ACP or the server should make it unnecessary.
- Does OpenCode's ACP `cancel` interrupt a running bash tool promptly, i.e. kill the process group? Behaviour wasn't checked beyond `session.abort`.
- Will Google add an individual-developer login back to Gemini CLI (gemini-cli#29613, AI Studio OAuth)? If it does, Gemini becomes a cheap ACP adapter again. Re-check before any post-MVP adapter work.
- Does Antigravity's CLI (`agy`) expose ACP? If so, it inherits Gemini's place, which is #7's question.
- Do OpenCode's ChatGPT Plus/Pro OAuth terms allow third-party orchestration the way Codex's own login does? This is a policy question for #19, not a technical one.

## Probe log (summary)

Scratch dir: `/private/tmp/.../scratchpad/probe-gemini/work` (empty, not a git repo). No credential files were read.

1. ACP: `initialize` → OK (capabilities above). `session/new {cwd, mcpServers: []}` → error `-32000` `IneligibleTierError` / `UNSUPPORTED_CLIENT`. No prompt was sent, so no quota was used.
2. Headless: `gemini -p "Reply OK" -o stream-json` → exit 55 (untrusted folder). With `--skip-trust` → exit 1, empty stdout, `IneligibleTierError` on stderr.
