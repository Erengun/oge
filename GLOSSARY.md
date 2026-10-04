# Öge

Öge is a multi-agent engineering harness that coordinates independent roles across native coding agents and accepts work only on evidence it gathers itself.

## Language

### Trust and acceptance

**Evidence**:
A record of a command that Öge itself ran and its result, tied to a specific workspace revision.
_Avoid_: Proof, test results (when reported by an agent)

**Claim**:
Anything an agent asserts about its own or another's work (e.g. "tests pass"); recorded, never treated as Evidence.
_Avoid_: Self-assessment, report

**Oracle**:
The tests and test configuration that decide acceptance, protected from change by the roles being judged. It has an append-only history of versions: verify Attempts add to it, and only a human at a Gate removes from it.
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

**Plan**:
A planner's proposal for carrying out a Task; a Claim until a human approves or edits it at a Gate.
_Avoid_: Design doc, spec

**Approved plan**:
A Plan after a human approved or edited it at a Gate; authoritative run input, no longer a Claim.
_Avoid_: Final plan

**Pipeline**:
A named template of Stages, Checks and Gates joined by bounded edges, each edge labelled by a Verdict, an Exit or a Gate choice. Agent output can never add an edge or raise a bound.
_Avoid_: Workflow, flow, recipe

**Run**:
One execution of one Pipeline for one Task, against a copy of the Pipeline resolved and frozen when the run starts.
_Avoid_: Job, session, task (for the execution)

**Role kind**:
One of the trust-bearing kinds of work that Öge itself defines (planner, implementer, verifier, reviewer; later challenger, decider, advisor). Write scope, Briefing rules, session reuse and the allowed Exits attach to the kind; a name chosen by a pipeline author never confers them.
_Avoid_: Persona, agent type, custom role (as a trust boundary)

**Stage**:
A node of a Pipeline where an agent of a given Role kind does one piece of work, with its capability requirements and retry budget.
_Avoid_: Step, phase, task

**Attempt**:
One execution of a Stage within a Run, labelled by its cause: first, retry, send-back, resume or user request. Each cause is counted against its own budget, and a resume after an Infrastructure stop against none.
_Avoid_: Retry (as a noun for the thing), iteration, new stage

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

**Recheck**:
Running the same Check again on the same Candidate and Oracle version, at a human's choice at a Gate. It is not an Attempt, keeps every Verdict, and is allowed once per Candidate and Oracle version; disagreeing Verdicts mark the Oracle as flaky for that pair.
_Avoid_: Retry (for a Check), re-run until green

**Extension**:
A human's one-off grant at a Gate of more room under exactly one named limit of a Run, with a reason. It holds for that Run only and is always reported.
_Avoid_: Budget increase, override (for a limit)

**Parked**:
Said of an unattended Run waiting at a Gate that needs a human. Parking never decides the Gate and is never a rejection; a human can later resume the Run and decide.
_Avoid_: Timed out, failed, rejected

**Exit**:
The result an agent declares at the end of an Attempt, from a small closed set fixed by its Role kind (for example "done", "infeasible", "issues found"). It is a Claim: it may route only along declared edges that never lead directly to acceptance.
_Avoid_: Report, verdict, status

**Briefing**:
The context Öge assembles and gives an agent at the start of an Attempt, according to its Role kind.
_Avoid_: Prompt, context (alone), handover

### Workspace

**Snapshot**:
The revision a Run's Workspace starts from, including the user's uncommitted and untracked work.
_Avoid_: Base, checkpoint

**Workspace**:
The Öge-owned working copy a Run's agents work in. The Oracle's protected and held-out parts are never in it.
_Avoid_: Sandbox, worktree (as the domain term), checkout

**Write scope**:
The paths a Role kind may change. Writes outside it are reverted after every Attempt and recorded.
_Avoid_: Permissions, allowed files

**Candidate**:
The Workspace revision that a Check judges.
_Avoid_: Patch, result, diff

### Verdicts and outcomes

**Verdict**:
Öge's pass or fail judgment of a Candidate against one Oracle version, derived only from Evidence. Agents never issue Verdicts.
_Avoid_: Review result, approval, LGTM

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
The outcome of a Run whose Candidate a human took without a passing Verdict. Never counted or reported as Accepted.
_Avoid_: Accepted anyway, force-accepted

**Cancelled**:
The outcome of a Run the user stopped before it finished.
_Avoid_: Aborted, killed

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

**Infrastructure stop**:
A run halting because of its environment, such as agent authentication or quota, not because of the work. It is not a failure, uses no retry budget, is never "infeasible", and the run can resume once the user repairs the cause.
_Avoid_: Blocked, auth error (as an outcome), failure
