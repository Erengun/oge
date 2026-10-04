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
