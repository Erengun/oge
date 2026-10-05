---
status: accepted; bare `oge "<task>"`, terse default output, `--confirm`/`--apply`/`--fast`/`--blind`/`--require`, `oge receipt` amended in part by ADR-0019; "no elaborate TUI" and append-only output superseded by ADR-0022
---

# The CLI is a versioned trust contract: exit code 0 means Accepted and only Accepted

[#28](https://github.com/Erengun/oge/issues/28) settles Öge's command surface. It builds on ADR-0005 to ADR-0014. Most of the CLI is ordinary UX and belongs in the spec. One part does not: scripts, CI and the evaluation harness (#31) will treat some of the CLI's behaviour as a statement about whether work was accepted. Once they depend on it, it is expensive to change. A reader might expect any of six things this design rejects:

- exit code 0 for any Run that "finished", including Overridden;
- a `--force` or `--yes` that skips a Gate or delivers anything;
- a default choice at a Gate, so that pressing Enter decides;
- an attended Run that quietly parks when no terminal is attached;
- delivering an Overridden or Rejected Candidate with no more than a warning;
- the human's view of held-out tests going unrecorded.

## Decisions

- **Exit codes are a documented, versioned contract.** They apply to `oge run` and `oge resume`:

  | Code | Meaning | Kind |
  |---|---|---|
  | 0 | Accepted | outcome |
  | 1 | internal Öge error | — |
  | 2 | usage, config or Preflight refusal | — |
  | 3 | Rejected | outcome |
  | 4 | Infeasible | outcome |
  | 5 | Overridden | outcome |
  | 6 | Cancelled | outcome |
  | 10 | Parked | status |
  | 11 | Infrastructure stop | status |
  | 130 | Interrupted | status |

  - **0 means Accepted and only Accepted.** Overridden is never success (ADR-0007, ADR-0008).
  - Outcomes use 3–6 and resumable statuses use 10 and above, so a script can tell a finished Run from one that can be resumed.
  - An attended process waiting at a Gate does not exit just because the Run is waiting. Code 10 applies when execution returns while the Run is still parked and resumable.
  - `oge run --dry-run` and `oge doctor` return 0 when valid or ready, and 2 otherwise. The other commands return 0 on success, 2 on a refusal and 1 on an internal error.
- **Machine-readable output is versioned.** `oge doctor --json` and `oge status [run] --json` follow a documented schema carrying `"schema": 1`. `oge run` has no JSON event stream in the MVP, and the Ledger stays internal (ADR-0014). Automation uses the Run id, the exit code and `status --json`.
- **An attended Run needs an interactive terminal.** Without one, an attended command refuses with exit code 2 and points to `--unattended`. Parking belongs to the unattended contract only (ADR-0008).
- **Decisions are typed deliberately.**
  - No Gate has a default choice, and Enter alone does nothing. There is no `--yes` for Gates and no `--force` anywhere.
  - Decisions that weaken trust or carry high consequences are entered as the full word, never a letter, and need a reason. They are: `override`, `reject`, `infeasible`, `freeze`, `extend <limit> +N`, `remove <test>` and `dismiss`. `reject` does not weaken trust, but it ends the Run, so it gets the same deliberate entry.
  - The decision is written durably before it takes effect (ADR-0008). Only then does the CLI print the recorded action and reason.
- **Delivering a non-Accepted Candidate takes a flag named after its outcome.**
  - `oge apply` and `oge branch` deliver only Accepted by default.
  - Overridden needs `--overridden` and Rejected needs `--rejected`. There is no generic override flag.
  - Before delivering a non-Accepted Candidate, Öge prints the outcome prominently, the Candidate revision, and why it was not Accepted. It never describes the result as verified.
  - Unresolved Ambiguous files in such a Candidate need an explicit include or exclude. They never come along silently.
  - `oge diff` works whenever a Candidate exists and is not a Delivery (ADR-0014).
- **The human may view held-out source, and the view is recorded.** It is shown only through an explicit, labelled inspect view at a Gate. The Ledger records an observation with `held_out_viewed`, the Oracle version, `viewed_by: human` and the time. Individual pager navigation is not recorded. Later human feedback may be informed by the view, which is normal use, but audits and evaluations must know that blindness was broken. Unattended Runs never view held-out source.
- **What the terminal never shows**, even with `-v`:
  - raw native protocol frames;
  - credentials;
  - held-out source, except in the inspect view above;
  - Briefing content a role is forbidden to see;
  - unredacted output.

  Output is append-only lines of summarised, normalised events. There is no elaborate TUI. *(Superseded by [ADR-0022](0022-the-terminal-experience-is-a-product-surface.md): a live TUI on interactive terminals, with plain lines as the CI/automation mode. These never-shown rules still apply.)*

## Consequences

- #26: the Ledger gains a held-out-viewed observation. Delivery records note the outcome flag that allowed them.
- #31: the harness reads outcomes from exit codes and `status --json`. Parked (10) and interrupted (130) Runs count separately from outcomes.
- #32: the CLI layer owns the exit-code mapping, the JSON schemas, the TTY check and Gate entry. Each is tested as a contract.
- The MVP cut line ([ADR-0016](0016-mvp-cut-planner-free-verify-before-and-harness-gated-thesis.md), #31) leaves out the plan Gate, the review Gate, Recheck, regenerate, `oge gc` and `oge doctor --live`. Their choices and flags follow the same rules when they arrive.
- The full command surface and the screens are recorded on #28 and in `docs/ux/oge-run-transcript.md`.

## What would reverse this

- Automation consistently needs one generic "not accepted" code. Even then, Overridden never gets 0.
- `override` and `reject` typed as full words become reflexive. Then the CLI also echoes the pins and asks for confirmation.
- A threat model in which the user's terminal is an agent-readable channel. Then held-out source is never shown, even to the human.
- Live consumers (CI, dashboards) need events during a Run. Then add a versioned `run --events jsonl`.
