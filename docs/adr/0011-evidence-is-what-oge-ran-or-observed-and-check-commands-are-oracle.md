---
status: accepted
---

# Evidence is what Öge ran or observed itself; Check commands are Oracle content; failures are attributed to their cause

[#23](https://github.com/Erengun/oge/issues/23) consolidates ADR-0005 to ADR-0010 into one Evidence model. A reader might expect any of five things this design rejects:

- Evidence meaning only "test output";
- whatever test command the agent suggests being run;
- exit code 0 meaning pass;
- every setup failure on a Candidate counting as the Candidate's fault;
- a hash-chained ledger being called tamper-proof.

**Evidence is widened.** It is a record Öge made itself of something it ran or directly observed. Kinds:

- command execution;
- Briefing manifest;
- Preflight observation;
- scope/revert observation;
- Tamper event;
- environment/capability observation.

Claims stay separate. Agent diffs, tool results, Exits, reviewer findings and Plans are all Claims. A Verdict comes only from a Check's command-execution Evidence, never from Claims or from observational Evidence alone.

- **The parent process captures command execution directly.** Öge records:
  - the argv and cwd;
  - the names of exposed environment variables, never their values;
  - the exit code, signal and timed-out flag;
  - timing;
  - redacted, capped stdout and stderr, with the raw byte count and raw hash;
  - refs to the Attempt, Stage or Check;
  - the Candidate commit and Oracle version;
  - the enforcement class of each path, and the uncontained marker;
  - versions of Öge, the OS/arch, the toolchain and the agents;
  - the observed envelope, the effective capabilities and any Degraded guarantee that was lost.

  A nonzero exit is data. Wherever possible, files the Candidate wrote are not trusted.
- **Check commands are part of the Oracle.** They are versioned, protected, pinned into Verdicts and covered by the tamper rules. An agent may propose a command, but only a human Gate adds one, because a new command means arbitrary code running in an uncontained Check. Held-out files that an existing command already collects follow the ordinary Oracle-growth limits.
- **What a pass needs.** A command passes when it exits 0 and, where a structured report is required or configured, the report exists, parses, shows at least one expected test ran and shows no test failed. Every Oracle command runs, and the Verdict is pass only if all of them pass.
  - Held-out commands must produce a structured report, because criterion-id feedback (ADR-0009) depends on it.
  - A missing, malformed or contradictory report fails the Check, unless Öge establishes that its own parser or infrastructure failed. That case is an Infrastructure stop.
  - The parser belongs to Öge. Agent text never substitutes for it.
- **Failures are attributed to their cause, not to the lifecycle step where they happened.** This applies to setup, build, dependency resolution, timeout, OOM and kill.
  - It works on the Snapshot in the same resolved environment but fails because of the Candidate's changes: fail Verdict.
  - It cannot be attributed to the Candidate (Öge fault, full disk, missing host resource, broken toolchain): no Verdict, Infrastructure stop.
  - It already fails on the Snapshot: Preflight refuses the Run.
- **Integrity is detection, not protection.** Content hashes and a hash-chained ledger exist to detect corruption and to keep audits and replay consistent. Uncontained Check code runs with the user's privileges, so it can rewrite both the content and the hashes. To limit that:
  - Öge takes a before/after integrity manifest of the critical private Run state around every uncontained Check.
  - An unexpected change gives an Infrastructure stop with a *compromised Evidence* condition, and that Run can never become Accepted.
  - Docs state that an unsandboxed Check can still evade detection by modifying state and restoring it.

  A cryptographically anchored ledger would need its trust anchor outside the filesystem the Check can write, so it waits for the post-MVP OS sandbox. The MVP ledger is never called tamper-proof or tamper-evident.
- **Redaction happens before anything is persisted, on a best-effort basis.**
  - Known-pattern redaction is applied to stdout and stderr before they are written. No unredacted copy is kept, even for debugging.
  - Only the raw byte count and hash are kept from the raw stream.
  - The Evidence names the redaction rules that ran.
  - Docs say pattern redaction cannot guarantee that arbitrary secrets are removed. ADR-0006 forbids reading credential values, so value-based scrubbing is impossible.
- **For reverted writes, Öge keeps:**
  - the path;
  - hashes before and after;
  - the size;
  - a size-capped, redacted textual patch.

  Binary content is never kept. When the patch is left out, the Gate still gets the path, hashes, sizes and the reason. The reverted material is Evidence of an observed mutation, not a Claim.

## Consequences

- The Candidate is a commit Öge makes in the Run repository, and the implementation diff is Candidate vs Snapshot. Verifier additions are deltas between Oracle versions, kept in private state. Implementer-authored tests run in a Candidate copy without the Oracle overlay and are labelled.
- Config (#27) must declare, for each Check command, its structured-report format and expected tests (required for held-out commands), along with toolchain probe commands, redaction rules and output caps. Gates (#24) need a choice, with a reason, for adding a proposed Check command.
- Failure handling (#25) gains the attribution rule and the compromised-Evidence Infrastructure stop.
- The final report shows:
  - the outcome;
  - each Verdict with its pins;
  - Tamper events, reverts and Ambiguous resolutions;
  - Overridden, freeze, flaky, Extension and compromised-Evidence flags, shown prominently;
  - implementer-authored results, labelled;
  - the uncontained warning;
  - the Briefing manifests.

## What would reverse this

- Verifiers routinely need new commands, so the command Gate becomes noise. Then admit commands matching a declared template under the growth limit.
- Common toolchains cannot emit structured reports for held-out tests.
- #31 shows the Snapshot-comparison rule often misattributes environment flakiness as Candidate failures, or the reverse.
- OS-level Check sandboxing lands. Then the private state can be made unwritable by Checks, and stronger integrity claims become possible.
- False redactions routinely break diagnostics.
