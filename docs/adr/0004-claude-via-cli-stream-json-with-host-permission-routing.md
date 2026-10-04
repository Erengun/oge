---
status: accepted (child-environment rule superseded in part by ADR-0006)
---

# Claude Code is driven over raw CLI stream-json, under an Öge-owned launch profile, with every permission decision routed to Öge

Öge talks to Claude Code by spawning the user's unmodified `claude` binary in `-p` bidirectional stream-json mode and speaking its control protocol directly. It does not depend on an Agent SDK or go through an ACP adapter ([#14](https://github.com/Erengun/oge/issues/14)). A reader may expect two other things: an SDK, and Claude's own fail-closed `dontAsk` mode for restricted roles. Neither is used, for the reasons below.

- **No SDK.** The TypeScript SDK is proprietary. The Python SDK (MIT) would add a Python runtime to Öge. Both are thin wrappers that spawn the same CLI with the same flags. Öge hand-writes types for the subset it consumes and feature-detects from `system/init.capabilities`.
- **Launch profile, not the user's config.** Every launch uses `--setting-sources=` (empty), `--strict-mcp-config` and `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`, and gets its policy from `--settings` JSON (enforced even with no setting sources). Öge injects the repo's CLAUDE.md itself when a role should see it. `--setting-sources user` or `project` would re-admit the user's plugins, MCP servers, hooks, memory and ancestor CLAUDE.md files. Öge checks `system/init` against the profile's expected envelope and fails or warns on anything new. `CLAUDE_CONFIG_DIR` is never changed, because changing it logs the user out.
- **`default` mode with stdio prompts and a host `PreToolUse` hook, not `dontAsk`.** `can_use_tool` alone misses sandbox-auto-allowed Bash. The host hook registered via `initialize` sees every tool call. Under `dontAsk` the host is never consulted, and the CLI's denial text invites the model to try other tools. In the spike it immediately rewrote a denied Edit as `sed -i` through Bash. With host routing, Öge authors every denial. This is also where a future independent decider agent plugs in (#29). Static allow rules and the sandbox stay underneath, and a dead Öge closes stdin, which cancels the prompt.

## Consequences

- Sandbox paths passed via `--settings` must be absolute. Relative paths there don't resolve to cwd and fail silently, and `allowWrite` does not reopen a path inside `denyWrite`. Verifier write scope therefore uses an absolute deny-list computed at stage start. The post-stage diff stays authoritative, because a Bash result can report success for a blocked write.
- Öge never parses Claude's transcript storage. It owns session ids, and it never asks for or handles an Anthropic API key. Authentication belongs to Claude.
- *Superseded in part by [ADR-0006](0006-oge-never-handles-provider-credentials.md):* #14's child-env rule "strip `CLAUDECODE`/`CLAUDE_CODE_*`" is replaced by ADR-0006's explicit session-marker strip list. A prefix strip would remove the cloud, OAuth-token and mTLS variables that Claude's own auth uses.

## What would reverse this

- Anthropic removes or restricts `-p` stream-json or the control protocol, or makes an SDK the only supported client.
- Hook callbacks or `can_use_tool` stop covering a tool class.
- `--settings` stops applying under empty setting sources.
