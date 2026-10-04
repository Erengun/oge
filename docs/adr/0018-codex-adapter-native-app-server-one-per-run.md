---
status: accepted
---

# Codex adapter: native app-server, one per Run; guarantees corrected by the spike

The Codex spike ([#13](https://github.com/Erengun/oge/issues/13); codex-cli 0.155.1, macOS) drove real turns through `codex app-server`. Branch `prototype/codex-app-server` holds the spike code and the redacted golden transcripts. Several assumptions in ADR-0005, 0006, 0009 and 0010 that came from the schema turned out wrong on the wire. This ADR fixes the Codex adapter's shape and the guarantees Öge may claim for it. Where it conflicts with those ADRs on Codex specifics, it amends them in part. A reader may expect three things this ADR rejects:

- ACP or `codex exec --json` as the simpler path;
- types generated from Codex's JSON Schema;
- the vendor sandbox enforcing everything the permission profile *says*.

## Decisions

- **Native `codex app-server` over stdio is the only Codex surface.** ACP (`codex-acp`) and `codex exec --json` are not used while they lose capabilities Öge requires: in-band approvals, interrupt short of a kill, `availableDecisions`, the `instructionSources` envelope and `serverRequest/resolved`.
- **Hand-written protocol subset.** Öge hand-writes only the messages it acts on.
  - Unknown or additive messages and fields become normalised `unknown` events. They are safe-redacted before persistence and never crash the adapter.
  - The wire already carries fields and methods the schema lacks: `environments`, `emittedAtMs`, `itemsView`, `thread/goal/cleared`, `deprecationNotice`, `remoteControl/*`.
  - The adapter keeps recorded protocol fixtures, a minimum supported version, a last-tested version, and CI that diffs `generate-json-schema` for those versions.
- **One app-server per Run, owned entirely by the adapter.** One process ran concurrent threads in separate clones with different profiles, and each was enforced independently. MVP Attempts are sequential, so normally only one Codex Attempt is active at a time.
  - If the app-server dies during an Attempt, that is an Attempt failure.
  - If it dies between Attempts, the adapter or infrastructure recovery path handles it.
  - The process is spawned in its own process group and killed as a group. Öge does not claim this contains every tool descendant: a SIGKILLed app-server left its tool child running. Escaped children are possible residue, handled by sweep and reporting (ADR-0012).
  - This settles ADR-0005's provisional per-session topology.
- **Interrupt and requests.**
  - Öge interrupts only a turn it knows is running (accepted, not settled), and treats `turn/completed{interrupted}` as the acknowledgement. `turn/interrupt` on an already-ended turn never gets a response.
  - Every protocol request gets a client-side timeout.
- **Approvals stay one-shot.** Öge sends only `accept`, `decline` or `cancel`, never `acceptForSession` or `acceptWithExecpolicyAmendment`, so there is no session-wide policy mutation.
  - Codex never expires a pending approval: 690 s of silence was observed. So no Öge Host-request timeout is required; ADR-0008 stands: none by default, and an optional per-pipeline timeout may only deny or cancel. (The client-side timeout above covers Öge's own protocol requests, not the wait for a human answer.)
  - The fileChange request carries only `itemId`. The adapter joins it with the earlier `item/started` to get the paths.
  - The capability "deny reason reaches the model" is **false** for Codex, and Evidence records this.
- **Envelope check.** The `thread/start` response does not prove everything.
  - It proves the approval policy, `approvalsReviewer`, `cwd`, model, `instructionSources` and write roots.
  - It does **not** show read or deny carve-outs, nor `extends`.
  - For what it cannot prove, such as deny-read of the private area and read-only test paths, Öge runs a no-model `codex sandbox -P <same inline profile>` self-test.
  - A result is reused only for the same effective combination of at least: Codex version/binary, platform, launch-profile hash, and relevant sandbox policy.
  - If a required isolation self-test fails, protected verification fails closed. An unloadable profile fails `thread/start` with `-32600`, which is also fail-closed.
- **Verifier write scope by enforcement class.** Codex 0.155.1 rejects glob *write* entries. Only `deny` may be a glob; writes need exact paths or a trailing `/**` subtree.
  - A supported test subtree is native, where proven.
  - Exact existing test files are native, where proven.
  - New colocated tests are **revert-only**.
  - Evidence records the enforcement class for each path. Revert-only is never described as sandbox enforcement.
- **Shell secrets.** In app-server turns, the default `shell_snapshot` feature defeats `shell_environment_policy`, whether set per thread or per process. This is a current capability limitation.
  - Verifier and reviewer threads set `features.shell_snapshot=false`, which makes the default secret excludes take effect. This is verified against the pinned/tested Codex version. A side effect is that the login shell's rc files run inside the sandbox.
  - The implementer keeps the tested normal behaviour, and shell credential exposure is reported as a launch-profile limitation.
  - Öge claims no stronger shell-secret isolation than the spike proved.
- **Trust entries are a Codex-owned side effect.** App-server adds `[projects."<cwd>"] trust_level="trusted"` to `~/.codex/config.toml` for every writable thread `cwd`, even for ephemeral threads.
  - A trust entry on a common parent, or on `~`, does not prevent per-Workspace entries.
  - Öge never writes or cleans these entries. `oge doctor` may report stale ones, and the docs explain the side effect.
- **Vendor resume is a capability, not an MVP behaviour.** After a SIGKILL mid-turn, `thread/resume` in a new process stayed coherent (n=1): the turn was marked `interrupted`, and the model's account matched the disk.
  - The adapter advertises `resume`, and recorded-session/integration tests cover it.
  - ADR-0016 still governs the MVP: resume, retry and send-back start a fresh Session from Öge's durable state, with no Codex exception.
  - When Session reuse returns, coherence is checked against Öge's Checkpoint, Candidate and diff. Codex's transcript or history is never authoritative. Its full-history load is deprecated, and the replacement `thread/turns/list` is experimental.
- **Confirmed as designed, with no change:**
  - ADR-0006's readiness probe writes nothing.
  - ADR-0009's isolation recipe gives `instructionSources: []` and `developerInstructions` take effect.
  - Reads of the private area, `~/.ssh` and `~/.codex/auth.json` are denied for shell in real turns.
  - Read entries inside a writable root are enforced: a patch touching them becomes an approval, and declining it drops the whole patch.
  - Network is off by default.
  - `codex sandbox -P` is usable as a Check sandbox (post-MVP), with network off and credential reads denied. `/tmp` stays writable under `:workspace`-based profiles.

## Consequences

- `fileChange` items and `turn/diff/updated` are Claims. The turn diff omits shell writes, and a failed patch emits no item. The post-Attempt filesystem comparison stays authoritative (ADR-0010).
- Domain-scoped network needs Codex's experimental `network_proxy` feature, and the domain list is silently ignored without it. Until that is adopted deliberately, Codex network is on/off only.
- **Requirements before claiming the guarantee** (they do not block the spec):
  - Linux sandbox and profile behaviour (globs, read-inside-write, deny-read, network, `/tmp`).
  - Fail-closed when a global `~/.codex/AGENTS.md` exists.
  - Plain `:read-only` + `on-request` with no write entries.
  - Per-thread network through app-server.
  - A real token refresh while several threads run.

## What would reverse this

- A Codex release breaks or removes the stable app-server methods used here, or OpenAI withdraws app-server auth for local/open-source clients.
- Refresh failures appear with one process per Run, or shared-fate crashes materially hurt fixture runs. Then go back to one app-server per Session with serialised start-up.
- Codex adds glob write entries, envelope reporting of read/deny entries, a decline reason, or a per-thread way to stop trust writes. Each upgrades the corresponding enforcement class or claim.
- Repeated kill-and-resume tests show incoherence. Then never advertise `resume` for Codex.
