---
status: accepted
---

# Antigravity is driven over CLI stream-json under Öge Launch profile with signal-based interrupt and soft-denial error detection

Öge talks to Google Antigravity by spawning the user's unmodified `agy` binary in stream-json mode (`--input-format stream-json --output-format stream-json`) and speaking its NDJSON protocol directly over stdio ([#7](https://github.com/Erengun/oge/issues/7), [docs/research/antigravity.md](../research/antigravity.md)). (Note: `-p`/`--print` is omitted because in `agy`, `-p` takes a prompt argument, whereas `--input-format stream-json` directly drives the multi-turn stdin/stdout protocol). It does not use the Python SDK or third-party ACP shims. Where the wire protocol differs from Claude Code or Codex, this ADR defines the Antigravity adapter's shape and guarantees. A reader may expect four other things:

- in-band permission approvals;
- protocol-level turn interruption;
- using `agy_acp_server`;
- trusting exit code 0 as success.

None of these was chosen, for the reasons below.

## Decisions

- **Official `agy` binary over stdio NDJSON.** `agy --input-format stream-json --output-format stream-json` keeps one process and conversation alive across turns. It is strictly turn-serial with no request IDs.
  - The Python SDK (`google-antigravity`) requires an API key or Vertex ADC, violating Öge's "never API keys" rule (ADR-0006).
  - ACP server (`agy_acp_server`) is proprietary and unverified outside Zed/JetBrains.
  - Community ACP shims rely on undocumented local language-server Connect APIs and are rejected.
- **Headless permission model: Launch profile + `--dangerously-skip-permissions` + `--sandbox`.**
  - Headless `agy` has no in-band interactive approval channel (`control_request`/`control_response` input causes an error and exit code 2). Any tool requiring approval is soft-denied by default.
  - Because Öge executes agents in isolated, disposable clones (ADR-0010), the adapter runs with `--dangerously-skip-permissions` to auto-approve tool execution, combined with `--sandbox` (restricting terminal commands to workspace and temporary directories with no network when `Network == "off"`).
  - This is disclosed as an Agent Limitation in the Run and Delivery Receipt: `implement (agy): headless runs operate under --dangerously-skip-permissions; in-band interactive approvals are not supported by the agy CLI`.
- **Soft-denial detection.**
  - A probe confirmed that when a tool is soft-denied, `agy` reports `status: "SUCCESS"`, an empty `response`, non-empty `denied_actions`, and exits 0.
  - Öge never treats a non-empty `denied_actions` with an empty response as success: the adapter maps it to a turn failure.
- **Signal-based interrupt.**
  - The stream-json protocol has no interrupt message. `Session.Interrupt()` sends `SIGINT` to the process group, which produces the documented `INTERRUPTED` status.
- **Structured error and authentication handling.**
  - Since `agy` 1.2.6, agent/model API failures exit with code 3 and print structured `AGY_ERROR: {...}` to stderr.
  - Unauthenticated runs or locked keyrings report on stderr; the adapter normalizes these to Infrastructure Stops (`Stop: "authentication_failed"`), never Attempt failures (ADR-0012).
- **Environment isolation.**
  - Child environment strips session markers (`AGY_*`, `GEMINI_CLI_*`, `AI_AGENT`, `TRACEPARENT`).
  - Filesystem isolation relies on `--sandbox` (workspace and temporary directories only, no network when `Network == "off"`) and disposable clones (ADR-0010); path-level deny lists are not exposed by agy CLI flags.
  - Go cache and run markers (`GOCACHE`, `OGE_RUN_ID`, `OGE_ROLE`) are injected.

## Consequences

- The adapter declares capability `CapInterrupt`, but not `CapHostRequests`.
- **Chat (amended 2026-10-19).** A Chat opens agy under `AutoPermissions` on this same Launch profile; nothing asks. agy can't deny reads by path, so the Chat's hard denies (Öge's private state, QA's and the cuts' directories) aren't enforced: the Agent Limitation says a Chat's implementer can read the held-out tests of earlier cuts. A `PreToolUse` hook in the Workspace would close this. agy can't run the verifier yet, so `--agent agy` leaves it to claude.
- The adapter works strictly with the installed `agy` CLI using cached Google credentials from the system keyring. Öge never touches tokens or credentials.
- Test suites replay recorded NDJSON fixtures via `fakeagy`, ensuring zero quota usage in CI (ADR-0017).
