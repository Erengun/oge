---
status: accepted
---

# Write Öge in Go

Öge is written in Go. The default was Go unless Rust showed a concrete architectural advantage big enough to justify the extra complexity, and none survived research. For Öge's workload (supervising agent subprocesses, JSONL/JSON-RPC over stdio, signals, CLI, a small local store, shelling out to `git`) the two ecosystems are at parity ([#11](https://github.com/Erengun/oge/issues/11)). Go is better for building and shipping: a cgo-free single binary cross-compiles from one host and GoReleaser packages it. Go also reaches more contributors to an open-source project. Rust's edges are smaller and sit at the protocol layer:

- **Typed Codex bindings.** typify compiles the Codex app-server schema, and no Go generator does. But hand-writing the MVP subset is bounded: about 1 day, then under 1h per Codex release ([#33](https://github.com/Erengun/oge/issues/33)). Hand-written decoding also tolerates wire drift by default, while typify's strict structs do not. The Claude stream-json protocol is hand-written in either language, because neither OpenAI nor Anthropic publishes a usable Rust or Go client.
- **Official ACP SDK.** This would matter only if ACP were central. For MVP it isn't: Codex and Claude Code are driven natively, and ACP is the generic path for post-MVP agents ([#3](https://github.com/Erengun/oge/issues/3)).
- **Race-free Windows tree-kill** via `process-wrap`. This matters only once Windows is supported, and Go has a workable design (below).

## Consequences

- **Protocol types.** Öge uses hand-written MVP protocol types and tolerant decoding. It records both a minimum supported Codex version and a last-tested Codex version, and CI flags upstream schema drift. Unknown additive fields and messages are logged and stay non-fatal where safe.
- **ACP (post-MVP).** Öge implements the smallest ACP v1 client that its actual adapters and negotiated capabilities require, written against the ACP specification. Community Go SDKs (coder/acp-go-sdk, ironpark/acp-go) can serve as implementation references; Öge does not depend on them.
- **Build invariant.** `CGO_ENABLED=0` is enforced in CI for every release target. If persistence uses SQLite, the driver must therefore be cgo-free. Which driver is the persistence decision's call.
- **Go version.** `go 1.27` in `go.mod`. Öge follows Go's own N / N-1 support window and raises the floor when a release leaves support.
- **Process control.**
  - On Unix, each agent runs in its own process group: SIGTERM to the group, then SIGKILL after a grace period.
  - Process-control code is split by build tags.
  - On Windows, the intended design is that Öge creates a Job Object with kill-on-close semantics and assigns spawned agent processes to it. This does not require Öge itself to be assigned to that Job Object.
  - Windows remains cross-build-only and unsupported for MVP: CI cross-compiles `GOOS=windows`, but nothing runs or tests there.

## What would reverse this

1. Hand-maintaining the Codex/Claude protocol types costs well over the ~1h-per-release estimate for several releases running, or drift bugs keep reaching users despite the CI drift check.
2. ACP becomes the primary transport for MVP-class agents, for example because native Codex/Claude protocols are deprecated in favour of ACP. The official Rust SDK would then carry real weight.
3. Windows becomes first-class and the Job Object design cannot be made reliable in Go.
4. OpenAI or Anthropic publishes a first-party Rust client for their agent protocols with no Go equivalent.
