# Claude Code stream-json fixtures

Recorded wire transcripts for the Claude adapter's contract tests (#44). They come from the stream-json spike (#14, ADR-0004) and were re-redacted before landing on main (ADR-0017). Findings from the spike are in [docs/research/claude-stream-json.md](../../../../docs/research/claude-stream-json.md).

- Source: `prototypes/claude-stream-json/logs/` at commit [`ab056605bbb3aecf8adee63fb9a46b0948a507f4`](https://github.com/Erengun/oge/tree/ab056605bbb3aecf8adee63fb9a46b0948a507f4/prototypes/claude-stream-json) (recordings added in `d8e2feced54178049766576013411aeae69ab9b2`).
- CLI: `claude` 2.1.289 (`claude_code_version` in `system/init`), `--model haiku`, subscription auth (`apiKeySource: "none"`), macOS, recorded 2026-10-04.

## Format

One JSON object per line:

- `{"dir": "meta", "argv": [...], "cwd": ..., "isolated": ...}` opens each file. It holds the launch flags. `{"dir": "meta", "exit": N}` closes it, and `{"dir": "meta", "killed": true}` marks a SIGKILL (in `resume.ndjson`, which has two processes).
- `{"dir": "in", "msg": ...}` is host to CLI (stdin). `{"dir": "out", "msg": ...}` is CLI to host (stdout).
- `t` is a Unix timestamp in seconds.

`msg` is the stream-json frame exactly as the CLI sent or received it, apart from the redactions below. All four runs use the isolated launch profile (`--setting-sources=`, `--strict-mcp-config`).

| File | What it covers |
|---|---|
| `turns.ndjson` | `initialize` request/response, two turns in one process, `system/init` (envelope, `capabilities`, version), tool use with `tool_use_result.structuredPatch`, `can_use_tool` allow (turn 1) and deny with a custom message (turn 2, `is_error` tool result and `permission_denials[]`), `rate_limit_event`, `result success`. |
| `interrupt.ndjson` | `interrupt` control request mid-turn, receipt `{"still_queued": []}`, `result error_during_execution` with `terminal_reason: aborted_streaming`, then a normal second turn in the same process. |
| `hook_decider.ndjson` | Host `PreToolUse` hook registered through `initialize.hooks`, `hook_callback` requests for every tool call (Read, Edit, Write, sandboxed Bash), allow (`continue: true`) and deny (`permissionDecision: deny`) responses. Launch uses `--permission-mode default`, `--allowedTools Edit(tests/**)` and inline `--settings` sandbox JSON with an absolute `denyWrite` list. |
| `resume.ndjson` | `--session-id` run that plants a fact, SIGKILL, then a new process with `--resume` recalling it under the same `session_id`. |

The spike's other logs (`leak_*`, `verifier_*`, `widen_*`, `bare`) are research evidence, not contract frames. They stay at the source commit.

## What was redacted

The spike's own redactor had already replaced `account`, `pid`, `messaging_socket_path`, thinking `signature` and `user_output_styles_dir` with `"<redacted>"`, and rewritten the home directory to `~`. The re-redaction on top of that:

- **Paths.** `~/dev/oge-scratch/proto-claude` became `/home/user/project`, the Claude config dir became `/home/user/.claude`, the encoded project dir became `-home-user-project`, and any other `~/` became `/home/user/`. The mapping is the same everywhere, so `tool_use.input.file_path` still equals `tool_use_result.filePath`.
- **Installed-environment lists.** These are trimmed, but every key is kept so envelope checks still see the full shape:
  - `initialize` response: `commands` keeps `compact` and `init`, `agents` keeps `general-purpose`, and their descriptions became `[trimmed]`. `models` keeps the first two entries.
  - `system/init`: `slash_commands` and `skills` keep `compact` and `init`, `agents` is `["general-purpose"]`, and `plugins` is one `example-plugin@builtin` entry in place of the org-managed plugins.
- **Model prose.** Assistant `text` blocks longer than 40 characters, and long `result.result` strings, became `[model prose trimmed]`. Empty `thinking` blocks, tool inputs, tool results and the host's prompts are unchanged.
- No credentials, emails or held-out project data were found. `internal/fixturescan` checks this on every `go test ./...`.

Every other field and frame is byte-for-byte as recorded (same key order, `ensure_ascii=False` JSON). Message, request and session IDs are the originals.
