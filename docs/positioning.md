# Positioning

Decided in the positioning review ([#65](https://github.com/Erengun/oge/issues/65)). Copy, README and launch material follow this. Product behaviour follows the ADRs.

## Current product story

> **Your coding agent says it's done. Öge checks.**

Supporting idea:

> Stop letting coding agents grade their own homework.

README first screen: the tagline, then *"Öge independently checks coding-agent work instead of letting the same agent grade its own homework."* The first screen describes the problem, not the architecture. The mechanism can be described accurately further down.

## Future direction (not current-product positioning)

> Stop being the bridge between your agents.

This is used only once the coordination roles (Decider, Advisor, Handoff) ship. See [#64](https://github.com/Erengun/oge/issues/64).

## Origin story

> I had become both the glue between models and the checker of every "all tests pass." Öge takes the second job first. The first comes next.

## Threat model

> Öge is designed to catch ordinary agent mistakes, reward hacking, test manipulation and straightforward attempts to bypass verification. The MVP does not claim isolation against deliberately hostile Candidate code executing with the user's OS privileges.

The Receipt's "Not covered" line states this limit. It never lists easy ways to counterfeit the Evidence Öge accepts on: those are acceptance-boundary bugs ([ADR-0020](adr/0020-protected-test-execution-attestation-is-required-evidence.md)).

## Usage boundary

> Use your agent when you're watching. Use Öge when you're not.

## Naming

- **Receipt** is the user-facing end-of-run artifact (`oge receipt`, `--md`, `--json`).
- **Verdict** keeps its internal meaning: one Check's pass/fail. Never "Öge Verdict" for the artifact.
- The Receipt's claim-versus-evidence block is headed **"Öge found"**. "Caught by Öge" is not product language because it implies intent. It's fine if users adopt it themselves.

## Wording rules

- No benchmark numbers before the pre-registered evaluation has run.
- No "best", "safer", "verified", "guaranteed", "AI-approved", or broad superiority claims.
- User-facing text doesn't lead with "multi-agent", "orchestration", "harness" or "council". These appear only as technical classification in docs.
- Results are always stated as "on these N fixtures", never as general rates.
