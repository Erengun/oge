# AgentAPI: current upstream state and reusable parts

> Moved from branch `research/agentapi` at commit [`637d0500e6bd`](https://github.com/Erengun/oge/blob/637d0500e6bde597450a062d0101c60946077b7a/research/agentapi.md). Content unchanged.

Ticket: [Erengun/oge#2](https://github.com/Erengun/oge/issues/2) · Researched 2026-10-04

**Short answer:** `coder/agentapi` is **deprecated, unmaintained and archived**. The final commit, 2026-09-13, added the deprecation banner. Its core is terminal emulation: the agent runs in a PTY, the code diffs screen snapshots and sends keystrokes. Its "experimental ACP" mode is fitted *into* that screen model rather than replacing it. Öge would keep about **0 LOC** of it. The one reusable thing Coder built near it is the separate **`coder/acp-go-sdk`** (Apache-2.0, active). AgentAPI gives no reason to choose Go.

## Sources and versions

| Source | Version / SHA / date |
|---|---|
| `github.com/coder/agentapi` (cloned) | HEAD `7c468d5b25ec9d3d76c41313cf4e08904ff5968d`, 2026-09-13; last release tag `v0.12.2` (2026-05-27) |
| GitHub repo metadata (`gh repo view`) | `isArchived: true`, MIT, ~1.5k stars, 138 forks, created 2025-04-07 |
| `coder/coder#21133` "Tasks should default to ACP instead of AgentAPI" | closed NOT_PLANNED 2026-09-13 |
| `coder/registry` `claude-code` module | commit `124d05f` (2026-04-24) "strip boundary, agentapi, tasks, tools (#861)" |
| `github.com/coder/acp-go-sdk` | latest release v0.13.5 (2026-06-02), not archived, not a fork, Apache-2.0, created 2025-09-26 |
| Coder Agents docs, https://coder.com/docs/ai-coder/agents | fetched 2026-10-04 |
| Local binary | `agentapi` not installed, so it was not probed |

## 1. Status: dead upstream

- README (at `7c468d5`): "AgentAPI is deprecated and no longer maintained. We recommend using Coder Agents … or the modules in the Coder Registry that no longer use AgentAPI." The repo is archived on GitHub.
- Activity: 120 commits in 2025-04, then a steady decline (29 → 22 → 18 → 18 → 14 per month through 2025-11), then single digits. Only 5 commits from 2026-04 to 2026-09. Since 2025-10, nearly all commits come from two Coder engineers (`35C4n0r` 44, Cian Johnston 15).
- The successor direction **is not ACP in AgentAPI**. `coder/coder#21133` proposed moving Tasks to ACP, and it was closed NOT_PLANNED on 2026-09-13: "tasks is now deprecated in favor of Coder Agents". Coder Agents is "a standalone agent written in Go" whose loop runs in Coder's control plane and calls LLM provider APIs directly. The docs say it "is not a wrapper around third-party agent tools like Claude Code or Codex". So Coder dropped the wrap-existing-CLIs approach entirely, in both its PTY and ACP forms, and moved to API keys. That is the opposite of Öge's premise.
- Coder's own registry `claude-code` module dropped AgentAPI on 2026-04-24 (`coder/registry@124d05f`).
- Open issues: 37 open, 5 open PRs. Issues #229–#233 were filed in the same second with generic titles, which looks like spam, so the count overstates real demand. The real signal:
  - **#207 "Claude Code 2.1.83 breaks terminal capture"** (2026-03-25, still open). Users pinned Claude to 2.1.78. This is direct evidence that screen-scraping breaks whenever a vendor updates its TUI.
  - #209: `Send()` blocks forever while `Status()` reports stable. This is a readiness-heuristic bug.
  - #235: the v0.12.2 binary was built with a Go toolchain that has CVEs. It will never be rebuilt.
  - #202: "the primary work of this repository seems to align closely with ACP".

## 2. Architecture: how much is terminal vs structured

The core abstraction is `screentracker.AgentIO` (`lib/screentracker/conversation.go`):

```go
type AgentIO interface {
    Write(data []byte) (int, error)
    ReadScreen() string
}
```

Every transport, ACP included, is modeled as "write bytes, read a screen string". `Conversation` (Messages/Send/Start/Status/Text/SaveState) and `Emitter` (messages/status/screen/error) sit on top.

**PTY path (the default and main path).**
- `lib/termexec` starts the agent in an `ActiveState/termtest/xpty` virtual terminal with `TERM=vt100`.
- It reads output by reaching into an **unexported xpty field via reflection/unsafe** (`util.GetUnexportedField(xp, "pp")`). The code comment says this "may break if xpty changes".
- `ReadScreen` waits for the screen to be stable for 16 ms (a "vsync").
- `lib/screentracker/pty_conversation.go` takes snapshots, decides a turn is over when the screen stops changing, diffs screens (`diff.go`) to extract the agent message, and persists state to JSON.
- `lib/msgfmt` holds per-agent heuristics: strip the echoed user input, remove each agent's input box (Codex/OpenCode/Amp variants), detect "ready for initial prompt" from screen text, and remove `coder_report_task` tool-call noise for Claude and Codex.
- Input to Claude is wrapped in bracketed-paste escapes (`lib/httpapi/claude.go`). A raw-keystroke message type exists for TUI selection prompts.

**ACP path (`--experimental-acp`, added in v0.12.0).** `x/acpio` (524 prod LOC) uses `coder/acp-go-sdk` v0.6.3. `SetupACP` starts the agent with stdio pipes, then calls `Initialize` with **empty ClientCapabilities**, then `NewSession(cwd, no MCP servers)`. After that, `ACPAgentIO`:
- implements the same `AgentIO`. `Write` strips paste escapes and the old `x\b` Claude hack, then sends a `Prompt`.
- accumulates `AgentMessageChunk` text into a buffer that `ReadScreen` returns.
- **flattens tool calls to text** (`"[Tool: %s] %s"`, `"[Tool Status: %s]"`).
- **auto-approves every `RequestPermission`** with `OptionId: "allow"` (commented "Phase 1").
- stubs every fs/terminal client callback with empty responses.
- does not support state persistence (the server rejects that combination). It is not mentioned in the README.

ACP was a late, experimental side path, adapted down to the screen model. AgentAPI throws away the structured data (tool calls, permission requests, stop reasons) that a harness like Öge needs.

**HTTP surface** (`lib/httpapi`, huma v2 + chi + go-sse; `openapi.json`):
- `GET /status`, `GET /messages`, `POST /message`, `POST /upload`
- SSE `GET /events` (`message_update`, `status_change`, `screen_update`, `agent_error`) and `/internal/screen`
- Host-header and CORS allow-lists. No authentication.
- An embedded Next.js chat UI at `/chat` (`chat/`, about 2,000 TS/TSX LOC, built with Bun).

## 3. Package inventory and verdict for Öge

Line counts are from `wc -l` (non-test / test) at `7c468d5`.

| Package | Prod / test LOC | What it is | Öge verdict |
|---|---|---|---|
| `lib/screentracker` | 978 / 1815 | PTY conversation: snapshots, stability, screen diff, state save | **Delete.** This is terminal scraping. |
| `lib/msgfmt` | 628 / 306 | Per-agent TUI text heuristics | **Delete.** It breaks on every TUI change (see #207). |
| `lib/termexec` | 192 / 0 | xpty spawn + unexported-field hack | **Delete.** |
| `cmd/attach` | 276 / 0 | Bubbletea client that attaches to the screen stream | **Delete.** |
| `lib/httpapi` | 1335 / 1193 | HTTP+SSE server, event emitter, Claude paste formatting, process setup | **Delete.** Öge is a local CLI and remote control is out of scope. `SetupACP`'s shutdown order (SIGTERM, close pipes, Kill after 5 s) is a pattern to rewrite, not code to copy. |
| `x/acpio` | 524 / 853 | ACP client adapted into `AgentIO` | **Delete.** Use an ACP SDK directly with real permission, tool-call and fs handling. |
| `cmd/server` | 562 / 801 | Cobra+Viper CLI, agent-type aliases, PID file, signals (unix/windows) | **Rewrite (trivial).** The Windows `isProcessRunning` is a stub that always returns false. |
| `e2e` | 360 / 558 | Fake scripted agents (`echo.go` PTY, `acp_echo.go` ACP) + JSON scripts | **Imitate the idea, don't copy the code.** See §5. |
| `lib/util`, `lib/logctx`, `internal/version`, root | 163 / 0 | helpers | Delete. |
| `chat/` | ~2,000 TS | Web chat UI | **Delete.** A web dashboard is out of scope. |

Totals: about **5,050 prod Go LOC** and about 5,500 test Go LOC, plus about 2,000 TS. Roughly 2,100 prod LOC (screentracker, msgfmt, termexec, attach) is pure terminal emulation. Most of the rest serves the HTTP/remote-chat shape. **Öge would keep about 0 LOC.** Everything that is useful in principle (subprocess-over-stdio, graceful kill, Cobra CLI) is easy to write in either Go or Rust.

**Release and build:** no goreleaser. Releases use `release.sh` plus `.github/workflows/release.yml`, which loops `make build` over a matrix with `CGO_ENABLED=0`: linux amd64/arm64, darwin amd64/arm64, windows amd64. Releases are hand-promoted from pre-release (`MAINTAINERS.md`). Go 1.24.11. golangci-lint, gofumpt and actionlint are pinned as `go tool` deps.

**Cross-platform:** builds for Windows (the PTY layer goes through `termtest/conpty`). PID liveness checks and SIGUSR1 save-state are unix-only. `SetupACP` sends `syscall.SIGTERM`, which does not work as a graceful signal on Windows.

## 4. License / NOTICE

MIT, "Copyright (c) 2025 Coder Technologies, Inc." There is no NOTICE file. If code were copied, Öge (Apache-2.0) would need to keep the MIT copyright and permission notice in the copied files or a third-party notices file. Under the verdict above nothing is copied, so there is no obligation. `coder/acp-go-sdk` is Apache-2.0. If Öge depends on it, it carries normal Apache-2.0 dependency attribution.

## 5. Things AgentAPI does that Öge would otherwise rebuild

- **A fake scripted agent for tests** (`e2e/echo.go`, `e2e/acp_echo.go`). A fake agent binary reads a JSON script (`expectMessage`, `thinkDurationMS`, `responseMessage`). The harness talks to it like a real agent, so tests use no quota. `acp_echo.go` implements the ACP agent side with acp-go-sdk. This is the most useful *idea* in the repo for Öge's testing-strategy fog item: fake protocol agents and recorded fixtures instead of live sessions. Öge would write its own, with richer scripts (tool calls, permission requests, failures).
- **Process lifecycle details:** SIGTERM, then close pipes, then Kill after a grace period. Also a deterministic clock (`coder/quartz`) and goroutine-leak checks (`goleak`) in tests. These are good practices but small.
- **A catalogue of TUI failure modes** (input echo, input boxes, readiness detection, paste vs keystroke). This is useful only as evidence for *why not* to scrape terminals.

Nothing here saves Öge real implementation work.

## Implications for Öge

- **Go vs Rust:** AgentAPI adds no weight for Go. No code is kept, and the project is archived. The Go-side asset is **`coder/acp-go-sdk`**, a separate, active Apache-2.0 library, which the ACP research ticket should evaluate against the official ACP SDKs (including Rust).
- **Repo strategy:** do not fork AgentAPI. Start fresh. The "Fork / license attribution" fog item mostly disappears for AgentAPI. It stays open only for whichever ACP SDK is chosen.
- **Validation of the thesis:** two things point the same way. Upstream's own record shows PTY scraping breaking on vendor TUI updates (#207, pinned Claude versions). And the "ACP-as-screen" shortcut drops exactly the data Öge needs: tool calls, permission decisions, stop reasons. Öge's adapters should expose structured events and handle permissions explicitly, never auto-allow by default.
- **Market signal:** Coder moved away from wrapping vendor CLIs toward its own API-key agent loop. Nobody in that space now offers a maintained "drive vendor CLIs via subscriptions" harness. That is an opening for Öge, but also a warning that vendor CLIs change fast. Öge should depend on official protocols (ACP, Codex app-server, Claude Agent SDK/stream-json) with recorded-fixture tests to catch drift.

## Open questions

- Is `coder/acp-go-sdk` still maintained now that Coder has dropped ACP for Tasks? The last release was 2026-06-02, and the last push 2026-06-05. Compare it with the official ACP SDKs in the ACP ticket.
- Do any active forks of AgentAPI exist with a structured-transport direction? Not investigated. There are 138 forks, and issue #193 mentions "API enhancements from fork".
- What were the actual reasons Coder deprecated Tasks/AgentAPI (cost, reliability, vendor ToS)? The public docs only describe the replacement.
