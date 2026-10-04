# Öge

Öge is a multi-agent engineering harness that coordinates independent roles across native coding agents and accepts work only on evidence it gathers itself.

## Language

### Trust and acceptance

**Evidence**:
A record of a command that Öge itself ran and its result, tied to a specific workspace revision.
_Avoid_: Proof, test results (when reported by an agent)

**Claim**:
Anything an agent asserts about its own or another's work (e.g. "tests pass"); recorded, never treated as Evidence.
_Avoid_: Self-assessment, report

**Oracle**:
The tests and test configuration that decide acceptance, protected from change by the roles being judged.
_Avoid_: Test suite (ambiguous), ground truth

**Held-out test**:
A part of the Oracle that the implementer never sees.
_Avoid_: Hidden test, secret test

### Running agents

**Launch profile**:
The Öge-owned, per-role flags, environment and policy used to start an agent, plus the configuration envelope the agent must report at startup. Anything outside the envelope is flagged, never silently accepted.
_Avoid_: Agent config, user settings

**Native adapter**:
The part of Öge that drives one agent over that agent's own richest documented protocol (Codex app-server, Claude stream-json).
_Avoid_: Driver, integration, wrapper

**Generic adapter**:
The part of Öge that drives any agent over ACP, used for agents without a materially richer native protocol.
_Avoid_: ACP bridge, fallback adapter

**Capability**:
A feature of an agent session (such as in-band approvals or non-terminal interrupt) that Öge may rely on only once it is negotiated for that session: declared by the adapter, reported by the agent where it can report, and enabled by the Launch profile.
_Avoid_: Feature flag, agent version (as a proxy)

**Host request**:
A request from an agent that needs an answer from outside the agent, such as a permission to act or a question to the user, with a typed set of allowed responses; answered by the user or, later, a decider.
_Avoid_: Permission prompt (too narrow), approval (when a question is meant)

**Degraded**:
Said of a session or its Evidence when an explicitly optional Capability was missing, with the exact lost guarantee stated; never the result of a missing required Capability.
_Avoid_: Best effort, partial
