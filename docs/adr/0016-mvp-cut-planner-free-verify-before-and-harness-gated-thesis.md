---
status: accepted; modes (Fast/Standard/Blind) and speed/interruption gate criteria amended in part by ADR-0019
---

# The MVP proves only independent verification: no planner or reviewer, `verify = "before"` briefed from the Task's Acceptance criteria, and a pre-registered fixture gate

[#31](https://github.com/Erengun/oge/issues/31) sets the MVP cut line. The MVP answers one question: *does separating implementation from harness-owned verification materially reduce false acceptance without making the system unusably conservative or expensive?* A reader may expect three things this design rejects:
- a planner Stage in the MVP;
- `verify = "before"` requiring an Approved plan;
- a reviewer giving a second LLM opinion before acceptance.

## Decisions

- **The MVP's only Pipeline is implement → verify → Check.** Trust properties (M1–M6, every mandatory Gate, the fail-closed rules) are never cut.
  - The planner and reviewer Stages are not implemented. Their Role kinds stay in the domain model.
  - Reviewer findings are Claims, and the thesis doesn't need them.
  - Without a planner, plan quality drops out of the experiment as a confounder.
- **`verify = "before"` no longer needs a planner.** The Task is human input, not an agent Claim, so its Acceptance criteria can brief a verifier directly. In order:
  1. A verifier reads the Task, its Acceptance criteria and the Snapshot.
  2. It writes held-out Oracle additions.
  3. The implementer works.
  4. The Check runs.

  Missing Acceptance criteria give a warning, not a refusal, and such a run may produce unmapped tests. *Amends in part* [ADR-0012](0012-failures-attributed-ledger-written-ahead-resume-from-durable-state.md), which tied `before` to an Approved plan, and [ADR-0013](0013-constrained-toml-pipelines-compiled-into-the-frozen-graph.md), whose validation rule rejected `before` without a planner. Where a planner exists post-MVP, the Approved plan joins the Task as a briefing input.
- **Codex is not on the thesis path.** Claude is required. Codex lands after its spike if it is technically usable, and a Claude-only release is not a failed MVP.
- **The evaluation harness is a mandatory MVP deliverable.** It lives in `eval/`, outside the release binary. It:
  - runs the same Snapshot and frozen Task/config per fixture for every arm;
  - keeps the scoring suite away from every arm and from Öge's Oracle;
  - keeps baseline definitions versioned in the repository;
  - reads Öge's metrics by replaying Ledgers.
- **The acceptance gate for the thesis is pre-registered.**
  - **Fixtures:** 12 (Go and TS; seeded bugs and spec/test conflicts), each run 3 times, with arm order interleaved. This is an engineering validation suite, not a population estimate.
  - **Arms:** Öge-after is the headline arm and can't be swapped after results; Öge-before is secondary. The primary baseline is B2 (Claude with its built-in verification on); the secondary is B1 (Claude checking its own work).
  - **Quality rules, which must hold against both baselines:**
    - escaped bugs ≤ 50% of each baseline's;
    - delivered tamper = 0;
    - conflicts surfaced ≥ 80%;
    - correct delivery no more than 10 percentage points below the higher baseline.
  - **Cost rule:** median normalised usage ≤ 3× B2, measured from protocol tokens, with no invented dollar prices.
  - If the rules fail, no feature expansion happens until the cause is understood.

## Considered options

- **Keep the planner for `before`.** The Approved plan is a richer briefing, but plan quality becomes a confounder. Unattended evaluation would also need authored plans per fixture.
- **Cut `before` along with the planner.** Simpler, but it drops the shape that #10 links to the strongest gain in fault detection.
- **Compare against the "best baseline" per metric.** Rejected because the reference could change after seeing results.

## Consequences

- Deferred until after the MVP:
  - `oge gc` (retention semantics stay);
  - Recheck and regenerate;
  - the after-failed-Check Gate;
  - agent-proposed Check commands;
  - Checkpoints and vendor Session resume;
  - the watchdog;
  - the Run-wide active-time limit;
  - the user config file and schema migrations;
  - custom Stage lists.
- Delivery (`diff`, `apply`, `branch`) stays IN, so the exact verified Candidate is shown to be consumable.
- #32 owns Öge's own testing strategy (fake agents, recorded transcripts, drift CI, cross-build, no paid quota in CI) and builds `eval/` alongside the Claude slices.

## What would reverse this

- The evaluation shows Task criteria are too thin to brief a `before` verifier: unmapped-test or withheld-file-needed rates are high. Then the planner returns ahead of other post-MVP work.
- B2 already lets almost no bugs escape on the fixtures. Then the fixtures are hardened before the gate is judged.
- Seeded defects the Check misses are routinely ones a reviewer would catch. Then the reviewer Stage moves forward in post-MVP order.
