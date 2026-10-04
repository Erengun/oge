---
status: accepted (`verify = "before"` planner requirement amended in part by ADR-0016)
---

# Pipelines are constrained TOML compiled into the frozen graph, and a Candidate with failing own tests or unresolved Ambiguous files never reaches Accepted

[#27](https://github.com/Erengun/oge/issues/27) settles how a Pipeline is configured, building on ADR-0005 to ADR-0012. A reader might expect any of five things this design rejects:

- a YAML workflow file with free-form nodes and edges;
- `oge run "task"` working with no configuration by guessing the toolchain;
- user-level defaults quietly loosening a project's trust settings;
- a third Check result for "Oracle passed but the implementer's own tests failed";
- promoting a withheld file at the last Gate and going straight to acceptance.

## Decisions

- **TOML, strict.** `schema = 1` is required and unknown keys are hard errors.
  - A schema newer than the binary understands is refused.
  - An older supported schema is migrated in memory only through an explicit, tested migration path. Indefinite migration is not promised.
  - The frozen Run records the source schema and the resolved current representation.
- **Layout and precedence.**
  - Built-in Pipelines and defaults ship in the binary.
  - The project file is `.oge/oge.toml`, read from the **Snapshot**. `.oge/` is Excluded and outside every judged role's Write scope.
  - The user file holds personal agent and model defaults only.
  - Precedence: CLI > project > user > built-in. The Run freezes the resolved config together with its CLI overrides.
  - **Trust-weakening options never come from user-level defaults:** Session reuse, user-global instructions, network on, removing an optional Gate, setup network. They come only from a project Pipeline or an explicit CLI flag, and are surfaced in the frozen configuration at startup and in Evidence and reports.
- **No graph authoring in the MVP.** An author chooses:
  - Stages, each with a free name and a mandatory built-in `role`;
  - `verify = "after" | "before"`, where `before` requires a planner and an Approved plan sufficient to brief the verifier (ADR-0012);
    - *Amended in part by [ADR-0016](0016-mvp-cut-planner-free-verify-before-and-harness-gated-thesis.md) ([#31](https://github.com/Erengun/oge/issues/31)):* `before` needs no planner. The Task's Acceptance criteria brief the verifier, and missing criteria warn rather than refuse.
  - the optional Gates and the reviewer `issues_found` route (ADR-0008);
  - limits.

  Öge compiles the static graph from these, inserting the mandatory Gates. The frozen Run stores the **compiled graph**, so a resumed Run replays the same graph whatever binary resumes it. The name of a shape is not enough.
- **No silent toolchain defaults.** `oge run` refuses without enough Oracle configuration to establish acceptance: at least one Check command and test globs.
  - The configuration comes from `oge init`, which detects likely commands from marker files and writes the file only after the user has reviewed it, or from explicit one-off `--check` / `--tests` flags.
  - Check commands are Oracle content (ADR-0011). Each declares its structured-report format, which is required where held-out tests are collected, and its expected tests, timeout and output cap.
- **Binding.** The canonical form is `--agent <stage>=<agent>[:<model>]`.
  - The Role-kind aliases (`--plan`, `--implement`, `--verify`, `--review`) resolve only when exactly one Stage of that kind exists, and are an error otherwise.
  - Verifier and reviewer may inherit the implementer's agent and model, but always in a fresh Session.
  - With nothing bound, Öge uses the user default, else the one installed agent, else refuses. There is no vendor preference.
- **Network.**
  - Only planner and implementer Stages take `network = "off" | "on"`, defaulting to off.
  - Judged roles and Checks are always off, and agent web tools are off by default.
  - The project setup command is a separate operation Öge owns, with its own explicit network setting. That setting is never inferred from an agent's.
- **Failing Implementer-authored tests go to a mandatory Own-test-failure gate, not to a new Verdict.** The Verdict stays `pass | fail` from Oracle commands alone.
  - Implementer-authored tests run separately as labelled Evidence, with zero acceptance authority when they pass.
  - A failure stops the Run at the Gate. The choices are:
    - `send back`, counted against the Check→implementer send-back limit, with the full failure output;
    - `reject`;
    - `quit`;
    - `override`, with a reason.
  - Taking a Candidate with known failing shipped tests is **Overridden**, never Accepted.
- **Ambiguous files go to a mandatory Ambiguous-file gate before final acceptance.**
  - It shares a screen with the result Gate when that Gate exists. Otherwise it stands alone, and an unattended Run parks there.
  - Every resolution creates a new Candidate.
  - **Promoting a file withheld from the latest verifier Briefing forces a fresh verifier Attempt** (cause: user request) when the Pipeline has a verifier, and only then the final Check. Dropping a file requires no re-verify.
  - Accepted needs:
    - zero unresolved Ambiguous files;
    - the latest required verifier Stage passed through;
    - a final Check on exactly the resolved Candidate against the latest Oracle version.

## Consequences

- This **amends ADR-0008 in part**: its list of mandatory Gates gains the Own-test-failure gate and the Ambiguous-file gate.
- Default limits and timeouts are provisional built-in values, not architecture. Every Run freezes them, and fixture evaluation may change them without an ADR.
- Hard validation errors:
  - Session reuse or user-global instructions on a verifier or reviewer;
  - removing, weakening or configuring a mandatory Gate;
  - an unknown Role kind;
  - credential values or credential-store settings (ADR-0006). `pass_env` holds names only.
  - `verify = "before"` without a planner.
    - *Amended by [ADR-0016](0016-mvp-cut-planner-free-verify-before-and-harness-gated-thesis.md) ([#31](https://github.com/Erengun/oge/issues/31)):* no longer an error. Missing Acceptance criteria give a warning.
- `--output <glob>` and `--tests <glob>` exist from day one, so incomplete globs can be fixed per Run.

## What would reverse this

- A real Pipeline needs an order the constrained fields can't express, such as two reviewers or a reviewer before the verifier. Then add explicit edges under a new schema version.
- Implementer-authored test runs are routinely flaky or slow, and the Own-test-failure gate becomes noise. Then that run may become skippable, but it is still reported, and taking a Candidate with known failures is still Overridden.
- Re-verifies forced by promotion, or "withheld file needed" reports, are common. Then require output globs.
- Users routinely bounce off `oge init`. Then `--check` / `--tests` may offer to write the file.
- The adapters prove they can enforce domain allowlists. Then Stage network gains allowlists.
