---
status: accepted (mandatory Gate list amended in part by ADR-0013)
---

# Gates never resolve themselves, decisions are pinned, and unattended runs park

Gates are where a human makes decisions in a Run ([#24](https://github.com/Erengun/oge/issues/24), building on ADR-0007). Most tools offer `--yes`, auto-approve defaults or approval timeouts. A reader may expect at least one of them here. Öge has none, because each one records a human decision that no human made.

- **No Gate auto-resolves.** A Pipeline may leave out an *optional* Gate when the pipeline is frozen: the result Gate (on by default), a Gate after each failed Check, or the review Gate. A Gate that is present always waits for a decision. The decider never resolves a Gate, and escalation is not a Gate (#29).
  - These Gates are mandatory, and Pipelines cannot remove them:
    - tamper;
    - infeasible;
    - bound exhaustion;
    - Oracle growth;
    - the plan Gate, wherever a planner Stage exists.
    - *Amended in part by [ADR-0013](0013-constrained-toml-pipelines-compiled-into-the-frozen-graph.md) ([#27](https://github.com/Erengun/oge/issues/27)):* the Own-test-failure gate (failing Implementer-authored tests; taking the Candidate anyway is Overridden) and the Ambiguous-file gate (promotion of a withheld file forces a fresh verifier Attempt before the final Check) are mandatory too.
- **Choices are closed sets made of existing terms.** `send back` is a send-back Attempt, `regenerate` is a *user request* Attempt, `override` gives Overridden, `reject` gives Rejected, `quit` gives Cancelled. The choices that weaken trust need a reason: override, reject, confirm infeasible, amend or remove Oracle tests, freeze, extend, dismiss. `freeze` (stop Oracle growth for the rest of the Run) is allowed only as one of these decisions, and it is flagged in every report and evaluation.
- **A pass does not force taking the result.** A passing Verdict is necessary for Accepted but not sufficient when a result Gate exists. Applying the Candidate to the user's branch is a separate operation.
- **Decisions are pinned.** Each decision records:
  - the Gate;
  - the Attempt or Plan, Candidate revision, Oracle version, Verdicts and Tamper events it was shown;
  - the actor kind (`human`), with no identity (ADR-0006);
  - the time;
  - the choice with its parameters;
  - the reason.

  It is written durably before it takes effect, and it is replayed on resume only while everything it was pinned to is unchanged. Host-request answers are not Gate decisions, but they get their own record: `answered_by`, `escalated_by`, the time and which request.
- **A Recheck is capped, not budgeted.** Re-running the same Check on the same (Candidate, Oracle version) is not an Attempt.
  - It is allowed once per pin, and every Verdict is kept.
  - Disagreeing Verdicts record a flaky-Oracle signal and disable further Rechecks for that pin.
  - Without the cap, rechecks would let a human sample until a pass appears.
- **Extensions:**
  - a human grants them only, at a Gate;
  - +N to exactly one named limit;
  - they apply to the Run only;
  - they are pinned, need a reason and are reported.
- **Oracle growth is never truncated.** Additions over a per-Attempt limit are held until the Oracle-growth Gate admits them, removes some by name (with a reason) or freezes growth.
- **Host requests have no Öge timeout by default.** An optional timeout may only deny or cancel. Scope expansion is denied automatically, and more scope needs an explicit pipeline or user action, never a widening of an in-flight Role's authority.
- **Unattended runs park; they don't decide.**
  - Optional Gates are removed.
  - Host requests are denied or cancelled.
  - Mandatory Gates park the Run. A park is never turned into Rejected.
  - Consequences:
    - tamper never reaches Accepted unattended;
    - planner pipelines park at the plan Gate;
    - reviewer findings need a declared route that can't accept the work, or the Run parks.

## Consequences

- Every path to Accepted requires a Check of the current Candidate against the latest active Oracle version.
- Exits per Role kind (#27 owns how they are spelled in config):
  - planner: `plan_ready` | `infeasible`
  - implementer: `done` | `infeasible`
  - verifier: `extended` | `no_additions` | `conflicts_with_oracle`
  - reviewer: `no_issues` | `issues_found`
- The `infeasible` and `conflicts_with_oracle` Exits route to the infeasible Gate. Reviewer `issues_found` may route directly to a bounded send-back, because that route cannot accept the work.
- The first evaluation (#31) measures implement → verify → Check unattended. Parked Runs count as "parked at <Gate kind>", never as success or Rejected.
- The Codex spike (#13) and the Claude integration tests must check that neither protocol expires a pending Host request on its own.

## What would reverse this

- Users rubber-stamp the plan Gate. Then let Pipelines opt out of it, with the Plan staying a Claim.
- `take` is chosen on nearly every passing Run without the diff being opened. Then make the result Gate off by default.
- A native protocol expires pending requests and that can't be turned off. Then a timeout below it, which may only deny, becomes mandatory.
- #31 must measure plan quality. Then add authored plans per fixture. A generated Plan never passes a Gate unattended.
- A multi-user mode puts actor identity back in scope.
