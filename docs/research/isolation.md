# Workspace isolation and write restriction: git worktrees, OS sandboxes, and native agent enforcement

> Moved from branch `research/isolation` at commit [`de6ceed03944`](https://github.com/Erengun/oge/blob/de6ceed03944d81e2a3e53bfd6a5bba40126792f/research/isolation.md). Content unchanged.

Research for issue #12. Date: 2026-10-04.

## Question

What do the candidate mechanisms for isolating Öge stages on disk and restricting writes actually do, and where do they stop working? The roles are planner (read-only), implementer (writes production code), verifier (fresh session, writes tests and fixtures only) and reviewer (read-only). The candidates:

1. git worktrees
2. post-stage diff inspection and staged patches
3. OS-level sandboxes (Seatbelt, Landlock, bubblewrap, Windows)
4. what Codex and Claude Code already enforce themselves, so Öge can reuse it instead of rebuilding it

## Environment and versions

| Thing | Version / ref |
|---|---|
| Host | macOS 15.7.5 (24G624), Darwin 24.6.0, APFS |
| git | 2.50.1 (Apple Git-155). The man pages cited are "Git 2.50.1.428.g0e8243, 2025-07-22" |
| Codex CLI | `codex-cli 0.155.1` (Homebrew cask). Source cited at `openai/codex@afb436df8b70bb5bc57b86d9a3e829968988cd21` (main, 2026-10-04T07:12Z) |
| Claude Code | `2.1.289 (Claude Code)`. Docs fetched 2026-10-04 from code.claude.com |
| bubblewrap, git-lfs | not installed on this host, so neither was tested |

Experiments ran in a scratch directory. Some Codex runs needed a directory outside `/tmp` (see the confound note under E5). No agent sessions were started: every Codex result below comes from `codex sandbox`, which runs a plain command under Codex's sandbox with no model call.

---

## 1. git worktrees

### Documented behaviour

- A linked worktree shares everything except per-worktree files such as `HEAD` and `index`. All `refs/` are shared, except `refs/bisect`, `refs/worktree` and `refs/rewritten`. (`git-worktree(1)`, DESCRIPTION and REFS)
- The repository config file is shared across worktrees unless `extensions.worktreeConfig` is enabled. (`git-worktree(1)`, CONFIGURATION FILE)
- The same branch cannot be checked out in two worktrees. Confirmed in E1b: `fatal: 'main' is already used by worktree at …`.
- Stale admin files are pruned by `git worktree prune`. `git gc` also runs `prune --expire 3.months.ago` by default (`gc.worktreePruneExpire`). Use `git worktree lock` to protect a worktree that isn't always mounted. (`git-worktree(1)`, `git-config(1)`)
- `remove` refuses an unclean worktree unless given `--force`. A worktree with submodules can only be removed with `--force`, and a locked one needs `--force --force`. `move` refuses worktrees that contain submodules. (`git-worktree(1)`)
- **BUGS section, verbatim:** "Multiple checkout in general is still experimental, and the support for submodules is incomplete. It is NOT recommended to make multiple checkouts of a superproject." (`git-worktree(1)`)

### Experiments (scripts `exp1.sh`, `exp2.sh`, `exp3.sh`)

| # | Experiment | Result |
|---|---|---|
| E1a | `git worktree add` from a repo with staged, unstaged, untracked and ignored (`.env`) changes | The worktree holds **only committed content**. Staged, unstaged, untracked and ignored files are all missing. |
| E1a | A `post-checkout` hook exists in the main repo | **The hook fires during `worktree add`**, inside the new worktree. Hooks are shared through the common `.git`. |
| E1c | Snapshot the dirty state without touching the user's index or working tree: `GIT_INDEX_FILE=tmp git read-tree HEAD && git add -A && git write-tree`, then `git commit-tree -p HEAD`, then `git update-ref refs/oge/snapshots/<run>` | Works. The user's `git status` is byte-identical before and after. A worktree checked out from the snapshot ref contains the staged, unstaged and untracked changes. Gitignored files (`.env`) are excluded. |
| E1d | `git stash create` as the alternative | Captures tracked changes only. **Untracked files are lost.** |
| E1e | User keeps editing the main checkout while a worktree exists | The worktree is unaffected (its status stays empty). The two are independent until something merges them. |
| E2a | Superproject with a submodule, then `worktree add` | The submodule directory is **empty**. It needs `git submodule update --init` inside the worktree, and the submodule's gitdir then lives under `.git/worktrees/<wt>/modules/…`. |
| E2b | `git -c core.hooksPath=/dev/null worktree add …` | Suppresses the hook. `--no-checkout` followed by a controlled `reset --hard` also avoids `post-checkout`. |
| E2c | Worktree directory deleted behind git's back (crash or `rm -rf`) | `git worktree list --porcelain` marks it `prunable gitdir file points to non-existent location`, and `git worktree prune -v` removes the metadata. |
| E2d | A stale `index.lock` in a worktree's gitdir (what a SIGKILLed `git add` leaves behind) | That worktree's `git add` fails with `Unable to create …/.git/worktrees/<wt>/index.lock: File exists`. **The main checkout is unaffected** because each worktree has its own index lock. Recovery is deleting the lock file. |
| E2e | User `mv`s a worktree without using `git worktree move` | The worktree shows up as `prunable`. **A `prune` (or a `gc` after the expiry) at this point would delete its metadata.** `git worktree repair <newpath>` fixes it. |
| E2f | `remove` on a worktree with an initialised submodule | Fails with `fatal: working trees containing submodules cannot be moved or removed`. `--force` works. |
| E3a | Cost: 25,001 files, 81 MB `.git` (mostly loose objects), warm cache, APFS | `worktree add` took **4.6 s**. The checkout uses 98 MB on disk while objects are shared. This is a single data point. |

### LFS (untested; LFS isn't installed here)

- LFS objects are stored under `lfs.storage`, which defaults to `.git/lfs` in the repository directory (git-lfs `git-lfs-config(5)`). In practice this is the common dir, so worktrees share it.
- LFS content is materialised by a smudge **filter driver** during checkout. Claude Code deliberately does not run repo-local filter drivers when it creates a worktree, because "a filter driver is a shell command, and anything that can write to the repository, including Claude, could have put one there". It tells users to run `git lfs pull` inside the worktree instead (Claude Code docs, *worktrees → Git LFS files are pointer files*).
- Codex's managed worktree does `worktree add --detach --no-checkout`, then `git --work-tree=. reset --hard --no-recurse-submodules <sha>` (`codex-rs/worktree/src/lib.rs`, `create`). Its comment says the order exists so it can "discover destination-only filters before materializing files".
- Takeaway: **both vendors treat hooks and filter drivers that run during worktree creation as an attack surface.** Öge should do the same: create with `--no-checkout` and `-c core.hooksPath=/dev/null`, then run LFS as an explicit step.

### Windows

- `MAX_PATH` is 260 characters. Lifting it needs both the `LongPathsEnabled` registry value and a `longPathAware` application manifest, on Windows 10 1607 or later (Microsoft Learn, *Maximum Path Length Limitation*, updated 2025-04-15). Deeply nested worktree paths such as `<repo>/.oge/worktrees/<run>/<stage>/…` make this worse. Git for Windows has its own `core.longpaths` switch. Its wiki page has moved, and it wasn't re-verified here.
- Claude Code on Windows does not delete through junctions or directory symlinks when it removes a worktree (Claude docs, *worktrees → Clean up*). That is evidence that worktree cleanup on Windows needs link-aware deletion.

---

## 2. Post-stage diff inspection and staged patches

Experiment E3 simulated a verifier that made legitimate test edits plus four illegal changes: it modified a production file, added a production file, deleted a production file, and wrote ignored build output.

| Step | Result |
|---|---|
| Detect | `git status --porcelain=v1 -z --untracked-files=all` found all M/D/?? changes in **0.20 s** on 25k files. Filtering paths by glob (`tests/**`) produced the three production violations. |
| Gap: ignored files | `build/out` **does not appear** in normal status. You need `--ignored=matching`. A stage that writes into gitignored paths, such as `node_modules`, caches or `.env`, is invisible without it. |
| Gap: agent commits | E3 diffed against `HEAD`. That only works if the agent never commits. **Diff against the stage-start snapshot ref instead** (`git diff --name-status <snap>` plus untracked and ignored files), so a commit made by the agent can't hide changes. |
| Gap: writes outside the worktree | Diff inspection only sees the worktree. It cannot see writes to `$HOME`, the main checkout, other worktrees, `/tmp`, or the shared `.git` (refs, config, hooks). **Only an OS sandbox prevents those.** |
| Revert | For each violating path: `git restore --source=<snap> --staged --worktree -- <p>` if it exists in the snapshot, otherwise `rm`. Afterwards only the test changes remained. |
| Staged patch | `git diff --cached --binary <snap> -- tests` produces an auditable patch (23 lines here) that can be applied or rejected elsewhere. |

Assessment: diff inspection is **cheap, deterministic, cross-platform and agent-agnostic**, and it gives evidence a human can read. It is detection after the fact, not prevention. It cannot undo side effects such as a migration the agent ran, a network call, or a write outside the tree. It should be the authoritative scope check, with OS sandboxing as a prevention layer underneath.

---

## 3. OS-level restriction primitives

| Primitive | Status | Model | Notes |
|---|---|---|---|
| **macOS Seatbelt** (`sandbox-exec`, `sandbox_init`) | `sandbox-exec(1)` says: "The sandbox-exec command is DEPRECATED". It still ships and works on macOS 15.7.5. **Both Codex and Claude Code build their macOS sandbox on it** (Codex `sandbox --help`: "Full command args to run under seatbelt"; Claude docs: "uses the built-in Seatbelt framework"). | SBPL profile with allow/deny rules per subpath. Inherited by child processes. | E4: `(deny file-write*)` plus `(allow file-write* (subpath "<wt>/tests"))` allowed a write in `tests/`, denied `sh` and `python3` writes in `src/` (child processes inherit the sandbox), and made `git add` fail because the worktree index lives in the main repo's `.git/worktrees/<wt>/`. |
| **Linux Landlock** | Mainline since 5.13. Needs `CONFIG_SECURITY_LANDLOCK` and LSM enablement. ABI 1 covers filesystem; 2 (5.19) adds REFER; 3 (6.2) truncate; 4 (6.7) TCP; 5 (6.10) ioctl; 6 (6.12) scoping; later ABIs add more (docs.kernel.org, *Landlock: unprivileged access control*). | Unprivileged and inherited across clone. Cannot be removed once applied. **Allow-list beneath directories only: you can't deny a subdirectory inside an allowed one.** | Codex has demoted it: "The legacy Landlock option is rejected for these policies because it cannot isolate app-server Unix sockets" (`codex-rs/linux-sandbox/README.md`). |
| **Linux bubblewrap** | Unprivileged and needs **user namespaces**. Ubuntu 24.04+ AppArmor blocks those by default unless a profile allows it (Claude sandbox docs). setuid mode is discontinued (github.com/containers/bubblewrap). | Builds a mount namespace: `--ro-bind / /`, then `--bind` for writable roots, then `--ro-bind` for protected subpaths. | Both Codex (default on Linux, with a bundled `bwrap` fallback) and Claude Code (requires `bubblewrap` and `socat`) use it. "The level of protection … is entirely determined by the arguments passed to bubblewrap" (bubblewrap README). |
| **Windows** | Codex ships a native Windows sandbox (`codex-rs/windows-sandbox-rs`; Codex docs: "the native Windows sandbox"). **Claude Code on native Windows runs commands unsandboxed** and recommends WSL2 (Claude sandbox docs). | n/a | Öge doesn't need its own Windows sandbox in the MVP, but it can't assume one exists for Claude. |

Conclusion: building Öge's own OS sandbox would mean re-implementing what both vendors already ship (Seatbelt and bubblewrap wrappers with protected-path lists, proxying and seccomp), on a macOS API Apple has deprecated. **Don't build it.** Configure the vendors' sandboxes instead.

---

## 4. What Codex and Claude Code enforce natively

### Codex CLI 0.155.1

- **Modes:** `-s read-only | workspace-write | danger-full-access`. **Approvals:** `-a on-request | never`. `--add-dir <DIR>` adds "directories that should be writable alongside the primary workspace". `--worktree` starts the session "in a new managed Git worktree" (`codex --help`, `codex exec --help`).
- **Permission profiles** (config reference): `default_permissions` picks a profile. The built-ins are `:read-only`, `:workspace` and `:danger-full-access`. `[permissions.<name>]` tables can `extends` another profile and map paths or globs to `read | write | deny`, including the special `":minimal"` and `":workspace_roots"`. On Linux, overlapping entries are applied in path-specificity order, so "narrower writable children can reopen broader read-only or denied parents" (`linux-sandbox/README.md`).
- **Protected paths inside writable roots:** `.git` stays read-only, and so does the **resolved `gitdir:` of a linked worktree or submodule**, plus `.agents`, `.codex` and `.aws` (`codex-rs/protocol/src/permissions.rs` L40–50, `default_read_only_subpaths_for_writable_root` L2347–2390).
- **File edits are sandboxed too.** `apply_patch` checks that a patch stays within writable paths (`is_write_patch_constrained_to_writable_paths`). It runs "under the orchestrator" with "sandboxing enforced by the explicit filesystem sandbox context". With `-a never`, an out-of-scope patch is **rejected** ("writing outside of the project; rejected by user approval settings") instead of prompting (`codex-rs/core/src/safety.rs`, `core/src/tools/runtimes/apply_patch.rs`).
- **Managed worktree:** a detached worktree at `HEAD` (or a given base) under a configured absolute root, in a random 4-hex-character bucket. It is created with `--no-checkout` followed by `reset --hard --no-recurse-submodules`. On macOS it writes `.metadata_never_index` to keep Spotlight out. **Only committed state is used, and submodules aren't initialised** (`codex-rs/worktree/src/lib.rs`, `paths.rs`).

**Experiment E5e** (`codex sandbox`, a real linked worktree outside `/tmp`):

| Profile | write `src/` | write `tests/` | overwrite `.git` pointer | `git add` / `git commit` |
|---|---|---|---|---|
| `:workspace` | OK | OK | DENIED | **DENIED** (`…/.git/worktrees/wt/index.lock: Operation not permitted`) |
| `verifier` = `{":minimal"="read", ":workspace_roots"={"."="read","tests"="write"}}` | **DENIED** | OK | DENIED | denied (and `~/.gitconfig` unreadable: `:minimal` doesn't grant home reads) |
| `verifier2` = `{extends=":read-only", filesystem={":workspace_roots"={"tests"="write"}}}` | **DENIED** | OK | DENIED | denied (macOS `/usr/bin/git` shim warns about `DARWIN_USER_TEMP_DIR`) |

Profiles can be passed inline: `-c 'permissions.verifier2={…}' -P verifier2`. That worked without writing anything to `~/.codex`. E5c, run in a plain directory with a fake `.git` dir, gave the same results.

**Confound worth knowing (E5a/b/d):** the scratch directory was under `/private/tmp`. **`/private/tmp` stayed writable under `:workspace` and also under custom `":minimal"="read"` profiles**, even with `sandbox_workspace_write.exclude_slash_tmp=true` set (those `-c` keys seemed to have no effect alongside `-P`). The built-in `:read-only` did deny `/tmp`. As a result, every workspace under `/tmp` looked fully writable and `git commit` "succeeded". **Öge must not place worktrees under `/tmp` or `$TMPDIR`.** Claude's sandbox also makes "a per-user temp directory" writable.

### Claude Code 2.1.289

- **Permission modes:** `default/manual`, `acceptEdits`, `plan`, `auto`, `dontAsk`, `bypassPermissions` (`claude --help`). `dontAsk` "Auto-denies every call that would otherwise prompt", and pre-approved `permissions.allow` rules still run. `--permission-prompts none` makes anything that would prompt get denied in `-p`. `--tools` limits which built-in tools exist at all. `--allowedTools` and `--disallowedTools` take rules such as `Edit(tests/**)`. (permissions docs)
- **Path rules:** `Edit(...)` and `Read(...)` use gitignore syntax. `//abs`, `~/home`, `/relative-to-settings-source` and `./cwd` are distinct anchors. "`Edit` rules apply to all built-in tools that edit files". `Write(...)` path rules are accepted but never consulted, so use `Edit(...)`. **Depth gotcha:** `Edit(tests/**)` as an *allow* matches only `<cwd>/tests`, but as a *deny* it matches `tests` at any depth. Deny beats allow, and an allow can't carve an exception out of a deny. (permissions docs)
- **The sandbox (`sandbox.enabled`, off by default) covers shell commands only.** "Claude's file tools, MCP servers, and hooks run outside it." Edit and Write are governed by permission rules alone. Sandboxed writes go to cwd, the per-user temp dir and `--add-dir` directories. `sandbox.filesystem.allowWrite` "Re-open[s] writing inside a region `denyWrite` blocks" (settings reference). `Edit` allow and deny rules are **merged into** the sandbox config. Hardening switches: `allowUnsandboxedCommands: false` disables the `dangerouslyDisableSandbox` retry, and `failIfUnavailable: true` exits instead of silently running unsandboxed (the default is to run unsandboxed when the sandbox can't start). (sandboxing docs)
- **Protected paths** are always write-denied by the sandbox, and `allowWrite` can't lift that: `.claude/*` config, `.mcp.json`, shell rc files, `.gitconfig`, and `.git/hooks` plus `.git/config`. **Unlike Codex, the sandbox allows writes to a linked worktree's shared `.git`**, except `hooks/` and `config`, "so commands such as `git commit` work". So Claude agents *can* commit inside a worktree. (sandboxing docs, *Git worktrees*)
- **Platforms:** Seatbelt on macOS. bubblewrap plus socat on Linux and WSL2, with optional seccomp. **No sandbox on native Windows.**
- **`--worktree`:** creates `.claude/worktrees/<name>` on branch `worktree-<name>`. The base is `origin/HEAD` by default or the local `HEAD` with `worktree.baseRef: "head"`. It copies only *gitignored* files listed in `.worktreeinclude`, never uncommitted tracked changes. It skips repo-local filter drivers, holds a `git worktree lock` while running, and **leaves worktrees and locks behind in `-p` mode.** Its extra isolation checks (blocking edits and `git -C` into the main checkout) **apply only to sessions started with `--worktree` or `EnterWorktree`.** A plain `claude` that Öge launches with cwd inside an Öge-made worktree gets none of them. (worktrees docs)
- **Trust:** a project's `.claude/settings.json` `permissions.allow` and `additionalDirectories` are not applied until workspace trust is granted, and `-p` skips the trust dialog. `deny`/`ask` rules always apply. `--settings` and `--setting-sources` control which sources load, and `--restricted` "confines the file tools to the working directories … refuses bypassPermissions". (permissions docs, `claude --help`)

### Natively expressible role policies

These are derived from the docs and E5. **The Claude rows and the Codex `exec` wiring are untested**, because no sessions were run.

| Role | Codex | Claude Code |
|---|---|---|
| Planner / reviewer (read-only) | `-s read-only -a never` (built-in `:read-only` verified in E5c) | `--permission-mode plan`, or `dontAsk` with `--tools "Read,Grep,Glob"`. Sandbox `denyWrite: ["."]` if Bash is allowed |
| Implementer | `-s workspace-write -a never`. The worktree must be outside `/tmp`. The agent can't commit; Öge commits | `--permission-mode acceptEdits` or `dontAsk` with `--allowedTools "Edit(/**)"…`, plus sandbox `{enabled, allowUnsandboxedCommands:false, failIfUnavailable:true}`. The agent *can* commit |
| Verifier (tests only) | `-c 'permissions.verifier={extends=":read-only", filesystem={":workspace_roots"={"tests"="write"}}}'` with `-c default_permissions="verifier" -a never`. Enforced for both shell and `apply_patch` | `--permission-mode dontAsk --allowedTools "Edit(tests/**)" …` (anchor carefully) plus `--settings '{"sandbox":{"enabled":true,"allowUnsandboxedCommands":false,"failIfUnavailable":true,"filesystem":{"denyWrite":["."],"allowWrite":["./tests"]}}}'` and `--setting-sources user`, so the repo can't widen the policy |

---

## Mechanism × property comparison

| Property | Worktree only | Post-stage diff check | Raw OS sandbox (Öge-built) | Codex native sandbox | Claude native (permissions + sandbox) |
|---|---|---|---|---|---|
| Isolates stage from user's checkout | **Yes** | n/a | Only if paths configured | Yes for writes (cwd + roots) | Bash: yes (sandbox). Edit: only via rules or `--restricted` |
| Prevents writes outside allowed glob inside the worktree | No | Detects + reverts (after the fact) | Yes | **Yes, per-path profiles, shell + apply_patch** | Edit: yes via rules. Bash: yes via `denyWrite`/`allowWrite` |
| Prevents writes outside the worktree (`$HOME`, main checkout) | No | **Cannot see them** | Yes | Yes (except `/tmp`) | Bash: yes. Edit: yes, unless `allow` rules widen it |
| Sees writes to gitignored paths | n/a | Only with `--ignored` | Yes | Yes | Yes |
| Child processes covered | No | n/a (it looks at the result) | Yes | Yes | Bash children: yes. Hooks and MCP: **no** |
| Agent can `git commit` | n/a | Must diff against snapshot, not HEAD | Depends | **No** (gitdir protected) | **Yes** |
| macOS | Yes | Yes | Seatbelt (deprecated API) | Yes | Yes |
| Linux | Yes | Yes | Landlock or bwrap (userns caveats) | bwrap (bundled fallback) | bwrap + socat required |
| Windows | Yes (path length) | Yes | Large effort | Native sandbox | **None** (WSL2 only) |
| Agent-agnostic (future adapters) | Yes | **Yes** | Yes | Codex only | Claude only |
| Cost | ~4.6 s / 25k files, full checkout on disk | ~0.2 s / 25k files | High build and maintenance cost | Free | Free |
| Failure mode | Stale metadata or locks, submodule friction | Can't undo side effects | Breaks toolchains | Silently permissive under `/tmp` | Silently unsandboxed unless `failIfUnavailable` |

---

## Implications for Öge

1. **Simplest robust MVP has three layers:**
   - **(a) An Öge-owned worktree per run, created from an Öge snapshot commit.** Use the temp-index snapshot from E1c so the user's uncommitted and untracked work is included and their index is never touched. Store it under `refs/oge/…`, create with `--no-checkout` and `-c core.hooksPath=/dev/null`, then do a controlled checkout. Put it **outside `/tmp` and `$TMPDIR`**, under a short path (Windows).
   - **(b) The vendor's native sandbox and permission policy for each role** (table above). Fail closed: Claude `failIfUnavailable` + `allowUnsandboxedCommands:false`, Codex `-a never`.
   - **(c) An authoritative post-stage scope check** against the stage-start snapshot ref, including untracked and `--ignored` files. Violations are reverted and recorded as evidence. This layer is the one that works for every agent and every OS. The sandbox only reduces how often it fires.
2. **Öge owns git history.** Codex agents *can't* commit in a worktree, and Claude agents *can*. Öge should make the stage-boundary snapshot commits itself and treat any HEAD movement during a stage as a change to inspect. Don't rely on the agent's commits.
3. **The verifier gets a fresh worktree (or a hard reset) from the implementer's snapshot.** Its allowed write set is a per-run glob list (`tests/**`, `**/*_test.go`, `testdata/**`, …). The same list feeds the Codex profile, the Claude `Edit`/`denyWrite`/`allowWrite` config and the diff check, so there's one source of truth.
4. **Don't use the vendors' `--worktree` flags for Öge stages.** Codex's and Claude's managed worktrees both start from committed state only, put worktrees in vendor-specific locations, have their own cleanup and lock lifecycles (Claude leaves worktrees behind in `-p`), and differ from each other. Öge needs one worktree lifecycle across agents.
5. **Cleanup/resume:** record worktree paths and snapshot refs in Öge's run state. On startup, run `git worktree list --porcelain` and handle `prunable` and `locked` entries, delete stale `index.lock` files only if no Öge process owns them, and `--force` remove worktrees that contain submodules. Use `git worktree lock --reason oge-run-<id>` while a stage is running, and never run a bare `prune` before `repair` on worktrees that may have been moved.
6. **Submodules and LFS are second-class in the MVP:** `submodule update --init` and `git lfs pull` happen as explicit post-create steps that can fail with clear errors. git itself calls multiple checkouts of a superproject "NOT recommended".

## Changes or inputs to other tickets

- **#22 (blocked by this one):** the three-layer design above. Note that "verifier writes only tests" can't be enforced *identically* across agents. Codex enforces it at the OS level for both shell and edits. Claude enforces it through permission rules for Edit and through the sandbox for Bash, has no sandbox on native Windows, and lets hooks and MCP servers run outside it.
- **Failure/resume:** the stale lock, prunable worktree and submodule-removal behaviours from E2 are concrete recovery cases.
- **Cross-platform fog:** the `/tmp` writable-root pitfall (macOS, Codex). Ubuntu 24.04+ AppArmor blocks bubblewrap (affects both agents on Linux). No Claude sandbox on native Windows, and `MAX_PATH` limits worktree paths.
- **Claude adapter:** `-p` sessions skip the trust dialog, so project `allow` rules don't apply while `deny` rules still do. Öge should pass its policy via `--settings` and `--allowedTools`/`--disallowedTools`, and probably `--setting-sources user`, so a repo's `.claude/settings.json` can't widen the policy.

## Open questions

1. Does `codex exec -c default_permissions="<profile>"` with an inline `permissions.<profile>` apply the same policy as `codex sandbox -P` to a real agent turn, including `apply_patch`? The source says yes. Needs a tiny prototype session.
2. Is Claude's `denyWrite: ["."]` + `allowWrite: ["./tests"]` enforced for Bash as the settings reference describes, and do `dontAsk` + `Edit(tests/**)` correctly block an `Edit` to `src/`? Needs a prototype session.
3. Why did `/private/tmp` stay writable under a custom `":minimal"="read"` profile (and `getcwd` break in one run), and is `exclude_slash_tmp` intentionally ignored alongside `-P`? It might be macOS-specific. Check on Linux.
4. Can APFS `clonefile` (`cp -c`) or reflinks give near-instant worktree population for large repos? Not measured.
5. What should a verifier be allowed to write besides test globs: build caches, snapshot files, lockfiles? This is a policy question for the domain model, and the glob list should probably be configurable per repo.
6. LFS behaviour in Öge-made worktrees, and Windows long-path behaviour, are untested here (no `git-lfs`, no Windows host).

## Sources

- `git-worktree(1)`, `git-config(1)` (`gc.worktreePruneExpire`): git 2.50.1 man pages, locally installed.
- `sandbox-exec(1)`: macOS 15.7.5 man page (marked DEPRECATED).
- Codex: `codex --help`, `codex exec --help`, `codex sandbox --help` (0.155.1). `openai/codex@afb436d`: `codex-rs/linux-sandbox/README.md`, `codex-rs/protocol/src/permissions.rs`, `codex-rs/core/src/safety.rs`, `codex-rs/core/src/tools/runtimes/apply_patch.rs`, `codex-rs/worktree/src/{lib.rs,paths.rs}`, `codex-rs/core/tests/suite/approvals.rs` (profile TOML). Docs: https://learn.chatgpt.com/docs/sandboxing, https://learn.chatgpt.com/docs/config-file/config-reference (redirected from developers.openai.com/codex/…).
- Claude Code: `claude --help` (2.1.289). https://code.claude.com/docs/en/sandboxing, https://code.claude.com/docs/en/permissions, https://code.claude.com/docs/en/settings-reference, https://code.claude.com/docs/en/worktrees.
- Landlock: https://docs.kernel.org/userspace-api/landlock.html
- bubblewrap: https://github.com/containers/bubblewrap
- Windows: https://learn.microsoft.com/en-us/windows/win32/fileio/maximum-file-path-limitation
- git-lfs: `docs/man/git-lfs-config.adoc` in github.com/git-lfs/git-lfs (`lfs.storage`)
- Experiment scripts (`exp1.sh` to `exp5e.sh`) were throwaway and not committed. Their outputs are summarised above.
