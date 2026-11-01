---
status: accepted
---

# Native sessions are discovered, not owned; adoption resumes them in place

The Desk shows the Claude and Codex sessions a developer already has, including ones started outside Öge, and can resume one exactly as the agent stores it. Öge separates **discovery** (a read-only catalog) from **supervision** (a Chat Öge drives). Spec: [`docs/ux/sessions.md`](../ux/sessions.md).

## Context

- The Desk's identity is "one Desk for everything you're building" (positioning, 2026-11-01). Sessions started with plain `claude` or `codex` were invisible to it, which is another context boundary and forces "start everything through Öge" before Öge is useful (user, 2026-11-01).
- Codex exposes its sessions through app-server `thread/list` (id, name, preview, cwd, updatedAt, source). Claude has no list command; its sessions are JSONL files under each config dir's `projects/`.
- Öge's own **Session** (GLOSSARY) is Öge-owned and maps the agent's id. A native session is the reverse: the agent owns it and Öge must not.
- herdr shows that state guessed from outside an agent is wrong exactly when it matters (#5546, #5490, #1217, #5024).

## Decisions

- **A Native session is the agent's.** Öge never converts, rewrites or reconstructs one. Adoption preserves its native id, project, cwd, provider, profile and history, and resumes it through the agent's own mechanism (Claude `--resume <id>`, Codex `thread/resume`).
- **Discovery reads indexes, not conversations.** Codex through `thread/list`. Claude through a fixed allowlist of top-level JSONL keys (`sessionId`, `cwd`, `timestamp`, `gitBranch`, `isSidechain`, `type`, the title record's title) under each profile's `projects/`, nothing else in the config dir. No message content, no tool results, nothing persisted. This is consistent with ADR-0006: no credential is read, copied or moved, and no config store is touched beyond reading that index.
- **States are only what can be established.** A session Öge does not drive shows existence, project, title and last activity, as `◌` with its age. It never shows `▲ ● ✓ ✗`. Exact state begins at adoption.
- **Nothing enters the Desk by itself.** Discovered sessions live in a catalog (`/sessions`). Adding to the rail, adopting and archiving are explicit acts (desk.md U3 and I3 apply: committing attention is intentional).
- **One writer at a time.** Adoption is refused while the session's file was written within the last 2 minutes. Öge does not assume how Claude or Codex behave with two concurrent writers.
- **An adopted session runs in place.** Its earlier turns already changed the real working tree; isolation cannot be added after the fact. It has no Workspace, Candidate or apply step, Checks run on demand against the working tree, and the Receipt's "Not covered" says the work was not isolated. Full isolation stays the default for new work (`/new`, ADR-0010).

## Adopted sessions resume with their original settings (user, 2026-11-01)

Adoption is continuation, not migration. An adopted session resumes with its native launch settings: provider configuration, model, MCP servers, tools, permissions and working directory, as the person ran it. Öge does not move an existing conversation into its own Launch profile, because changing tools or behaviour mid-conversation breaks what earlier turns assumed.

Three guarantees:

1. **Native continuity.** Öge never silently rewrites the session's environment. The only thing it adds is how permission prompts reach the person: through Öge Gates (Claude `--permission-prompt-tool stdio`, Codex approvals), where the agent supports that.
2. **Honest supervision.** Öge says exactly what it can gate. Operations the original settings already allow never produce a prompt, so Öge never sees them before they run. The coverage comes from what the agent reports at start (Claude's init: permission mode and tools; Codex's resumed thread: approval policy and sandbox), not from reading config files. The Chat shows it in one line: for example `gated: what your settings would ask · allowed without Öge: your allow rules`, or `not gated: bypassPermissions`.
3. **No false trust.** An adopted session is never shown or recorded as fully supervised when its permissions bypass Öge. With a bypass mode, nothing is gated and the label says so.

The `in place · not isolated` label stays visible on the rail row and in the Chat's header for the session's whole life, not only in docs or the Receipt.

Rejected: (b) resuming under Öge's Launch profile envelope, since it changes the agent's capabilities mid-conversation; and forking into an isolated Workspace (`--fork-session`), since it creates a new id and is not the exact session.

## Why this is still supervision

Öge adds attention routing and evidence to work it can see; it does not take over conversations it did not start. An adopted session's turns are driven and recorded like any Chat's, but Öge claims only what it checked, and says plainly that in-place work was not isolated.

## What would reverse this

- Claude or Codex forbids third-party listing or resume of sessions, or changes the session store in a way that cannot be read without content.
- Dogfood shows the catalog adds more attention cost than it saves (the Desk evaluation in desk.md).
