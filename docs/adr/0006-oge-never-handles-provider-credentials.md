---
status: accepted (Codex tool-shell scrubbing and config trust-entry side effect amended in part by ADR-0018)
---

# Öge controls the agent process, never the provider credential

Öge drives the coding agents the user already has installed, authenticated however the user set them up: a subscription login, an API key, or a cloud provider. Authentication belongs to the agent. Öge has no key setting, no login flow and no per-role auth choice ([#19](https://github.com/Erengun/oge/issues/19)). A reader might expect Öge to manage keys, or to supply tokens to the agent (for example Codex's `chatgptAuthTokens` mode). It does neither. Either would turn Öge into a credential intermediary, which provider terms restrict (#5, #4) and which the positioning rules out (#30).

**Öge never:**
- reads credential values into application logic;
- persists, logs, transforms or copies credential values or credential stores;
- moves credential or config stores (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `~/.pi`);
- invokes login, logout or credential-printing commands or protocol methods (`codex login/logout`, `account/login/*`, `account/logout`, `claude auth login`, `setup-token`, `pi auth print-*`, ACP `authenticate`);
- forces a token refresh (for example, no `account/read{refreshToken:true}`);
- stores account identity such as email or organisation.

**Öge may:**
- pass the user's ambient environment to the official agent process unchanged, because existing agent authentication and configuration may depend on it;
- check whether known auth environment variables are present, by name, for doctor diagnostics. It never reads their values.
- store only non-secret metadata, such as auth mode and plan tier.

An agent may refresh its own token as a side effect of being used. That is the agent's behaviour, not Öge's.

## Consequences

- **Agent child environment.** These are explicit lists, versioned with each adapter's last-tested agent version and checked in CI.
  - *Pass:* everything by default.
  - *Strip:* named session markers only, never a prefix. Claude: `CLAUDECODE`, `CLAUDE_CODE_CHILD_SESSION`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_SESSION_ATTENDED`, `CLAUDE_PID`, `CLAUDE_EFFORT`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_EXECPATH`, `CLAUDE_CODE_MESSAGING_SOCKET`, `CLAUDE_CODE_MESSAGING_TOKEN`, `CLAUDE_CODE_BRIDGE_SESSION_ID`, `CLAUDE_CODE_SSE_PORT`, `CLAUDE_PROJECT_DIR`, `CLAUDE_ENV_FILE`, `CLAUDE_CODE_REMOTE*`, `AI_AGENT`, `TRACEPARENT`. Codex: `CODEX_THREAD_ID`, `CODEX_SESSION_ID`, `CODEX_VERSION`, `CODEX_PERMISSION_PROFILE`, `CODEX_SANDBOX`, `CODEX_SANDBOX_NETWORK_DISABLED`, `CODEX_CI`.
  - *Never set, pass through untouched:* config homes and every auth or provider variable (`ANTHROPIC_*`, `CLAUDE_CODE_USE_*`, `CLAUDE_CODE_OAUTH_*`, `CLAUDE_CODE_CLIENT_*`, `CODEX_ACCESS_TOKEN`, AWS, Vertex and GCP variables).
  - *Öge sets:* only launch-profile isolation knobs (today `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`), plus `OGE_RUN_ID` and `OGE_ROLE`.
  - This **replaces** the broad "strip `CLAUDECODE`/`CLAUDE_CODE_*`" rule from #14 / ADR-0004. That rule would also strip `CLAUDE_CODE_USE_BEDROCK`/`VERTEX`/`FOUNDRY`, `CLAUDE_CODE_OAUTH_TOKEN` and the mTLS variables, which breaks cloud and API-key setups.
- **Identification is truthful.** Codex gets `clientInfo {name:"oge"}`. Claude's `CLAUDE_CODE_ENTRYPOINT` is stripped and left unset, so the CLI reports `sdk-cli`. Öge never imitates `cli` or an IDE.
- **Evidence commands** that Öge runs itself get known credential variables scrubbed by default. A project can opt in to specific variables by name only. Values are never stored, and the Evidence records which secret variable names were exposed. Öge claims scrubbing of an agent's *own* tool shells only where the adapter proves it is enforceable without breaking the agent's auth. Where it isn't, the exposure is reported as a launch-profile limitation.
  - *Amended in part by [ADR-0018](0018-codex-adapter-native-app-server-one-per-run.md) ([#13](https://github.com/Erengun/oge/issues/13)):* in Codex app-server turns, scrubbing works only with `features.shell_snapshot=false` set per thread. Verifier and reviewer threads use that setting. The implementer keeps the default behaviour, and its shell credential exposure is reported as a launch-profile limitation. App-server also adds trust entries to `~/.codex/config.toml` for writable Workspaces. That is a side effect owned by Codex: Öge never writes or cleans those entries, and `oge doctor` may report them.
- **Readiness** comes from status-only, zero-quota probes. Codex: app-server `initialize` → `account/read{refreshToken:false}` → `account/rateLimits/read`. Claude: `claude auth status` with PII dropped (presence only), plus an opt-in `oge doctor --live` turn. Auth or quota failure during a run is an **Infrastructure stop**. Öge never re-authenticates automatically.
- **Hosted and commercial use.** The current architecture does not centrally run model inference on users' subscription credentials, and does not operate a hosted credential proxy. Any future hosted or commercial mode needs a fresh provider-terms and authentication-design review, and must use provider-supported mechanisms.

## What would reverse this

- A vendor requires the host to supply or refresh tokens.
- A vendor forbids third-party hosts from driving its unmodified binary on the user's own login. That agent would then become API-key-only in docs and doctor.
- A real need for per-role auth selection appears.
