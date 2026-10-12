# More agents and models: integration paths beyond Claude Code and Codex

Research for [Erengun/oge#133](https://github.com/Erengun/oge/issues/133). Date: 2026-10-12. It feeds post-M1 (MVP map) planning. This is research only: it takes no product decision.

**Question.** What are the realistic integration paths for coding agents and models beyond Claude Code and Codex? What is the cheapest route that covers the most models? And what does an "agent" mean for a model-only API?

## Short answer

- **Most candidates now speak ACP v1 natively.** The ACP registry lists 42 agents. Its probe matrix from 2026-10-12 got a successful `initialize` from 33 of 35. Agents with a native ACP entry point include OpenCode, Gemini CLI, Cursor, Goose, GitHub Copilot CLI, Cline, Qwen Code, Kilo, Devin CLI, Junie and Mistral Vibe. Amp has only a third-party wrapper. Aider has no ACP and no structured output.
- **Cheapest route that covers the most models:** one generic ACP v1 client (already planned post-MVP by ADR-0003), with OpenCode as the first target.
  - OpenCode reaches OpenRouter natively, and Ollama or llama.cpp through `@ai-sdk/openai-compatible`.
  - The same client also reaches the multi-model agents. A zero-quota probe showed Copilot CLI offering Claude, GPT, Kimi and MAI models on a GitHub login.
- **Two existing adapters may already reach open-weight models with no new adapter code, but neither route is vendor-supported or probed.**
  - **Codex:** the user's own `config.toml` can set `model_providers` or `--oss --local-provider ollama`.
  - **Claude Code:** the user can set `ANTHROPIC_BASE_URL`, which Öge passes through. Ollama, OpenRouter and llama.cpp all expose an Anthropic-format endpoint. Anthropic says it does not support routing Claude Code to non-Claude models.
- **A model-only API has no agent loop.** Driving one directly means Öge holds a credential and runs the tool loop, the sandbox and the policy itself. That conflicts with ADR-0006 as written. Every surveyed gateway, including OpenRouter, Ollama and llama.cpp, is already reachable through some agent CLI that owns the key.

## Sources and versions

All URLs were accessed 2026-10-12. Repos were read through the GitHub API on that date.

| Thing | Version / ref | Date |
|---|---|---|
| ACP spec repo [agentclientprotocol/agent-client-protocol](https://github.com/agentclientprotocol/agent-client-protocol) | HEAD `d9d2f52`; Rust crate `v1.11.0`; schema `schema-v1.25.0` and `schema-v2.0.0-alpha.8` | releases 2026-10-09 |
| ACP registry [agentclientprotocol/registry](https://github.com/agentclientprotocol/registry) | HEAD `9a7f296` | 2026-10-12 |
| Registry probe matrix ([`.protocol-matrix/latest.md`](https://github.com/agentclientprotocol/registry/blob/main/.protocol-matrix/latest.md)) | generated 2026-10-12T13:12Z | 35 probed, 33 `initialize` ok, 22 `session/new` returned `auth_required` |
| Local binaries used in probes | `copilot 1.0.59`, `cline 3.0.61` | probed 2026-10-12 |
| Other local binaries (not probed here) | `codex-cli 0.160.1`, `Claude Code 2.1.296`, `gemini 0.46.0`, `ollama` client 0.33.3 (no server running) | — |

Registry versions are newer than the installed ones: Copilot CLI 1.0.95 and Cline 3.0.70.

**Provenance caveat.** Most vendor doc pages were read through a summarising fetch tool, so the wording below is paraphrase unless it appears in quotation marks. Facts confirmed only by a search-result summary are marked *(search summary)*. Anything not found on a primary page is marked **unconfirmed**. Earlier notes on the same ground were not repeated: [acp.md](acp.md), [opencode-gemini.md](opencode-gemini.md) and [pi.md](pi.md).

## What Öge needs from an agent

The checklist comes from ADR-0003, ADR-0005, ADR-0006, ADR-0018 and ADR-0019 §4.

| # | Need | Why |
|---|---|---|
| N1 | Headless, scriptable session | ADR-0005 `Open`/`Send`/`Interrupt`/`Close`; "No PTY". An agent without a structured interface is out of scope. |
| N2 | Streamed structured events (tool calls, edits) | Normalised core events. Agent diffs are claims; Öge diffs the worktree itself. |
| N3 | Host permission routing | ADR-0019 §4: pre-authorise / auto-deny / raise a Host request. ADR-0005: approvals are per call with no "always allow", so Öge would answer only `allow_once`/`reject_once`. |
| N4 | Sandbox or cwd confinement | Launch profile; fail closed when a trust property can't be kept. |
| N5 | Clear exit / turn settled | Accepted vs settled are separate states. Infrastructure stop on auth or quota failure. |
| N6 | Agent-owned auth | ADR-0006: Öge never invokes login or ACP `authenticate`, and never reads or stores credential values. |

N3 can come from three families of mechanism:

- **(a) In-band per-call request:** ACP `session/request_permission`, Codex approvals or Claude `can_use_tool`. This is the only family that gives a true Host request.
- **(b) Pre-declared policy:** a config file, env var or flag that Öge sets per role. This covers pre-authorise and auto-deny.
- **(c) Out-of-band helper or hook:** the agent calls a program Öge supplies, and that program decides.

## Candidate table

Legend: ✅ confirmed in a primary source or probe; ~ partial or with caveats; ✗ absent; ? unconfirmed.

| Candidate | Kind | Headless surface (N1/N2) | ACP | Permission controls (N3) | Sandbox / cwd (N4) | Exit (N5) | Auth, ADR-0006 fit (N6) | Models reachable | Basis |
|---|---|---|---|---|---|---|---|---|---|
| **Devin** (Cognition) | Cloud service (API v3) **and** local "Devin for Terminal" CLI | CLI: `devin -p` single turn; output format ? | ✅ `devin acp` (registry 3000.11.3) | (b) `allow`/`deny`/`ask` rules `Read()/Write()/Exec()/Fetch()`; modes normal/accept-edits/smart/bypass/autonomous | ✅ `--sandbox` (OS-level) forces Autonomous mode; edits still prompt | ? | ~ `/login` to a Devin account; Cognition hosts the models. Cloud API needs a service-user or PAT key → ✗ for that path | Anthropic, OpenAI, Google, Cognition, open-weight (via Devin) | docs + registry |
| **OpenRouter** | Model gateway (API only) | n/a | n/a | n/a | n/a | n/a | ✗ if Öge calls it: API key, or OAuth PKCE that **returns a key to the app** | Hundreds; tool calling varies by model | docs |
| **Ollama / llama.cpp** | Local model servers | n/a | n/a | n/a | n/a | n/a | ✅ local: no key | Any local GGUF model | docs |
| **OpenCode** | Local CLI agent (MIT) | `opencode run --format json`; `opencode serve` HTTP+SSE | ✅ `opencode acp` (1.19.0), full session caps | (a) ACP request for `ask` rules; (b) `allow`/`ask`/`deny` per tool via `opencode.json` or `OPENCODE_PERMISSION` env; defaults mostly `allow` | ~ `external_directory` rule only; no OS sandbox | ✅ ACP `stopReason` | ~ own store (`opencode auth login`, `/connect`) | OpenRouter (native), Ollama/llama.cpp (OpenAI-compatible), ChatGPT/Copilot OAuth | docs + source (earlier note) |
| **Gemini CLI** | Local CLI agent | `-p -o stream-json` | ✅ `gemini --acp` (0.63.0) | (a) over ACP only; headless `ask_user` → deny | ~ folder trust gate; `--sandbox` | ✅ codes 0/1/42/53 | ✗ for consumers: individual Google login ended 2026-06-18; API key or Vertex only | Gemini only | docs + earlier probe |
| **Aider** | Terminal pair programmer (Apache-2.0) | `--message`, `--yes-always`; **no JSON events** | ✗ | ✗ yes-to-all only | ✗ | ? | ✅ agent-held keys | OpenRouter, Ollama, OpenAI-compatible | docs |
| **Goose** | Local agent (Apache-2.0) | `goose run --output-format stream-json`, `--max-turns` | ✅ `goose acp` (1.54.0); `goose serve` over HTTP/WS | (a) claimed "via ACP", details ?; modes auto/approve/smart_approve/chat | ? "isolated execution environment" named, no detail | ? | ✅ agent-held (`goose configure`); keychain prompt can block unattended runs unless `GOOSE_DISABLE_KEYRING=1` | Multi-provider (OpenRouter/Ollama ?) | docs |
| **Amp** | CLI agent plus hosted service (proprietary) | `amp -x --stream-json` (described as Claude-Code-compatible *(search summary)*) | ~ third-party `amp-acp` wrapper only | (b) `amp.permissions` allow/ask/reject; **(c) `delegate` helper: JSON on stdin, exit 0 allow / 1 ask / 2 reject (stderr goes to the model)** | ? | ? | ~ `amp login` or `AMP_API_KEY` | Amp-chosen models via `mode`; no provider choice documented | docs (partial) |
| **Cursor CLI** | Local CLI agent (proprietary) | `agent -p --output-format stream-json`; events `system`/`assistant`/`tool_call`/`result` | ✅ `agent acp` | (a) ACP `request_permission`; (b) `permissions.allow/deny` with `Shell()/Read()/Write()/WebFetch()/Mcp()`, deny wins | ~ relative paths workspace-scoped; absolute paths can escape; OS sandbox ? | ~ `result` event; exit codes undocumented | ~ `agent login` or `CURSOR_API_KEY`; ACP `authenticate` `cursor_login` exists, and **Öge must not call it** | Cursor-hosted multi-vendor | docs |
| **GitHub Copilot CLI** | Local CLI agent (proprietary licence) | `copilot -p --output-format json` (JSONL; schema ?) | ✅ `copilot --acp` (public preview) | (a) ACP request (docs show a `requestPermission` handler); (b) `--allow-tool`/`--deny-tool`/`--allow-all-tools`; ACP config option `allow_all` on/off | ~ `--add-dir` path allow-list; sandbox semantics ? | ? for `-p` | ✅ existing `copilot login` reused (probe); env `COPILOT_GITHUB_TOKEN`/`GH_TOKEN` | **Multi-vendor on one GitHub login** (probe: Claude Haiku 4.5, GPT-5.x, Kimi K3, MAI-Code) | docs + probe |
| **Qwen Code** | Gemini CLI fork (Apache-2.0) | `-p -o stream-json`, `--max-session-turns`, `--max-tool-calls` | ✅ registry runs `--acp` (0.25.1) | (b) `--approval-mode plan/default/auto-edit/auto/yolo`, permission rules and hooks; (a) over ACP presumably ? | ✅ `--sandbox`; Linux bwrap/Landlock `workspace-write`, network closed | ~ 53 turn cap, 55 budget, 130 SIGINT | ✅ agent-held; Qwen OAuth free tier ended 2026-04-15; `OPENAI_API_KEY`/`OPENAI_BASE_URL` | OpenRouter, any OpenAI-compatible | docs |
| **Cline CLI** | CLI agent (Apache-2.0) | `--json` NDJSON (`agent_event`, `run_result`, `run_aborted`) | ✅ `cline --acp` | (a) ACP: every edit and command goes to the client by default; headless defaults to **auto-approve all**; (c) `PreToolUse` hooks | ~ `--cwd`; `CLINE_SANDBOX` semantics ? | ~ `run_result.finishReason` | ~ own store (`cline auth`); probe showed a ChatGPT-subscription provider | Cline, ClinePass, ChatGPT subscription, BYO key | docs + probe |
| **Kilo CLI** | OpenCode fork (MIT) | `kilo run --format json`, `kilo serve` | ✅ `kilo acp` (7.8.8) | (b) `allow`/`ask`/`deny` per tool, last match wins; headless auto-rejects without `--auto` | ~ `external_directory` | ✅ 0 / 1 / 124 timeout | ~ own store (`kilo auth login`) | Same family as OpenCode (?) | docs |

## Notes per candidate

**Devin.** There are two different things.

- The **cloud API** (v3; v1 and v2 deprecated) runs sessions on Cognition's machines and authenticates with an org service-user key ([API overview](https://docs.devin.ai/api-reference/overview)). It has none of Öge's local cwd, worktree or Evidence model, and Öge would hold the key. It fails N4 and N6.
- The **local CLI** is a normal candidate:
  - `devin -p` ([essential commands](https://docs.devin.ai/cli/essential-commands)).
  - `allow`/`deny`/`ask` rules where deny always wins, plus an OS-level `--sandbox` that forces Autonomous mode while edit and write tools still prompt ([permissions](https://docs.devin.ai/cli/reference/permissions)).
  - ACP (registry `devin/agent.json` runs `devin acp`; the README links Zed, JetBrains and Xcode ACP guides: [CognitionAI/devin-cli](https://github.com/CognitionAI/devin-cli)).
  - The repo has no licence.
  - Output format of `-p` and auth env vars: **unconfirmed**.

**OpenRouter.** OpenAI-style `POST /api/v1/chat/completions` ([API overview](https://openrouter.ai/docs/api-reference/overview)). The OpenAPI spec also lists Anthropic-format `/messages` and `/responses` ([openapi.json](https://openrouter.ai/openapi.json)).

- Tool calling varies by model. A `supported_parameters=tools` filter and a per-model tool-call error rate exist ([tool calling](https://openrouter.ai/docs/guides/features/tool-calling)).
- App auth is OAuth PKCE, and it ends by `POST /api/v1/auth/keys` returning a **user-controlled API key to the app** ([OAuth](https://openrouter.ai/docs/guides/overview/auth/oauth)). That is the ADR-0006 problem for any direct route.
- OpenRouter documents Claude Code via `ANTHROPIC_BASE_URL=https://openrouter.ai/api` plus `ANTHROPIC_AUTH_TOKEN`, "only guaranteed to work with the Anthropic first-party provider" ([Claude Code guide](https://openrouter.ai/docs/guides/guides/coding-agents/claude-code-integration)).

**Ollama** (v0.40.3, 2026-10-11, [releases](https://github.com/ollama/ollama/releases)):

- OpenAI-compatible `/v1/chat/completions` (tools, no `tool_choice`) and `/v1/responses` (stateless, function tools only) ([OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)).
- An Anthropic-compatible `/v1/messages` subset with tool_use ([Anthropic compatibility](https://docs.ollama.com/api/anthropic-compatibility)).
- `ollama launch` integrations for Claude Code and OpenCode, but not Codex ([integrations](https://docs.ollama.com/integrations)). It recommends a context window of at least 64k for Claude Code.

**llama.cpp** (`llama-server`, release v0.6.0, 2026-10-05):

- `/v1/chat/completions`, `/v1/responses` and `/v1/messages` (the last with "no strong claims of compatibility").
- Tool calls need `--jinja`. The README is inconsistent about whether that is the default.
- An experimental built-in `--tools`/`--agent` mode exposes file and shell access, with a warning not to enable it in untrusted environments ([server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md)).

**OpenCode.** See [opencode-gemini.md](opencode-gemini.md) for the ACP surface. New since then:

- v1.19.0 (2026-10-11, registry).
- The provider list confirms a native OpenRouter provider, Ollama (`http://localhost:11434/v1`) and llama.cpp (`http://127.0.0.1:8080/v1`) through `@ai-sdk/openai-compatible`. Custom keys can be `{env:NAME}` ([providers](https://opencode.ai/docs/providers/)).
- Permissions are `allow`/`ask`/`deny` per tool, last match wins. Defaults are mostly `allow`; `external_directory` and `doom_loop` are `ask`, and `*.env` reads are denied ([permissions](https://opencode.ai/docs/permissions/)).
- `OPENCODE_PERMISSION` is documented in the CLI reference (`packages/web/src/content/docs/cli.mdx` in [anomalyco/opencode](https://github.com/anomalyco/opencode)). Öge can inject role policy without touching user config.

**Gemini CLI.** Unchanged from [opencode-gemini.md](opencode-gemini.md): the protocol is a good fit, but consumer auth is the blocker.

- Google confirms individual "Login with Google" ended 2026-06-18 and points users to the Antigravity CLI ([deprecation](https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals)).
- [gemini-cli#29613](https://github.com/google-gemini/gemini-cli/issues/29613) (AI Studio developer login) is still open (last update 2026-10-08).
- v0.63.0 (2026-10-06) has no auth-flow change.
- No route to non-Google models was found. `GOOGLE_GEMINI_BASE_URL` only repoints Gemini-API traffic ([configuration](https://github.com/google-gemini/gemini-cli/blob/main/docs/reference/configuration.md)).

**Aider** fails N2 and N3: there is no structured event stream and no per-tool permission model.

- Its Python scripting API "is not officially supported" ([scripting](https://aider.chat/docs/scripting.html), [options](https://aider.chat/docs/config/options.html)).
- Latest release v0.86.0 (2025-08-09).
- It reaches OpenRouter and Ollama ([LLMs](https://aider.chat/docs/llms.html)), but OpenCode covers the same models with a structured surface.

**Goose.** The repo moved to `aaif-goose/goose` (v1.54.0, 2026-10-08).

- `goose run` takes `--output-format text|json|stream-json`, `--max-turns` and `--no-session` ([CLI commands](https://goose-docs.ai/docs/guides/goose-cli-commands)).
- `goose acp` serves ACP over stdio.
- The permissions page names approval "via ACP" and an isolated execution environment without detail ([permissions](https://goose-docs.ai/docs/guides/goose-permissions)).
- Unattended runs need `GOOSE_DISABLE_KEYRING=1`, or a macOS Keychain prompt can block. That variable only changes where Goose itself reads keys.

**Amp** (npm `@sourcegraph/amp`, proprietary, rolling builds):

- `--stream-json` requires `--execute` and writes one object per line *(search summary of [streaming JSON](https://ampcode.com/news/streaming-json))*.
- The SDK streams `system`/`assistant`/`result` messages, uses the local CLI, and authenticates by `AMP_API_KEY` or `amp login`. Model choice is a `mode` (low/medium/high/ultra), not a provider ([SDK](https://ampcode.com/docs/sdk)).
- The notable control is the **`delegate` permission action**: a helper on `$PATH` receives tool parameters as JSON on stdin and exits 0 (allow), 1 (ask) or 2 (reject, with stderr forwarded to the model) ([permissions notes](https://ampcode.com/notes/permissions)). That maps directly onto ADR-0019's three outcomes. The manual suggests `amp.permissions` may now be a legacy path behind a plugin *(search summary)*.
- How `ask` behaves in `--execute` is **unconfirmed**.
- ACP only via a third-party wrapper (`tao12345666333/amp-acp`, registry `amp-acp` 0.10.0).

**Cursor CLI:**

- Headless `-p` proposes file changes only, unless `--force`/`--yolo` is passed ([headless](https://cursor.com/docs/cli/headless)).
- ACP `agent acp` sends `session/request_permission` with allow-once/allow-always/reject-once options ([ACP](https://cursor.com/docs/cli/acp)).
- Policy file `~/.cursor/cli-config.json` or `.cursor/cli.json`, where deny wins ([permissions](https://cursor.com/docs/cli/reference/permissions)).
- Licence and terms: **unconfirmed**.

**GitHub Copilot CLI** (v1.0.95, 2026-10-09; proprietary [licence](https://raw.githubusercontent.com/github/copilot-cli/main/LICENSE.md)):

- `-p` exits when done; `--output-format json` is JSONL; `--allow-tool`/`--deny-tool` ([programmatic reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-programmatic-reference)).
- `copilot --acp` is in public preview ([ACP server](https://docs.github.com/en/copilot/reference/copilot-cli-reference/acp-server)).
- Auth precedence `COPILOT_GITHUB_TOKEN` > `GH_TOKEN` > `GITHUB_TOKEN`, stored in the OS keychain ([command reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference)).
- The probe (below) shows the existing login reused at `session/new` with no `authenticate` call, and modes Agent/Plan/Autopilot. "Autopilot" "enables allow-all and runs until task completion". There is also an `allow_all` on/off config option.

**Qwen Code:**

- Headless events `system`/`assistant`/`result`/`stream_event`; exit 53 (turn cap), 55 (budget), 130 (SIGINT) ([headless](https://qwenlm.github.io/qwen-code-docs/en/users/features/headless/)).
- Approval modes, where `auto` is an LLM classifier and the default ([approval mode](https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/approval-mode.md)).
- Linux tool sandbox via bwrap/Landlock with `workspace-write` and network closed ([sandbox](https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/sandbox.md)).
- The Qwen OAuth free tier ended 2026-04-15. OpenAI-compatible env and `modelProviders` remain, including OpenRouter ([auth](https://qwenlm.github.io/qwen-code-docs/en/users/configuration/auth/)).

**Cline CLI** ([overview](https://docs.cline.bot/cline-cli/overview), [reference](https://docs.cline.bot/cline-cli/cli-reference), [ACP](https://docs.cline.bot/usage/acp)):

- Headless defaults to `--auto-approve true`. ACP defaults to routing every edit and command to the client.
- No allow/deny list; the docs recommend `PreToolUse` hooks.
- The probe shows a ChatGPT-subscription provider (`openai-codex`) held by Cline itself, exposed as an ACP `configOptions` select.

**Kilo CLI.** "A fork of OpenCode" ([CLI](https://kilo.ai/docs/code-with-ai/platforms/cli), [reference](https://kilo.ai/docs/code-with-ai/platforms/cli-reference)). It has the same permission shape as OpenCode and documented exit codes 0/1/124. Adding it on top of OpenCode would add little model coverage.

## ACP status (2026-10-12)

- **Wire protocol is still version `1`.** `2` exists only behind `unstable_protocol_v2` ([`version.rs`](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/agent-client-protocol-schema/src/version.rs)).
- Latest releases are Rust `v1.11.0` / schema `v1.25.0` (2026-10-09). That release stabilised session notices and compaction ([CHANGELOG](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/CHANGELOG.md)).
- v2 is at `alpha.8`, with no stabilisation date. The v2 removals listed in [acp.md](acp.md) still stand.
- **Go SDKs are unchanged since acp.md.** [coder/acp-go-sdk](https://github.com/coder/acp-go-sdk) is still at v0.13.5 (last commit 2026-06-02). [ironpark/acp-go](https://github.com/ironpark/acp-go) is active (commit 2026-10-12). There is still no official Go SDK.
- **Registry matrix** ([latest.md](https://github.com/agentclientprotocol/registry/blob/main/.protocol-matrix/latest.md)):
  - `session/list` supported by 23 agents, `session/resume` by 12, `session/fork` by 10.
  - Auth kinds are `agent` or `terminal`. `terminal` means the agent hands back a login command through `_meta["terminal-auth"]`. Under ADR-0006, Öge may **show** that command but never run it, and never call `authenticate` (Gemini's clears credentials, per opencode-gemini.md).

## Ranked options (evidence, not a decision)

### 1. Cheapest route covering the most models

| Rank | Route | New Öge code | Models covered | Caveats |
|---|---|---|---|---|
| A | **Generic ACP v1 client, OpenCode first** (ADR-0003's stated post-MVP target) | One ACP v1 client (hand-written or generated from `schema/v1/schema.json`; no official Go SDK) | OpenRouter (all its models), Ollama, llama.cpp, any OpenAI-compatible gateway, ChatGPT/Copilot via OpenCode's own login | OpenCode defaults are permissive, so Öge must inject `ask`/`deny` via `OPENCODE_PERMISSION` to see Host requests. No OS sandbox, only `external_directory`. Separate login store. |
| A+ | **Same client, more agents**, each a config entry plus a doctor probe: Copilot CLI, Cursor, Cline, Goose, Qwen Code, Kilo, Devin CLI | Per-agent launch profile, permission-policy mapping, envelope checks | Copilot: multi-vendor on a GitHub login (probe). Devin: frontier and open-weight models. Qwen Code: OpenAI-compatible endpoints. | Per-agent policy formats differ (family b). "Supported" in the registry ≠ Öge's fail-closed guarantees; each needs its own envelope proof. |
| B | **Existing Codex adapter + user's `config.toml`**: `model_providers.<id>` (`base_url`, `env_key`, `wire_api`) or `--oss --local-provider ollama\|lmstudio` ([Codex advanced config](https://learn.chatgpt.com/docs/config-file/config-advanced)) | Possibly none; doctor and docs | Ollama, LM Studio, and any `/v1/responses` endpoint (Ollama, llama.cpp and OpenRouter all have one) | Docs show only `wire_api = "responses"`. Whether app-server approvals, sandbox self-test and `thread/start` envelope behave the same with a non-OpenAI provider is **unprobed**. |
| C | **Existing Claude adapter + user-set `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`** pointing at Ollama, OpenRouter or llama.cpp `/v1/messages` | Possibly none: ADR-0006 already passes these through untouched | Any model behind those gateways | Anthropic "doesn't support routing Claude Code to non-Claude models through any gateway". The subscription login stays active unless a gateway credential var is set ([LLM gateway](https://code.claude.com/docs/en/llm-gateway)). The adapter assumes Claude's `system/init`. **Unprobed.** |

Ranking logic: A covers the most model families for one new adapter and is already the ADR-0003 plan. B and C are cheaper in code but rely on unsupported or unprobed vendor paths. They would change what an existing adapter's Evidence can honestly claim.

### 2. What "agent" means for a model-only API

A model API (OpenRouter, Ollama, llama.cpp) is a completion endpoint. The tool loop, file edits, shell, sandbox and permission prompts all live in an agent. Two options:

- **Option 1: always require an agent CLI.**
  - Every ADR holds as written: the agent owns the key (ADR-0006), the adapter seam is unchanged (ADR-0005), and policy maps onto the agent's controls (ADR-0019 §4).
  - Every gateway surveyed is already reachable through at least one agent that speaks a supported surface: OpenCode (ACP), Codex (native), Qwen Code/Goose/Cline (ACP), and Claude Code (unsupported).
  - Cost: the user installs and logs into one more tool, and model quality depends on that agent's tool-call handling.
- **Option 2: Öge ships a minimal agent loop over a model API.**
  - Öge would hold or obtain a provider credential. OpenRouter's PKCE flow hands the app an API key. ADR-0006 says Öge never reads credential values into application logic, so this would need that ADR reversed or amended.
  - Öge would own the tool implementations, the sandbox and the permission engine. Today the agent does that, and Öge only checks the envelope.
  - The trust product's claim (Öge checks what an independent agent did) weakens when Öge is also the agent.
  - It runs against AGENTS.md "Trust must earn its overhead" / "don't build infrastructure ahead of a felt UX", unless a concrete user need appears.
  - Precedent: llama.cpp's experimental `--tools/--agent` shows model servers growing their own agent modes, with a "not in untrusted environments" warning.

The evidence shows no model family that is reachable *only* by a raw API. That would be the condition that makes Option 2 necessary.

### 3. Risks, unknowns, and what needs a live probe

| Unknown | Why it matters | Probe |
|---|---|---|
| OpenCode `session/new` with no provider configured: clean auth error, or silent fallback to OpenCode Zen? | Fail-closed readiness; silent model substitution | Install `opencode`, run `initialize` + `session/new` (zero quota) |
| OpenCode with `OPENCODE_PERMISSION` setting `ask` for `bash`/`edit`: does every call arrive as `request_permission`, and is `reject_once` honoured promptly? | Host request (N3) | One real turn against a local Ollama model (no paid quota) |
| Codex app-server with `--oss`/`model_providers`: are approvals, `codex sandbox -P` self-test and the `thread/start` envelope unchanged? | Route B guarantees | Spike against local Ollama `/v1/responses` |
| Claude Code via `ANTHROPIC_BASE_URL` to Ollama: does `system/init` still report capabilities, and do `can_use_tool` requests arrive? Does a subscription login leak to the gateway? | Route C guarantees and billing | Spike against local Ollama, no credentials read |
| Open-weight tool-call reliability (malformed calls, small context windows) | Policy-friction turns (ADR-0019 §4) and Attempt failures | Eval arm on Fast mode with one Qwen or DeepSeek model; OpenRouter publishes a per-model tool-call error rate |
| Copilot ACP: does `request_permission` fire with `allow_all=off` in Agent mode? Does the proprietary licence allow third-party orchestration? | N3; ADR-0006 "what would reverse" | One turn; licence and terms read for #19 |
| Cline ACP permission flow end to end; and whether Cline/OpenCode driving a ChatGPT subscription is within OpenAI's terms (carried from opencode-gemini.md) | N3; provider terms | One turn; terms review |
| Amp: `ask` behaviour under `--execute`, whether the `delegate` helper is current or legacy, exact stream-json schema | Family (c) viability without ACP | Docs pages behind sign-in; a probe needs an Amp account |
| Devin CLI: `-p` output format, ACP + `--sandbox` interaction, auth env vars | N2/N4 | Install and `initialize` + `session/new` |
| Goose ACP permission flow; whether the keychain prompt blocks a headless ACP session | N3/N5 | Install and probe |
| Gemini CLI individual login (#29613) | Would make Gemini a cheap ACP agent again | Re-check the issue before post-MVP adapter work |
| ACP v2 timing; Go SDK choice | Dual-stack cost | Watch the spec releases |

## Probe log

Scratch dir: an empty directory under the session scratchpad (not a git repo). Each probe ran `initialize` with `fs`/`terminal` capabilities off, then `session/new {cwd, mcpServers: []}`, then killed the process. No `authenticate`, no prompt, no quota, and no credential files read.

1. **`copilot --acp` (1.0.59).**
   - `initialize`: `protocolVersion: 1`, `loadSession`, `sessionCapabilities {list}`, MCP http/sse, image and embedded context.
   - `authMethods`: `copilot-login` with `_meta["terminal-auth"]` (`copilot login`).
   - `session/new`: succeeded on the existing login and sent `available_commands_update`.
   - Models: auto, claude-haiku-4.5, gpt-5.4-mini, gpt-5-mini, gpt-5.6-luna, gpt-6-luna, kimi-k3, mai-code-1.1-flash.
   - Modes: `…#agent`, `…#plan`, `…#autopilot`. Config options: `mode`, `allow_all` (on/off).
2. **`cline --acp` (3.0.61).**
   - `initialize`: `protocolVersion: 1`, `loadSession`, image.
   - `authMethods`: `cline`, `cline-pass`, `openai-codex` ("Sign in with ChatGPT Subscription").
   - `session/new`: succeeded with modes `plan`/`act` (current `act`), a `provider` config option (current `openai-codex`), and GPT-5.x models.
