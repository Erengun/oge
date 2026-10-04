---
status: accepted (Ambiguous-file rule amended in part by ADR-0010)
---

# Öge builds every Briefing from the Snapshot, approved inputs and Evidence; judged roles never get Claims, authorship or withheld files

Öge assembles each Role kind's Briefing itself ([#21](https://github.com/Erengun/oge/issues/21)). A reader may expect four things this design rejects:

- the verifier reading the repo's CLAUDE.md/AGENTS.md as the implementer left them;
- the verifier or reviewer seeing everything the implementer wrote into the Workspace;
- a failing held-out test explaining itself to the implementer;
- a Codex verifier running with whatever user instructions Codex loads.

Each of these is a channel for the implementer's narrative or for held-out content, and #10 measured that both kinds of leak weaken verification (test bias, guessing hidden tests, self-preference, label bias). The verifier's independence is structural, not something a prompt promises.

- **Instruction files come from the Snapshot.** Öge injects the repo's instruction files into each role, so reading them from the Candidate would let an implementer's edit become part of the verifier's system prompt. A judged role that writes to an agent instruction or config path commits an ordinary scope violation, which is reverted and recorded.
- **Judged roles see the Promoted part of the Candidate, as one change on top of the Snapshot.**
  - **Excluded:** Öge's fixed list of agent, config and instruction paths, plus the per-Attempt scratch area outside the Candidate. These never reach judged roles.
  - **Promoted:** changes to files that already existed, and new files matching the explicitly resolved output and test globs.
  - **Ambiguous:** every other new file. It stays in the Candidate, so the Check judges exactly what would be taken, but it is withheld from the verifier and reviewer. It is recorded, and a human promotes or drops it at a Gate. If a Check needs a withheld file, Öge reports it and never promotes the file silently.
  - The core encodes no file-extension heuristic, because documentation and config can be real deliverables.
- **Briefings carry no Claims and no authorship.**
  - The verifier and reviewer never get the implementer's transcript, Exit, self-report, an unapproved Plan or an authorship label.
  - The verifier also does not see earlier Verdicts.
  - The reviewer never sees held-out test source, because reviewer findings can cross back to the implementer.
- **Held-out failures go back only as a count plus Acceptance criterion ids.** The file name, source, assertion text, expected values and command output never go back. Visible failures go back in full.
- **Send-back, retry and repair start a fresh Session.** Reuse is an explicit opt-in, never where a trust property needs fresh context. Resume after an Infrastructure stop may reuse the Session.
- **Context the provider injects is trust-sensitive; installed tooling is not.** User-global instructions, memories and other auto-injected context are off by default. Planner and implementer may opt in, never verifier or reviewer. An unexpected instruction source makes a verifier or reviewer fail closed, and only warns for the other roles. Codex cannot suppress the global `~/.codex/AGENTS.md` without changing `CODEX_HOME`, which ADR-0006 forbids. So when `instructionSources` reports that file, a Codex verifier or reviewer refuses to start. Extra tools only warn, unless they break a declared isolation guarantee.
- **By default the verifier and reviewer use the same provider and model as the implementer, each in a fresh Session.** They are distinct actors, such as "Claude implementer Session A" and "Claude verifier Session B", never "the same agent". Neither actor accepts work; the Check does. Cross-model verification is configuration, never described as stronger, because the evidence is weak either way.

*Amended in part by [ADR-0010](0010-agents-work-in-disposable-clones-of-an-oge-owned-run-repository.md) ([#22](https://github.com/Erengun/oge/issues/22)):* an Ambiguous file may stay only in a working Candidate. Before final acceptance a human promotes or drops every Ambiguous file at a Gate, and the final Check runs on exactly that resolved Candidate, so Öge never tests one tree and delivers another.

## Consequences

- Each Attempt records a **Briefing manifest** as Evidence: provenance and hash of each item, the classes denied, the withheld files, the observed configuration envelope and whether the Session was fresh or reused. It stores no held-out or secret contents. This is what makes "the verifier never saw X" checkable.
- The default verifier runs after implementation and is not implementation-blind. The parallel shape, where the planner is followed by the verifier writing tests alongside the implementer, is the one that is. Öge never claims blindness for the default.
- Implementer-written tests are part of the Candidate and labelled implementer-authored in the ledger. They are never Held-out tests and cannot extend the protected Oracle.
- Pipelines must resolve output and test globs. If they are incomplete, Ambiguous files reach Gates and "withheld file needed" reports appear. That is visible friction, never a silent leak.

## What would reverse this

- [#31](https://github.com/Erengun/oge/issues/31) shows criterion-id feedback narrowing the gap between visible and held-out results faster than real fixes accumulate. Then send back a count only.
- "Withheld file needed" is common. Then require output globs, or revert Ambiguous files instead of withholding them.
- Codex gains an ignore-user-instructions control (the fail-closed case disappears), or the refusal blocks common setups (the Codex verifier then runs Degraded, never satisfying a protected-verification pipeline).
- #31 shows cross-model verification or the parallel shape catches clearly more seeded bugs at acceptable cost. Then make it the default.
- A provider stops honouring instructions that Öge injects.
