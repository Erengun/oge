---
status: accepted
---

# Öge is tested fakes-first with zero model quota in CI; release binaries stay cgo-free; Windows cross-builds but refuses execution

[#32](https://github.com/Erengun/oge/issues/32) settles how Öge tests itself and what the MVP does on Windows. A reader might expect any of five things this design rejects:

- CI exercising real Claude or Codex sessions;
- a hidden test-only switch that bypasses the state-root hardening;
- `go test -race` proving that the shipped binary builds;
- Windows support claimed through the unverified Job Object design;
- `oge doctor` refusing to run on an unsupported platform.

## Decisions

- **Fakes first, and two kinds.**
  - An in-process, scripted fake adapter (`internal/agent/fake`) satisfies the same session contract as real adapters (ADR-0005). It is the primary vehicle for deterministic orchestration tests. Every orchestration slice is proven on it before a manual live run. It is a test implementation, never presented as a supported product Agent.
  - A fake `claude` executable replays recorded stream-json over real pipes. It exercises the real stdio, process and protocol boundary of the Claude adapter and the process-control code. These tests target protocol behaviour, not model prose.
- **Recorded sessions are re-redacted before they reach main.** They contain:
  - no credentials;
  - no personal paths where avoidable;
  - no held-out data from real user projects;
  - no unnecessary model prose.
- **Golden terminal output belongs to the tests.** `docs/ux/oge-run-transcript.md` stays explanatory and is not kept byte-for-byte in sync with the goldens.
- **Normal CI spends zero model quota.** Live-provider tests need explicit local or manual enablement, and CI rejects any accidental live-agent configuration. The recorded and fake-agent tests are the required CI path.
- **No secret bypass of path hardening.**
  - Unit and integration tests may inject filesystem and state dependencies through constructors.
  - Real-binary E2E tests use only supported inputs: the documented state-root override, or an isolated synthetic `HOME`/XDG environment.
  - Each E2E run gets a unique state root, cleaned afterwards where safe.
  - Every state-root refusal stays active.
- **Release binaries are `CGO_ENABLED=0`, and race testing is a cgo exception.**
  - A no-cgo job runs test, vet, staticcheck and govulncheck. It is what validates ADR-0001's build invariant.
  - A separate Ubuntu job runs `CGO_ENABLED=1 go test -race`, for testing only.
  - The cross-build matrix (linux, darwin, windows × amd64/arm64) is `CGO_ENABLED=0` and blocking, Windows included.
  - *Amendment (#99, CI cost):* the no-cgo checks and the six cross-builds run as one job, since each job pays its own setup. macOS runs on main pushes and on demand, not on PRs, and there is no nightly schedule. Docs-only changes skip CI, and a newer push cancels the run it supersedes.
- **Windows cross-builds but refuses execution in the MVP.**
  - `oge run` and every other command that needs execution or isolation semantics exits 2 on Windows.
  - `oge doctor` still runs there. It reports that Windows is unsupported for MVP execution and which capabilities are unavailable.
  - Windows unit tests may run as a non-blocking job.
  - ADR-0001's Job Object design stays the post-MVP plan and is not implemented to claim support.

## Consequences

- The full pre-registered evaluation (ADR-0016) runs only after resume and crash recovery exist, so a harness crash can't force expensive arms to restart. One-fixture evaluation smoke can start earlier.
- GoReleaser builds only the product binaries, so the `eval/` harness is never shipped.
- No public supported release happens until three things hold: the evaluation gate passes, provider-terms documentation exists, and the provider enquiries recorded on the map are done.

## What would reverse this

- Replayed recordings miss real-CLI regressions often enough that a scheduled, quota-aware live tier becomes necessary.
- A Windows user enters the MVP audience. Then the Job Object design has to be verified, not assumed.
- A needed dependency requires cgo. The invariant holds, so the dependency is replaced, not the rule.
