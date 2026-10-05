---
status: accepted
---

# Strict inside, simple outside: zero-interaction happy path, modes and the Receipt

The trust architecture (ADR-0001…0018) stays as it is. This ADR changes how much of it reaches the user. It comes from the product/DX review ([#62](https://github.com/Erengun/oge/issues/62)) and the user's corrections to it.

Every simplification here was audited against one rule: **does this hide complexity, or does it silently remove a guarantee?** The rule is to hide aggressively and never weaken silently.

## Product targets

- **Strict inside, simple outside.** Normal use needs four concepts: Task, Checks, Result (with its Receipt) and Mode. Everything else appears only on inspection, with `-v`, or when a Run needs the human.
- **A normal successful Run needs zero human interaction after launch.** This is pre-registered: at least 90% of eligible happy-path Runs.

## Decisions

1. **The Result gate is off by default.** `--confirm` restores it. If the exact resolved Candidate passes the latest Oracle and no mandatory trust issue is unresolved, the Run finishes Accepted with no further confirmation. `oge apply` stays the explicit act of taking the result. `--apply` is an opt-in convenience that applies only an Accepted Candidate, to the working tree, and never commits. This amends ADR-0008 and ADR-0013.

2. **There is one end-of-run review instead of mid-run stops. The semantics are unchanged.**
   - A Run keeps doing useful work after a Tamper event, but it can't become Accepted until the human has acknowledged the event.
   - Ambiguous files can accumulate until the final review. Before Accepted, every one must be promoted or dropped. Each promotion creates a new Candidate. If the verifier never saw the promoted content, fresh verification is still mandatory. The final Check runs on exactly the deliverable Candidate.
   - Unattended Runs park at this review only when it is needed.
   - This amends the ordering in ADR-0008 and ADR-0013 only.

3. **Failing implementer-written tests go back for repair automatically**, within the existing bounded send-back loop. The human is involved only when that budget is exhausted or another mandatory condition requires it. Passing implementer tests still have zero positive acceptance authority, and override still gives Overridden. This amends the Own-test-failure gate's trigger in ADR-0013.

4. **Permissions run on a three-way policy, not "inside the folder means safe".** Scope and permission are different things: an arbitrary shell command can be dangerous while writing only inside the Workspace. Each adapter's Launch profile carries a deterministic, versioned set of operations Öge pre-authorises because they fall within the declared role capabilities and the sandbox.
   - **Pre-authorise** a known low-risk operation allowed by the role and the sandbox.
   - **Auto-deny** anything clearly outside the role or the Write scope, with a reason.
   - **Raise a Host request** for anything that needs network, scope expansion, an unusual or destructive capability, or any other capability that isn't pre-authorised.

   **Constraint (2026-10-05):** a normal successful task must not require the coding agent to noticeably change its working style because of Öge. Pre-authorisation is measured by its friction: denied and retried tool calls, extra model turns, and time added against native. If policy friction is more than about one wasted turn on a normal task, simplify the interface rather than grow the shell parser. The likely direction is structured capabilities (read_file, search, list_files, run_tests, format, git_diff, git_status), with Bash kept for exceptional cases.

   Interactive Host requests should be rare in normal MVP use. After the MVP, the Decider handles this grey zone and escalates only unusual cases. Each pre-authorised operation is recorded as "pre-authorised by Launch profile <hash>". This amends ADR-0005 and ADR-0008.

5. **Agent questions are never answered on the human's behalf.** The Briefing tells every role: *"Make reasonable, minimal and reversible assumptions where possible. Do not ask the user unless missing information genuinely blocks progress or would materially change the Task, acceptance criteria or required authority."* An actual question-type Host request is treated as meaningful. Attended, it goes to the human. Unattended, it is cancelled under the existing semantics (ADR-0008, ADR-0012). Öge never fabricates an answer. This amends ADR-0009 (Briefing content).

6. **There are three modes, shown on every result, and they are not a quality ladder.**
   - **Fast** (`--fast`): implementer → Öge Check. It keeps M1, M2 and M4. It gives up the independent verifier and held-out tests (M3/M5), and says so.
   - **Standard** (the default): implementer → fresh verifier → Check (`verify = "after"`).
   - **Blind** (`--blind`): verifier from the Task and Snapshot → implementer → Check (`verify = "before"`). Blind has stronger blindness but isn't strictly stronger than Standard: a verifier that sees the implementation can catch implementation-specific problems.
   - **`--require` names guarantees, not an ordering.** For example `--require held-out` is satisfied by Standard or Blind, and fails with exit 2 otherwise.
   - A mode called **Strict** is reserved for a later composite (blind verifier before → implementer → fresh verifier after → Check, and possibly the Challenger), to be chosen from evaluation evidence.
   - `verify = "before" | "after"` stays as the advanced control.
   - Fast becomes a product mode (it is the walking skeleton's graph) and an evaluation arm. The headline arm stays Standard.
   - This amends ADR-0016.

7. **The Receipt.** Every Run ends with a compact, Evidence-derived Receipt: outcome, mode, Candidate, visible checks, held-out checks, tamper and scope status, duration, interruptions, and a mandatory **"Not covered"** line.
   - "Not covered" lists Degraded guarantees, uncontained Checks, unmapped tests, held-out source viewed by a human, and what Fast mode skips.
   - It never uses "verified" or other confidence language.
   - It is shown at the end of every Run, including failed ones.
   - `oge receipt [run] [--md|--json]` exports it.
   - The user-facing name ("Receipt" vs. "Öge Verdict") is open for the positioning review. Note that **Verdict** is already a glossary term for a Check's pass/fail result.

8. **The default output is terse.** It shows one line per stage, then the summary and Receipt. Today's event stream moves to `-v`, and today's `-v` to `-vv`. Nothing trust-relevant is hidden from the Receipt. In particular, the uncontained-Check notice moves from the startup banner to the Receipt's "Not covered" line, plus a one-time first-run notice.

9. **Entry is lighter.**
   - The bare `oge "<task>"` is the run command; `oge run` stays as the explicit form.
   - On the first run in a repository with no config, Öge proposes the detected checks, test globs and **project-specific output globs** in one reviewed prompt and saves them. That is ADR-0013's `init` folded in, still never silent.
   - An unattended Run with no config refuses, as before.
   - Acceptance criteria stay optional (a warning only).
   - Bindings default to the installed agent.
   - Amends ADR-0013 and ADR-0015.

10. **Speed and interruption metrics** (amends ADR-0016).
    - **Pre-registered**, added to the gate: Standard's median total time ≤ 2× B2; at least 90% of eligible happy-path Runs complete with zero human interruption.
    - **First-class (amended 2026-10-05, positioning review):** human attention time per completed task, i.e. time the human spends on the Run (prompts, gates, reviews, watching for required input), measured and reported for every arm next to correctness, speed and interruptions.
    - **Reported:** time to first useful change, verifier time, repair loops, model turns, Host-request count, Öge's non-model overhead, verifier overhead vs Fast, and failures caused by auto-denied requests.

## Audit: hidden vs removed

| Simplification | Verdict | Why |
|---|---|---|
| Result gate off by default | hides | Accepted was already computed from Evidence; `apply` remains the human's "take" |
| End-of-run tamper/Ambiguous review | hides | Acknowledgement and resolution are still required before Accepted; re-verify and the final Check are unchanged |
| Automatic own-test repair | hides | It stays bounded; known-failing tests still can't reach Accepted; override is still Overridden |
| Pre-authorised operations | hides | It is a deterministic, versioned, recorded list; grey-zone actions still ask; nothing outside the role is allowed |
| Assumptions instruction | hides | Real questions are still asked when attended and cancelled when unattended; nothing is fabricated |
| Fast mode | **removes M3/M5, explicitly** | It is opt-in, shown on every result, refused by `--require held-out`, and the Receipt says what it skips. Not silent |
| Terse output | hides | Every trust-relevant fact stays in the Receipt |
| First-run config proposal | hides | It is reviewed and never silent |
| **Location-based default for Promoted files** (#62 proposal f) | **rejected: would silently remove a guarantee** | Implementer notes placed in existing folders would reach the verifier. Replaced by project-specific output globs proposed at first run or `init` |

## Consequences

- Tickets #43, #45, #47, #48, #49, #39, #40, #52, #54, #56, #57 and #59 are updated, and a Receipt ticket is added.
- The coordination direction ("Öge is the bridge between your agents") is recorded as a post-MVP direction, not part of the trust MVP.

## Would reverse this

- The evaluation shows the zero-interaction defaults cause false acceptances or hidden guarantee loss, rather than just fewer prompts.
- Pre-authorised operations prove exploitable (a pre-authorised operation causing harm outside the role's intent): shrink the list.
- Users routinely want the Result gate back: make it the default again.
