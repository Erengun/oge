# Öge MVP terminal transcript (UX fixture)

This is a realistic transcript of the MVP command-line interface, decided in [#28](https://github.com/Erengun/oge/issues/28) and [ADR-0015](../adr/0015-the-cli-is-a-versioned-trust-contract.md). Use it as a UX fixture and a reference for the spec and for output tests.

**Normative:**
- the order of events;
- which Gates appear and the choices each offers;
- which choices are typed as full words and require a reason;
- the labels and flags;
- what is never shown;
- exit codes.

**Illustrative:** exact wording, column widths, ids, hashes, timings and versions.

The MVP cut ([#31](https://github.com/Erengun/oge/issues/31)) leaves out the following, so the transcript doesn't show them:
- planner and reviewer Stages, and so the plan Gate and the review Gate;
- Recheck and regenerate;
- `oge gc` and `oge doctor --live`;
- the user-level config file.

The plan Gate appears only in the appendix, marked post-MVP.

Setting: a Go service in `~/src/authsvc`. Claude Code implements and Codex verifies.

---

## 1. `oge doctor`

```text
$ oge doctor
Öge 0.1.0  darwin/arm64

Agents
  claude   Claude Code 2.3.4   (min 2.1.0, last tested 2.3.2 → newer than tested: warning)
           readiness     logged in (unverified)        claude auth status; no live turn in the MVP
           auth source   subscription login            no auth environment variables present
           tier          max
           adapter       native (stream-json)
           capabilities  host-approvals ✓  interrupt ✓  resume ✓  structured-diff ✗ (optional)
           envelope      checked when each Session opens
           approvals     answered by: you (Öge-mediated)   native auto mode: off
  codex    Codex 0.157.0       (min 0.155.0, last tested 0.157.0)
           readiness     ready                         app-server account/read, rateLimits/read (zero quota)
           auth source   ChatGPT login                 CODEX_ACCESS_TOKEN not set
           tier          pro
           adapter       native (app-server)
           capabilities  host-approvals ✓  interrupt ✓  resume ✓  structured-diff ✓
           envelope      checked when each Session opens (approvalsReviewer pinned to user)
           approvals     answered by: you (Öge-mediated)   native auto mode: off

Project  ~/src/authsvc
  config        .oge/oge.toml  schema 1  valid
  pipeline      default: implement → verify (after) → Check
  bindings      implement = claude:sonnet   (project)   ready
                verify    = codex:gpt-5.5   (project)   ready
  Oracle        1 Check command: go test -json ./...   (go-test-json report)
                test globs: **/*_test.go
  trust-weakening options   setup network = on   (project: setup = "go mod download")
  Deep Run-specific checks: oge run --dry-run

What Öge changes on your machine
  WHERE                                         WHAT                                        WRITTEN BY
  ~/Library/Application Support/oge/private/    Ledger, Run repositories, Oracle, locks     Öge (no agent can read it)
  ~/Library/Application Support/oge/work/       disposable Workspaces, agent caches         Öge
  your repository                               nothing, until you run oge apply/branch     Öge, only on your command
  .oge/oge.toml                                 project config, only via oge init           Öge, after your review
  ~/.claude/projects/…                          session transcripts                         the agent, not Öge
  ~/.codex/sessions/…, ~/.codex/auth.json       session files, token refresh                the agent, not Öge
  Öge never reads, stores or moves credentials, and never logs in or out for you.

$ echo $?
0
```

---

## 2. `oge run`: startup, resolved Task and frozen Pipeline

```text
$ cat task.md
# Rate-limit /api/login

Failed logins should be throttled per account and per IP.

## Acceptance criteria
- After 5 failed logins for one account within 15 minutes, further attempts get HTTP 429.
- The limit resets 15 minutes after the first failure in the window.
- A successful login clears that account's failure count.

$ oge run --task-file task.md
Run 01JQZ8M4T2  ·  ~/src/authsvc
Snapshot   HEAD 9c41e2a (main) + 2 modified, 1 untracked file

Task       Rate-limit /api/login
Criteria   AC-1  After 5 failed logins for one account within 15 minutes, further attempts get HTTP 429.
           AC-2  The limit resets 15 minutes after the first failure in the window.
           AC-3  A successful login clears that account's failure count.

Pipeline   default (frozen, compiled graph a3f9…)
           implement ─▶ verify (after) ─▶ Check ─▶ [Ambiguous-file gate] ─▶ [Result gate]
           mandatory Gates: tamper · infeasible · bound exhaustion · Oracle growth · own-test failure · Ambiguous file
  implement   claude:sonnet   network off   (project)
  verify      codex:gpt-5.5   network off   fresh Session every Attempt
  Check       go test -json ./...   timeout 10m   network off
  limits      retries 2/Stage · send-backs 3 · Oracle growth 20 tests/Attempt · Stage 60m active, idle 10m

! Trust-weakening options in effect
!   setup network = on      from .oge/oge.toml   (setup = "go mod download")
! Checks are uncontained: they run agent-written code with your user privileges and can read your files.
!   Mitigations: credential variables scrubbed, fresh directory, network off, process-group timeout.

Preflight  claude logged in (unverified) · codex ready · setup on Snapshot ok (12s)
```

Without criteria and with no planner, startup prints a warning and continues:

```text
! No acceptance criteria found in the Task, and this Pipeline has no planner.
!   Held-out tests will be reported as unmapped, and send-back feedback can name no criteria.
```

---

## 3. Implementation with a Host request

```text
[implement #1 claude] started (cause: first) · Workspace from Snapshot · Briefing manifest bm-01
[implement #1 claude] read internal/auth/handler.go, internal/auth/store.go
[implement #1 claude] "Adding a per-account sliding-window limiter in internal/auth/limiter.go…"
[implement #1 claude] edit internal/auth/limiter.go (+88)

┌ Host request · implement #1 claude · permission                                    1 of 1
│ Bash: go get golang.org/x/time/rate
│ needs network (implement network = off)
│ [a]llow once   [d]eny   [i]nspect
└ > d
  reason (optional, sent to the agent)> use the standard library; no new dependencies
  recorded: deny (answered_by: human)

[implement #1 claude] "Understood, implementing with a mutex-guarded map instead."
[implement #1 claude] edit internal/auth/limiter.go (+41 −12)
[implement #1 claude] edit internal/auth/handler.go (+19 −3)
[implement #1 claude] write internal/auth/limiter_test.go (+64)
[implement #1 claude] write notes/ratelimit-design.md (+22)
[implement #1 claude] write internal/auth/testdata/burst.json (+30)
[implement #1 claude] edit .github/workflows/ci.yml (+2)
[implement #1 claude] Bash: go test ./internal/auth/...   (agent's own run: a Claim)
[implement #1 claude] Exit: done   (Claim)
[scope] reverted .github/workflows/ci.yml (outside the implementer's Write scope, revert-only)
[candidate] c1 3f2a7d1 · Promoted 3 · Ambiguous 2 · Excluded 0 · reverted 1
```

The stream pauses while the request waits. Events that arrive meanwhile are buffered and flushed after the answer. The wait counts no active time. Ctrl-C at this prompt denies the request and then interrupts the Run.

---

## 4. Verifier, Check and implementer-authored tests

```text
[verify #1 codex] started (cause: first) · fresh Session · Briefing manifest bm-02
[verify #1 codex]   sees: Task + AC-1..3, Promoted view (3 files), visible Oracle v1
[verify #1 codex]   withheld: 2 Ambiguous files, implementer transcript, Exit and Claims, authorship
[verify #1 codex] write internal/auth/limiter_heldout_test.go (+71)   tags AC-1, AC-2, AC-3
[verify #1 codex] Exit: extended   (Claim)
[oracle] v1 → v2: +4 held-out tests (limit 20) · held-out source stays in private state

[check #1] Candidate c1 3f2a7d1 · Oracle v2 · fresh Check directory
[check #1] go test -json ./...   52 ran · 0 failed · 41.2s · report go-test-json
[verdict] PASS   (c1, Oracle v2)
[own-tests] implementer-authored, run separately, no acceptance authority
[own-tests] go test -json ./internal/auth/...   7 ran · 1 failed: TestLimiterResetsAfterWindow
```

---

## 5. Own-test-failure gate

```text
┌ Own-test-failure gate · Run 01JQZ8M4T2
│ Candidate c1 3f2a7d1 · Oracle v2 · Verdict PASS
│ The implementer's own tests fail on this Candidate (labelled; they are not the Oracle):
│   FAIL TestLimiterResetsAfterWindow   internal/auth/limiter_test.go:41
│ Taking this Candidate can only be Overridden, never Accepted.
│ send-backs used 0/3
│
│ [s]end back   [i]nspect   [q]uit
│ override   reject          (type the full word; a reason is required)
└ > s
  recorded: send back (implement, cause: send-back, 1/3) · full failure output goes to the implementer
```

Enter alone does nothing, and an unknown or ambiguous input asks again. `i` opens a menu, and each view goes to `$PAGER`:

```text
  [d] diff Snapshot→Candidate   [e] Check Evidence   [o] own-test output   [t] reverts / Tamper events
  [b] Briefing manifests        [h] held-out test source (recorded as viewed)
```

Choosing `h` writes `held_out_viewed` (Oracle version, viewed_by: human, time) to the Ledger once. The final report then says so.

```text
[implement #2 claude] started (cause: send-back) · same Workspace · Briefing manifest bm-03
[implement #2 claude] edit internal/auth/limiter.go (+6 −4)
[implement #2 claude] Exit: done   (Claim)
[candidate] c2 81bd0e4 · Promoted 3 · Ambiguous 2

[verify #2 codex] started · fresh Session · Briefing manifest bm-04
[verify #2 codex] Exit: no_additions   (Claim)

[check #2] Candidate c2 81bd0e4 · Oracle v2
[check #2] go test -json ./...   52 ran · 0 failed · 40.7s
[verdict] PASS   (c2, Oracle v2)
[own-tests] 7 ran · 0 failed   (informational)
```

---

## 6. Result gate with Ambiguous files on the same screen

```text
┌ Result gate · Run 01JQZ8M4T2
│ Candidate c2 81bd0e4 · Oracle v2 · Verdict PASS · own tests 7/7 (informational)
│ 3 files changed (+150 −7) · reverts 1 · Tamper events 0
│
│ Ambiguous files: resolve each before taking the Candidate
│   1  notes/ratelimit-design.md            22 lines   "# Rate limiter design …"
│   2  internal/auth/testdata/burst.json    30 lines   "{ \"attempts\": [ …"
│
│ p <n,…|all> promote   d <n,…|all> drop   i <n> inspect
│ [s]end back   [i]nspect   [q]uit   [t]ake (unavailable: 2 unresolved)
│ reject                    (type the full word; a reason is required)
└ > d 1
  recorded: drop notes/ratelimit-design.md
└ > p 2
  internal/auth/testdata/burst.json was withheld from the verifier.
  Promoting it creates a new Candidate, a fresh verifier Attempt and then a final Check.
  recorded: promote internal/auth/testdata/burst.json
[candidate] c3 d07c5a9 · Promoted 4 · Ambiguous 0

[verify #3 codex] started (cause: user request) · fresh Session · Briefing manifest bm-05
[verify #3 codex] Exit: no_additions   (Claim)
[check #3] Candidate c3 d07c5a9 · Oracle v2 · final
[check #3] go test -json ./...   52 ran · 0 failed · 41.0s
[verdict] PASS   (c3, Oracle v2)
[own-tests] 7 ran · 0 failed   (informational)

┌ Result gate · Run 01JQZ8M4T2
│ Candidate c3 d07c5a9 · Oracle v2 · Verdict PASS · Ambiguous 0
│ [t]ake   [s]end back   [i]nspect   [q]uit
│ reject                    (type the full word; a reason is required)
└ > t
  recorded: take
```

A trust-weakening or high-consequence choice reads like this (here, on a failed-Check Gate):

```text
└ > override
  reason (e to open $EDITOR)> flaky upstream DNS in TestRemoteAudit; reviewed manually
  recorded: override · reason: "flaky upstream DNS in TestRemoteAudit; reviewed manually"
```

Ctrl-C at the reason prompt drops that choice and returns to the Gate. It doesn't interrupt the Run.

---

## 7. Final report

```text
════ Run 01JQZ8M4T2 · ACCEPTED ════
Flags        none   (no Overridden · freeze · flaky Oracle · Extension · compromised Evidence)
! Checks ran uncontained (no OS sandbox): agent-written code ran with your privileges.

Task         Rate-limit /api/login   AC-1..3
Candidate    c3 d07c5a9 (final) vs Snapshot 9c41e2a+dirty · 4 files (+180 −7)
Verdicts     check #1  PASS  c1 3f2a7d1  Oracle v2
             check #2  PASS  c2 81bd0e4  Oracle v2
             check #3  PASS  c3 d07c5a9  Oracle v2   ← final
Oracle       v1 → v2 · +4 held-out tests (AC-1: 2, AC-2: 1, AC-3: 1, unmapped: 0) · held-out source viewed: no
Implementer-authored tests (labelled; no acceptance authority)
             c1: 1 failed → Own-test-failure gate → send back · c2, c3: 7/7 passed
Gates        own-test failure → send back · result → drop 1, promote 1 → take
Host requests 1 · denied 1 (answered_by: human)
Scope        reverted .github/workflows/ci.yml (revert-only) · Tamper events 0
Attempts     implement 2 (first, send-back) · verify 3 (first, first, user request)
Budgets      send-backs 1/3 · retries 0 · active time 14m 02s
Guarantees   none Degraded
Briefings    bm-01 … bm-05   (oge status 01JQZ8M4T2 --briefings)

Nothing has been written to your repository.
  oge diff 01JQZ8M4T2      show the change
  oge apply 01JQZ8M4T2     3-way apply to your working tree (never commits)
  oge branch <name>        create a branch from it (no checkout)

$ echo $?
0
```

---

## 8. Delivery

```text
$ oge apply
Run 01JQZ8M4T2 (latest finished Run here) · ACCEPTED · Candidate c3 d07c5a9
3-way apply onto your working tree … ok
  M internal/auth/handler.go
  A internal/auth/limiter.go
  A internal/auth/limiter_test.go
  A internal/auth/testdata/burst.json
Not committed. Delivery recorded.
```

A non-Accepted Candidate takes the flag named after its outcome:

```text
$ oge apply 01JQX2B7HC
refused: Run 01JQX2B7HC is OVERRIDDEN, not Accepted.
  use: oge apply 01JQX2B7HC --overridden
$ echo $?
2

$ oge apply 01JQX2B7HC --overridden
════ OVERRIDDEN, NOT VERIFIED ════
Candidate 5e19a04 · Oracle v3 · Verdict FAIL (check #2)
Why not Accepted: human override at the failed-Check gate · reason: "flaky upstream DNS in TestRemoteAudit; reviewed manually"
3-way apply onto your working tree … ok   (Not committed. Delivery recorded with outcome Overridden.)
```

---

## 9. Infrastructure stop, parking and resume (unattended)

```text
$ oge run --unattended --task-file task2.md
Run 01JR04KQ9P · … (startup summary as in §2) …
[implement #1 claude] … Exit: done   (Claim)
[verify #1 codex] started (cause: first) · fresh Session
[verify #1 codex] stopped: usage limit reached (structured rate-limit signal from codex)
■ Infrastructure stop: codex quota. No Verdict, no retry budget used.
  Resume once the limit resets: oge resume 01JR04KQ9P
$ echo $?
11

$ oge status
RUN          TASK                          STATE                   WAITING ON        AGE   FLAGS
01JR04KQ9P   Add audit log for logins      Infrastructure stop     codex quota       2h    —
01JQZ8M4T2   Rate-limit /api/login         Accepted (delivered)    —                 1d    —

$ oge resume --unattended
Run 01JR04KQ9P (the only unfinished Run here) · resuming from the Ledger
[verify #1 codex] restarted fresh (cause: resume; verifier Sessions are never resumed)
[verify #1 codex] Exit: extended   (Claim)
[check #1] PASS   (c1, Oracle v2)
■ Parked at the Ambiguous-file gate (1 unresolved file). Unattended Runs never decide.
  Decide: oge resume 01JR04KQ9P
$ echo $?
10

$ oge status 01JR04KQ9P
Run 01JR04KQ9P · parked · Ambiguous-file gate
Graph      implement ✓ ─▶ verify ✓ ─▶ Check ✓ PASS ─▶ [Ambiguous-file gate] ◀ here ─▶ [Result gate]
Candidate  c1 77aa310 · Oracle v2 · Ambiguous 1: scripts/seed_audit.sh
Budgets    retries 0/2 · send-backs 0/3 · active time 9m 40s
Degraded   none · Flags: none · Residue: none
Next       oge resume 01JR04KQ9P     (needs a terminal)
```

Running `oge run` attended without a terminal:

```text
$ echo task | oge run --task-file - | tee log
refused: an attended Run needs an interactive terminal. Use --unattended to park at Gates instead.
$ echo $?
2
```

Ctrl-C during an Attempt:

```text
^C
[implement #1 claude] interrupting… (press Ctrl-C again to force-kill)
[implement #1 claude] stopped · scope and tamper check ok · Checkpoint saved
■ Interrupted. Resume: oge resume 01JR1Y0AAB
$ echo $?
130
```

---

## Appendix: plan Gate (post-MVP, not in the MVP)

The plan Gate arrives with the planner Stage after the MVP:

```text
┌ Plan gate · Run 01K…
│ Plan from plan #1 codex (a Claim until approved) · criteria AC-1..3 (Task) + AC-4 (proposed)
│ [a]pprove   [e]dit   [g] regenerate   [i]nspect   [q]uit
└ > e
  (opens $VISUAL / $EDITOR / vi on a private copy; never in your repository)
  edit diff: −AC-4 "…"  +AC-5 "…"   · AC ids are never reused or renumbered; a semantic edit gets a new id
  Removing a Task-authored criterion requires a reason.
└ > a
  recorded: approve → Approved plan
```

The plan Gate has no send back, reject or infeasible choice. Regenerate covers sending the work back. A planner's `infeasible` Exit goes to the Infeasible gate, and a human who wants a different Task ends the Run.
