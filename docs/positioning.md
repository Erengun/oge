# Positioning

Decided in the positioning review ([#65](https://github.com/Erengun/oge/issues/65)). Copy, README and launch material follow this. Product behaviour follows the ADRs.

## Identity

Revised 2026-11-01 (the attention review, after the Desk work in [ADR-0024](adr/0024-several-chats-one-attention-queue.md)). It supersedes the 2026-10-05 identity, which superseded the #65 headline.

> **One Desk for everything you're building.**

Thesis:

> **AI multiplied execution. Öge protects attention.**

Category (what Öge is):

> **Öge is an attention layer for AI-accelerated development.**

The problem it answers: AI raised a developer's execution capacity past their ability to track the work it produces. You can start five tasks before finishing one, across several repos, with agents producing output while you do something else. Decisions, reviews, failures and unfinished work pile up, and you switch contexts to keep it all moving. The bottleneck moved from execution to human attention: context switching, prioritisation, decision fatigue and the cost of resuming.

Öge is not another coding agent, not a test runner, not a project-management tool and not generic multi-agent orchestration. The agents are not the product, and neither are the projects: the Desk is where you keep control of the work AI lets you start. It drives the native agents you already use and stays provider-independent, terminal-native and trust-aware.

Supervision is the mechanism that lets you look away: Öge checks the work and accepts it only on evidence it gathers itself, so you don't re-check every "done". Verification makes the Desk credible; it is not the identity.

The aim is **maximum productive parallelism with minimum context switching**: not fewer agents, but fewer times you rebuild context or watch a project by hand.

Three things Öge must get right:

1. **Stop forcing context switches.** Something happening in project A never makes you open it. Öge brings what matters to you at a natural breakpoint ([`ux/desk.md`](ux/desk.md)).
2. **Preserve context so you resume at once.** Coming back to a Chat, you see what you were doing, what happened since, what you decided, what is waiting and the next action, all from records ([`ux/away.md`](ux/away.md)).
3. **Make committing attention intentional.** Starting work is cheap; Öge lets you get an idea out of your head without turning it into another running Chat (Later, in [`ux/desk.md`](ux/desk.md)).

Earlier lines, kept as explanation, not headline:

> Öge makes delegation actually feel like delegation.

> Hand off the task. Öge brings you back when the work is ready or genuinely needs you.

### Three layers

| Layer | What the user experiences | What Öge does |
|---|---|---|
| **Delegate** | "Do this task." | Runs the native coding agent |
| **Protect attention** | "Don't bother me unless it matters." | Permissions, isolation, later the Decider and routing |
| **Earn done** | "Tell me when I can trust the result." | Independent verifier, Oracle, Evidence, Receipt |

### North star

Unchanged by the 2026-11-01 revision: it is the compass, and the new identity says it out loud.

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

1. **M1: the trust loop plus an excellent CLI/TUI.** Implement → QA → repair → Check → Accepted → apply and Receipt, with zero interruptions on the happy path. The terminal experience is part of M1 acceptance, not later polish.
2. **Decider:** answers grey-zone permissions without the human, reducing supervision friction.
3. **Advisor:** resolves uncertainty and stalls. It removes the human as the copy-paste bridge between models, which is the workflow that started Öge. It is **event-driven, never a permanent agent in every Run**. It is invoked when:
   - the implementer is stuck;
   - the implementer asks a material technical question;
   - repeated repair fails;
   - two plausible approaches have meaningful trade-offs.
4. **Stronger QA and fixer loop.**
5. **Handoff:** routes around quota and model failures.
6. **Challenger:** hardens the Oracle.

> One supervision experience, not an AI org chart.

The risk to avoid is finishing all the trust machinery while postponing the two things that made Öge interesting: a Claude-quality terminal experience and intelligent cross-agent consultation.

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

- **QA** is the user-facing label for verification, review and repair activity (`● QA · Fresh Claude · reviewing`). **Verifier** stays the internal, trust-bearing Role kind in ADRs, the glossary and Evidence. The QA concept may grow later (runtime checks, browser testing, several checks) while the roles underneath stay precise. QA must never claim more than the Verifier and Checks actually did.

- **Receipt** is the user-facing end-of-run artifact (`oge receipt`, `--md`, `--json`).
- **Verdict** keeps its internal meaning: one Check's pass/fail. Never "Öge Verdict" for the artifact.
- The Receipt's claim-versus-evidence block is headed **"Öge found"**. "Caught by Öge" is not product language because it implies intent. It's fine if users adopt it themselves.

## Wording rules

- No benchmark numbers before the pre-registered evaluation has run.
- No "best", "safer", "verified", "guaranteed", "AI-approved", or broad superiority claims.
- User-facing text doesn't lead with "multi-agent", "orchestration", "harness" or "council". These appear only as technical classification in docs.
- Results are always stated as "on these N fixtures", never as general rates.
