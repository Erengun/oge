# Codex app-server fixtures

Recorded JSON-RPC wire transcripts for the Codex adapter's contract tests (#60). They come from the app-server spike (#13, ADR-0018) and were re-redacted before landing on main (ADR-0017). Findings from the spike are in [docs/research/codex-app-server.md](../../../../docs/research/codex-app-server.md).

- Source: `prototypes/codex-app-server/golden/` at commit [`b23db14d3a1ce38db15787af25cff3b73a50f2ad`](https://github.com/Erengun/oge/tree/b23db14d3a1ce38db15787af25cff3b73a50f2ad/prototypes/codex-app-server) (wire transcripts added in `a00f28fef7d76e2251306ca521a8aca34eabd7a1`).
- CLI: `codex-cli` 0.155.1 (`codex app-server` over stdio), model `gpt-5.6-sol` with `reasoningEffort: low`, ChatGPT-account auth, macOS.

## Format

One JSON object per line: `{"t": seconds-since-process-start, "proc": label, "dir": "c2s" | "s2c", "msg": ...}`. `c2s` is client (Öge) to app-server, and `s2c` is app-server to client. `proc` labels one app-server process, and a new label means a new process. `msg` is the JSON-RPC message as sent or received, apart from the redactions below. Stderr tracing was not recorded.

| File | Source | What it covers |
|---|---|---|
| `init_account.jsonl` | `s0_readiness_probe.jsonl` | `initialize` / `initialized`, `remoteControl/status/changed`, `account/read`, `account/rateLimits/read`. |
| `threads_file_approval.jsonl` | `s2_…jsonl` lines 1–403 (proc A) | `thread/start` failing closed with `-32600` on a glob write rule, two concurrent threads with different permission profiles in one process, `item/fileChange/requestApproval` declined, `serverRequest/resolved`, `turn/diff/updated`, `turn/completed`. |
| `steer_interrupt_resume.jsonl` | `s2_…jsonl` lines 404–644 (proc B) | `thread/resume` in a new process (`deprecationNotice`, `thread/goal/cleared`), a file-change approval, `turn/steer`, `turn/interrupt`, and the `-32600 no active turn to steer` error. |
| `kill_resume.jsonl` | `s2_…jsonl` lines 645–769 (procs C1, C2) | A turn cut off by killing the app-server mid-command (C1), then `thread/resume` of the same thread in a fresh process (C2). |
| `held_approval.jsonl` | `s2_…jsonl` lines 770–875 (proc D) | A `fileChange` approval held unanswered for about 690 seconds, then declined. The turn still completes. |
| `command_approval.jsonl` | `s2f_command_approval.jsonl` (proc F) | `item/commandExecution/requestApproval` with `availableDecisions` and `proposedExecpolicyAmendment`, answered `decline`. |

`s2_…jsonl` is `s2_threads_approvals_steer_interrupt_kill_resume_hold.jsonl`. The other spike outputs (`s1_sandbox_profiles.txt`, `s1b_network.txt`, `s2e_env_policy.jsonl`, `s3_trust_entries.txt`) are findings, summarised in the research note, and stay at the source commit.

## What was redacted

The spike's own redactor had already replaced `email`, `installationId`, `serverName` and similar account keys with `"<REDACTED>"`, scrubbed JWTs, emails and IPs, dropped stderr, and rewritten the home directory to `~`. The re-redaction on top of that:

- **Paths.** `~/dev/oge-scratch/proto-codex` (the workspaces `ws-a`, `ws-b`, `runs/…` and `private/`) became `/home/user/scratch`, `~/.codex` became `/home/user/.codex`, and any other `~/` became `/home/user/`. Paths used as object keys (permission profiles) are rewritten too.
- **Account and machine identity.** Every `planType` became `"<redacted>"`. The `userAgent` OS build and terminal app became `Mac OS 15` and `terminal`.
- **Model prose.** Every non-empty `agentMessage.text` (in `item/completed`, `turn/completed` and resumed `thread.turns`) became `[model prose trimmed]`. Each run of `item/agentMessage/delta` keeps only its first delta, with `delta` set to the same placeholder, so the deltas still concatenate to the completed text. 533 deltas were dropped. Tests must not rely on delta counts.
- `OGE_PROBE_TOKEN` is a synthetic variable the spike set (`fake-not-a-secret`) to test environment policy. `private/heldout.txt` and `~/.ssh` show up only as denied command paths. No file contents from them were recorded.
- No credentials, emails or held-out project data were found. `internal/fixturescan` checks this on every `go test ./...`.

Every other field and frame is byte-for-byte as recorded (same key order, ASCII-escaped JSON). Thread, turn and item IDs are the originals.
