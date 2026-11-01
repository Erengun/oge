# Held requests: does an agent expire a pending approval?

Question (Away slice 6, #363): when Öge holds a request from the agent for a long time, can it still be answered? `docs/ux/away.md` (Trust) needs this answered before any copy says that Gates and asks wait while you are away.

Verdict: neither protocol expired a held request in the cases tested. The Claude recordings are default mode, not Chat's auto mode, so that case rests on the same frame, not on a recording. No expiry card and no deny-only timeout is built.

## Claude (stream-json control requests)

Only auto-mode asks are ever held. In every other mode the adapter decides a `can_use_tool` or hook callback at once, so there is nothing to hold.

- **Live recordings, 2026-11-01.** `claude` 2.1.316, `--model haiku`, `--setting-sources=`, `--permission-prompt-tool stdio`, `--permission-mode default`, `initialize.hooks: null`. A script reads stdout on its own thread and stamps each frame on receipt, so a frame sent during the hold would show. Redacted copies are in `internal/agent/claude/testdata/` (`held_ask.ndjson`, `held_ask_question.ndjson`, rows in its README).
  - `Edit`: the `can_use_tool` arrived at +0 s, was held 300 s, then answered `allow`. Between the two, claude sent nothing (only a `rate_limit_event` in the same millisecond as the ask). The edit applied, the tool result was not an error, and the turn ended `result success`.
  - `AskUserQuestion`: it reaches the host as an ordinary `can_use_tool` with `tool_name: AskUserQuestion`. Held 200 s, then allowed with its input unchanged. Nothing was sent in between, and the turn ended `result success`. The answers themselves were not supplied, so what claude does with a real answer after a long hold is not recorded.
  - Each cost about $0.001.
- **#73125.** [claude-code #73125](https://github.com/anthropics/claude-code/issues/73125) reports `AskUserQuestion` continuing with "No response after 60s" in the interactive TUI. Its comments tie that to an AFK idle timer, `CLAUDE_AFK_TIMEOUT_MS` (default 60000, reset by keypresses), and the issue is closed with the timeout made default-off. 2.1.316 is after that change, so the 200 s pass shows the default no longer expires a held ask over stream-json. It does not show the mechanism was TUI-only. If a future version turns the timer on by default, `held_ask_question.ndjson` is the case to record again. Öge does not set `CLAUDE_AFK_TIMEOUT_MS`; setting it high at launch would be a cheap guard, and `0` fires at once.
- **Not recorded: the path Öge actually holds.** Chat launches with `--permission-mode auto` and the PreToolUse hook registered. Two attempts to provoke an auto-mode ask (an `Edit` outside the working directory, a `rm -rf` of a scratch directory) were both allowed by the classifier without a `can_use_tool`, and in default mode with the hook registered the hook's `continue: true` meant no ask either. So the recordings use the same `can_use_tool` frame but not auto mode or the hook, and that they behave alike is an assumption. Attended run #114 is the place to check it. Holds longer than 300 s are not probed.
- **Contract tests** (`internal/agent/claude/held_test.go`): `TestRecordedHeldAsk` asserts from the two recordings a hold of at least 280 s and 180 s, no frame from claude in between, no error tool result and `result success` after. The multi-turn fake raises an Ask as a `can_use_tool` (and an AskUserQuestion); the other tests hold it 1.5 s with a short real wait, check no event or write happens meanwhile, then answer, and a human deny after the hold is a human deny with Öge's usual reason. The 1.5 s hold catches only a timer shorter than that in the adapter or fake; set `OGE_HELD_FOR=5m` to run a long one.
- The adapter has no timer on a pending Ask. Only an interrupt withdraws it (`TestAutoModeAskWithdrawn`).

## Codex (app-server approvals)

- **Recording** (`internal/agent/codex/testdata/held_approval.jsonl`, `codex-cli` 0.155.1, see `docs/research/codex-app-server-spike.md` case D): an `item/fileChange/requestApproval` was left unanswered from t=10.5 s to t=700.5 s, about 690 s. The server sent nothing in between (the `thread/status/changed` to waitingOnApproval came just before the request). Then the client declined, the fileChange item completed with `status: declined`, the server sent `serverRequest/resolved`, and the turn completed.
- `TestCodexDoesNotExpireAHeldApproval` asserts this from the recording: a hold of at least 600 s, nothing from the server during it (a status change would be flagged), and the answer, the declined item, the resolution and turn completion in order.
- Öge's Codex adapter answers every approval at once (decline), so it holds none today. The recording shows what would happen if it did. Its own 30 s `requestTimeout` covers only Öge-to-codex calls, not server requests.
- Not probed live: an accept after the hold (the recording only has a decline), a hold over 690 s, or a `commandExecution` approval.

## Real-world durations versus test durations

| Case | Real evidence | In tests |
|---|---|---|
| Claude can_use_tool | 300 s recorded (default mode) | 1.5 s, `OGE_HELD_FOR` for longer |
| Claude AskUserQuestion | 200 s recorded (default mode) | 1.5 s |
| Claude auto-mode ask | not recorded | 1.5 s |
| Codex fileChange approval | 690 s recorded | recording read, no wait |

## Expiry

None found, so no expiry card. If a later CLI version adds one, the plan in `docs/ux/away.md` stands: a deny-only timeout just below the expiry, the needs-you push first, and the card `the approval expired at 14:02 · the agent was told no`.
