# Antigravity: current public integration surfaces

> Moved from branch `research/antigravity` at commit [`3f37e44e99fe`](https://github.com/Erengun/oge/blob/3f37e44e99fe22269de911f39a16c66222e1fbab/research/antigravity.md). Changes: home directory replaced with `~`.

Research for [#7](https://github.com/Erengun/oge/issues/7). Feeds [#18](https://github.com/Erengun/oge/issues/18) and [#19](https://github.com/Erengun/oge/issues/19) (adapter architecture, auth boundary, post-MVP placement).

## Provenance

- **Installed binary:** `~/.local/bin/agy`, Mach-O arm64, file mtime 2026-09-17. `agy --version` prints `1.2.5`. All `--help` output quoted below comes from this build.
- **Latest release:** 1.2.16, tagged 2026-10-03T03:56:08Z in [`google-antigravity/antigravity-cli`](https://github.com/google-antigravity/antigravity-cli). HEAD is `65a3c69e388148c9327f307efe82ddb1c0c8d7d4` (2026-10-03). The repo holds only `README.md`, `CHANGELOG.md`, `examples/{statusline,title}` and a demo gif. It has no source and no license file (`license: null` from the GitHub API). The CLI is closed source.
- **Changelog:** `agy changelog` on the installed 1.2.5 prints entries 1.1.21 to 1.2.16, so it fetches remotely. The version a feature first appears in, as cited below, comes from that output.
- **Official docs:** `https://antigravity.google/docs/...`, fetched 2026-10-04. The pages carry no dates or versions. Pages used: `cli/headless`, `cli/install`, `cli/conversations`, `cli/modes`, `permissions`, `hooks`, `faq`, `ide/extensions` (+ `/zed`, `/jetbrains`), `sdk/overview`, `remote-control`, `cli/troubleshooting`.
- **Terms:** <https://antigravity.google/terms>, fetched 2026-10-04. No effective date is shown.
- **Official SDK:** [`google-antigravity/antigravity-sdk-python`](https://github.com/google-antigravity/antigravity-sdk-python) (Apache-2.0), HEAD `12f9a4c3becf487302dc799b0f59054f01f3ddb9` (2026-09-27), latest tag `v0.1.20`.
- **ACP registry:** [`agentclientprotocol/registry`](https://github.com/agentclientprotocol/registry) HEAD `50f1621e` (2026-10-04), entry `antigravity-acp/agent.json`.
- **Probe:** one headless run in a throwaway scratch dir on 2026-10-04 with installed 1.2.5 (§9). No credential files were read.

## 1. Surface inventory

| Surface | Official? | Transport | Auth | Notes |
|---|---|---|---|---|
| `agy` interactive TUI | yes | terminal | Google account (keyring) or `GEMINI_API_KEY` | Driving it would mean terminal scraping. Rejected. |
| **`agy -p` headless / print mode** | yes, documented ([cli/headless](https://antigravity.google/docs/cli/headless/)) | process args + stdout (text / json / stream-json NDJSON) | cached Google-account credentials, or API key | One prompt per process. |
| **`agy --input-format stream-json --output-format stream-json`** | yes, documented (same page) | NDJSON on stdin and stdout | same | Long-lived and multi-turn in one process. |
| **Google Antigravity ACP server** (`agy_acp_server`) | yes. Google LLC is listed as author in the ACP registry, and binaries come from `dl.google.com` | ACP (JSON-RPC over stdio) | `oauth-personal`, `oauth-business`, `gemini-api-key`, `agent-platform` (docs for the Zed/JetBrains extensions) | A separate binary, not `agy`. See §5. |
| Hooks (`hooks.json`) | yes, documented ([hooks](https://antigravity.google/docs/hooks/)) | Antigravity runs a command with JSON on stdin and reads JSON from stdout | n/a | Includes `PreToolUse` allow/deny/ask. See §4. |
| `transcript.jsonl` on disk | documented as a path in the hook payload (`transcriptPath`) | file | n/a | The record format is not documented. |
| Python SDK `google-antigravity` | yes | in-process Python plus a bundled runtime binary | **API key or Vertex/ADC only** | Ruled out. See §7. |
| Remote Control (`--remote-control`, `agy remote-control start`) | yes | Google-hosted relay to a browser UI | Google account | Remote control of a human session. The map rules out remote control. Not an integration surface for Öge. |
| `agy mcp`, `agy plugin` | yes | config management | n/a | Configures agy. Does not drive it. |

`agy --help` (1.2.5) has no `acp`, `server`, `rpc`, `--settings` or `--hooks` flag. The full flag list is `--add-dir --agent -c/--continue --conversation --dangerously-skip-permissions --disable-slash-commands --effort -i/--prompt-interactive --input-format --json-schema --log-file --mode (accept-edits|plan) --model --new-project --output-format -p/--print/--prompt --print-timeout --project --remote-control --sandbox`. Subcommands are `agent(s) changelog help install mcp mic-serve models plugin(s) remote-control update`.

## 2. Structured I/O (headless)

Source: [cli/headless](https://antigravity.google/docs/cli/headless/).

- **stdout and stderr are split.** stdout carries only the response or events. "Diagnostics—errors, authentication prompts, progress, and permission notices—go to stderr."
- **`--output-format json`** prints one envelope at exit: `conversation_id, status, response, error?, duration_seconds, num_turns, usage{input_tokens,output_tokens,thinking_tokens,cache_read_tokens,total_tokens}`, plus `structured_output`/`json_schema` when `--json-schema` is set. The changelog adds `denied_actions` (since 1.1.27). The probe confirmed it (§9).
- **`--output-format stream-json`** prints NDJSON in this order: one `init` (`cwd`, `tools[]`, `permission_mode`, and `model`/`agent`/`json_schema` when set), then `step_update` events, then `result` (same shape as the json envelope). A `step_update` has `conversation_id, step_index, state, step_type, tool_name?, text_delta?, duration_seconds?, usage?, tool_info{name,parameters,output,error{type,message}}?, subagent_info{subagents[{type_name,role,conversation_id,log_uri,workspace_uris}]}?`. Documented `step_type` values are `user_input, agent_response, tool, checkpoint`, described as "observed" values, so the list is not closed. Documented `state` values are `ACTIVE` and `DONE`. **The probe also emitted `state:"ERROR"`**, which the docs do not list.
- **`--json-schema`** gives structured final output. The schema root must be `"type":"object"`, and since 1.2.14 anything else fails at startup with exit 1. The docs page still says primitive type names are accepted, so the docs lag the CLI here.
- **Status values:** `SUCCESS, ERROR, CANCELED, INTERRUPTED (e.g. SIGINT), INVALID, WAITING, RUNNING`.
- **Exit codes:** 0 on success. Non-zero on failure: 1 for invalid JSON, a missing `event`, an unknown model or a bad schema; 2 for `control_request`/`control_response` input or a CLI-handled slash command in stream mode. Since **1.2.6**, a turn that ends on an agent or model API failure exits **3** and prints a structured `AGY_ERROR: {...}` line on stderr with canonical status, HTTP/gRPC code, retryability and error ID. 1.2.10 fixed partial-stream failures that wrongly exited 0.
- **Versioning:** stream-json has no protocol version or handshake. The only forward-compatibility rule is that unknown input `event` names are skipped with a stderr warning.

## 3. Long-lived sessions, resume, cancellation

- **Long-lived process:** `--input-format stream-json` keeps one process and one conversation alive. Input lines look like `{"event":"user","message":{"content": "<string>" | [{"type":"text","text":…}]}}`. Each turn ends with its own `result`. In the `result`, `num_turns`, `usage` and `duration_seconds` are **cumulative**, while `response` covers only that turn. The docs say: "Wait until you receive the `result` event for the current prompt before writing the next one." That makes the protocol **strictly turn-serial, with no request ids**. Closing stdin ends the session after the current turn finishes. Only `text` content blocks are accepted. Any other block type ends the session with exit 1.
- **Resume:** `--continue`/`-c` resumes the most recent conversation, scoped to the cwd ([cli/conversations](https://antigravity.google/docs/cli/conversations/)); 1.2.1 fixed its fallback behaviour. `--conversation <id>` resumes by the `conversation_id` taken from `init` or `result`. Each resume is a new process. Conversations are scoped to the workspace or cwd. `/fork` exists only in the TUI.
- **Cancellation:** **signals only.** The stream-json input protocol has no cancel or interrupt message. `control_request` is explicitly rejected (ERROR, session ends, exit 2). The documented outcome is `INTERRUPTED (for example, SIGINT)`. Since 1.2.5, commands killed by an outside signal are recorded as canceled. Since 1.1.28, Ctrl+C in print mode exits non-zero. Since 1.2.9, headless runs terminate daemon background processes when they exit. `--print-timeout` caps a run, and on expiry it returns the partial output with exit 0 and a stderr warning (1.1.28). The docs say the default is 5m, as does `--help` on 1.2.5. The 1.2.6 changelog says the default changed to unlimited. These two sources conflict, so **always pass `--print-timeout` explicitly**. Whether SIGINT during a stream-json session cancels only the turn or kills the process is **not documented** (open question).

## 4. Permissions and approvals

- **No in-band approval channel in headless mode.** "There is no interactive prompt in headless mode, so tools that would normally ask for confirmation are handled by policy." A tool that needs approval is **soft-denied**: the run continues, exits 0, prints a notice on stderr, and lists the action in `denied_actions`. Since 1.2.15 the agent "respects the denial and no longer tries to work around it."
- **Policy levers:**
  1. `permissions.allow`/`deny`/`ask` rules in `~/.gemini/antigravity-cli/settings.json`, in the form `action(target)`, e.g. `command(git)`, `command(regex:…)`, `write_file(src/)` ([permissions](https://antigravity.google/docs/permissions/)). There is no CLI flag to point at a different settings file.
  2. `--dangerously-skip-permissions` sets permission mode to `always-proceed` (auto-approves everything, including MCP calls and URL reads since 1.1.21).
  3. `--mode accept-edits|plan`.
  4. `--sandbox`, and the Default preset's terminal sandbox: workspace and temp directories only, no network ([permissions](https://antigravity.google/docs/permissions/), [sandbox](https://antigravity.google/docs/sandbox/)).
  5. **`PreToolUse` hooks** ([hooks](https://antigravity.google/docs/hooks/)). A command receives `{toolCall{name,args}, stepIdx, conversationId, workspacePaths, transcriptPath, artifactDirectoryPath, modelName}` as JSON on stdin and returns `{decision: allow|deny|ask|force_ask|deny_unless_prior_grant, reason?, permissionOverrides?[]}`. The default timeout is 30 s. This is a **documented, synchronous, programmatic approval gate**. Öge could install a hook that forwards the decision to the Öge process, for example over a local socket. Hooks load from `.agents/hooks.json` in the workspace, from global `~/.gemini/config/hooks.json` or `settings.json`, or from an installed plugin. There is **no per-invocation flag**, so Öge would have to write into the workspace or global config, or ship a plugin. Other hook events are `PostToolUse`, `PreInvocation`, `PostInvocation` and `Stop`. `Stop` can return `decision:"continue"` to inject a message and keep the agent looping.
- Headless never stalls on implementation-plan approval. Since 1.1.28 it proceeds through plan review automatically.

## 5. ACP

- **`agy` itself has no ACP mode** in 1.2.5 (`--help`). Changelogs 1.1.21–1.2.16 never mention ACP or Agent Client Protocol.
- **Google ships a separate ACP server.** The ACP registry entry `antigravity-acp` reads: `"name": "Google Antigravity"`, `"authors": ["Google LLC"]`, `"license": "proprietary"`, `"license_url": "https://antigravity.google/terms"`, `"version": "1.3.0"`. Binaries are `https://dl.google.com/agy-extensions/releases/{macos,linux,windows}/agy-acp-server-1.3.0-<os>-<arch>.zip`, with `cmd` `./agy_acp_server.par` (macOS/Linux; Linux adds `--uid=`) or `./agy_acp_server.exe` (Windows). Registry history: added 2026-08-20 (`a3d294f`), then 1.1.1 on 2026-09-03 (`81bf71b`), 1.2.1 on 2026-09-23 (`3ee7f11`), and 1.3.0 on 2026-10-02 (`f6c0f4e`).
- This server is what the official **Zed** extension ("External Agents > Add > Install from Registry") and the **JetBrains** extension ("JetBrains AI > Settings > Agents") install ([ide/extensions/zed](https://antigravity.google/docs/ide/extensions/zed/), [jetbrains](https://antigravity.google/docs/ide/extensions/jetbrains/)). Both document `auth.type: "oauth-personal"` for "individual Google AI subscription plans (including Free, Pro, and Ultra)". [ide/extensions](https://antigravity.google/docs/ide/extensions/) says: "Antigravity uses a unified authentication system across all IDE extensions, the Antigravity CLI, and Antigravity 2.0."
- **Not verified here:** whether the ACP server shares `agy`'s keyring session or runs its own OAuth, which ACP capabilities it advertises (`session/load`, `session/request_permission`, `session/cancel`), and how it behaves outside Zed or JetBrains. It was not downloaded or run. It is a closed-source `.par` (Python zip-app) bundle and is recorded here only as existing.
- **Community ACP shims** (e.g. `jameslunardi/agy-agent-acp`, which "drives a warm language server over its local Connect API", and several `antigravity-acp` Bun wrappers) exist. They are not official, and at least one relies on undocumented internals. **Do not use them.**

## 6. Auth reuse and terms of service

- **Mechanism:** sign-in tokens live in the OS keyring (Keychain, Secret Service over D-Bus, or Windows Credential Manager) ([cli/install](https://antigravity.google/docs/cli/install/), [cli/troubleshooting](https://antigravity.google/docs/cli/troubleshooting/)). "Headless mode uses your cached credentials. Authenticate once with an interactive agy session first." In CI with no terminal, an unauthenticated run "exits with an authentication required error instead of hanging." On Linux, a locked keyring or a missing D-Bus session causes `secret keyring is locked`. Öge never needs to touch the tokens. Spawning `agy` is enough.
- **The API-key path** (`modelProvider` + `GEMINI_API_KEY`) exists but is out of scope under Öge's "never API keys" rule.
- **Terms (verbatim, [antigravity.google/terms](https://antigravity.google/terms)):** "You must not abuse, harm, interfere with, or disrupt the Service. This includes, but is not limited to, using the Service in connection with products not provided by us. Using third party software, tools, or services to access the Service (e.g. using OpenClaw with Antigravity OAuth) is a breach of this Agreement. Such actions may be grounds for suspension or termination of your Antigravity and/or Gemini CLI accounts."
- **FAQ (verbatim, [faq](https://antigravity.google/docs/faq/)):** "Why can't I use third-party software (such as Claude Code, OpenClaw, or OpenCode) with my Antigravity login? Using third-party software, tools, or services to access Antigravity is a violation of our Terms of Service … Such actions can result in suspension or termination of your account. To use a third-party coding agent with Gemini, we recommend using a Gemini Enterprise or Google AI Studio API key."
- **Against that,** the official headless page itself documents driving `agy` from a program (`subprocess.Popen(["agy", "--input-format", "stream-json", ...])`, "Drive a session programmatically") and from CI scripts, using cached account credentials.
- **Unverified community statement:** a forum reply on [discuss.ai.google.dev topic 183051](https://discuss.ai.google.dev/t/is-external-orchestration-of-antigravity-cli-headless-mode-supported-with-account-based-usage/183051) (2026-09-15) by user `Engineer760` says that spawning the official `agy` binary headless with cached Google credentials "is a supported workflow" consuming normal entitlements, and that extracting OAuth tokens or calling backend endpoints is unsupported. **The Discourse API marks this user `staff: false, moderator: false, admin: false`, with no group.** It is not a Google statement.
- **Reading:** the clearly prohibited case is a third-party client using Antigravity *OAuth* to reach the Service (OpenClaw, OpenCode). Launching the official binary as a child process is the pattern the docs themselves show. But the clause "using the Service in connection with products not provided by us" is broad enough that an orchestrator like Öge is **not clearly safe**, and the penalty is account suspension. There is no first-party statement that covers it.

## 7. Python SDK: ruled out

`google-antigravity` (PyPI, wraps a compiled runtime) authenticates only via `GEMINI_API_KEY`/`api_key`, Vertex express (API key) or Vertex ADC (`LocalAgentConfig` resolves to `GeminiAPIEndpoint(api_key)` or `VertexEndpoint`, per `google/antigravity/connections/local/local_connection_config.py` at `12f9a4c`, and the README). It has no Google-account login path, so it violates Öge's "never API keys" rule. It is also Python-only and in-process. It does have turn cancellation (`ChatResponse.cancel()`) and a hooks/policy system, which shows those concepts exist in the engine even though the CLI protocol does not expose them.

## 8. Classification

**Partial, via documented surfaces. No terminal scraping or undocumented APIs needed. Placement: post-MVP, gated on the ToS question.**

| Requirement | `agy` stream-json | Verdict |
|---|---|---|
| Structured I/O | NDJSON events, json envelope, `--json-schema` | **full** |
| Long-lived process / session | `--input-format stream-json`, turn-serial | **full** (no request ids, one turn at a time) |
| Resume | `--conversation <id>`, `-c` | **full** |
| Permissions / approvals | no in-band channel; pre-granted rules, `--dangerously-skip-permissions`, or `PreToolUse` hook | **partial**: policy plus a synchronous hook gate, but the hook must be installed into config files |
| Cancellation | signals only; `control_request` rejected | **partial** |
| ACP | not in `agy`; separate official `agy_acp_server` | **unverified**, possibly full (§5) |
| Programmatic control (model/effort/agent/mode) | per-process flags; slash commands rejected in stream mode | **partial** (no mid-session changes) |
| Auth reuse of Google login | spawning `agy` uses keyring credentials | **technically full; ToS-ambiguous** |

Full support would **not** need terminal scraping. The missing parts are in-protocol approval and cancel. The docs-sanctioned workarounds are hooks and signals. The only way to get an undocumented "fuller" API would be the local language-server Connect API that community shims use, and that is off-limits.

## 9. Probe (installed 1.2.5, 2026-10-04)

The probe ran in a throwaway scratch dir with no `--dangerously-skip-permissions`:

```
agy -p "Run the shell command: echo oge_probe. Then reply with exactly: ok" \
    --output-format stream-json --print-timeout 3m
```

- `init.permission_mode` was `"request-review"`. `init.tools` listed 58 tools, including `ask_permission`, `ask_custom_permission`, `ask_question`, `run_command`, `write_to_file`, `invoke_subagent`, `define_subagent`, `schedule`, `send_message` and `browser_*`.
- The tool step was emitted `ACTIVE`, then `state:"ERROR"` (undocumented state value) with `tool_info.error = {type:"TOOL_ERROR", message:"permission check failed for unsandboxed \"echo oge_probe\": user denied permission to run command: …"}`. The command was treated as *unsandboxed*. No sandbox run was attempted.
- `result.status` was `"SUCCESS"` with an **empty `response`**, `denied_actions:[{"action":"command","display_name":"RunCommand"}]`, and **exit code 0**. The agent did not reply "ok" after the denial.
- stderr: `jetski: no output produced — a tool required the "command" permission that headless mode cannot prompt for, so it was auto-denied. Add an allow-rule under permissions.allow in settings.json …`. The prefix `jetski` looks like an internal component name. It is recorded only as observed.
- **Takeaway:** a soft-denied run reports `SUCCESS` and exits 0. Öge must treat a non-empty `denied_actions` (and an empty `response`) as a failed or blocked step, not as success.

## Implications for Öge

1. **The adapter shape matches the "spawn the official CLI, speak NDJSON" family** (like Claude Code `-p --input-format stream-json`), not ACP. An Antigravity adapter needs: turn-serial send, waiting for `result`, cumulative→per-turn usage deltas, `conversation_id` capture for resume, a SIGINT-based cancel, and `denied_actions`-aware outcome mapping. The capability model (#18) must allow **no in-band approvals** and **signal-only cancel**. Antigravity is the concrete example of an adapter where those capabilities are false.
2. **Approvals are policy plus hooks.** If Öge wants interactive approvals, the documented route is a `PreToolUse` hook that calls back into Öge. That means Öge writes `.agents/hooks.json` into the workspace or uses global config or a plugin, which conflicts with "don't pollute the user's repo" unless the workspace is an Öge-managed worktree. Otherwise, run agents with a pre-computed allow-list per role, e.g. a read-only verifier.
3. **Auth boundary holds:** Öge spawns `agy` and never touches the keyring. Öge must surface "run `agy` once interactively to sign in" and handle the "authentication required" and locked-keyring failures.
4. **ToS is the gating risk.** The ToS and FAQ prohibit "third-party software … to access the Service" and use "in connection with products not provided by us". Spawning the official binary is the docs-shown pattern but is not explicitly blessed for orchestrators. Öge should not ship an Antigravity adapter that runs on the user's Google login without either a first-party clarification or an explicit user opt-in that names the risk.
5. **ACP near-free?** Possibly, through Google's own `agy_acp_server`, if #18/#19 settle on an ACP adapter. It still sits under the same ToS clause, and its auth and capabilities are unverified. A cheap prototype handshake (`initialize` only, no prompt) would settle the capability question.
6. **Post-MVP placement.** It is not near-free enough to pull into the MVP: the ToS risk, the unverified ACP server and the hook-file injection all argue against it. It is a good second NDJSON adapter for validating the abstraction later.
7. **Version-pin the adapter.** The CLI ships several releases a week (1.2.14→1.2.16 in four days). Docs and CLI disagree in places (`--print-timeout` default, `--json-schema` primitives). The stream has no protocol version. Öge should record `agy --version` per run and test against a pinned minimum (≥1.2.6 for `AGY_ERROR`/exit 3).

## Open questions

- Does Google consider an orchestrator spawning official `agy` headless with a personal Google login to be "third-party software accessing the Service"? This needs a first-party answer (support or Terms contact). The forum reply is unverified.
- What does `agy_acp_server` 1.3.0 advertise on `initialize`: auth methods, `loadSession`, `session/request_permission`, `session/cancel`? Does it reuse the `agy` keyring session or run its own OAuth? Is it supported outside Zed and JetBrains?
- In a stream-json session, does SIGINT cancel only the current turn (and keep the process alive) or end the process? Which `status` does it produce?
- Is the `step_update.state` enum (`ACTIVE`, `DONE`, `ERROR`, …) and the `step_type` set stable, and is there a published schema?
- Is there any supported per-invocation way to supply hooks and permission rules (a flag or env var) without writing to the workspace `.agents/` or global config? There is none in 1.2.5 `--help`.
- What is the `transcript.jsonl` record format? Only its path is documented (hooks `transcriptPath`).
- Was `--print-timeout` actually changed to unlimited by default? 1.2.6 says yes, but the docs and 1.2.5 `--help` say 5m.
