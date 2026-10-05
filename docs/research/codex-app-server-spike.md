# Codex app-server spike: results

> Condensed from the spike outputs on branch `prototype/codex-app-server` at commit [`b23db14d3a1c`](https://github.com/Erengun/oge/tree/b23db14d3a1ce38db15787af25cff3b73a50f2ad/prototypes/codex-app-server) (#13). The decisions it drove are in [ADR-0018](../adr/0018-codex-adapter-native-app-server-one-per-run.md). The re-redacted wire transcripts used as test fixtures are in [`internal/agent/codex/testdata/`](../../internal/agent/codex/testdata/).

The spike was a small Python JSON-RPC client for `codex app-server` over stdio, plus no-model probes through `codex sandbox -P`. It used `codex-cli` 0.155.1 on macOS with ChatGPT-account auth, `gpt-5.6-sol` at `reasoningEffort: low`, and scratch workspaces in a directory outside `/tmp`. Each workspace held `src/calc.py`, `src/calc_test.go`, `tests/test_calc.py` and `AGENTS.md`, next to a `private/heldout.txt` that stood in for held-out data.

Every profile also denied `private/`, `~/.ssh` and `~/.codex/auth.json`.

## Step 0: readiness probe

`initialize`, `account/read` (with `refreshToken: false`) and `account/rateLimits/read` worked without writing anything. Fixture: `init_account.jsonl`.

## Step 1: inline permission profiles under `codex sandbox -P`

These probes ran with no model and spent no quota. Profiles were passed as `-c permissions.<name>={...} -P <name>`.

| Profile | Definition (beyond the shared denies) | Result |
|---|---|---|
| `ver` | `:read-only` + `tests`=write + `**/*_test.go`=write | Every command fails with `filesystem glob path **/*_test.go only supports deny access; use an exact path or trailing /** for write subtree access`. Glob *write* entries are rejected, and the profile fails closed. |
| `ver2` | `:read-only` + `tests`=write + `src/calc_test.go`=write | Writes land only in `tests/test_calc.py` and `src/calc_test.go`. A new `src/new_test.go`, a new top-level file, `AGENTS.md`, `src/calc.py` and `/tmp` are all denied. Reading `src/calc_test.go` is allowed. |
| `impl` | `:workspace` + `tests`=read + `AGENTS.md`=read | Read entries inside the writable root are enforced: `tests/` and `AGENTS.md` are denied for writes. `src/`, new files and `/tmp` are writable. |
| `impl2` | `:workspace` + `tests`=read + `**/*_test.go`=deny | A deny glob works. `src/calc_test.go` can't be read or written, and new `*_test.go` files are denied. `/tmp` is still writable. |

In every working profile, reads of `private/heldout.txt`, `~/.ssh` and `~/.codex/auth.json` were denied (`Operation not permitted`), and network was off (`Could not resolve host`). Adding `-c sandbox_workspace_write.exclude_slash_tmp=true` didn't change any result, so `/tmp` stayed writable under the `:workspace`-based `impl2`.

### Step 1b: network granularity

| Profile | First run | Second run |
|---|---|---|
| `:read-only` | Both hosts fail DNS (off). | Same. |
| `network={enabled=true}` | Both hosts return 200. | Both get `CONNECT tunnel failed, response 403`. |
| `network={enabled=true, mode="limited", domains={"example.com"="allow"}}` | Both return 200, so the domain list is ignored. | example.com returns 200, and wikipedia.org gets 403. |

The second run passed extra `codex sandbox` arguments, which the output file doesn't record. Per ADR-0018, domain scoping only takes effect with the experimental `network_proxy` feature, and without it the domain list is silently ignored.

## Step 2: live app-server turns

Fixtures: `threads_file_approval.jsonl` (A), `steer_interrupt_resume.jsonl` (B), `kill_resume.jsonl` (C1, C2), `held_approval.jsonl` (D) and `command_approval.jsonl` (F).

- **A, envelope and concurrent threads.** A `thread/start` with a glob write rule fails with JSON-RPC `-32600` (fail closed). One process ran two threads with different profiles in separate workspaces, and each was enforced independently. A patch that touched a read entry inside the writable root became `item/fileChange/requestApproval`, and declining it dropped the whole patch.
- **B, steer and interrupt after resume.** `thread/resume` in a new process emits `deprecationNotice` and `thread/goal/cleared`. `turn/steer` on a running turn is accepted. `turn/interrupt` ends the turn with `turn/completed` status `interrupted`. `turn/steer` after the turn ended returns `-32600 no active turn to steer`, and `turn/interrupt` on an ended turn got no response.
- **C, kill mid-turn and resume.** The app-server was SIGKILLed during a `sleep 40` command. A fresh process resumed the thread coherently (n=1): the turn was marked `interrupted`, and the model's account matched the disk. The killed server's tool child kept running.
- **D, held approval.** A `fileChange` approval was left unanswered for about 690 s. Codex never expired it, and the turn completed after the client declined.
- **F, command approval.** `item/commandExecution/requestApproval` carries `availableDecisions` (including `acceptWithExecpolicyAmendment`) and `proposedExecpolicyAmendment`. A decline is acknowledged with `serverRequest/resolved`.

### Environment policy (phases E, E2, F)

The spike set a synthetic `OGE_PROBE_TOKEN=fake-not-a-secret` and a plain variable in the app-server's environment.

- **E.** With a process-level `-c shell_environment_policy.ignore_default_excludes=false`, the token still reached the shell. Phases A and D had shown that per-thread policy is ignored too.
- **E2.** With per-thread `inherit: "core"`, turning off `features.shell_snapshot` made the policy take effect. Turning off `unified_exec` instead did not.
- **F.** With `shell_snapshot` off and default excludes, the token was excluded and the plain variable passed through. The login shell's rc files now ran inside the sandbox, and one of them hit `operation not permitted`.

## Step 3: trust entries

App-server added a `[projects."<cwd>"] trust_level="trusted"` entry to `~/.codex/config.toml` for every writable thread `cwd`, including ephemeral threads. A thread started at the common parent added a parent entry, but later workspaces under it still got their own entries.
