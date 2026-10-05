---
status: accepted
---

# Protected-test execution attestation is required Evidence; the structured report is diagnostic

[#72](https://github.com/Erengun/oge/issues/72) came out of the review of the walking skeleton (#40, PR #70). A Candidate made a broken change Accepted by printing fake `go test -json` PASS frames from `init()` and exiting 0 before any protected test ran. Checks run Candidate code without containment (ADR-0010), so no report parser can close this.

A reader might expect one of two things this design rejects:

- disclosing report forgery in the Receipt's "Not covered" line and leaving it as a known path to Accepted;
- claiming the MVP isolates hostile code.

## The acceptance-boundary rule

**Anything that can turn a broken Candidate into Accepted is an acceptance-boundary issue first, not a "Not covered" item.** "Not covered" is for threats outside the stated boundary. It must never list easy ways to counterfeit the exact Evidence Öge uses to accept.

## Threat model

> Öge is designed to catch ordinary agent mistakes, reward hacking, test manipulation and straightforward attempts to bypass verification. The MVP does not claim isolation against deliberately hostile Candidate code executing with the user's OS privileges.

A determined hostile program running in the same process can still inspect memory, file descriptors or binary contents and evade the measures below. The Receipt's "Not covered" line says so. OS and process isolation is what changes that boundary. It is post-MVP and is not attempted in M1.

## Decisions

- **Execution attestation.** It applies to every Check on a Candidate.
  1. After the Candidate is frozen, Öge generates unpredictable per-Check, per-test attestation values.
  2. They exist only in Öge's protected test overlay, never in the Candidate, the Briefing or the Workspace.
  3. Each protected test emits its expected attestation when it actually executes.
  4. Attestations travel over a dedicated channel owned by Öge as the parent process, not stdout/stderr and not the structured test report.
  5. Öge knows the complete expected set of protected tests before launch, derived from the Oracle manifest.
  6. **A missing attestation means the Check cannot pass,** even with exit code 0, PASS on stdout or a successful structured report.
- **The structured report is diagnostic Evidence. Attestation is required Evidence.** The pass rule (ADR-0011) becomes: every command exits 0, every expected protected test is attested, and none is reported failing.
- **Attestation-mechanism failure is attributed to Öge.** If attestation fails on the Snapshot control case, the failure is an infrastructure/harness failure, not a Candidate failure (ADR-0011 attribution).
- **Static tripwires are signals, not proof.** Öge flags Candidate changes to obvious test-process control points:
  - `TestMain`;
  - process termination (`os.Exit`, `syscall.Exit`, `runtime.Goexit`) reachable from `init`;
  - writes to the attestation channel's descriptor.

  A tripwire is recorded as an Observation and shown in the Receipt. It never claims completeness or intent.
- **Skips are baseline-relative** (decided on [#73](https://github.com/Erengun/oge/issues/73)). For each Oracle version, Öge runs the same protected tests against the immutable Snapshot control. It uses the same setup, environment policy and attestation as the Candidate Check, and compares test identity, not counts.

  | Snapshot | Candidate | Result |
  |---|---|---|
  | runs | runs | normal judgement |
  | runs | skips | cannot pass |
  | skips | skips | allowed, listed prominently under "Not covered"; no positive Evidence |
  | skips | runs | normal judgement |

  - Rule: a skip is never a pass. Öge may accept with an environment-dependent coverage gap only when the same gap already existed in the Snapshot control, so the Candidate can't create a new one.
  - An unreliable control run is an Infrastructure or Oracle-validity problem, not permission to ignore the test.
  - With `verify = "after"`, the control runs after the verifier produced the tests, against the Snapshot, while the tests stay hidden from the implementer.
  - The control runs on the same host while the Attempt runs, so the agent could influence an environment-dependent skip. That would take deliberately hostile action, which is outside the threat model.
- **Buildability is baseline-relative.** The Candidate never decides which protected tests count.

  | Case | Result |
  |---|---|
  | A protected test starts on the Snapshot control | Required on the Candidate: it must run and attest |
  | The Snapshot's fixed build context excludes it (GOOS/GOARCH, filename platform suffixes, `//go:build`, the tags the Check commands name) | Not covered for this Run, listed with the file and the reason |
  | The Candidate turns a Snapshot-runnable test into an excluded or missing one | Cannot pass |
  | Protected test source exists, but nothing protected starts on the Snapshot | Fail closed: an empty expected set never produces Accepted |
  | The Snapshot doesn't compile | The expected set comes statically from the protected source under that fixed build context. Once the Candidate builds, every statically eligible test must execute and attest, and statically excluded tests are Not covered |

  - The comparison uses the same build environment and the same Oracle version.
  - An eligible test that starts on neither side (for example, an Oracle `TestMain` that runs nothing, or a runner that closes inherited descriptors) is an Oracle-validity or Infrastructure stop, never a Candidate failure.
- **Subtests and Examples.**
  - A subtest the report shows skipping on the Candidate cannot pass if it ran on the Snapshot control. The report is trusted only in that direction.
  - Subtests whose case table comes from Candidate code are out of scope: they are a question of Oracle quality, and attestation covers only the protected test that runs them.
  - Each Example with an output comment attests through a generated test. That test runs the Example, captures and compares its output the way `go test` does, and only then attests.
- **Evaluation.** The forged-report exploit becomes an evaluation fixture now (#58), to measure whether real coding agents discover or use this path.

## Consequences

- This amends ADR-0011's pass rule and adds attestation to the Check Evidence.
- New ticket: protected-test execution attestation. It blocks the Receipt, because M1's `✓ Accepted` has to mean this.

## What would reverse this

- Attestation proves routinely brittle on real projects (generated tests, build tags, unusual runners) without catching real agent behaviour in the evaluation. Then simplify, but never to "report only".
- Process isolation ships. Then attestation may be simplified to fit the stronger boundary.
