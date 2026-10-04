---
status: accepted
---

# A Run is a static, bounded graph and only Öge issues Verdicts

Öge runs a Task through a Pipeline: a static graph of three node kinds ([#20](https://github.com/Erengun/oge/issues/20)). A **Stage** is agent work of a built-in Role kind. A **Check** is Öge running the Oracle on a Candidate. A **Gate** is a human decision. Edges are labelled only from closed sets (a Verdict, a Stage's Exit, a Gate choice) and carry bounds. Agent output can never add a node or an edge, or raise a bound. Most peers do this differently, so a reader may expect four things this design rejects: a verifier agent that passes or fails the work, reviewer comments that send work back on their own, a "retry" that adds a new stage, and a user "accept anyway" that counts as acceptance. The reasons are M1–M6 ([#30](https://github.com/Erengun/oge/issues/30)) and the literature in [#10](https://github.com/Erengun/oge/issues/10). LLM judgments of code are unreliable and gameable. Tests the harness runs from its own protected copy are not.

- **Only a Check issues a Verdict** (pass or fail), and only from Evidence. Each Verdict is pinned to a Candidate revision and an Oracle version. **Accepted** requires a pass on the final Candidate against the latest Oracle version.
- **The verifier extends the Oracle; it does not judge.** Its held-out tests and probes go into an append-only Oracle history. Only a human at a Gate removes a test. If the Oracle needs to stop growing, that is a concrete limit (on verifier Attempts, on additions per Attempt, or on additions per Run) that routes to a Gate. It is never a silent freeze or truncation.
- **Agent findings have teeth, but only through closed sets.** A free-form Claim never chooses an edge. A Stage may summarise its Attempt into an **Exit** from its Role kind's closed set, for example a reviewer's `issues_found` or an implementer's `infeasible`. An Exit may route only to a Gate or to another path that does not accept, such as a send-back. Architectural or security defects that can't be written as tests therefore still reach a human, and none of them can produce acceptance.
- **Role kinds are a closed set of trust-bearing kinds defined by Öge.** Write scope, Briefing rules, session reuse and the allowed Exits attach to the kind. A name chosen by a pipeline author never confers them. Future custom stages without trust properties are not ruled out.
- **Retry, send-back, resume and user-requested reruns are Attempts of the same Stage, labelled by cause.** Each cause has its own visible budget. Resume after an Infrastructure stop uses none. Exhausting a budget routes to a Gate. Only a human can extend a budget, and the extension is recorded.
- **Outcomes are a closed set.** Accepted, Rejected (judged unacceptable by a human; exhausting a budget is not a rejection), Infeasible (an agent's Exit confirmed by a human), Overridden, Cancelled. **Overridden** means a human took a Candidate without a passing Verdict. It is never counted or reported as Accepted. Run status (running, waiting at a Gate, Infrastructure stop) is separate from the outcome.

## Consequences

- The post-Attempt scope check is not a graph node. It is a mandatory invariant around every Attempt. A Write-scope violation is reverted and recorded, and does not change routing. A **Tamper event** is an observed change to the protected Oracle or test configuration by a role being judged. It is reverted and recorded, and adds a mandatory Gate before the Run can become Accepted. Pipelines cannot remove that Gate. The record states what changed, not intent, and it stays in the ledger after the human continues.
- Held-out tests never sit in the Workspace that agents see. The Oracle lives outside it.
- Each Attempt uses exactly one Session. A Session belongs to one Stage and continues across Attempts only where reuse is explicitly allowed. Fresh-context Role kinds (verifier, reviewer) get a new Session on each Attempt by default.
- The Run freezes the resolved Pipeline at start, so a resumed Run replays the same graph.

## What would reverse this

- Fixture runs ([#31](https://github.com/Erengun/oge/issues/31)) show that the Check-only Verdict misses defects reviewers routinely catch, and that routing through Gates loses them. Then admit a budgeted, non-accepting send-back driven directly by an Exit.
- False Tamper events are common, for example toolchains touching test configuration. Then narrow what counts as protected state, but keep the Gate.
- Unattended runs become a requirement and the Gate on budget exhaustion blocks them. The terminal state for unattended runs is [#25](https://github.com/Erengun/oge/issues/25)'s call.
- New evidence shows that LLM verdicts on code match harness-run tests on the #31 metrics. Then the Verdict's sole reliance on Evidence would no longer be warranted.
