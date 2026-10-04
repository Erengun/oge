---
status: accepted
---

# Failures are attributed to their cause, the ledger is written ahead, and resume never trusts what it cannot prove

[#25](https://github.com/Erengun/oge/issues/25) builds on ADR-0005 to ADR-0011 to settle how a Run fails, retries and resumes. A reader may expect five things this design rejects:

- every agent error being "infrastructure" and therefore free;
- the model being asked to repair broken protocol output;
- Ctrl-C cancelling the Run;
- a resumed agent always continuing its old conversation;
- Öge promising that no stray processes survive a crash.

## Decisions

- **The ledger is written ahead of every effect.** Each Attempt writes three records in order:
  1. *AttemptStarting*, before spawn: the Attempt id, cause, starting revision, intended Session mapping and Launch profile.
  2. *ProcessStarted*, right after a successful spawn and before any protocol interaction: process id, process group and the process's start information.
  3. *AttemptEnded*, only after the resulting checkpoint or Candidate is durably captured.

  No process information exists before launch, and none is claimed. A subprocess crash never destroys run state, because recovery reads only the ledger and the Run repository. Writes still in flight in a Workspace are not covered.
- **Failures are attributed to their cause.**
  - An agent process crash, OOM, a lost subprocess (a turn accepted but never settled), or a structured transport that is malformed or can't be decoded is an **Attempt failure** with a reason code. It uses the retry budget, and exhausting the budget goes to the bound-exhaustion Gate.
  - Provider overload, 5xx or rate limits are an **Infrastructure stop**, but only when the adapter receives them as a reliable structured signal. Model-written text never counts.
  - Faults in Öge, private state, git or Öge's own parser are an Infrastructure stop.
  - A compromised-Evidence condition (ADR-0011) is an Infrastructure stop, and that Run can never become Accepted.
  - Command, setup, timeout, OOM and kill failures follow ADR-0011's attribution rule:
    - caused by the Candidate: fail Verdict;
    - also fails on the Snapshot: Preflight or environment problem;
    - host, toolchain, Öge or resource problem: Infrastructure stop, no Verdict.

    An agent's own tool failure is just the work.
- **A missing or invalid Exit gets exactly one follow-up turn.** This applies only when the turn otherwise completed. Öge sends "Declare one Exit from {…}", and records the follow-up and its answer as Claims. If the Exit is still missing or invalid, the Attempt fails. Öge never asks the model to repair protocol output, because only a valid Exit, which is already a Claim, may be asked for.
- **Ctrl-C interrupts; it does not cancel.**
  - The first press interrupts, waits a grace period and kills the remaining agent processes.
  - It then scope- and tamper-checks whatever is on disk and, if that is safe, persists it as a **Checkpoint**, never a Candidate.
  - The Run becomes **interrupted**, and a second press may force termination.
  - Cancelled comes only from `oge cancel` or `quit` at a Gate.
- **Resume starts from the last durable revision.** That is a Checkpoint if one was durably recorded, otherwise the Attempt-start revision; after an Öge crash it is usually the latter.
  - Verifier and reviewer always restart fresh.
  - A killed Check has no Verdict, so running it again is not a Recheck.
  - There is no resume budget in the MVP. The Run-wide Attempt cap is the backstop.
- **Vendor Session resume is conditional.** Only a planner or implementer can resume the agent's own Session, and only when all three hold:
  - the adapter advertises `resume`;
  - a matching durable Checkpoint exists;
  - the relationship between Session and files is known to stay coherent after interruption.

  Otherwise the resume gets a fresh Session with a Briefing Öge builds. Verifier and reviewer Sessions are never resumed.
- **Retry, regenerate and resume stay distinct.**
  - *Retry*: after an Attempt failure, a fresh Session from the Attempt-start revision.
  - *Regenerate*: a user-request Attempt chosen at a Gate.
  - *Resume*: re-entry from the ledger. The frozen graph and pinned decisions are replayed, and completed Attempts never re-run.

  There is no full-run restart; the user starts a new Run.
- **Time limits count only active time.**
  - Each Stage has a wall-clock timeout and an idle timeout. Both pause while waiting on Host requests or Gates.
  - A timeout means interrupt, grace period, kill, and an Attempt failure with the reason `timeout`.
  - The optional Run-wide **active-time limit** excludes time spent at Gates, parked, in an Infrastructure stop, or waiting for a human answer to a Host request. Leaving Öge alone overnight never exhausts a Run.
- **Run statuses are running, waiting at Gate, parked, Infrastructure stop and interrupted.** Parked stays a status. When an unattended Run exhausts a limit, it parks and the CLI returns a distinct exit code. There is no new outcome.
- **Crash recovery is layered and never claims a clean machine.**
  - An exclusive lock per Run, released by the OS when its holder dies.
  - Recorded process identity to reduce mistakes from reused process ids.
  - A live watchdog where practical, plus a sweep at startup and resume as the second layer.
  - No automatic resume after a crash.
  - Processes that escaped the group are recorded as possible residue.
- **Runs may run concurrently. Attempts within a Run do not, in the MVP.**
  - Codex start-up serialisation (ADR-0005) becomes an Öge lock shared between processes.
  - The blind verifier shape is supported *sequentially*. With `verify = "before"`, the verifier writes held-out tests from the Snapshot and the Approved plan before the implementer starts. With `verify = "after"`, it runs after the implementer.
  - True verifier ∥ implementer is post-MVP. This amends [ADR-0009](0009-briefings-built-by-oge-from-snapshot-and-evidence.md) in part.

## Consequences

- #26 persists AttemptStarting, ProcessStarted and AttemptEnded, Checkpoints, Run statuses, lock and process-identity records, and active-time accounting.
- #27 owns the timeout and active-time defaults, and `verify = "before" | "after"`.
- #28 renders Ctrl-C handling, `oge resume`/`oge cancel`, the interrupted and parked states, the unattended exit code and residue reports.
- #31 compares verify before vs after, and counts parked and interrupted Runs separately from outcomes.
- The Codex spike (#13) and the Claude integration tests must verify mid-turn interruption and Session coherence after a kill.

## What would reverse this

- Fixture runs show agent crashes that are clearly environmental using up retry budgets. Then attribute more crash classes to Infrastructure stop.
- Dogfooding shows a resume loop that the Attempt and edge budgets miss. Then add a resume budget.
- Integration tests show resumed vendor Sessions stay coherent after a kill mid-turn. Then relax the Checkpoint condition. If they show the opposite, never resume vendor Sessions.
- The watchdog proves impractical on macOS. Then the startup sweep alone remains, and the residue wording stays.
- Latency from the sequential verify shapes dominates fixture runs. Then add true parallel verifier Attempts.
