---
status: accepted (Codex topology, envelope check and interrupt guard amended in part by ADR-0018; Host-request policy (pre-authorise / auto-deny / ask) amended in part by ADR-0019)
---

# Agents sit behind a session-level adapter seam with negotiated capabilities, typed host requests and fail-closed degradation

Öge drives every agent through one session-level interface ([#18](https://github.com/Erengun/oge/issues/18)). `Open(LaunchSpec, resumeID?)` returns a session, and a session has `Send(turn)`, an event stream, `Interrupt()` and `Close()`. Process topology stays inside each adapter. A reader may expect four other things: an adapter that also manages the agent process, a common "allow/deny" permission prompt, every native message kept in the ledger, and a best-effort fallback when an agent lacks a feature. None of these was chosen, for the reasons below.

- **Session-level, not process + session.** Only Codex hosts many sessions per process, so two integrations do not justify a process level. The Codex adapter hides its topology completely, and no product or domain API may depend on it. It starts with one `codex app-server` per session and serialised start-up. This is provisional. Serialising start-up only mitigates refresh races at start-up, and the deferred Codex spike must test multi-thread/multi-worktree operation and later refreshes. If one process turns out to be safe, Öge moves to one app-server per run. If not, the remaining refresh-race risk is documented.
  - *Amended in part by [ADR-0018](0018-codex-adapter-native-app-server-one-per-run.md) ([#13](https://github.com/Erengun/oge/issues/13)):* the spike settled the topology as one app-server per Run, owned entirely by the adapter. `Interrupt()` is sent only for a known-running turn, every request has a client-side timeout, and "deny reason reaches the model" is false for Codex.
- **Capabilities are negotiated per session, not inferred from versions.** The effective set is what the adapter declares, intersected with what the agent reports (Claude `system/init.capabilities`) and what the Launch profile enables. Codex reports nothing, so a minimum-supported version is its only floor. An operation that proves unsupported at runtime turns that capability off for that session/adapter-version combination and is recorded. If the stage declared the capability required, the stage fails.
  - The MVP interface carries:
    - in-band host requests;
    - non-terminal interrupt;
    - resume;
    - whether a deny reason reaches the model.
  - Steer, fork, native review and subagent events are named but have no methods: fork would leak implementer history into the verifier, and native review runs the agent's prompt instead of Öge's briefing.
- **One host-request envelope, typed responses.** `HostRequest{kind, payload, response_schema}` replaces a binary permission prompt, because approvals (`allow` / `deny(reason)`) and questions to the user (`answer(value)` / `cancel`) are different families, and future kinds may need their own. Rules:
  - Approvals are per call. There is no "always allow".
  - Scope expansion is denied by default.
  - Pending requests are denied or cancelled on crash or close.
  - Today the user answers. Later a decider answers through the same seam.
- **Normalised core events, sanitised native capture.**
  - The orchestrator branches only on normalised events (session opened, turn accepted, turn settled, host request, claim, usage, warning, unknown). Accepted and settled are separate states.
  - Raw native frames are not stored verbatim by default. They are stored only after adapter-specific redaction and within size limits. Credential material never enters the ledger.
  - Unknown messages record their type and metadata.
- **Fail closed.**
  - Refuse when the stage or pipeline requires a capability, or when a trust property cannot be kept.
  - Degrade only for capabilities explicitly classified optional, with the Evidence naming the lost guarantee.
  - Emulate only with Öge-controlled mechanisms whose result Öge can verify, never by asking the model.
- **No PTY.** Transport tiers (native, generic/ACP, structured subprocess) are vocabulary, not types. An agent without a supported structured interface is out of scope, not screen-scraped.

## Consequences

- Every adapter's `Open` includes an envelope check against the Launch profile. For Claude the observed envelope is `system/init`. For Codex it is the `thread/start` response (`approvalPolicy`, `approvalsReviewer`, `sandbox`, `cwd`, `model`, `instructionSources`). `approvalsReviewer` is pinned to `user`.
  - *Amended in part by [ADR-0018](0018-codex-adapter-native-app-server-one-per-run.md):* the Codex `thread/start` response does not show read/deny carve-outs. Those properties are proven instead by a no-model `codex sandbox -P` self-test, cached per Codex version, platform, launch-profile hash and sandbox policy.
- The interface was checked on paper against ACP v1:
  - `initialize`/`session/new` map to `Open`;
  - `session/prompt` maps to `Send`/settled;
  - `session/cancel` maps to `Interrupt`;
  - `request_permission` maps to a host request.

  None of these relies on surfaces ACP v2 removes.
- Pipelines and stages must be able to declare capabilities as required or optional.

## What would reverse this

- **The deferred Codex spike contradicts the schema-based assumptions.** For example:
  - approval shapes or ordering don't fit the host request;
  - interrupt ends the thread;
  - `thread/start` doesn't reflect the effective config;
  - one process with many threads proves safe, which flips the topology to one per run.
- Refresh failures persist under serialised per-session start-up.
- Codex starts reporting capabilities in `initialize`.
- A third adapter needs process-level operations the orchestrator must drive.
- An MVP pipeline step genuinely needs steer, fork, native review or subagent events.
- Evidence or failure handling needs to branch on something the normalised core drops. The fix is to extend the core, not to open passthrough.
