# Claude Code stream-json spike: results

> Condensed from the spike README on branch `prototype/claude-stream-json` at commit [`ab056605bbb3`](https://github.com/Erengun/oge/tree/ab056605bbb3aecf8adee63fb9a46b0948a507f4/prototypes/claude-stream-json) (#14). The decisions it drove are in [ADR-0004](../adr/0004-claude-via-cli-stream-json-with-host-permission-routing.md). The re-redacted recordings used as test fixtures are in [`internal/agent/claude/testdata/`](../../internal/agent/claude/testdata/).

The spike was a dependency-free Python client that spoke stream-json to the CLI directly, with no SDK. Each scenario ran against a plain scratch repository outside `/tmp`, because `/tmp` is writable under agent sandboxes and would invalidate the write-scope tests. The host took a byte snapshot diff of the repository after each run, and that diff is the authoritative "what was written" check.

Environment: `claude` 2.1.289, `--model haiku`, subscription auth (`apiKeySource:"none"`), macOS 15, 2026-10-04. The child env dropped `CLAUDECODE`, `CLAUDE_CODE_*`, `CLAUDE_PID` and `CLAUDE_EFFORT`, because the spike ran from inside a Claude Code session. `CLAUDE_CONFIG_DIR` was passed through unchanged.

The Log column names the spike's log file. `turns`, `interrupt`, `hook_decider` and `resume` are committed as fixtures. The others stay at the source commit.

## Results

| # | Behaviour | Result | Log |
|---|---|---|---|
| 1 | `initialize` control request → response | PASS. The response has `account` (PII), `pid`, `models`, `commands`... Its `capabilities` field is just `["ui_surface_v1"]`; the full list (`interrupt_receipt_v1`, `msg_lifecycle_v1`, ...) is only in `system/init`. | turns |
| 2 | Two turns in one process | PASS. `result_index` 0 then 1, same `session_id`. `system/init` is re-emitted every turn. | turns |
| 3 | Structured file-edit events | PASS. `Edit`/`Write` tool results carry `tool_use_result.structuredPatch`. `Write` of a new file has `structuredPatch: []`. | turns, verifier_* |
| 4 | `can_use_tool` allow and deny | PASS. Allow ran the Edit. Deny with a custom message became `tool_result is_error` with our text, plus `result.permission_denials[]`, and the turn still completed `success`. | turns |
| 5 | Interrupt mid-turn | PASS. Receipt `{"still_queued":[]}`, then `result error_during_execution` / `terminal_reason:aborted_streaming`. **The session stays usable**: the next user turn answered normally and the exit code was **0**. #5 saw exit 1 when the interrupted turn was the last one, which fits this run, but closing after an interrupted last turn wasn't re-tested here. | interrupt |
| 6 | Resume by id after SIGKILL | PASS. `--session-id <uuid>`, plant a fact, SIGKILL, then a new process with `--resume <uuid>` recalls it with the same `session_id`. `result_index` restarts at 0 in the new process. | resume |
| 7 | Startup `rate_limit_event` | Present in every run, after the first API call (`status:allowed`, `rateLimitType:five_hour`). It's per-request, not strictly "startup". | all |
| 8 | Leakage, default `-p` | 13 plugins, 9 MCP servers (7 claude.ai connectors), 99 skills, 135 commands, a SessionStart hook, auto-memory path, the repo `CLAUDE.md` canary, **and `~/.claude/CLAUDE.md` read as an ancestor project memory** (because this user's config dir is not `~/.claude`). About 16.9k input tokens. `--tools ""` doesn't remove MCP tools (22 tools left). | leak_default |
| 9 | Leakage, isolated (`--setting-sources=` + `--strict-mcp-config` + `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`) | 0 MCP, 0 hooks, no memory paths, no canary, no graphify. What's left: 4 org-managed `cc-plugin-*` plugins, 19 skills, 54 commands. About 3.5k input tokens. | leak_isolated |
| 10 | `--setting-sources project` | Brings back the repo `CLAUDE.md` **and** the ancestor `~/.claude/CLAUDE.md`, plus auto memory. | leak_project |
| 11 | `--setting-sources user` | Brings back **everything** from #8 (plugins, MCP, hooks, memory). | verifier_user_sources |
| 12 | `--settings <json>` applies with `--setting-sources=` empty | PASS. The sandbox and denyWrite from `--settings` are enforced with no setting sources at all. | verifier_denylist |
| 13 | Edit/Write to `src/` under `dontAsk` + `--allowedTools Edit(tests/**)` | BLOCKED (`system/permission_denied`, `decision_reason_type: mode`). `Write` to a new `tests/` file is ALLOWED by the `Edit(tests/**)` rule. | verifier_* |
| 14 | Sandbox, relative paths (`denyWrite:["."]`, `allowWrite:["./tests"]`) in inline `--settings` | **FAIL, silently.** `.` did not resolve to cwd. Bash writes to `src/` landed. What got denied was the CLI's own `/tmp/claude-501/cwd-*` tracking file, so every Bash call reported exit 1 even though the command succeeded. The docs only define `./` for project and user settings files. | verifier_empty_sources (2 runs) |
| 15 | Sandbox, absolute allow-list (`denyWrite:[repo]`, `allowWrite:[repo/tests]`) | Bash to `src/` blocked, but Bash to `tests/` was **also blocked**: `allowWrite` doesn't reopen a path inside `denyWrite`. The current docs only describe reopening for reads. | verifier_abs |
| 16 | Sandbox, absolute deny-list of every non-test top-level entry | PASS. Bash `src/` gets `operation not permitted` and `tests/` is writable. FS diff = tests only. **The blocked write came back `is_error:false`**, because the trailing `echo rc=$?` succeeded. A Bash tool result can't tell you whether the sandbox blocked something; only the FS diff can. A new top-level file in cwd would *not* be blocked. | verifier_denylist |
| 17 | cwd = `<repo>/tests`, sandbox on, no lists | Write scope PASS: the default "cwd writable" *is* the test scope, and FS diff = tests only. The Read of `src/calc.py` (outside cwd) worked under `dontAsk`. The `Read(/<abs>/**)` rule passed here is project-relative syntax, so the read was probably allowed by default, not by that rule. This only fits repos whose tests sit in one directory, not `**/*_test.go`. Run before step 6 (new test file) was added. | verifier_cwd_tests |
| 18 | Repo `.claude/settings.json` allowing `Edit(src/**)` | It doesn't widen under empty sources (not loaded) or under `user,project` (`-p` doesn't apply project allow rules for an untrusted dir). It only shows the untrusted-dir case; a worktree under a repo the user trusted interactively wasn't tested. This doesn't matter with empty sources. | widen_* |
| 19 | Decider via `can_use_tool` (default mode, stdio) | Rule misses for Edit/Write reach the host, and the host's deny is honoured. **Sandboxed Bash is auto-allowed and never reaches `can_use_tool`.** | verifier_decider |
| 20 | Decider via host `PreToolUse` hook (`initialize.hooks` → `hook_callback`) | PASS. It sees **every** tool call (Read, Edit, Write, sandboxed Bash), can deny each one with a reason, and needs no settings file. Under `dontAsk`, `can_use_tool` never fires. | hook_decider |
| 21 | `--bare` with subscription only | `Not logged in · Please run /login`, rc 1, zero tokens. There's no opt-out flag in 2.1.289. | bare |
| 22 | Model behaviour under denial | The `dontAsk` denial text tells the model it "*may* attempt to accomplish this action using other tools", and Haiku immediately tried `sed -i` through Bash. Only the sandbox stopped it. | verifier_* |

Every write-scope verdict above comes from the host's file snapshot diff, not from what the model said.
In row 14 the model reported "ok", and the files had in fact changed.
