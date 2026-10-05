# Öge

Öge is a multi-agent engineering harness that coordinates independent roles across native coding agents and accepts work only on evidence it gathers itself.

## Language

### Trust and acceptance

**Evidence**:
A record Öge made itself of something it ran or directly observed: a command execution, a Briefing manifest, a Preflight observation, a scope or revert observation, a Tamper event, or an environment or capability observation. A Verdict rests only on a Check's command-execution Evidence.
_Avoid_: Proof, test results (when reported by an agent)

**Attestation**:
Evidence that a protected test actually executed during a Check: an unpredictable per-test value, known only to Öge and its protected overlay, which the test sends to Öge over a channel Öge owns. A Check can't pass without the attestation of every expected protected test. A test report is diagnostic; the attestation is required.
_Avoid_: Signature, proof of correctness

**Check command**:
A command that a Check runs on a Candidate. It is part of the Oracle, so it is versioned, protected and pinned into every Verdict. An agent may propose one, but only a human at a Gate adds it.
_Avoid_: Test command (when an agent's own run is meant), verification script

**Claim**:
Anything an agent asserts about its own or another's work (e.g. "tests pass"); recorded, never treated as Evidence.
_Avoid_: Self-assessment, report

**Oracle**:
The tests, test configuration and Check commands that decide acceptance, protected from change by the roles being judged. It has an append-only history of versions: verify Attempts add to it, and only a human at a Gate removes from it.
_Avoid_: Test suite (ambiguous), ground truth

**Tamper event**:
An observed change to the protected Oracle or test configuration by a role being judged, recorded with what changed, by which Attempt and what Öge reverted. It describes the change, not intent, and stays in the Run's record whatever happens next.
_Avoid_: Cheating, hacking, test tampering (as an accusation)

**Held-out test**:
A part of the Oracle that the implementer never sees.
_Avoid_: Hidden test, secret test

### Runs and pipelines

**Task**:
The user's statement of what a run must change, with its acceptance criteria; the input of a run.
_Avoid_: Spec, requirement, prompt

**Acceptance criterion**:
One testable statement of what the Task requires, with a stable id. The user writes it in the Task, or a planner proposes it and a human approves it at a Gate; held-out tests name the criteria they check.
_Avoid_: Requirement, AC (alone), spec item

**Plan**:
A planner's proposal for carrying out a Task; a Claim until a human approves or edits it at a Gate.
_Avoid_: Design doc, spec

**Approved plan**:
A Plan after a human approved or edited it at a Gate; authoritative run input, no longer a Claim.
_Avoid_: Final plan

**Pipeline**:
A named template of Stages, Checks and Gates joined by bounded edges, each edge labelled by a Verdict, an Exit or a Gate choice. Its author chooses Stages, verify shape, optional Gates and limits; Öge derives the edges and inserts the mandatory Gates. Agent output can never add an edge or raise a bound.
_Avoid_: Workflow, flow, recipe

**Run**:
One execution of one Pipeline for one Task, against a copy of the Pipeline resolved and frozen when the run starts.
_Avoid_: Job, session, task (for the execution)

**Role kind**:
One of the trust-bearing kinds of work that Öge itself defines (planner, implementer, verifier, reviewer; later challenger, decider, advisor). Write scope, Briefing rules, session reuse and the allowed Exits attach to the kind; a name chosen by a pipeline author never confers them. In the product UI, the verifier's work is labelled **QA**. The label may grow to cover more verification activity later, but the Role kind stays precise.
_Avoid_: Persona, agent type, custom role (as a trust boundary)

**Stage**:
A node of a Pipeline where an agent of a given Role kind does one piece of work, with its capability requirements and retry budget.
_Avoid_: Step, phase, task

**Attempt**:
One execution of a Stage within a Run, labelled by its cause: first, retry, send-back, resume or user request. Each cause is counted against its own budget, and a resume after an Infrastructure stop against none. A retry follows an Attempt failure, a regenerate is a user-request Attempt chosen at a Gate, and a resume re-enters the Run from its record without re-running completed Attempts.
_Avoid_: Retry (as a noun for the thing), iteration, new stage, restart

**Attempt failure**:
An Attempt ending without a usable result for a reason attributed to the agent's side: its process crashed or vanished, its output could not be decoded, it gave no valid Exit, or it timed out. It uses the retry budget.
_Avoid_: Crash (as the category), error, infrastructure failure

**Ledger**:
The append-only record of one Run's control events (Attempts, Gate decisions, statuses, Oracle versions, Tamper events, Deliveries), written before the effects it describes. The Run's state is rebuilt from it, and its history is never rewritten.
_Avoid_: Log, journal, database, history (alone)

**Delivery**:
The user taking a Run's final Candidate into their own repository by applying it or making a branch from it. It is recorded in the Ledger; viewing a diff is not a Delivery.
_Avoid_: Merge, export, apply (as the general term)

**Checkpoint**:
The state an interrupted Attempt left behind, recorded after the scope and tamper check passed, from which that Attempt's resume continues. It is never a Candidate and no Check judges it.
_Avoid_: Snapshot, save point, partial Candidate

**Session**:
One Öge-owned conversation with an agent. An Attempt uses exactly one Session; a Session belongs to one Stage and may continue into later Attempts of that Stage only where the Role kind or Stage explicitly allows reuse.
_Avoid_: Thread, conversation, agent session id (the agent's own id is mapped, not used)

**Check**:
A node of a Pipeline where Öge runs Oracle commands on a Candidate and derives a Verdict from the resulting Evidence.
_Avoid_: Test stage, validation step

**Gate**:
A node of a Pipeline where the Run waits for a human decision from a typed set of choices. A Gate never resolves itself and a decider never resolves one; a Pipeline may leave out an optional Gate, but a Gate that is present always needs a decision. Each decision is pinned to what the human was shown and is replayed only while all of it is unchanged.
_Avoid_: Approval, checkpoint

**Result gate**:
The optional Gate after a passing Check where the human takes the Candidate or refuses it. A pass does not oblige the human to take it.
_Avoid_: Final approval, sign-off

**Own-test-failure gate**:
The mandatory Gate a Run reaches when Implementer-authored tests fail on a Candidate, whatever the Verdict. The human can send the work back, reject it, quit, or take it anyway as Overridden; it can never lead to Accepted.
_Avoid_: Test failure gate (ambiguous with a failed Check), self-test gate

**Ambiguous-file gate**:
The mandatory Gate before final acceptance where a human promotes or drops each remaining Ambiguous file. Any resolution makes a new Candidate, which the final Check judges. It is one batch review at the end of the Run, shown only when Ambiguous files remain: promote and drop act on the files the human selects, and a bare promote or drop covers every file shown, which is an explicit choice, not a default. The review stays open until every file is resolved, then routes once: a fresh verifier Attempt if any file was promoted and the mode has a verifier, then the final Check (#97).
_Avoid_: Cleanup gate, file review

**Recheck**:
Running the same Check again on the same Candidate and Oracle version, at a human's choice at a Gate. It is not an Attempt, keeps every Verdict, and is allowed once per Candidate and Oracle version; disagreeing Verdicts mark the Oracle as flaky for that pair.
_Avoid_: Retry (for a Check), re-run until green

**Extension**:
A human's one-off grant at a Gate of more room under exactly one named limit of a Run, with a reason. It holds for that Run only and is always reported.
_Avoid_: Budget increase, override (for a limit)

**Parked**:
Said of an unattended Run waiting at a Gate that needs a human. Parking never decides the Gate and is never a rejection; a human can later resume the Run and decide. Parked is a status of the Run, never an outcome.
_Avoid_: Timed out, failed, rejected

**Exit**:
The result an agent declares at the end of an Attempt, from a small closed set fixed by its Role kind (for example "done", "infeasible", "issues found"). It is a Claim: it may route only along declared edges that never lead directly to acceptance.
_Avoid_: Report, verdict, status

**Briefing**:
The context Öge assembles and gives an agent at the start of an Attempt, according to its Role kind.
_Avoid_: Prompt, context (alone), handover

**Briefing manifest**:
The record of what one Attempt's Briefing contained and was denied: each item's provenance and hash, the files withheld, the agent configuration observed at startup, and whether the Session was fresh. It never copies held-out or secret contents.
_Avoid_: Prompt log, context dump

### Workspace

**Snapshot**:
The revision a Run's Workspace starts from, including the user's uncommitted and untracked work.
_Avoid_: Base, checkpoint

**Snapshot control**:
The protected tests of one Oracle version run on the immutable Snapshot, with the same setup, environment policy and Attestation as a Check. A Check consults it to judge skips: a protected test may skip on the Candidate only if it also skipped on the Snapshot control.

**Run repository**:
The Öge-owned private repository of one Run, holding its Snapshot and every Candidate. The user's own repository is only the source of the Snapshot and is never where agents work.
_Avoid_: Shadow repo, worktree, the user's repo

**Private state**:
The part of a user's Öge state that no agent can read: every Run's Ledger, Run repository, Oracle versions and Check directories. It lies outside the user's repository, every Workspace and the temp directory.
_Avoid_: Cache, .oge folder, hidden directory

**Workspace**:
An Öge-owned working copy derived from the Run repository that an agent of a Run works in: read-only from the Snapshot for the planner, one persistent copy for the implementer, and a fresh sanitised copy of the Candidate for every verifier or reviewer Attempt. The Oracle's protected and held-out parts are never in any of them.
_Avoid_: Sandbox, worktree (as the domain term), checkout

**Write scope**:
The paths a Role kind may change. Writes outside it are reverted after every Attempt and recorded.
_Avoid_: Permissions, allowed files

**Candidate**:
The Workspace revision that a Check judges. A working Candidate may still contain Ambiguous files; the final Candidate contains none, and only it can be Accepted and is exactly what the user receives.
_Avoid_: Patch, result, diff

**Check directory**:
The fresh folder Öge builds for one execution of a Check: the Candidate with the Oracle version laid over its test paths. No agent ever works in it.
_Avoid_: Test workspace, CI dir

**Cache seed**:
A Run's warm build and module caches in Private state, filled on the Snapshot by the setup command and Öge's warm step. Each Check starts from a private clone or copy of it, or cold; no Check ever writes to the seed.
_Avoid_: Shared cache, Run cache (as a cache Checks write to)

**Implementer-authored test**:
A test the implementer wrote into the Candidate. Passing it never produces or strengthens a pass Verdict, and it never becomes part of the Oracle; a known failure stops the Run at the Own-test-failure gate, and a Candidate taken anyway is Overridden, never Accepted.
_Avoid_: Visible test (as the Oracle's), self-test

**Promoted file**:
A file in the Candidate that verifiers and reviewers may see: a change to a file that already existed, or a new file matching the run's declared output or test patterns.
_Avoid_: Output, deliverable (when the classification is meant)

**Excluded file**:
An agent's notes, configuration or instruction file that never reaches a verifier or reviewer, from a fixed list Öge owns or the Attempt's scratch area outside the Candidate.
Delivery: held back by `oge apply` and `oge branch` unless declared as output (an output glob that names it, e.g. `--output CLAUDE.md`); a declared one is ordinary Candidate content, seen by QA as data, never as instructions (#107).
_Avoid_: Ignored file, junk

**Ambiguous file**:
Any other new file the implementer created. It may stay in a working Candidate but is withheld from verifiers and reviewers, and a human must promote or drop it at a Gate before the final Candidate. Promoting a file the verifier never saw requires a fresh verifier Attempt before the final Check. A promotion holds by path for the rest of the Run: if a later Attempt rewrites the file, the verifier and the Check judge the new content, and the human isn't asked again (#97).
_Avoid_: Unknown file, untracked file

### Verdicts and outcomes

**Verdict**:
Öge's pass or fail judgment of a Candidate against one Oracle version, derived only from Evidence. Agents never issue Verdicts.
_Avoid_: Review result, approval, LGTM

**Receipt**:
The compact summary every Run ends with, derived only from the Ledger's Evidence: outcome, mode, what was checked and what was not covered. It shows what the agent claimed next to what Öge found only when the two disagree.
_Avoid_: Öge Verdict (Verdict is one Check's pass/fail), certificate, report card, verification badge

**Accepted**:
The outcome of a Run whose final Candidate received a pass Verdict against the latest Oracle version and, where a Result gate exists, the human took it.
_Avoid_: Passed, done, merged

**Rejected**:
The outcome of a Run whose Candidate a human judged unacceptable. Running out of a retry or loop budget is not, by itself, a rejection.
_Avoid_: Failed (ambiguous with Infrastructure stop)

**Infeasible**:
The outcome of a Run where an agent's Exit claimed the Task cannot be done or conflicts with the Oracle and a human confirmed it at a Gate. Unconfirmed, it is only a Claim.
_Avoid_: Impossible, gave up

**Overridden**:
The outcome of a Run whose Candidate a human took without a passing Verdict, or despite known failing Implementer-authored tests. Never counted or reported as Accepted.
_Avoid_: Accepted anyway, force-accepted

**Cancelled**:
The outcome of a Run the user stopped before it finished.
_Avoid_: Aborted, killed, interrupted

### Running agents

**Launch profile**:
The Öge-owned, per-role flags, environment and policy used to start an agent, plus the configuration envelope the agent must report at startup. Anything outside the envelope is flagged, never silently accepted.
_Avoid_: Agent config, user settings

**Native adapter**:
The part of Öge that drives one agent over that agent's own richest documented protocol (Codex app-server, Claude stream-json).
_Avoid_: Driver, integration, wrapper

**Generic adapter**:
The part of Öge that drives any agent over ACP, used for agents without a materially richer native protocol.
_Avoid_: ACP bridge, fallback adapter

**Capability**:
A feature of an agent session (such as in-band approvals or non-terminal interrupt) that Öge may rely on only once it is negotiated for that session: declared by the adapter, reported by the agent where it can report, and enabled by the Launch profile.
_Avoid_: Feature flag, agent version (as a proxy)

**Host request**:
A request from an agent that needs an answer from outside the agent, such as a permission to act or a question to the user, with a typed set of allowed responses; answered by the user or, later, a decider. Every answer records who gave it.
_Avoid_: Permission prompt (too narrow), approval (when a question is meant)

**Supervision**:
What Öge does across a Run on the human's behalf: routing each agent operation through the Launch profile's policy (and, post-MVP, the Decider), sending work back for repair, escalating real ambiguity to the human, and stopping at trust boundaries. It is a product concept, not a Role kind. Authority stays with Öge, and only Evidence accepts. "Supervisor" may label the TUI panel that summarises it.
_Avoid_: Supervisor agent, auto mode, orchestration

**Decider**:
A Role kind with no Stage that answers an agent's approval Host requests while work is happening, in the human's place; the agent doing the work never grants itself permission. A decider only authorises actions: it never creates Evidence, issues a Verdict, resolves a Gate or accepts a Candidate.
_Avoid_: Approver, auto mode, policy engine

**Escalation**:
A decider passing a pending Host request on to the human, who then answers that same request. It is not a Gate.
_Avoid_: Escalation gate

**Challenger**:
A Role kind whose Stage tries to get a wrong Candidate past the Oracle, so as to harden it. Its findings count only once reproduced as Oracle additions that a Check runs, or through an Exit to a Gate.
_Avoid_: Red team, adversarial reviewer

**Advisor**:
A Role kind with no Stage that gives advice when consulted; the advice is always a Claim.
_Avoid_: Consultant, expert, oracle

**Consultation**:
One request for an Advisor's advice, made by pipeline policy, the user or an agent, with or without a pending Host request. It cannot create Evidence, choose an accepting edge or resolve a Gate.
_Avoid_: Advisor request, second opinion

**Handoff**:
Moving a Stage to a different agent within a Run: a new Attempt with a fresh Session and a Briefing Öge builds from the Run's record. It needs no transcript transfer.
_Avoid_: Transfer, take-over, session import

**Degraded**:
Said of a session or its Evidence when an explicitly optional Capability was missing, with the exact lost guarantee stated; never the result of a missing required Capability.
_Avoid_: Best effort, partial

**Preflight**:
The readiness check a run makes, before any stage starts, on the agents its pipeline uses: installed, supported version, signed in, and usable where that can be verified without spending quota.
_Avoid_: Health check, login check

**Interrupted**:
Said of a Run the user stopped mid-Attempt, or whose Öge process died, without cancelling it. It is a status, not an outcome, and the Run can be resumed from its last durable revision.
_Avoid_: Cancelled, aborted, paused

**Active-time limit**:
A limit on the time a Run spends actually working. Time at Gates, parked, in an Infrastructure stop or waiting for a human's answer to a Host request does not count.
_Avoid_: Run timeout, deadline

**Infrastructure stop**:
A run halting because of its environment, such as agent authentication, quota, a provider outage or a fault in Öge itself, not because of the work. It is not a failure, uses no retry budget, is never "infeasible", and the run can resume once the user repairs the cause.
_Avoid_: Blocked, auth error (as an outcome), failure
