---
status: accepted (Codex write-glob and envelope assumptions amended in part by ADR-0018; private Check caches may start from a warm seed per ADR-0021)
---

# Agents work in disposable clones of an Öge-owned Run repository; Öge guarantees only what reaches the Candidate and the Oracle

Öge isolates a Run on disk with its own git repository and per-role copies ([#22](https://github.com/Erengun/oge/issues/22)). A reader may expect four other things, and this design rejects each:

- **Agents working in a git worktree of the user's repository.** That was #12's first recommendation. In a linked worktree, Claude's sandbox lets agents write the shared `.git` "so git commit works", so an agent could move the user's branches or push with the user's remotes.
- **Öge's revert step as a security boundary.** It isn't one ([#17](https://github.com/Erengun/oge/issues/17)).
- **Checks running inside a sandbox.** In the MVP they do not.
- **The result appearing in the user's checkout.** Öge never puts it there on its own.

Decisions:

- **The user's repository is only the source of the Snapshot.** Each Run gets an Öge-owned **Run repository**. It holds the Snapshot and every Candidate, has no remotes, and has hooks disabled.
  - Agents work only in **Workspaces**: self-contained copies derived from it, never linked worktrees, with no alternates or hard links into private state.
  - Öge makes every commit with its own index, with hooks and fsmonitor off. It ignores whatever an agent does to a Workspace's `.git`.
- **Separate copies per role.**
  - The planner gets a read-only copy of the Snapshot.
  - The implementer has one persistent Workspace.
  - The verifier and the reviewer each get a fresh, sanitised copy of the Candidate on every Attempt. The reviewer never sees the implementer's caches, scratch area or other residue.
  - Each Check execution gets a fresh **Check directory**: the Candidate with the Oracle version laid over its test paths, and dependencies only from Öge's own setup command.
- **Private state that agents cannot read is a required trust property.** The Run repository, the Oracle and the ledger live in a private area of a per-user state directory, outside the user's repository and never under a temp directory. The Oracle never enters any git object store an agent copy can reach.
  - Every Launch profile denies reads of the private area.
  - If a verifier or reviewer can't be kept out of it, a pipeline that requires held-out verification fails closed. Degraded is allowed only for pipelines that explicitly do not require held-out confidentiality.
- **The claim is deliberately narrow.** "Out-of-scope writes inside an Öge Workspace never reach the Candidate or Oracle. Öge does not claim to contain side effects outside the Workspace; only supported native/OS sandbox mechanisms constrain external filesystem, process and network effects."
  - Each agent's native sandbox and permissions are configured fail-closed per Role kind. They prevent what they can.
  - Öge's comparison after every Attempt is authoritative inside the Workspace. It covers every file, ignored ones included, and reverts and records whatever is out of scope.
  - Evidence names each path's enforcement class: native-enforced, revert-only or degraded.
- **Checks are uncontained in the MVP, and this is stated.** Check commands run agent-written code with the user's privileges. The MVP mitigations:
  - credential variables scrubbed;
  - a fresh directory;
  - private temp and cache directories;
  - network off;
  - a process-group timeout and kill.

  Where no OS sandbox is active, Evidence and docs say that arbitrary reads of the user's filesystem are not contained. OS-level Check sandboxing is a high-priority post-MVP item.
- **Implementer-authored tests have only negative authority.** They run separately from the Oracle and are labelled as the implementer's. A pass is informational. A failure routes along a declared edge that does not accept, to a send-back or a Gate. It never changes the Oracle's Verdict.
- **The final Candidate is exactly what gets delivered.** Every Ambiguous file is promoted or dropped at a Gate before final acceptance, and the final Check runs on that resolved Candidate. This amends [ADR-0009](0009-briefings-built-by-oge-from-snapshot-and-evidence.md) in part.
  - Öge writes nothing to the user's repository until the user runs `oge apply`, `oge branch` or `oge diff`.
  - Delivery reads the immutable Candidate data that Öge keeps in private state.
  - Öge never pushes, merges, commits to or checks out the user's working branch.

## Consequences

- Preflight refuses submodules, Git LFS, an unmerged index, and a merge, rebase, cherry-pick or bisect in progress. Ignored files reach Workspaces only through a project copy list or the setup command.
- Network is off by default for verifier, reviewer, Checks and other judged roles. Planner and implementer get narrowly scoped access only through an explicit rule, or a per-call approval where the adapter can enforce it. Agent caches are private to the Run, and Check caches private to the Check. These cache settings count as Launch-profile isolation knobs under ADR-0006.
- On Claude, writes to colocated tests, new top-level files, caches, and anything hooks or MCP servers do are revert-only. Windows is degraded and never satisfies protected verification.
- *Amended in part by [ADR-0018](0018-codex-adapter-native-app-server-one-per-run.md) ([#13](https://github.com/Erengun/oge/issues/13)):* Codex 0.155.1 rejects glob *write* entries in permission profiles. On Codex, writes to test subtrees and to exact existing test files are native where proven. **New colocated tests are revert-only.** Evidence records the class for each path. Deny-read of the private area, read entries inside a writable root, `on-request` escalation and network-off-by-default were confirmed on macOS. Linux remains a requirement before claiming them there.
- Copies per Attempt and setup runs cost time. Cleanup removes disposable directories, but never the data that delivery needs.

## What would reverse this

- The deferred Codex spike ([#13](https://github.com/Erengun/oge/issues/13)) contradicts the Codex assumptions:
  - glob writes and read-inside-write rules in permission profiles;
  - `on-request` under `:read-only`;
  - deny-read of the private area;
  - network control.
- Copy and setup cost dominates fixture runs ([#31](https://github.com/Erengun/oge/issues/31)). Then cache installs that Öge makes from the Snapshot, never ones copied from a Workspace.
- Toolchains cause frequent false Tamper events. Then narrow the protected set, but keep the Gate.
- Agents are seen exfiltrating held-out tests or credentials through Checks. Then OS-level Check sandboxing moves into the MVP.
- A vendor stops letting agents write a linked worktree's common directory, and the cost of cloning becomes material. Then revisit linked worktrees of the Run repository, never of the user's repository.
