# Go vs Rust: ecosystem facts for Öge's workload

> Moved from branch `research/lang-ecosystem` at commit [`9e6656ee8436`](https://github.com/Erengun/oge/blob/9e6656ee8436a1711088f2bd3bdc9108aab8bcba/research/lang-ecosystem.md). Content unchanged.

Ticket: Erengun/oge#11. Feeds: #15 (Go vs Rust). Researched 2026-10-04.

This is not a general language comparison. Each section takes one concrete Öge need and gives the Go answer, the Rust answer, and which language (if either) has a material edge. "Parity" means both ecosystems have a credible, maintained answer and the choice should not hinge on it.

## Sources and versions

Versions and dates come from registries and GitHub (`gh api`, `crates.io/api/v1`, `proxy.golang.org/<mod>/@latest`), all queried 2026-10-04. Doc text comes from the official doc pages linked inline. Tags used below:

| Tag | Source | Version / date |
|---|---|---|
| **[GO]** | Go stdlib source and docs at tag `go1.27.1` (`src/syscall/exec_{linux,libc2,windows}.go`, `api/go1.20.txt`, pkg.go.dev `os/exec`, `os/signal`), release notes go.dev/doc/go1.23, go1.27 | go1.27.1 is current; go1.26.8 is the other supported release (go.dev/dl JSON) |
| **[RS]** | Rust std docs (doc.rust-lang.org `std::os::unix::process::CommandExt`), docs.rs `tokio::process`, `tokio::signal` | Rust stable 1.99.0 (static.rust-lang.org channel, 2026-10-01); tokio 1.53.2 (2026-10-03) |
| **[XSYS]** | `golang.org/x/sys` v0.48.0 (2026-08-31), module zip from proxy.golang.org, grepped | |
| **[REG]** | Registry/GitHub metadata, table in "Library inventory" below | |
| **[PROBE]** | Local throwaway builds in a scratch dir (not in any repo): Rust hello world, a Rust probe binary, and `typify` run against the Codex schema | rustup stable 1.95.0, aarch64-apple-darwin, macOS (Darwin 24.6.0) |
| **[CODEX]** | Installed `codex-cli 0.155.1`, `codex app-server generate-json-schema`; `openai/codex` repo (Apache-2.0) | 2026-10-04 |

No Go toolchain was installed (per ticket), so **nothing on the Go side was compiled or measured**. Every Go claim is from docs, source or registry metadata.

## Summary table

| # | Öge need | Go | Rust | Edge |
|---|---|---|---|---|
| 1 | Spawn and supervise long-lived agent subprocesses, async stdio pipes | `os/exec` + goroutines; `Cmd.Cancel`/`WaitDelay` (Go 1.20) for context-driven shutdown | `tokio::process` (async), `kill_on_drop`; std `Command` is blocking | Parity |
| 2 | Kill a whole agent process tree (Unix) | `SysProcAttr{Setpgid: true}` + `syscall.Kill(-pgid, sig)`; Linux also `Pdeathsig`, `PidFD`, `CgroupFD` | `CommandExt::process_group(0)` (std, 1.64); `process-wrap` 10.0.1 `ProcessGroup`/`ProcessSession` wrappers | Parity |
| 3 | Kill a whole agent process tree (Windows, "not designed out") | No job-object support in `syscall`/`os/exec`; x/sys has the Win32 calls but `os/exec` closes the main thread handle, so suspend-assign-resume is not possible through it | `process-wrap` `JobObject` does CREATE_SUSPENDED, assign, resume for you | **Rust (moderate)**; Go has a workaround (see §3) |
| 4 | Signal handling / Ctrl-C | `os/signal`, `signal.NotifyContext`; Windows Ctrl-C/Break mapped to `os.Interrupt`, close/logoff/shutdown to `SIGTERM` | `tokio::signal::ctrl_c`, `unix::signal(SignalKind)`, `windows::ctrl_{c,break,close,logoff,shutdown}` | Parity |
| 5 | Cancellation | `context.Context` threaded through stdlib (`exec.CommandContext`) | `tokio_util::sync::CancellationToken`, `select!`, drop-based cancellation | Parity (different idioms) |
| 6 | PTY (fallback only) | `creack/pty` v1.1.24 is Unix-only (Windows returns `ErrUnsupported`); ConPTY needs `aymanbagabas/go-pty` or `charmbracelet/x/xpty` | `portable-pty` 0.9.0 (wezterm) covers Unix + ConPTY behind one trait | Slight Rust; irrelevant if PTY stays a fallback |
| 7 | JSON-RPC 2.0 / JSONL over stdio | No stdlib JSON-RPC 2.0. `golang.org/x/exp/jsonrpc2` (experimental, pseudo-versions), `sourcegraph/jsonrpc2` v0.2.3. `encoding/json/v2` GA in Go 1.27 | `serde_json` 1.0.151; `jsonrpsee` 0.26.1 (HTTP/WS-centric). Framing is ~100 lines either way | Parity |
| 8 | Typed bindings from the Codex app-server JSON Schema | `omissis/go-jsonschema` v0.24.1: README marks `allOf`/`anyOf`/`oneOf` unsupported; Codex schema is full of them | `typify` 0.8.0 compiled the whole Codex v2 bundle (638 defs, 54 `oneOf`, 218 `anyOf`, 41 `allOf`) first try [PROBE]. Codex itself is Rust | **Rust (material)** |
| 9 | ACP SDK (if adapters go via ACP) | Community `coder/acp-go-sdk` v0.13.5 (Apache-2.0, last push 2026-06-05). No Go SDK in the `agentclientprotocol` org | Official `agent-client-protocol` crate 2.2.0 (2026-09-18), Apache-2.0 | **Rust (moderate)** |
| 10 | Claude Code SDK | None official (Anthropic ships Python + TypeScript only) | None official | Parity: both speak the CLI's stream-json directly |
| 11 | SQLite | Three options, two of them CGO-free: `modernc.org/sqlite` v1.60.1 (transpiled C, SQLite 3.53.4), `ncruces/go-sqlite3` v0.35.6 (wasm2go); CGO `mattn/go-sqlite3` v1.14.52 | `rusqlite` 0.40.2 with `bundled` (SQLite 3.53.2, compiled by `cc`, needs a C compiler per target); `sqlx` 0.9.0 (async) | **Go (moderate)** on build simplicity; Rust is faster at runtime and has no real downside on native CI runners |
| 12 | CLI framework + interactive prompts | `spf13/cobra` v1.10.2; `charm.land/huh/v2` v2.0.3 forms; Bubble Tea v2.0.10 | `clap` 4.6.7; `dialoguer` 0.12.0, `inquire` 0.9.4; `ratatui` 0.30.2 | Parity (huh is the more polished prompt library) |
| 13 | Git operations | `go-git` v5.19.2: merge is fast-forward only, no rebase/stash/apply, linked worktrees partial | `gix` 0.88.0: no commit merge, no rebase, no reset, commit runs no hooks; `git2` 0.21.0 (libgit2, C) | Parity: **shell out to `git`** in both |
| 14 | Cross-compile + single-binary packaging | `GOOS/GOARCH` cross-compiles a CGO-free binary from one host. GoReleaser v2.18.2 | `dist` (cargo-dist) v0.33.0 builds on native GitHub runners per target and generates the CI. GoReleaser can build Rust via `cargo zigbuild` since v2.5 | **Go (slight)**: trivially cross-compiles from a laptop; Rust's CI-matrix route works fine |
| 15 | Binary size / startup | Not measured (no toolchain). Go 1.27 notes: allocator change adds about 60 KB | Measured: 431 KB hello; **3.0 MB stripped** with tokio + clap + serde + rusqlite(bundled) + process-wrap; dynamic deps only `libSystem`, `libiconv`; `--help` under 10 ms [PROBE] | Unknown for Go; both are "small single binary" class |
| 16 | WebSocket / SSE (later) | `net/http` streams SSE natively; `coder/websocket` v1.8.15 (ISC). `gorilla/websocket` last release 2024-06 | `tokio-tungstenite` 0.30.0; axum/hyper for SSE | Parity |
| 17 | Local toolchain | `go` not installed | `cargo` present, but **Homebrew `rustc` 1.98.1 is broken** (see §17); rustup stable 1.95.0 works when selected explicitly | n/a; fix before any Rust prototype |

## Details

### 1. Subprocess supervision with async stdio

**Go.** `exec.Cmd` with `StdinPipe`/`StdoutPipe` and one goroutine per stream is the idiomatic shape. `exec.CommandContext` "sets the command's Cancel function to invoke the Kill method on its Process, and leaves its WaitDelay unset. The caller may change the cancellation behavior by modifying those fields before starting the command." `WaitDelay` "bounds the time spent waiting on … a child process that fails to exit after the associated Context is canceled, and a child process that exits but leaves its I/O pipes unclosed" [GO, pkg.go.dev/os/exec]. `Cancel` and `WaitDelay` were added in Go 1.20 (`api/go1.20.txt`: `pkg os/exec, type Cmd struct, Cancel func() error #50436`, `WaitDelay time.Duration #50436`). So a graceful "SIGTERM, then SIGKILL after N seconds, then stop waiting on stuck pipes" is expressible with stdlib fields alone. Since Go 1.23, on Linux 5.4+ `os.Process` uses a pidfd internally, "eliminating potential mistargeting when a PID is reused" [go.dev/doc/go1.23].

**Rust.** `std::process::Command` is blocking. For several concurrent agents Öge would use `tokio::process::Command` (tokio 1.53.2). `kill_on_drop` is false by default. Tokio says zombie reaping of dropped children is "best-effort … no additional guarantees are made", and recommends explicit `wait()`/`kill()` [RS, docs.rs tokio::process]. A grace-period kill is a `select!` over `child.wait()` and a timer. There is no built-in equivalent of `WaitDelay` for pipes that stay open after exit; you write it.

**Edge: parity.** Go's stdlib is a little more complete here (`WaitDelay`). Rust needs a runtime choice, and tokio is the de facto one.

### 2. Process groups and signals on Unix (macOS + Linux)

**Go.** `syscall.SysProcAttr` on Linux has `Setpgid`, `Pgid`, `Setsid`, `Pdeathsig` ("sent on thread termination, which may happen before process termination", go.dev/issue/27505), `PidFD *int`, `UseCgroupFD`/`CgroupFD`, plus namespace fields. On darwin (`exec_libc2.go`) it has only `Setpgid`, `Pgid`, `Setsid`, `Setctty`, `Noctty`, `Ctty`, `Foreground`, `Chroot`, `Credential`, `Ptrace`: **no `Pdeathsig` on macOS** [GO source]. Group kill is `syscall.Kill(-pgid, syscall.SIGTERM)`.

**Rust.** `std::os::unix::process::CommandExt::process_group` has been stable since 1.64.0 ("Equivalent to a `setpgid` call in the child process"); `setsid` is still nightly-only; `pre_exec` (stable, `unsafe`) covers anything else, e.g. `prctl(PR_SET_PDEATHSIG)` via `nix`/`libc` [RS]. Tokio exposes `process_group` too. `process-wrap` 10.0.1 (2026-09-23, watchexec, successor to `command-group`) wraps this: with `ProcessGroup`, "`kill` will send a signal to the process group"; it also offers `ProcessSession` and `ResetSigmask` [docs.rs/process-wrap].

**Edge: parity.** Neither OS gives macOS a parent-death signal, so orphan cleanup on macOS needs Öge's own mechanism in either language (e.g. children noticing stdin EOF, or a supervisor sweep of recorded PGIDs on restart).

### 3. Process trees on Windows (not designed out)

The Windows tool for killing a tree is a job object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. The clean way to avoid a race (the child spawning grandchildren before it joins the job) is to create the child suspended, assign it, then resume.

**Go.** Windows `SysProcAttr` has `HideWindow`, `CmdLine`, `CreationFlags`, `Token`, `ProcessAttributes`, `ThreadAttributes`, `NoInheritHandles`, `AdditionalInheritedHandles`, `ParentProcess`, and **no job-object field** [GO `exec_windows.go`]. `x/sys/windows` v0.48.0 has `CreateJobObject`, `AssignProcessToJobObject`, `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, `CREATE_SUSPENDED`, `ResumeThread`, `NewProcThreadAttributeList`, `GenerateConsoleCtrlEvent` and `CREATE_NEW_PROCESS_GROUP` [XSYS]. But `syscall.StartProcess` does `defer CloseHandle(Handle(pi.Thread))` right after `CreateProcess` and returns only the PID and process handle [GO `exec_windows.go` lines 438–450]. So passing `CREATE_SUSPENDED` through `os/exec` leaves no thread handle to resume. A Go implementation has three choices:
- assign to the job right after `Start()` and accept a small race;
- call `CreateProcess` directly via x/sys and lose `os/exec` conveniences for that platform;
- put Öge itself in a kill-on-close job at startup. Since Windows 8, children inherit the parent's job (nested jobs), so there is no race. This is the simplest option. (Inference from Win32 job semantics; not verified on Windows here.)

**Rust.** `process-wrap`'s `JobObject` wrapper: "Internally the `JobObject` wrapper always sets the `CREATE_SUSPENDED` flag, but as it is able to access the `CreationFlags` value it will either resume the process after setting up, or leave it suspended if `CREATE_SUSPENDED` was explicitly set" [docs.rs/process-wrap 10.0.1]. The same `CommandWrap` code takes `ProcessGroup` on Unix and `JobObject` on Windows behind `#[cfg]` (the [PROBE] binary is written this way and builds).

**Edge: Rust, moderate.** This is the one place where Rust has a maintained, race-free, cross-OS library and Go needs custom Windows code. It only matters if Windows support is near-term. The "job for self" workaround narrows the gap a lot.

### 4. Signal handling / Ctrl-C

**Go.** `signal.Notify`, `signal.NotifyContext` (Go 1.16). On Windows, Ctrl-C and Ctrl-Break become `os.Interrupt`; `CTRL_CLOSE_EVENT`, `CTRL_LOGOFF_EVENT` and `CTRL_SHUTDOWN_EVENT` arrive as `syscall.SIGTERM`. Writes to a closed stdout/stderr pipe exit the program unless SIGPIPE is handled; other fds get `EPIPE` [GO, pkg.go.dev/os/signal]. The SIGPIPE rule matters for Öge: a dead agent's stdin pipe yields `EPIPE`, not a crash.

**Rust.** `tokio::signal::ctrl_c()`, `tokio::signal::unix::signal(SignalKind::…)`, and `tokio::signal::windows::{ctrl_c, ctrl_break, ctrl_close, ctrl_logoff, ctrl_shutdown}`. On Windows, once a handler is registered "the handler stays" for the process lifetime [docs.rs tokio::signal]. Rust ignores SIGPIPE by default, so writes return `EPIPE` errors.

**Edge: parity.**

### 5. Cancellation

Go threads `context.Context` through the stdlib, including `exec.CommandContext`. Rust uses `tokio_util::sync::CancellationToken` (tokio-util 0.7.19) plus `tokio::select!`. Dropping a future cancels it, which is powerful but easy to get wrong around half-written state. **Parity.** The idioms differ but neither is missing anything Öge needs.

### 6. PTY (fallback only)

**Go.** `creack/pty` v1.1.24 (2024-10-31, MIT) is Unix-only: `start_windows.go` returns `nil, ErrUnsupported` [GitHub source]. For Windows ConPTY: `aymanbagabas/go-pty` v0.2.3 (2026-05-17, MIT, "Cross platform Go Pty interface") and `charmbracelet/x/xpty` v0.1.4 (2026-07-30; has `conpty_windows.go`).

**Rust.** `portable-pty` 0.9.0 (crates.io, published 2025-02-11; part of the wezterm repo): traits with Unix and ConPTY implementations, chosen at runtime [docs.rs/portable-pty].

**Edge: slight Rust**, and only if a PTY path ships on Windows. PTY is a fallback for Öge, so this should not weigh much.

### 7. JSON-RPC 2.0 / JSONL over stdio

Codex app-server and ACP are JSON-RPC 2.0 over newline-delimited stdio; Claude Code `stream-json` is plain JSONL. The work is: line framing, request-id correlation, server-initiated requests (approvals), and tolerating unknown fields and methods. The [CODEX] research found that the live protocol adds fields that are missing from the schema.

**Go.** No stdlib JSON-RPC 2.0 (`net/rpc/jsonrpc` is 1.0). `golang.org/x/exp/jsonrpc2` (pseudo-version 2026-09-08, experimental). `sourcegraph/jsonrpc2` v0.2.3 (2026-09-10, MIT, has `NewPlainObjectStream` for newline framing). `modelcontextprotocol/go-sdk` v1.8.0 has its own internal jsonrpc2. `encoding/json/v2` and `encoding/json/jsontext` are **GA in Go 1.27**: stricter defaults (reject invalid UTF-8, duplicate names), faster unmarshal, and `encoding/json` is now backed by v2 [go.dev/doc/go1.27].

**Rust.** `serde_json` 1.0.151; `#[serde(tag = "method", content = "params")]` enums model method unions directly. `jsonrpsee` 0.26.1 (Parity) is HTTP/WebSocket-centric; custom stdio transports exist but it is heavyweight for this. `lsp-server` 0.10.0 (rust-analyzer) and `async-lsp` 0.2.4 show the hand-rolled-over-stdio pattern.

**Edge: parity.** Most real projects hand-roll this layer in either language. The bigger difference is in typing the payloads (§8).

### 8. Typed bindings from JSON Schema / TS types

Codex publishes its app-server protocol as draft-07 JSON Schema (`codex app-server generate-json-schema`, marked `[experimental]`; also committed upstream under `codex-rs/app-server-protocol/schema/{json,typescript,precomputed}`) [CODEX]. The v2 bundle `codex_app_server_protocol.v2.schemas.json` (597 KB) has **638 definitions, 54 `oneOf`, 218 `anyOf`, 41 `allOf`** and 15 `additionalProperties: false` objects [PROBE, counted with jq/grep].

**Go.** `omissis/go-jsonschema` v0.24.1 (2026-08-01, MIT) is the main generator. Its README feature checklist leaves **`allOf`, `anyOf`, `oneOf`, `not`, `if/then/else`, `const`** unchecked. The checklist may lag the code, but that was not verified because there is no Go toolchain. Go also has no sum types, so a `oneOf` keyed on `method` or `type` becomes an interface plus a hand-written `UnmarshalJSON` switch, whatever the generator does. `invopop/jsonschema` v0.14.0 and `google/jsonschema-go` v0.4.3 go the other way (Go → schema). Expect a hand-written or custom-generated type layer for the tagged unions.

**Rust.** `typify` 0.8.0 (oxidecomputer, 2026-09-28, Apache-2.0): `typify::import_types!(schema = "schema.json")` on the **unmodified v2 bundle compiled in 32 s (debug)**. It emitted `ClientRequest`, `ServerNotification`, `ThreadStartParams` and `TurnStartParams` as usable types [PROBE]. (Runtime parsing of real Codex traffic was not tested. Note that typify maps `additionalProperties: false` to strict structs, which conflicts with "tolerate unknown fields" on those 15 objects.) Codex itself is written in Rust. The upstream `codex-app-server-protocol` crate is **not published by OpenAI**: it is workspace-internal and depends on about a dozen `codex-*` workspace crates (`codex-protocol`, `codex-rollout`, `codex-secrets`, `rmcp`, …) [openai/codex `codex-rs/app-server-protocol/Cargo.toml`]. A git-tag dependency is possible but drags much of the Codex workspace in. The `codex-app-server-protocol`/`codex-protocol` crates on crates.io (0.63.0, 2025-12-11) come from a **third-party fork (`namastexlabs/codex`)**. Do not use them.

**Edge: Rust, material.** It is the only side where a generator was shown to handle Öge's most important schema, and its enums fit the protocol's tagged unions. Go can certainly be made to work, at the cost of hand-maintained union decoding that has to track a schema that changes per Codex release.

### 9. ACP SDKs

The `agentclientprotocol` GitHub org has official SDKs in **Rust (`rust-sdk`; crate `agent-client-protocol` 2.2.0, 2026-09-18), TypeScript, Python, Kotlin, Java**, all Apache-2.0. There is **no Go SDK** in the org. `coder/acp-go-sdk` v0.13.5 (Apache-2.0, 239 stars, last push 2026-06-05) is a community SDK. The org's `claude-agent-acp` and `codex-acp` adapters are **TypeScript**, so if Öge reaches either agent via ACP there is a Node process in the chain whatever language Öge uses [REG].

**Edge: Rust, moderate**, and only if ACP is chosen as an adapter transport (see the ACP / adapter-architecture tickets).

### 10. Claude Code integration libraries

Anthropic's GitHub org has `claude-agent-sdk-python` and `claude-agent-sdk-typescript` and no Go or Rust SDK. The crates.io `claude-agent-sdk` 0.1.1 (2025-09-30) lists `github.com/anthropics/claude-agent-sdk-rust` as its repository, but that repo returns 404. Treat it as unofficial. **Parity**: both languages talk to `claude` CLI's stream-json directly.

### 11. SQLite

**Go.**
- `modernc.org/sqlite` v1.60.1 (2026-09-29; canonical source gitlab.com/cznic/sqlite, the GitHub `cznic/sqlite` mirror is archived): C SQLite 3.53.4 transpiled to Go, **no CGO**. darwin amd64/arm64, linux (8 arches), windows 386/amd64/arm64, BSDs. BSD-3-Clause. Its own benchmarks put it 1.3–2.0× slower than C SQLite on heavy queries [pkg.go.dev/modernc.org/sqlite].
- `ncruces/go-sqlite3` v0.35.6 (2026-09-23, MIT): "a `cgo`-free SQLite wrapper", builds SQLite to Wasm and "uses wasm2go to translate it to Go", with its own pure-Go VFS. Tested on Linux, macOS, Windows, BSDs, illumos [README].
- `mattn/go-sqlite3` v1.14.52 (2026-09-05): "is cgo package … you need gcc"; cross-compiling needs a target C toolchain [README].
- `zombiezen.com/go/sqlite` (ISC; wraps modernc, last release 2025-05) for a non-`database/sql` API.

**Rust.**
- `rusqlite` 0.40.2 (2026-08-08, MIT). The `bundled` feature compiles SQLite 3.53.2 via the `cc` crate, so a C compiler for each target is needed. Sync API; MSRV is "latest stable Rust version at the time of release" [README].
- `sqlx` 0.9.0 (2026-05-21). The repo has moved from `launchbadge/sqlx` to **`transact-rs/sqlx`**. Async, with compile-time-checked queries.

For Öge's load (a run log written by one process, low volume), SQLite speed is irrelevant. What counts is build and cross-compile friction.

**Edge: Go, moderate.** A CGO-free SQLite keeps `GOOS=… go build` working from any host. In Rust, `bundled` needs a C toolchain per target. That is trivial on native CI runners (what `dist` uses) or with `cargo zigbuild`, but it is a real moving part.

### 12. CLI framework and interactive prompts

**Go.** `spf13/cobra` v1.10.2 (2025-12-03, Apache-2.0): subcommands, completions. `charm.land/huh/v2` v2.0.3 (2026-03-10, MIT): forms, select, confirm, with an accessible mode. `charm.land/bubbletea/v2` v2.0.10 (2026-09-24) and `bubbles` v2.2.1 if a richer TUI is ever needed. Charm v2 modules moved to the `charm.land/...` import path.

**Rust.** `clap` 4.6.7 (2026-09-14, derive API, completions via `clap_complete`). `dialoguer` 0.12.0 (crates.io 2025-08-23; repo active), `inquire` 0.9.4 (2026-02-24). `ratatui` 0.30.2 (2026-06-19) for full TUIs (out of MVP scope).

**Edge: parity.** Charm's huh is the more polished prompt library, but the MVP rules out an elaborate TUI, and the Rust prompt crates cover confirm/select/input.

### 13. Git

Öge needs worktrees, status, diff, commit, branch, and possibly merge/cherry-pick of verified work.

**Go.** `go-git` v5.19.2 (2026-07-29); v6 is still alpha (`v6.0.0-alpha.5`). The `COMPATIBILITY.md` (main, 2026-10-04) says: `merge` "Fast-forward only"; `stash`, `rebase`, `apply`, `mergetool` ❌; `cherry-pick` partial; `worktree add` partial, "via the `x/plumbing/worktree` package".

**Rust.** `gix` 0.88.0 (2026-09-25). The README checklist has `merge` (commits) unchecked, `rebase` unchecked, `reset` unchecked, `push` unchecked, and `commit` without hooks. `git2` 0.21.0 (libgit2 bindings, C dependency).

**Edge: parity.** Neither pure library covers worktree plus merge plus hooks. Both languages should **shell out to the user's `git`**. That also respects user config, hooks and credential helpers, which matters for "Öge controls the process, not the credential".

### 14. Cross-compilation and single-binary packaging

**Go.** A CGO-free program cross-compiles with `GOOS`/`GOARCH` from one host. GoReleaser v2.18.2 (2026-09-17, MIT) builds archives, Homebrew taps, nfpm packages, checksums and signing. CGO is the exception: "Compiling with CGO is tricky, especially when cross-compiling" (fixes: Pro split/merge, Docker cross images, Zig) [goreleaser.com/limitations/cgo]. Picking a CGO-free SQLite (§11) avoids it.

**Rust.** `dist` (cargo-dist) v0.33.0 (GitHub release 2026-09-11; crates.io shows 0.32.0) "generates its own CI scripts": per-target builds on GitHub Actions, with shell/PowerShell/Homebrew/MSI/npm installers and an updater [axodotdev cargo-dist book]. Cross builds from one host use `cross` (Docker; last release v0.2.5, 2023-02, repo still active) or `cargo zigbuild`. GoReleaser has supported Rust via `cargo zigbuild` since v2.5, with caveats: "will not install Cargo, Rustup, Zig, or cargo-zigbuild", and workspaces "might not work".

**Edge: Go, slight.** Both produce single binaries for macOS/Linux/Windows through GitHub Actions. Go additionally makes local cross-builds trivial.

### 15. Binary size and startup

**Rust [PROBE]** (aarch64-apple-darwin, rustc 1.95.0, `--release`):
- hello world: 431,200 bytes (not stripped);
- probe binary with tokio (rt-multi-thread, process, signal, io-util), clap derive, serde/serde_json, rusqlite `bundled`, process-wrap (ProcessGroup + KillOnDrop), `strip = true`: **3,034,560 bytes**. Dynamic deps: `/usr/lib/libSystem.B.dylib` and `/usr/lib/libiconv.2.dylib` only. `probe --help` ran in about 0 ms wall (`time -p` resolution). The probe spawned a child in its own process group, parsed a JSON-RPC line from its stdout, and printed it. 68 crates compiled.

**Go.** Not measured. The only primary-source number found is the Go 1.27 note that size-specialised allocation routines add about 60 KB. Go binaries embed the runtime, and modernc SQLite is large, so expect a larger binary than Rust's. That has not been quantified here (open question).

**Edge: unknown, probably irrelevant.** Both are in the "few-MB single binary, instant start" class that matters for a CLI.

### 16. WebSocket / SSE (later)

**Go.** `net/http` handles SSE with `http.Flusher`, no library needed. `coder/websocket` v1.8.15 (2026-06-15, ISC; formerly nhooyr). `gorilla/websocket` last release v1.5.3 (2024-06-14).
**Rust.** `tokio-tungstenite` 0.30.0 (2026-07-11); axum/hyper for SSE.
**Edge: parity.**

### 17. Local toolchains (2026-10-04)

- `go`: not installed (as charted).
- Rust: `cargo 1.98.1 (Homebrew)` is first on `PATH`, and **Homebrew `rustc` 1.98.1 fails to start**: `libLLVM.dylib` (llvm 22.1.6) wants `/opt/homebrew/opt/z3/lib/libz3.4.15.dylib`, but z3 is now 5.1.0. `rustup` is installed with `stable-aarch64-apple-darwin` = rustc **1.95.0**, which works only when selected explicitly (`RUSTC=~/.rustup/toolchains/stable-aarch64-apple-darwin/bin/rustc ~/.rustup/toolchains/.../cargo build`), because Homebrew's binaries shadow rustup's. Current stable is 1.99.0. Fix with `brew reinstall llvm` or `brew uninstall rust` and let rustup own the toolchain, then `rustup update`. Any Rust prototype ticket needs this first.

## Library inventory

Collected 2026-10-04 from GitHub (`license`, `pushed_at`, latest release) and crates.io / proxy.golang.org.

| Library | Lang | Latest | Date | License | Notes |
|---|---|---|---|---|---|
| os/exec, syscall, os/signal | Go | go1.27.1 | — | BSD-3 | stdlib |
| golang.org/x/sys | Go | v0.48.0 | 2026-08-31 | BSD-3 | Windows job/ctrl APIs |
| creack/pty | Go | v1.1.24 | 2024-10-31 | MIT | Unix only |
| aymanbagabas/go-pty | Go | v0.2.3 | 2026-05-17 | MIT | ConPTY |
| charmbracelet/x/xpty | Go | v0.1.4 | 2026-07-30 | MIT | ConPTY |
| golang.org/x/exp/jsonrpc2 | Go | pseudo 2026-09-08 | 2026-09-08 | BSD-3 | experimental |
| sourcegraph/jsonrpc2 | Go | v0.2.3 | 2026-09-10 | MIT | |
| modelcontextprotocol/go-sdk | Go | v1.8.0 | 2026-09-04 | — | MCP; internal jsonrpc2 |
| omissis/go-jsonschema | Go | v0.24.1 | 2026-08-01 | MIT | schema → Go |
| coder/acp-go-sdk | Go | v0.13.5 | 2026-06-02 | Apache-2.0 | community ACP |
| modernc.org/sqlite | Go | v1.60.1 | 2026-09-29 | BSD-3 | no CGO; SQLite 3.53.4 |
| ncruces/go-sqlite3 | Go | v0.35.6 | 2026-09-23 | MIT | no CGO (wasm2go) |
| mattn/go-sqlite3 | Go | v1.14.52 | 2026-09-05 | MIT | CGO |
| spf13/cobra | Go | v1.10.2 | 2025-12-03 | Apache-2.0 | |
| charm.land/huh/v2 | Go | v2.0.3 | 2026-03-10 | MIT | |
| charm.land/bubbletea/v2 | Go | v2.0.10 | 2026-09-24 | MIT | |
| go-git/go-git | Go | v5.19.2 (v6 alpha.5) | 2026-07-29 | Apache-2.0 | |
| goreleaser | Go | v2.18.2 | 2026-09-17 | MIT | |
| coder/websocket | Go | v1.8.15 | 2026-06-15 | ISC | |
| tokio | Rust | 1.53.2 | 2026-10-03 | MIT | |
| tokio-util | Rust | 0.7.19 | 2026-07-21 | MIT | CancellationToken |
| process-wrap | Rust | 10.0.1 | 2026-09-23 | Apache-2.0/MIT | successor to command-group (5.0.1, 2023) |
| nix | Rust | 0.31.3 | 2026-05-11 | MIT | |
| portable-pty | Rust | 0.9.0 | 2025-02-11 | MIT | ConPTY + Unix |
| serde_json | Rust | 1.0.151 | 2026-07-20 | MIT/Apache-2.0 | |
| jsonrpsee | Rust | 0.26.1 | 2026-09-30 | MIT | |
| typify | Rust | 0.8.0 | 2026-09-28 | Apache-2.0 | schema → Rust; Codex schema OK |
| schemars | Rust | 1.2.2 | 2026-07-27 | MIT | Rust → schema |
| agent-client-protocol | Rust | 2.2.0 | 2026-09-18 | Apache-2.0 | official ACP SDK |
| rusqlite | Rust | 0.40.2 | 2026-08-08 | MIT | bundled SQLite 3.53.2 |
| sqlx | Rust | 0.9.0 | 2026-05-21 | Apache-2.0/MIT | now transact-rs/sqlx |
| clap | Rust | 4.6.7 | 2026-09-14 | Apache-2.0/MIT | |
| dialoguer | Rust | 0.12.0 | 2025-08-23 | MIT | |
| inquire | Rust | 0.9.4 | 2026-02-24 | MIT | |
| ratatui | Rust | 0.30.2 | 2026-06-19 | MIT | |
| gix | Rust | 0.88.0 | 2026-09-25 | Apache-2.0/MIT | |
| git2 | Rust | 0.21.0 | 2026-05-18 | Apache-2.0/MIT | libgit2 (C) |
| dist (cargo-dist) | Rust | v0.33.0 | 2026-09-11 | Apache-2.0/MIT | |
| tokio-tungstenite | Rust | 0.30.0 | 2026-07-11 | MIT | |

## Implications for the Go vs Rust decision

1. **Most of Öge's workload is parity.** Subprocess supervision, Unix process groups, signals, cancellation, JSON over stdio, CLI/prompts, git (shell out in both) and WebSocket/SSE all have mature answers in both ecosystems. Neither language is blocked on anything.
2. **Rust's real edges are in the agent-protocol layer**:
   - (a) Codex's schema is generated from Rust types. `typify` handles it as-is, and Rust enums match its tagged unions. Go needs hand-maintained union decoding that has to follow a schema that changes per release.
   - (b) The official ACP SDK exists for Rust and not for Go.
   - (c) Race-free Windows process-tree control is a library call (`process-wrap`).

   (a) is a recurring maintenance cost, so it is the most material. (b) matters only if ACP becomes a transport. (c) matters only when Windows becomes real, and Go has a cheap workaround (Öge in its own kill-on-close job).
3. **Go's real edges are in build and ship**:
   - a CGO-free SQLite (modernc / ncruces) keeps one-host cross-compilation trivial;
   - GoReleaser is the most complete packager.

   Rust closes most of this with `dist` on native CI runners, at the cost of a C compiler per target for bundled SQLite.
4. **Nothing found here overturns a Go default on hard capability grounds.** If the decision rests on the protocol layer (typed Codex/ACP bindings tracking upstream), the evidence leans Rust. If it rests on build simplicity and keeping concurrency code simple, it leans Go. #15 should weigh those two, not the parity rows.
5. **What would change this:**
   - a Go generator shown to handle the Codex v2 bundle's `oneOf`/`anyOf` (cheap to test once Go is installed);
   - an official Go ACP SDK;
   - the adapter decision dropping ACP;
   - Windows being pushed far beyond the MVP.

## Open questions

- **Go codegen on the real Codex schema:** does `go-jsonschema` v0.24.1 (or another tool, e.g. quicktype from the TS output) produce usable types for the v2 bundle? Its README says no `oneOf`/`anyOf`. Needs a Go toolchain; a 30-minute check would settle the largest discriminating row.
- **Go binary size and startup** with modernc SQLite + cobra + huh: unmeasured.
- **Windows job "for self" approach in Go**: verify that nested-job inheritance plus `KILL_ON_JOB_CLOSE` on Öge's own job kills agent trees on Öge's exit, including when Öge itself runs under a job (Windows Terminal, CI).
- **typify vs live traffic**: generated strict structs (`additionalProperties: false`, 15 objects) may reject fields the live Codex binary sends that are missing from the schema (the Codex research saw `thread.environments[]`, `thread.extra`). Check whether typify's settings can turn strictness off.
- **macOS orphan cleanup** (no `Pdeathsig`): which mechanism Öge uses is a design question for the failure/resume ticket, not a language one.
