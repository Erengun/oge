## Product rule

**Trust must earn its overhead.** Before building any mechanism, check:

1. Can a developer understand the benefit quickly?
2. Does it remove a real failure mode?
3. What latency or interruption does it add?
4. Can the complexity stay invisible on the happy path?

**Product constraint:** if Öge is dramatically slower or more annoying than native Claude/Codex workflows without proportional value, we failed. A polished, Claude Code-like terminal experience is a core product goal, not decoration (ADR-0022). The trust architecture stays invisible underneath it.

**Product equation (internal):** value = (assurance + autonomy gained) / (latency + human attention added). The north star is useful agent work per minute of human attention (`docs/positioning.md`). Cautionary tale: someone spent 883 commits and eight months on an overengineered agent harness, then abandoned it because the tooling became more interesting than the work. Build what the next felt UX needs, nothing more.

Implementation priority: get `oge "task"` working end to end as early as possible, first on the fake agent, then on Claude, then with verification. Don't build infrastructure ahead of a felt UX, and don't add architecture unless implementation exposes a real problem. See issue #66 and milestone M1.

Decisions live in `docs/adr/`, vocabulary in `GLOSSARY.md`, and wording in `docs/positioning.md`.

## Agent skills

### Issue tracker

Issues live in GitHub Issues for Erengun/oge, managed with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary: needs-triage, needs-info, ready-for-agent, ready-for-human, wontfix. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` + `docs/adr/`. See `docs/agents/domain.md`.
