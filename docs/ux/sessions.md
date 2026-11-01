# Native sessions: your existing agent work on the Desk

Status: direction approved (user, 2026-11-01). Decision record: [ADR-0025](../adr/0025-native-sessions-are-discovered-not-owned.md). Builds on [`desk.md`](desk.md) (Projects P1–P2) and [`positioning.md`](../positioning.md) ("One Desk for everything you're building").

## Why

Sessions you start with plain `claude` or `codex` (the ones `claude --resume` and `codex resume` list) are invisible to the Desk today. That is another context boundary, exactly where Öge is supposed to remove them, and it makes onboarding "start everything through Öge first".

> **Principle: Öge finds your existing AI work before asking you to move it into Öge.**

## Model

Discovery and supervision are separate. A **Native session** is a conversation the agent itself stores and lists, whoever started it. Öge never owns its format, its id or its history.

| State | What it means | Where it shows |
|---|---|---|
| **Discovered** | Found in the agent's own store, in a project on the Desk | `/sessions` only (the catalog) |
| **On the Desk** | You added it explicitly | A rail row under its project: title and last activity, no process |
| **Managed** | You adopted it: Öge drives its turns through the agent's own resume | A Chat on the rail, with exact state |
| **Archived** | You took it off the Desk | Back to the catalog; the native session is untouched |

Nothing moves between states by itself. 300 Claude sessions and 150 Codex sessions stay in the catalog; the rail holds only what you put there (fan-out: Olsen & Wood 2004; peripheral display: Matthews 2004).

## What Öge can and cannot know

Only report what the provider integration can establish (the herdr lesson: screen-guessed state produced missed approvals and false "done", `docs/research/herdr-and-attention.md` §2).

| Fact | Discovered / On the Desk | Managed |
|---|---|---|
| Exists, resumable | yes, from the agent's store or API | yes |
| Project, cwd, provider, profile, title | yes | yes |
| Last activity | yes, from the newest record's time: `last active 4m`, `idle 3h` | yes |
| Running in another terminal | **no.** Shown only as recent activity, never as `●` | n/a: Öge runs it |
| Waiting for input | **no** (a later opt-in hook may report it exactly) | yes, `▲` |
| Done, failed | **no** | yes, `✓` / `✗` |

A Discovered or On-the-Desk row never shows `▲ ● ✓ ✗`. Its glyph is `◌` (outside Öge) with its age.

## Discovery

- **Codex: the API, not files.** App-server `thread/list` with the project's `cwd` filter returns id, name, preview, cwd, updatedAt and source. Only `cli`, `vscode` and `appServer` sources are listed; sub-agent threads are not. `status` there is relative to that app-server process, so `notLoaded` never means "nobody is running it".
- **Claude: metadata from the session files, nothing else.** Claude has no list command, so the adapter reads `projects/*/*.jsonl` under every Claude profile (`Profiles()`: `~/.claude`, `~/.claude-*`, `CLAUDE_CONFIG_DIR`). It reads a fixed allowlist of top-level keys (`sessionId`, `cwd`, `timestamp`, `gitBranch`, `isSidechain`, `type`, and a title record's title). Never message content, tool results, or any file outside `projects/`. Sidechains are skipped. The format is Claude's, versioned per adapter with fixture files; a shape it doesn't recognise lists the session by id and never breaks the Desk.
- **Projects:** a session belongs to the git repo of its recorded `cwd` (`gitRoot`, `repoKey`), not to the encoded directory name. Sessions whose cwd is not a repo on the Desk (open, pinned or recent) are left out by default.
- **Independent of `oge add`.** `oge add` registers projects and work; discovery finds what already exists in those projects without any registration.
- Titles and timestamps are held in memory for display. Öge does not copy, persist or index transcript text.

## Adoption (Managed)

Choosing a session and adopting it resumes **that exact session**: Claude `--resume <id>` with the session's own cwd and profile; Codex `thread/resume` with its thread id and cwd. Native id, project, cwd, provider and history are preserved; Öge reconstructs nothing.

- **One writer at a time.** If the session's file (Claude JSONL, Codex rollout `path`) was written within the last 2 minutes, adoption is refused: `written to 40s ago: close it there first`. No override.
- **Original settings (ADR-0025).** The session resumes with its own model, MCP servers, tools, permissions and cwd. Öge adds only one thing: permission prompts arrive as Gates where the agent supports that.
- **Honest coverage, visible.** One line in the Chat says what Öge can gate, from what the agent reports at start: `gated: what your settings would ask · allowed without Öge: your allow rules`, or `not gated: bypassPermissions`. Öge never presents an adopted session as fully supervised when its permissions bypass Öge.
- **In place, not isolated.** The session's earlier turns already edited your real working tree, so adoption cannot isolate it after the fact. `in place · not isolated` shows on its rail row and in its header for as long as it is open. No Workspace, no Candidate, no apply step. Checks run on demand against the working tree, and the Receipt's "Not covered" says the work was not isolated.
- To get Öge's full isolation for new work, start a new Chat (`/new`) in the project: it gets its own Workspace as today.

## Commands and rail

- `/sessions`: the catalog for the focused project, newest first: `◌ claude · fix the parser · last active 4m`. Keys: `a` add to Desk, Enter adopt, Esc close. Works at 80 columns. `/sessions all` lists every Desk project.
- A row On the Desk sits under its project in the rail as `◌ title · 4m`, folded with idle Chats. Clicking or Enter on it opens the adopt prompt.
- `/close` on a row archives it.
- Later (P2): search across sessions (Codex `searchTerm`; Claude by title), more providers, opt-in hooks for exact "waiting for input" from outside sessions.

## Build slices

Each is about 45 min / 150k tokens, fakes first (ADR-0017).

| # | Priority | Slice |
|---|---|---|
| S1 | P0 | **Discover Claude sessions**: the metadata reader over every profile, fixtures, project association. |
| S2 | P0 | **Discover Codex sessions**: `thread/list` through the adapter, a canned response in tests, project association. |
| S3 | P1 | **`/sessions` catalog and On the Desk rows**: the picker, `◌` rows with age under their project, archive. |
| S4 | P0 | **Adopt: resume the exact native session** with its original settings, in place, with the two-writer refusal and the coverage line. |
| S5 | P2 | Search; more providers; opt-in hooks for exact outside state. |

## Not doing

- Showing `▲ ● ✓ ✗` for a session Öge does not drive.
- Owning, converting or rewriting another agent's conversation format, or reconstructing a session's context in a new conversation.
- Turning every discovered session into a Chat or a rail row.
- Reading Claude or Codex credentials or anything outside the session index (ADR-0006).
