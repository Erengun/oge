# Held requests: does an agent expire a pending approval?

Question (Away slice 6, #363): when Öge holds a request from the agent for a long time, can it still be answered? `docs/ux/away.md` (Trust) needs this answered before any copy says that Gates and asks wait while you are away.

Verdict: neither protocol expired a held request in the cases tested. No expiry card and no deny-only timeout is built.

## Claude (stream-json control requests)

Only auto-mode asks are ever held. In every other mode the adapter decides a `can_use_tool` or hook callback at once, so there is nothing to hold.

- **Live probe, 2026-11-01.** `claude` 2.1.316, `--model haiku`, the isolated launch profile from `turns.ndjson`, `--permission-prompt-tool stdio`, `--permission-mode default`. A script asked for one `Edit`, received the `can_use_tool` control request at +12.8 s, waited 90 s, then answered `allow`.
  - claude sent no frame during the hold, applied the edit, and the turn ended `result success` at +103.9 s. The cost was about $0.001.
  - 90 s is past the 60 s "No response after 60s — continued without an answer" that [claude-code #73125](https://github.com/anthropics/claude-code/issues/73125) reports for AskUserQuestion. That report is about the interactive TUI. Over stream-json, the host answers the control request and no auto-continue happened.
- **Not probed live:** AskUserQuestion over stream-json, and holds longer than 90 s. Both reach the adapter as the same `can_use_tool` request and share its code path.
- **Contract tests** (`internal/agent/claude/held_test.go`): the multi-turn fake raises an Ask as a `can_use_tool` (and an AskUserQuestion). The test holds it 1.5 s with a short real wait, checks no event or write happens meanwhile, then answers. The turn proceeds, and a human deny after the hold is a human deny with Öge's usual reason, not a timeout.
- The adapter has no timer on a pending Ask. Only an interrupt withdraws it (`TestAutoModeAskWithdrawn`).

## Codex (app-server approvals)

- **Recording** (`internal/agent/codex/testdata/held_approval.jsonl`, `codex-cli` 0.155.1, see `docs/research/codex-app-server-spike.md` case D): an `item/fileChange/requestApproval` was left unanswered from t=10.5 s to t=700.5 s, about 690 s. The server sent only a `thread/status/changed` in between. Then the client declined, the server sent `serverRequest/resolved`, and the turn completed.
- `TestCodexDoesNotExpireAHeldApproval` asserts this from the recording: a hold of at least 600 s, nothing from the server during it, and the answer, resolution and turn completion in order.
- Öge's Codex adapter answers every approval at once (decline), so it holds none today. The recording shows what would happen if it did. Its own 30 s `requestTimeout` covers only Öge-to-codex calls, not server requests.
- Not probed live: a hold over 690 s, or a `commandExecution` approval.

## Real-world durations versus test durations

| Case | Real evidence | In tests |
|---|---|---|
| Claude can_use_tool | 90 s live | 1.5 s |
| Claude AskUserQuestion | not live | 1.5 s |
| Codex fileChange approval | 690 s recorded | recording read, no wait |

## Expiry

None found, so no expiry card. If a later CLI version adds one, the plan in `docs/ux/away.md` stands: a deny-only timeout just below the expiry, the needs-you push first, and the card `the approval expired at 14:02 · the agent was told no`.
