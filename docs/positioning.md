# Positioning

Decided in the positioning review ([#65](https://github.com/Erengun/oge/issues/65)). Copy, README and launch material follow this. Product behaviour follows the ADRs.

## Identity

Revised after the market review of 2026-10-05, which superseded the #65 headline.

> **Öge makes delegation actually feel like delegation.**

Category (what Öge is):

> **Öge is the supervision layer for coding agents.**

It is not another coding agent, not a test runner and not generic multi-agent orchestration. It supervises the native agents you already use, and stays provider-independent, terminal-native and trust-aware.

Supporting thought:

> AI made coding fast. Supervision became the bottleneck.

Product behaviour:

> Give Öge the task. It supervises the agents, checks the work, and brings you in only when your judgment is actually needed.

Promise:

> Hand off the task. Öge brings you back when the work is ready or genuinely needs you.

Öge is not a verification tool, not a multi-agent tool and not a "stop babysitting" tool. Verification is the mechanism that makes delegation credible, not the identity.

### Three layers

| Layer | What the user experiences | What Öge does |
|---|---|---|
| **Delegate** | "Do this task." | Runs the native coding agent |
| **Protect attention** | "Don't bother me unless it matters." | Permissions, isolation, later the Decider and routing |
| **Earn done** | "Tell me when I can trust the result." | Independent verifier, Oracle, Evidence, Receipt |

### North star

> **How much useful agent work happens per minute of human attention?**

The internal product equation (not marketing copy):

> Öge value = (assurance + autonomy gained) / (latency + human attention added)

- A feature that makes verification 3% stronger but makes the average run 2× slower probably doesn't ship by default.
- A feature that removes five permission prompts while keeping the same authority probably does.

**Human attention time per completed task** is a first-class evaluation metric, alongside correctness, speed and interruptions. If Öge is 20% safer but you have to watch it for 15 minutes, it failed. If it is safer **and you can leave**, it's a product.

### Phrases we don't try to own

Other projects already use these, so Öge doesn't build on them as identity:
- "Your coding agent says it's done. … checks / prove it / show receipts" (the former headline);
- "Stop babysitting your agents";
- "acceptance layer";
- "done is a state, not a claim";
- "don't let one agent grade its own homework".

They may appear as explanation, never as the headline.

### How supervision works

Öge stays the authority. It never asks another model "looks good?" and calls that verification.

| Situation | Öge's response |
|---|---|
| Safe operation | continue (pre-authorised by the Launch profile) |
| Uncertain permission | the Decider answers within its budget (post-MVP); until then, a recorded deny |
| Implementation issue | send back to the implementer |
| QA issue (verifier or Check failure) | automatic repair loop: fix, then QA again |
| Real ambiguity | ask the human |
| Trust boundary hit | stop |

**Evidence still decides whether a result can become Accepted.**

The rhythm (from Wayfinder's implementer → independent QA → fixer): Implement → QA (a fresh agent plus Öge's Checks) → issue found? If not, the Receipt. If so, fix, then QA again. The user doesn't orchestrate this.

## Direction

The coordination roles extend the same identity rather than pivoting it:
Sequence, by user value (revised 2026-10-05; supersedes the #65 order):

1. **MVP trust loop:** implement → QA → Check → Receipt, zero interruptions on the happy path.
2. **Supervisor/Decider:** grey-zone permissions answered without the human.
3. **Stronger QA and fixer loop:** QA findings drive automatic repair rounds, possibly with a separate fixer.
4. **Handoff:** route around quota and model failures.
5. **Advisor:** resolves uncertainty at stall points.
6. **Challenger:** hardens the Oracle.

Supervision turns Öge from "Claude with another verifier" into "I delegate development to Öge, and Öge manages the coding agents for me."

> Eventually: you stop managing agents. You manage intent.

The long-term job is removing the human as middleware between agents: copying questions from one model to another, and re-checking every "done". See [#64](https://github.com/Erengun/oge/issues/64).

## Origin story

> My attention had become middleware: copying questions from one model to another, and re-checking every "all tests pass." Öge takes the checking first and routes the rest next, so I'm pulled in only for decisions that are actually mine.

## Threat model

> Öge is designed to catch ordinary agent mistakes, reward hacking, test manipulation and straightforward attempts to bypass verification. The MVP does not claim isolation against deliberately hostile Candidate code executing with the user's OS privileges.

The Receipt's "Not covered" line states this limit. It never lists easy ways to counterfeit the Evidence Öge accepts on: those are acceptance-boundary bugs ([ADR-0020](adr/0020-protected-test-execution-attestation-is-required-evidence.md)).

## Usage boundary

> Use your agent when you're watching. Use Öge when you're not.

## Terminal philosophy

Not more information: better attention routing. At a glance, the screen answers three questions:

1. What is happening?
2. Does Öge need me?
3. Can I trust where this is going?

It never shows hundreds of lines of agent thought by default. See [ADR-0022](adr/0022-the-terminal-experience-is-a-product-surface.md).

## Naming

- **Receipt** is the user-facing end-of-run artifact (`oge receipt`, `--md`, `--json`).
- **Verdict** keeps its internal meaning: one Check's pass/fail. Never "Öge Verdict" for the artifact.
- The Receipt's claim-versus-evidence block is headed **"Öge found"**. "Caught by Öge" is not product language because it implies intent. It's fine if users adopt it themselves.

## Wording rules

- No benchmark numbers before the pre-registered evaluation has run.
- No "best", "safer", "verified", "guaranteed", "AI-approved", or broad superiority claims.
- User-facing text doesn't lead with "multi-agent", "orchestration", "harness" or "council". These appear only as technical classification in docs.
- Results are always stated as "on these N fixtures", never as general rates.
